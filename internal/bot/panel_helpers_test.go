package bot

import (
	"strings"

	"github.com/disgoorg/disgo/discord"
)

// panelText is everything the panel embed shows, for assertions.
func panelText(m discord.MessageCreate) string {
	var b strings.Builder
	b.WriteString(m.Content)
	for _, e := range m.Embeds {
		b.WriteString("\n" + e.Title + "\n" + e.Description)
		for _, f := range e.Fields {
			b.WriteString("\n" + f.Name + "\n" + f.Value)
		}
	}
	return b.String()
}

// panelButtonIDs lists the custom IDs of every button, row after row.
func panelButtonIDs(m discord.MessageCreate) []string {
	var ids []string
	for _, r := range m.Components {
		row, ok := r.(discord.ActionRowComponent)
		if !ok {
			continue
		}
		for _, c := range row.Components {
			if b, ok := c.(discord.ButtonComponent); ok {
				ids = append(ids, b.CustomID)
			}
		}
	}
	return ids
}
