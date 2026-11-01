package bot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// TestBotPermissions guards against accidental changes to the install URL
// permission bitmask. The constant must equal the original value so that
// existing bot invitations continue to work.
func TestBotPermissions(t *testing.T) {
	const want discord.Permissions = 391565762894144
	if botPermissions != want {
		t.Errorf("botPermissions = %d, want %d; update the install URLs if the permission set changed intentionally", botPermissions, want)
	}
}

// ownerInviteDocs are the tracked files that hard-code the owner bot's invite
// URL. Paths are relative to this package.
var ownerInviteDocs = []string{
	"../../README.md",
}

// TestOwnerPermissions pins the owner bot's bitmask, which is botPermissions
// plus DEAFEN_MEMBERS.
func TestOwnerPermissions(t *testing.T) {
	const want discord.Permissions = 391565771282752
	if ownerPermissions != want {
		t.Errorf("ownerPermissions = %d, want %d; also update every file in ownerInviteDocs", ownerPermissions, want)
	}
	if !ownerPermissions.Has(discord.PermissionDeafenMembers) {
		t.Error("ownerPermissions is missing DEAFEN_MEMBERS; non-capture speakers will never be deafened")
	}
	if botPermissions.Has(discord.PermissionDeafenMembers) {
		t.Error("speaker bots do not deafen anyone and must not request DEAFEN_MEMBERS")
	}
}

func TestInstallOwnerURL(t *testing.T) {
	id := snowflake.ID(123456789)
	got := installOwnerURL(id)
	want := fmt.Sprintf(
		"https://discord.com/oauth2/authorize?client_id=%s&scope=bot&permissions=%d",
		id, ownerPermissions,
	)
	if got != want {
		t.Errorf("installOwnerURL() = %q, want %q", got, want)
	}
}

func TestInstallOwnerChannelsURL(t *testing.T) {
	url := installOwnerChannelsURL(1)
	p := ownerPermissions | discord.PermissionManageChannels | discord.PermissionManageRoles | discord.PermissionViewChannel
	if !strings.HasSuffix(url, fmt.Sprintf("permissions=%d", p)) {
		t.Fatalf("installOwnerChannelsURL() = %q", url)
	}
}

func TestInstallURL(t *testing.T) {
	clientID := snowflake.ID(123456789)
	guildID := snowflake.ID(987654321)
	got := installURL(clientID, guildID)
	want := fmt.Sprintf(
		"https://discord.com/oauth2/authorize?client_id=%s&scope=bot&permissions=%d&guild_id=%s",
		clientID, botPermissions, guildID,
	)
	if got != want {
		t.Errorf("installURL() = %q, want %q", got, want)
	}
}

// TestOwnerInviteURLsMatchPermissions checks the invite URLs published in the
// docs against ownerPermissions.
//
// The number is hard-coded in the README: an invite missing a permission (for
// example Deafen Members) silently loses the matching optimisation, so the
// published link is checked rather than trusted.
func TestOwnerInviteURLsMatchPermissions(t *testing.T) {
	wantParam := fmt.Sprintf("permissions=%d", ownerPermissions)
	staleParam := fmt.Sprintf("permissions=%d", botPermissions)

	for _, path := range ownerInviteDocs {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			body := string(raw)
			if !strings.Contains(body, wantParam) {
				t.Errorf("%s has no owner invite URL with %q", path, wantParam)
			}
			// botPermissions is the speaker bitmask and the previous owner
			// value; either way it must not appear in an install link here.
			if strings.Contains(body, staleParam) {
				t.Errorf("%s still carries a stale invite URL with %q", path, staleParam)
			}
		})
	}
}
