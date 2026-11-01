package manager

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// Access channel: the board grants RvR access by posting in a text channel,
// either mentioning players or pasting their Discord usernames (one per line,
// as copied from a spreadsheet column). A message starting with "retirer"
// revokes instead. Only messages from board members are acted on.

// AccessChannelEnabled reports whether the access channel is configured.
func (m *Service) AccessChannelEnabled() bool {
	return m.AccessEnabled() && m.channels.opts.AccessChannel != ""
}

// IsAccessChannel reports whether channelID is the configured access channel.
func (m *Service) IsAccessChannel(guildID, channelID snowflake.ID) bool {
	want := strings.TrimSpace(m.channels.opts.AccessChannel)
	if want == "" || m.ownerClient == nil {
		return false
	}
	if id, err := snowflake.Parse(want); err == nil {
		return id == channelID
	}
	ch, ok := m.ownerClient.Caches.Channel(channelID)
	if !ok || ch.GuildID() != guildID {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(ch.Name()), want)
}

// MemberPermissions returns the guild permissions of member, from the cache.
func (m *Service) MemberPermissions(member discord.Member) discord.Permissions {
	if m.ownerClient == nil {
		return 0
	}
	return m.ownerClient.Caches.MemberPermissions(member)
}

// FindMember returns the member of guildID with userID, from the cache or
// Discord.
func (m *Service) FindMember(ctx context.Context, guildID, userID snowflake.ID) (discord.Member, bool) {
	if mb, ok := m.ownerClient.Caches.Member(guildID, userID); ok {
		return mb, true
	}
	mb, err := m.ownerClient.Rest.GetMember(guildID, userID, rest.WithCtx(ctx))
	if err != nil || mb == nil {
		return discord.Member{}, false
	}
	return *mb, true
}

// FindMembersByName returns the members called name: the one whose Discord
// username (unique) is name, else every member whose server nickname, global
// display name or username equals name, ignoring case. Several results mean
// the board has to pick.
func (m *Service) FindMembersByName(ctx context.Context, guildID snowflake.ID, name string) ([]discord.Member, error) {
	members, err := m.guildMembers(ctx, guildID)
	if err != nil {
		return nil, err
	}
	want := normName(name)
	for _, mb := range members {
		if strings.EqualFold(mb.User.Username, want) {
			return []discord.Member{mb}, nil
		}
	}
	var out []discord.Member
	for _, mb := range members {
		if mb.User.Bot {
			continue
		}
		if normName(DisplayName(mb)) == want || (mb.User.GlobalName != nil && normName(*mb.User.GlobalName) == want) {
			out = append(out, mb)
		}
	}
	return out, nil
}

// DisplayName is the name shown in the server: nickname, else global display
// name, else username.
func DisplayName(mb discord.Member) string {
	if mb.Nick != nil && *mb.Nick != "" {
		return *mb.Nick
	}
	if mb.User.GlobalName != nil && *mb.User.GlobalName != "" {
		return *mb.User.GlobalName
	}
	return mb.User.Username
}

func normName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// memberListTTL bounds how stale the member list used for name lookups is.
const memberListTTL = 2 * time.Minute

type memberList struct {
	at      time.Time
	members []discord.Member
}

// guildMembers lists every member of guildID (1000 per request), cached for
// memberListTTL so one access message costs at most one listing.
func (m *Service) guildMembers(ctx context.Context, guildID snowflake.ID) ([]discord.Member, error) {
	m.accessMembersMu.Lock()
	defer m.accessMembersMu.Unlock()
	if m.accessMembers == nil {
		m.accessMembers = map[snowflake.ID]memberList{}
	}
	if l, ok := m.accessMembers[guildID]; ok && time.Since(l.at) < memberListTTL {
		return l.members, nil
	}
	var all []discord.Member
	after := snowflake.ID(0)
	for range 100 { // 100 000 members at most
		page, err := m.ownerClient.Rest.GetMembers(guildID, 1000, after, rest.WithCtx(ctx))
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < 1000 {
			break
		}
		after = page[len(page)-1].User.ID
	}
	m.accessMembers[guildID] = memberList{at: time.Now(), members: all}
	return all, nil
}

// AccessRequest is a parsed access channel message.
type AccessRequest struct {
	Revoke bool
	IDs    []snowflake.ID // mentioned or pasted user IDs
	Names  []string       // pasted usernames or display names
}

// MaxAccessTargets caps the members handled from one message.
const MaxAccessTargets = 100

var (
	mentionRe  = regexp.MustCompile(`<@!?(\d{17,20})>`)
	rawIDRe    = regexp.MustCompile(`^\d{17,20}$`)
	revokeWord = map[string]bool{"retirer": true, "retire": true, "retrait": true, "remove": true, "revoke": true, "-": true}
	actionWord = map[string]bool{"ajouter": true, "ajout": true, "autoriser": true, "add": true, "grant": true, "+": true}
)

// maxNameRunes is the longest Discord username or display name.
const maxNameRunes = 32

// ParseAccessMessage extracts the targets of an access channel message.
// Mentions and raw IDs are always taken. A line is a name when it is short
// enough to be one (a pasted spreadsheet column, display names may contain
// spaces); in a longer line only the "@name" words are taken, so ordinary
// sentences are ignored. A first word "retirer" revokes.
func ParseAccessMessage(content string) AccessRequest {
	var req AccessRequest
	seenID := map[snowflake.ID]bool{}
	seenName := map[string]bool{}
	addID := func(s string) {
		if id, err := snowflake.Parse(s); err == nil && !seenID[id] {
			seenID[id] = true
			req.IDs = append(req.IDs, id)
		}
	}
	addName := func(s string) {
		s = strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(s), "@")), " ")
		if s == "" || len([]rune(s)) > maxNameRunes {
			return
		}
		if k := strings.ToLower(s); !seenName[k] {
			seenName[k] = true
			req.Names = append(req.Names, s)
		}
	}
	for _, mm := range mentionRe.FindAllStringSubmatch(content, -1) {
		addID(mm[1])
	}
	remaining := mentionRe.ReplaceAllString(content, " ")

	first := true
	for _, line := range strings.Split(remaining, "\n") {
		line = strings.Trim(strings.TrimSpace(line), ",;")
		if line == "" {
			continue
		}
		if first {
			first = false
			words := strings.Fields(line)
			w := strings.ToLower(strings.Trim(words[0], ":"))
			if revokeWord[w] || actionWord[w] {
				req.Revoke = revokeWord[w]
				line = strings.TrimSpace(strings.Join(words[1:], " "))
				if line == "" {
					continue
				}
			}
		}
		tokens := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' })
		ats := 0
		for _, t := range tokens {
			if strings.HasPrefix(t, "@") {
				ats++
			}
		}
		switch {
		case rawIDRe.MatchString(line):
			addID(line)
		case ats > 1 || (ats == 1 && !strings.HasPrefix(line, "@")):
			// several "@name" in one line, or one inside a sentence
			for _, t := range tokens {
				if strings.HasPrefix(t, "@") {
					addName(t)
				} else if rawIDRe.MatchString(t) {
					addID(t)
				}
			}
		case len([]rune(line)) <= maxNameRunes && !strings.HasSuffix(line, ":"):
			addName(line)
		}
	}
	return req
}
