package bot

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/TepqeT/voice-relay/internal/ally"
	"github.com/TepqeT/voice-relay/internal/announce"
	"github.com/TepqeT/voice-relay/internal/classsort"
	"github.com/TepqeT/voice-relay/internal/config"
	"github.com/TepqeT/voice-relay/internal/dave"
	"github.com/TepqeT/voice-relay/internal/diag"
	"github.com/TepqeT/voice-relay/internal/guild"
	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/manager"
	"github.com/TepqeT/voice-relay/internal/pool"
	"github.com/TepqeT/voice-relay/internal/quality"
	"github.com/TepqeT/voice-relay/internal/store"
	"github.com/TepqeT/voice-relay/internal/telemetry"
	"github.com/TepqeT/voice-relay/internal/web"
	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave/golibdave"
	"github.com/disgoorg/snowflake/v2"
	"go.opentelemetry.io/otel/metric"
)

// SessionManager handles voice raid session lifecycle.
type SessionManager interface {
	StartVoiceRaid(ctx context.Context, guildID snowflake.ID, cancelFunc context.CancelFunc, mode guild.RaidMode) (ally.Code, error)
	// CheckDeafenReadiness reports whether the owner bot can server-deafen the
	// guild's speakers. Surfaced in the /start reply so a fixable guild
	// misconfiguration reaches the operator rather than only the logs.
	CheckDeafenReadiness(guildID snowflake.ID) manager.DeafenReadiness
	StopVoiceRaid(ctx context.Context, guildID snowflake.ID) error
	JoinSession(ctx context.Context, guestGuildID snowflake.ID, cancelFunc context.CancelFunc, mode guild.RaidMode, code ally.Code) (guild.RaidMode, error)
	HasActiveSession(guildID snowflake.ID) bool
	AutoRoute(guildID, channelID snowflake.ID)
	CheckGuildChannelAccess(guildID snowflake.ID) []manager.ChannelAccessWarning
	SpeakersWithMissingChannel(guildID snowflake.ID) []manager.ChannelAccessWarning
	EnsureGuildReady(guildID snowflake.ID)
	ReconnectBotChannel(ctx context.Context, guildID, botUserID snowflake.ID)
	OnBotVoiceMove(ctx context.Context, guildID, botUserID snowflake.ID, currentChannelID *snowflake.ID)
	NotifyMemberUpdate(guildID snowflake.ID, member discord.Member)
}

// BindingManager handles channel and role bindings.
type BindingManager interface {
	BindRole(guildID snowflake.ID, roleType store.RoleType, roleID snowflake.ID)
	UnbindRole(guildID snowflake.ID, roleType store.RoleType)
	BindChannel(guildID, userID, channelID snowflake.ID)
	UnbindChannel(guildID, userID snowflake.ID)
	GetBoundChannel(guildID, userID snowflake.ID) (snowflake.ID, bool)
	ChannelDeleted(guildID, channelID snowflake.ID)
	OwnerBotID() snowflake.ID
	BindLocale(guildID snowflake.ID, locale string)
	UnbindLocale(guildID snowflake.ID)
	GetLocale(guildID snowflake.ID) string
}

// SpeakerManager handles speaker registration and configuration.
type SpeakerManager interface {
	ToggleSpeaker(guildID, speakerID snowflake.ID, enabled bool) error
	NextSpeakerID(guildID snowflake.ID) (snowflake.ID, bool)
	HasAvailableToken(guildID snowflake.ID) bool
}

// StatusProvider provides read-only status and authorization queries.
type StatusProvider interface {
	GetStatus(guildID snowflake.ID) guild.Status
	HasManagerRole(guildID snowflake.ID, memberRoleIDs []snowflake.ID) bool
	HasCallerRole(guildID snowflake.ID, memberRoleIDs []snowflake.ID) bool
	IsBot(user discord.User) bool
}

// SeedManager handles speaker bot registration on startup and guild events.
type SeedManager interface {
	SeedExistingSpeakers(guildIDs []snowflake.ID)
	TrySeedMember(guildID, newUserID snowflake.ID)
	RemoveSpeaker(guildID, userID snowflake.ID)
}

// LifecycleManager handles startup hooks and graceful shutdown.
type LifecycleManager interface {
	StartMetrics()
	Shutdown(ctx context.Context)
}

// ManagerService is the full interface consumed by the bot layer.
// It composes focused sub-interfaces for better separation of concerns.
type ManagerService interface {
	SessionManager
	BindingManager
	SpeakerManager
	StatusProvider
	SeedManager
	LifecycleManager
}

// Bot wraps the disgo client and all application services.
type Bot struct {
	client        *bot.Client
	manager       ManagerService
	store         store.Store
	poolSvc       *pool.Service
	speakerTokens []string
	guildReadyCh  chan []snowflake.ID
	bundle        *i18n.Bundle
	daveStats     *dave.Stats
	daveMetrics   *telemetry.DaveMetrics
	// dynamicChannels: raids create their channels, the invite asks for more.
	dynamicChannels bool
	// extraCommands are registered with the slash commands (RvR access user
	// commands when configured).
	extraCommands []discord.ApplicationCommandCreate
	// site is the administration site, nil when WEB_ADDR is not configured.
	site *web.Server
}

// New creates and configures a new Bot instance. It performs no network I/O —
// all connections are established in Run.
// st is the persistent store; callers are responsible for creating it
// (e.g. store.NewYAMLStore for production, store.NewInMemoryStore for tests).
func New(cfg *config.Config, st store.Store, meter metric.Meter) (*Bot, error) {
	// Command router
	r := handler.New()

	// Buffered channel (cap 1) receives guild IDs from the Ready event for command sync.
	guildReadyCh := make(chan []snowflake.ID, 1)

	// Shared by the owner client and the speaker pool. Created here because the
	// clients are built before the meter exists; StartMetrics attaches it later.
	daveStats := dave.NewStats()
	// Same reason: the manager that acts on a stall is built after the clients.
	daveStalls := dave.NewEncryptStalls()

	// Manager (owner) bot client — production adds the command router.
	intents := []gateway.Intents{
		gateway.IntentGuilds,
		gateway.IntentGuildMembers,
		gateway.IntentGuildVoiceStates,
	}
	// The access channel is read only when configured: Message Content is a
	// privileged intent, refused at connection unless enabled in the
	// Developer Portal (Bot > Privileged Gateway Intents).
	if ch := cfg.RvR.Channels; ch.AccessChannel != "" && ch.AccessRole != "" && ch.BoardRole != "" {
		intents = append(intents, gateway.IntentGuildMessages, gateway.IntentMessageContent)
		slog.Info("access channel enabled: Message Content intent requested", slog.String("channel", ch.AccessChannel))
	}
	client, err := NewOwnerClient(cfg.OwnerBotToken, daveStats, daveStalls,
		bot.WithGatewayConfigOpts(gateway.WithIntents(intents...)),
		bot.WithEventListeners(r),
	)
	if err != nil {
		return nil, err
	}

	// Capture guild IDs from the Ready event for use in command sync.
	client.AddEventListeners(bot.NewListenerFunc(func(e *events.Ready) {
		ids := make([]snowflake.ID, 0, len(e.Guilds))
		for _, g := range e.Guilds {
			ids = append(ids, g.ID)
		}
		select {
		case guildReadyCh <- ids:
		default:
		}
	}))

	ownerBotID, ok := guild.BotUserID(cfg.OwnerBotToken)
	if !ok {
		return nil, fmt.Errorf("failed to get owner bot ID from token")
	}

	// Metrics — must be created after telemetry.Setup so the OTel SDK is initialised.
	metrics, err := telemetry.NewMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("failed to init metrics: %w", err)
	}

	// Registered here rather than in NewOwnerClient because the metrics are
	// built after the client — same reason as the Ready listener above.
	client.AddEventListeners(pool.VoiceRegionListeners(ownerBotID, &metrics.Voice)...)

	poolSvc := pool.NewService(&metrics.Pool, &metrics.Voice, daveStats, daveStalls)
	managerSvc := manager.NewService(st, poolSvc, client, ownerBotID, cfg.Test, metrics)
	daveStalls.SetHandler(managerSvc.OnEncryptStall)
	managerSvc.SetSessionIdleTimeout(cfg.SessionIdleTimeout)

	// RvR additions: priority speaker, announcements, quality reports, diagnostics.
	pool.SetPrioritySpeaking(cfg.RvR.PrioritySpeaker)
	diag.Configure(diag.Settings{Dir: cfg.RvR.DiagDir, Interval: cfg.RvR.DiagInterval})
	sounds, err := announce.Load(cfg.RvR.SoundsDir)
	if err != nil {
		// A broken sounds directory must not keep the relay down.
		slog.Warn("announcements disabled", slog.Any("err", err))
		sounds = nil
	}
	managerSvc.ConfigureRvR(manager.RvROptions{
		Sounds:             sounds,
		SoundOnStart:       cfg.RvR.SoundOnStart,
		SoundOnStop:        cfg.RvR.SoundOnStop,
		ReportsDir:         cfg.RvR.ReportsDir,
		SquadLeadRole:      cfg.RvR.SquadLeadRole,
		SquadLeadKey:       cfg.RvR.SquadLeadKey,
		SquadLeadsToSquads: cfg.RvR.SquadLeadsToSquads,
		RaidLeadRole:       cfg.RvR.RaidLeadRole,
	})
	ch := cfg.RvR.Channels
	managerSvc.ConfigureChannels(manager.ChannelOptions{
		Enabled:       ch.Enabled,
		Faction:       ch.Faction,
		FactionRole:   ch.FactionRole,
		Category:      ch.Category,
		After:         ch.After,
		RaidLeadName:  ch.RaidLeadName,
		SquadName:     ch.SquadName,
		SquadCount:    ch.SquadCount,
		Region:        ch.Region,
		StateFile:     ch.StateFile,
		AccessRole:    ch.AccessRole,
		BoardRole:     ch.BoardRole,
		AccessChannel: ch.AccessChannel,
	})
	if rule, err := classsort.ParseRule(ch.ClassRule); err != nil {
		slog.Warn("invalid CLASS_RULE, class sorting disabled", slog.String("value", ch.ClassRule), slog.Any("err", err))
	} else {
		managerSvc.ConfigureClassSort(manager.ClassSortOptions{
			ClassRoles: ch.ClassRoles,
			Waiting:    ch.WaitingChannel,
			Rule:       rule,
			RuleText:   ch.ClassRule,
		})
	}
	slog.Info("rvr settings",
		slog.Bool("prioritySpeaker", cfg.RvR.PrioritySpeaker),
		slog.Int("sounds", sounds.Len()),
		slog.String("soundOnStart", cfg.RvR.SoundOnStart),
		slog.String("soundOnStop", cfg.RvR.SoundOnStop),
		slog.String("reportsDir", cfg.RvR.ReportsDir),
		slog.String("diagDir", cfg.RvR.DiagDir),
		slog.Duration("diagInterval", cfg.RvR.DiagInterval),
		slog.String("squadLeadRole", cfg.RvR.SquadLeadRole),
		slog.String("squadLeadKey", cfg.RvR.SquadLeadKey),
		slog.Bool("squadLeadsToSquads", cfg.RvR.SquadLeadsToSquads),
		slog.String("raidLeadRole", cfg.RvR.RaidLeadRole),
	)

	bundle, err := i18n.NewBundle()
	if err != nil {
		return nil, fmt.Errorf("failed to load i18n bundle: %w", err)
	}
	bundle.SetEventLabel(cfg.RvR.Channels.EventLabel)
	slog.Info("i18n bundle loaded", slog.Int("locales", len(bundle.Tags())), slog.String("eventLabel", bundle.EventLabel()))

	// Wire command handlers.
	cmdHandlers := NewCommandHandlers(managerSvc, &metrics.Bot, bundle)
	cmdHandlers.SetRvR(managerSvc)
	cmdHandlers.Register(r)
	if cmdHandlers.AccessChannelEnabled() {
		client.AddEventListeners(bot.NewListenerFunc(cmdHandlers.OnAccessMessage))
	}

	if cfg.RvR.ReportToDiscord {
		lang := cfg.RvR.ReportLang
		managerSvc.SetReportSink(func(rep quality.Report) {
			guildID, err := snowflake.Parse(rep.GuildID)
			if err != nil {
				return
			}
			channelID := cmdHandlers.ReportChannel(guildID)
			if channelID == 0 {
				return
			}
			if _, err := client.Rest.CreateMessage(channelID, discord.MessageCreate{
				Content:         quality.RenderWithLabel(rep, lang, bundle.EventLabel()),
				AllowedMentions: &discord.AllowedMentions{},
			}); err != nil {
				slog.Warn("failed to post quality report", slog.String("channelID", channelID.String()), slog.Any("err", err))
			}
		})
	}

	b := &Bot{
		client:        client,
		daveStats:     daveStats,
		daveMetrics:   &metrics.Dave,
		manager:       managerSvc,
		store:         st,
		poolSvc:       poolSvc,
		speakerTokens: cfg.SpeakerTokens,
		guildReadyCh:  guildReadyCh,
		bundle:        bundle,

		dynamicChannels: cfg.RvR.Channels.Enabled,
		extraCommands:   append(cmdHandlers.AccessCommands(), cmdHandlers.SquadLeadCommands()...),
	}

	if cfg.Web.Enabled() {
		backend := newWebBackend(managerSvc, ownerBotID, func() string {
			if u, ok := client.Caches.SelfUser(); ok {
				return u.Username
			}
			return ""
		}, cfg.SpeakerTokens)
		site, err := web.New(web.Config{Addr: cfg.Web.Addr, BaseURL: cfg.Web.BaseURL, ClientID: ownerBotID, ClientSecret: cfg.Web.ClientSecret}, backend)
		if err != nil {
			return nil, fmt.Errorf("web site: %w", err)
		}
		b.site = site
	}

	// Built before the listeners are attached so onGuildJoin can call back into
	// b.syncGuildCommands. No network I/O happens here — the gateway opens in Run.
	client.AddEventListeners(EventListeners(managerSvc, b.syncGuildCommands)...)

	return b, nil
}

// Run connects the speaker pool, opens the owner gateway, registers slash commands,
// and blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	if err := connectPool(ctx, b.poolSvc, b.speakerTokens); err != nil {
		return err
	}
	b.poolSvc.StartWatchdog(ctx, 30*time.Second)
	b.poolSvc.StartMetrics()
	b.daveStats.StartMetrics(b.daveMetrics)
	b.manager.StartMetrics()

	if err := b.client.OpenGateway(ctx); err != nil {
		return err
	}
	defer func() {
		// Graceful shutdown: stop all raids, close all speaker gateways, then the owner gateway.
		shutdownCtx := context.Background()
		b.manager.Shutdown(shutdownCtx)
		// Backstop for any audio sender whose conn never went through
		// GuildVoice.Leave — disgo's Conn.Close leaves them running.
		pool.CloseAllAudioSenders()
		b.store.Close()
		b.client.Close(shutdownCtx)
	}()

	// Wait for the Ready event to deliver guild IDs, then sync slash commands.
	var guildIDs []snowflake.ID
	select {
	case guildIDs = <-b.guildReadyCh:
	case <-time.After(10 * time.Second):
		slog.WarnContext(ctx, "timed out waiting for Ready event, skipping command sync")
	}
	b.syncCommands(ctx, guildIDs)

	if selfUser, ok := b.client.Caches.SelfUser(); ok {
		url := installOwnerURL(selfUser.ID)
		if b.dynamicChannels {
			url = installOwnerChannelsURL(selfUser.ID)
		}
		slog.InfoContext(ctx, "owner bot invite URL", slog.String("url", url))
		b.poolSvc.RegisterBot(selfUser.ID, selfUser.Username, b.client)
	}

	if b.site != nil {
		go func() {
			if err := b.site.Run(ctx); err != nil {
				slog.ErrorContext(ctx, "web site stopped", slog.Any("err", err))
			}
		}()
	}

	slog.InfoContext(ctx, "bot is running. Press Ctrl+C to stop.")

	<-ctx.Done()

	slog.Info("shutting down...")

	return nil
}

// syncCommands refreshes the owner bot's slash commands: any command edited in
// BuildCommands is overwritten and any removed one is deleted, because each
// per-guild sync fully replaces that guild's command set.
//
// Commands are registered per guild (instant propagation, unlike the ~1h global
// cache). Because we never register globally, the global set must stay empty:
// a global command left over from an earlier build shows up as a ghost duplicate
// that survives guild re-syncs and only clears with a manual delete + restart.
// Overwriting the global set with an empty list on every boot prevents that.
func (b *Bot) syncCommands(ctx context.Context, guildIDs []snowflake.ID) {
	commands := b.allCommands()

	appID := b.client.ApplicationID

	if _, err := b.client.Rest.SetGlobalCommands(appID, nil); err != nil {
		slog.WarnContext(ctx, "failed to clear global commands", slog.Any("err", err))
	}

	if len(guildIDs) == 0 {
		slog.WarnContext(ctx, "no guilds discovered, skipping guild command sync")
		return
	}

	slog.InfoContext(ctx, "syncing slash commands", slog.Int("guilds", len(guildIDs)))
	for _, gid := range guildIDs {
		b.setGuildCommands(ctx, gid, commands)
	}
}

// syncGuildCommands registers the full command set for a single guild.
//
// syncCommands only covers the guilds present in the Ready payload at boot, and
// because nothing is ever registered globally there is no fallback: a guild the
// bot joins while running would otherwise show no commands at all until the next
// restart. Wired into onGuildJoin so a fresh invite is usable immediately.
func (b *Bot) syncGuildCommands(ctx context.Context, guildID snowflake.ID) {
	b.setGuildCommands(ctx, guildID, b.allCommands())
}

// allCommands is the slash command set plus the configured extra commands.
func (b *Bot) allCommands() []discord.ApplicationCommandCreate {
	return append(BuildCommands(b.bundle), b.extraCommands...)
}

// setGuildCommands replaces one guild's command set. Guild-scoped registration
// propagates instantly, unlike the ~1h global command cache.
func (b *Bot) setGuildCommands(ctx context.Context, guildID snowflake.ID, commands []discord.ApplicationCommandCreate) {
	if _, err := b.client.Rest.SetGuildCommands(b.client.ApplicationID, guildID, commands); err != nil {
		slog.ErrorContext(ctx, "failed to sync guild commands",
			slog.String("guild_id", guildID.String()),
			slog.Any("err", err),
		)
		return
	}

	names := make([]string, 0, len(commands))
	for _, c := range commands {
		names = append(names, c.CommandName())
	}
	slices.Sort(names)

	slog.InfoContext(ctx, "synced guild commands",
		slog.String("guild_id", guildID.String()),
		slog.Int("count", len(commands)),
		slog.String("commands", strings.Join(names, ", ")),
	)
}

// NewOwnerClient builds a disgo client for the owner (manager) bot.
// Base config covers DAVE E2EE voice and the owner cache. Callers supply
// their own intents and any extra options (e.g. event listeners, extra intents).
// daveStats counts decrypt outcomes per sending user; pass the same one given
// to the speaker pool so owner and speakers report into a single series.
// daveStalls receives the bot's stalled-encryptor reports.
func NewOwnerClient(token string, daveStats *dave.Stats, daveStalls *dave.EncryptStalls, opts ...bot.ConfigOpt) (*bot.Client, error) {
	botUserID, _ := guild.BotUserID(token)

	var caches cache.Caches
	base := []bot.ConfigOpt{
		bot.WithCacheConfigOpts(ownerCacheOpts(&caches)...),
		bot.WithEventListeners(ownerCacheListeners()...),
		bot.WithVoiceManagerConfigOpts(
			voice.WithDaveSessionCreateFunc(dave.Instrument(golibdave.NewSession, daveStats, daveStalls.Option())),
			pool.SafeUDPConnOpt(),
			pool.SafeAudioSenderOpt(),
			pool.SafeGatewayOpt(),
			voice.WithLogger(telemetry.VoiceLogger(botUserID)),
		),
	}
	client, err := disgo.New(token, append(base, opts...)...)
	if err != nil {
		return nil, err
	}
	caches = client.Caches
	return client, nil
}

// connectPool opens one gateway per speaker token and fails if any are not connected.
func connectPool(ctx context.Context, poolSvc *pool.Service, tokens []string) error {
	poolCtx, poolCancel := context.WithTimeout(ctx, 30*time.Second)
	poolSvc.ConnectPool(poolCtx, tokens)
	poolCancel()

	total := len(poolSvc.GetIDs())
	connected := len(poolSvc.GetClients())
	if connected < total {
		return fmt.Errorf("speaker pool: only %d/%d speaker gateways connected at startup", connected, total)
	}
	slog.InfoContext(ctx, "speaker pool ready", slog.Int("total", total))
	return nil
}
