// Package classsort spreads waiting players over the raid squads according to
// their class. Everything here is pure: the manager gathers the players and
// the squads, calls Plan, then moves the members.
package classsort

import (
	"fmt"
	"strconv"
	"strings"
)

// Reason tells why a player stays in the waiting channel.
type Reason string

const (
	// ReasonNoClass: the player holds none of the class roles.
	ReasonNoClass Reason = "no_class"
	// ReasonMultipleClasses: the player holds more than one class role.
	ReasonMultipleClasses Reason = "multiple_classes"
	// ReasonNoRoom: every squad is full for that class (quota or size).
	ReasonNoRoom Reason = "no_room"
	// ReasonSquadLead: squad leads are never moved automatically.
	ReasonSquadLead Reason = "squad_lead"
)

// Player is a member to place, or already sitting in a squad. Classes holds
// the class roles the member has, as configured names.
type Player struct {
	ID        string
	Classes   []string
	SquadLead bool
}

// class returns the single class of p, or a reason why there is none.
func (p Player) class() (string, Reason) {
	switch len(p.Classes) {
	case 0:
		return "", ReasonNoClass
	case 1:
		return normalize(p.Classes[0]), ""
	default:
		return "", ReasonMultipleClasses
	}
}

// Rule is the composition rule set by the speakers. The zero value is the
// "balanced" rule without size limit.
type Rule struct {
	// Quotas caps each class per squad: Quotas[class][i] is the cap of squad
	// i; squads past the end of the list use its last value. Empty = no cap.
	Quotas map[string][]int
	// Default caps the classes missing from Quotas. Nil with Quotas set
	// means 0: unlisted classes are not placed.
	Default []int
	// Max caps the number of members per squad, 0 = no limit.
	Max int
}

// Balanced reports whether r has no class quota.
func (r Rule) Balanced() bool { return len(r.Quotas) == 0 && r.Default == nil }

// limit returns the cap of class in squad i, -1 = unlimited.
func (r Rule) limit(class string, i int) int {
	if r.Balanced() {
		return -1
	}
	if q, ok := r.Quotas[class]; ok {
		return at(q, i)
	}
	if r.Default != nil {
		return at(r.Default, i)
	}
	return 0
}

func at(q []int, i int) int {
	if len(q) == 0 {
		return 0
	}
	if i < len(q) {
		return q[i]
	}
	return q[len(q)-1]
}

// Move sends one player to a squad (index in the squads given to Plan).
type Move struct {
	PlayerID string
	Squad    int
}

// Waiting is a player left in the waiting channel, with the reason.
type Waiting struct {
	PlayerID string
	Reason   Reason
}

// Result is the outcome of Plan.
type Result struct {
	Moves   []Move
	Waiting []Waiting
}

// Plan assigns the waiting players, in the given order, to the squads.
// squads holds the members already in each squad: they count toward the
// quotas and sizes but are never moved. Each player goes to the squad that
// has the fewest members of their class, then the fewest members, then the
// lowest index, among the squads with room left.
func Plan(waiting []Player, squads [][]Player, rule Rule) Result {
	var res Result
	n := len(squads)
	perClass := make([]map[string]int, n)
	totals := make([]int, n)
	for i, members := range squads {
		perClass[i] = make(map[string]int)
		for _, m := range members {
			totals[i]++
			if c, r := m.class(); r == "" {
				perClass[i][c]++
			}
		}
	}
	for _, p := range waiting {
		if p.SquadLead {
			res.Waiting = append(res.Waiting, Waiting{p.ID, ReasonSquadLead})
			continue
		}
		class, reason := p.class()
		if reason != "" {
			res.Waiting = append(res.Waiting, Waiting{p.ID, reason})
			continue
		}
		best := -1
		for i := range n {
			if rule.Max > 0 && totals[i] >= rule.Max {
				continue
			}
			if l := rule.limit(class, i); l >= 0 && perClass[i][class] >= l {
				continue
			}
			if best < 0 || perClass[i][class] < perClass[best][class] ||
				(perClass[i][class] == perClass[best][class] && totals[i] < totals[best]) {
				best = i
			}
		}
		if best < 0 {
			res.Waiting = append(res.Waiting, Waiting{p.ID, ReasonNoRoom})
			continue
		}
		perClass[best][class]++
		totals[best]++
		res.Moves = append(res.Moves, Move{p.ID, best})
	}
	return res
}

// ParseRule reads CLASS_RULE:
//
//	balanced                       every class spread evenly, no limit
//	balanced; max=6                same, at most 6 members per squad
//	Tank=1; Healer=2; *=1          caps per class in every listener channel
//	Healer=2/1/1                   per channel: channel 1 two, then one each
//
// Entries are separated by ";" or ",". "*" sets the cap of unlisted classes;
// without it they are not placed. Class names match CLASS_ROLES entries,
// ignoring case. Empty means balanced.
func ParseRule(s string) (Rule, error) {
	var r Rule
	for _, part := range strings.FieldsFunc(s, func(c rune) bool { return c == ';' || c == ',' }) {
		part = strings.TrimSpace(part)
		if part == "" || strings.EqualFold(part, "balanced") || strings.EqualFold(part, "equilibre") || strings.EqualFold(part, "équilibré") {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			return Rule{}, fmt.Errorf("class rule %q: expected class=number", part)
		}
		key = strings.TrimSpace(key)
		if strings.EqualFold(key, "max") {
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil || n < 0 {
				return Rule{}, fmt.Errorf("class rule %q: max must be a positive number", part)
			}
			r.Max = n
			continue
		}
		var caps []int
		for _, v := range strings.Split(val, "/") {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				return Rule{}, fmt.Errorf("class rule %q: quotas must be numbers >= 0", part)
			}
			caps = append(caps, n)
		}
		if key == "*" {
			r.Default = caps
			continue
		}
		if key == "" {
			return Rule{}, fmt.Errorf("class rule %q: missing class name", part)
		}
		if r.Quotas == nil {
			r.Quotas = make(map[string][]int)
		}
		r.Quotas[normalize(key)] = caps
	}
	return r, nil
}

// Classes lists the classes named by the quotas, normalized.
func (r Rule) Classes() []string {
	out := make([]string, 0, len(r.Quotas))
	for c := range r.Quotas {
		out = append(out, c)
	}
	return out
}

// Normalize is the comparison form of a class name.
func Normalize(s string) string { return normalize(s) }

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
