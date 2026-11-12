package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/gatewayguard"
)

// installGuardActions tells the reconnect guard what to do when a bot loops:
// record the goroutines, close the bot's gateway past the daily limit, and
// send a direct message to the application owner. REST only: it works while
// the looping gateway is down.
func (b *Bot) installGuardActions(ctx context.Context) {
	var ownerUser snowflake.ID
	if app, err := b.client.Rest.GetCurrentApplication(); err != nil {
		slog.WarnContext(ctx, "gateway guard: application owner unknown, alerts go to the logs only", slog.Any("err", err))
	} else if admins := applicationAdmins(app); len(admins) > 0 {
		ownerUser = admins[0]
	}

	b.guard.SetActions(gatewayguard.Actions{
		Dump: func(botID snowflake.ID) string {
			path, err := gatewayguard.WriteGoroutines(b.diagDir, botID, time.Now())
			if err != nil {
				slog.Warn("gateway guard: goroutines not recorded", slog.Any("err", err))
				return ""
			}
			return path
		},
		Halt: func(botID snowflake.ID) {
			haltCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if botID == b.ownerBotID {
				b.client.Gateway.Close(haltCtx)
				slog.Error("owner gateway halted by the reconnect guard")
				return
			}
			b.poolSvc.Halt(haltCtx, botID)
		},
		Alert: func(botID snowflake.ID, text string) {
			b.alertOwner(ownerUser, b.botName(botID)+" : "+text)
		},
	})
}

// botName names a bot for the operator: its Discord name when known.
func (b *Bot) botName(botID snowflake.ID) string {
	if botID == b.ownerBotID {
		if u, ok := b.client.Caches.SelfUser(); ok {
			return u.Username
		}
		return "Voice Relay"
	}
	if c, ok := b.poolSvc.GetClientByID(botID); ok {
		if u, ok := c.Caches.SelfUser(); ok {
			return u.Username
		}
	}
	return "bot relais " + botID.String()
}

// alertOwner sends text to userID in a direct message from the main bot.
func (b *Bot) alertOwner(userID snowflake.ID, text string) {
	if userID == 0 {
		slog.Error("gateway guard alert (no owner to message)", slog.String("text", text))
		return
	}
	ch, err := b.client.Rest.CreateDMChannel(userID)
	if err == nil {
		_, err = b.client.Rest.CreateMessage(ch.ID(), discord.MessageCreate{Content: text, AllowedMentions: &discord.AllowedMentions{}})
	}
	if err != nil {
		slog.Error("gateway guard: alert not delivered", slog.String("text", text), slog.Any("err", err))
	}
}
