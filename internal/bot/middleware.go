package bot

import (
	"github.com/TepqeT/voice-relay/internal/ratelimit"
	"time"

	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/telemetry"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/snowflake/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// guildCommandHandler is a slash command handler that receives a validated guild ID and localizer.
type guildCommandHandler func(guildID snowflake.ID, loc *i18n.Localizer, data discord.SlashCommandInteractionData, e *handler.CommandEvent) error

// withGuild wraps a handler to validate the guild context and create an OTel span.
func (h *CommandHandlers) withGuild(fn guildCommandHandler) func(discord.SlashCommandInteractionData, *handler.CommandEvent) error {
	return func(data discord.SlashCommandInteractionData, e *handler.CommandEvent) error {
		guildID, errMsg := requireGuild(h.bundle.For("", e.Locale()), e.GuildID())
		if errMsg != nil {
			return e.CreateMessage(*errMsg)
		}
		loc := h.loc(guildID, e.Locale())
		if tooFast(guildID, e.User().ID) {
			return e.CreateMessage(ephemeral(loc.T("err.too_fast")))
		}

		ctx, span := telemetry.Tracer.Start(e.Ctx, "discord.command",
			trace.WithAttributes(
				attribute.String("command", data.CommandName()),
				attribute.String("guild.id", guildID.String()),
				attribute.String("user.id", e.User().ID.String()),
			),
		)
		// Recorded before fn runs: a handler that never returns records no
		// completion, so without this the invocation leaves no trace at all.
		h.metrics.RecordCommandStart(ctx, data.CommandName(), guildID.String())

		start := time.Now()
		e.Ctx = ctx
		err := fn(guildID, loc, data, e)
		duration := time.Since(start).Seconds()

		h.metrics.RecordCommand(ctx, data.CommandName(), guildID.String(), duration)

		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()

		return err
	}
}

// withManager wraps a handler to require Manage Server permission or the manager role.
func (h *CommandHandlers) withManager(fn guildCommandHandler) func(discord.SlashCommandInteractionData, *handler.CommandEvent) error {
	return h.withGuild(func(guildID snowflake.ID, loc *i18n.Localizer, data discord.SlashCommandInteractionData, e *handler.CommandEvent) error {
		if !h.isManagerAuthorized(guildID, e.Member()) {
			return e.CreateMessage(ephemeral(loc.T("err.need_manager")))
		}
		return fn(guildID, loc, data, e)
	})
}

// withAdmin wraps a handler to require Administrator permission or the manager role.
func (h *CommandHandlers) withAdmin(fn guildCommandHandler) func(discord.SlashCommandInteractionData, *handler.CommandEvent) error {
	return h.withGuild(func(guildID snowflake.ID, loc *i18n.Localizer, data discord.SlashCommandInteractionData, e *handler.CommandEvent) error {
		if !h.isAdminAuthorized(guildID, e.Member()) {
			return e.CreateMessage(ephemeral(loc.T("err.need_admin")))
		}
		return fn(guildID, loc, data, e)
	})
}

// guildButtonHandler is a button component handler that receives a validated guild ID and localizer.
type guildButtonHandler func(guildID snowflake.ID, loc *i18n.Localizer, data discord.ButtonInteractionData, e *handler.ComponentEvent) error

// withGuildButton wraps a button component handler to validate the guild context.
func (h *CommandHandlers) withGuildButton(fn guildButtonHandler) func(discord.ButtonInteractionData, *handler.ComponentEvent) error {
	return func(data discord.ButtonInteractionData, e *handler.ComponentEvent) error {
		guildID, errMsg := requireGuild(h.bundle.For("", e.Locale()), e.GuildID())
		if errMsg != nil {
			return e.CreateMessage(*errMsg)
		}
		loc := h.loc(guildID, e.Locale())
		if tooFast(guildID, e.User().ID) {
			return e.CreateMessage(ephemeral(loc.T("err.too_fast")))
		}
		return fn(guildID, loc, data, e)
	}
}

// interactionLimit caps each member at a few clicks or commands in a row,
// then about one per second: a member hammering a button gets a short
// "slow down" instead of starting work for every click.
var interactionLimit = ratelimit.New(time.Second, 4)

func tooFast(guildID, userID snowflake.ID) bool {
	return !interactionLimit.Allow(guildID.String() + ":" + userID.String())
}

// guildSelectMenuHandler is a select menu component handler that receives a validated guild ID and localizer.
type guildSelectMenuHandler func(guildID snowflake.ID, loc *i18n.Localizer, data discord.SelectMenuInteractionData, e *handler.ComponentEvent) error

// withGuildSelectMenu wraps a select menu component handler to validate the guild context.
func (h *CommandHandlers) withGuildSelectMenu(fn guildSelectMenuHandler) func(discord.SelectMenuInteractionData, *handler.ComponentEvent) error {
	return func(data discord.SelectMenuInteractionData, e *handler.ComponentEvent) error {
		guildID, errMsg := requireGuild(h.bundle.For("", e.Locale()), e.GuildID())
		if errMsg != nil {
			return e.CreateMessage(*errMsg)
		}
		loc := h.loc(guildID, e.Locale())
		if tooFast(guildID, e.User().ID) {
			return e.CreateMessage(ephemeral(loc.T("err.too_fast")))
		}
		return fn(guildID, loc, data, e)
	}
}

// isAdminAuthorized reports whether the member has Administrator or Manage
// Server permission, or holds the guild's configured manager role. Manage
// Server is enough to invite and remove bots, so it is enough to configure
// this one: a server can be set up without granting anyone Administrator.
func (h *CommandHandlers) isAdminAuthorized(guildID snowflake.ID, member *discord.ResolvedMember) bool {
	if member == nil {
		return false
	}
	if member.Permissions.Has(discord.PermissionAdministrator) || member.Permissions.Has(discord.PermissionManageGuild) {
		return true
	}
	return h.manager.HasManagerRole(guildID, member.Member.RoleIDs)
}

// isManagerAuthorized reports whether the member has Manage Server permission
// or holds the guild's configured manager role.
func (h *CommandHandlers) isManagerAuthorized(guildID snowflake.ID, member *discord.ResolvedMember) bool {
	if member == nil {
		return false
	}
	if member.Permissions.Has(discord.PermissionManageGuild) {
		return true
	}
	return h.manager.HasManagerRole(guildID, member.Member.RoleIDs)
}
