package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/TepqeT/voice-relay/internal/telemetry"
)

// TestConfig holds optional test overrides.
type TestConfig struct {
	// AllowBots disables the bot-user filter in allowUser so that bot accounts
	// with the caller role can be captured. Set TEST_ALLOW_BOTS=true for E2E tests.
	AllowBots bool
}

// Config holds all application configuration.
type Config struct {
	// OwnerBotToken is the manager bot token (required)
	OwnerBotToken string
	// SpeakerTokens is the ordered pool of speaker bot tokens loaded from env
	SpeakerTokens []string
	// StorePath is the path to the YAML persistence file (default: store.yaml)
	StorePath string
	// OtelEndpoint is the OTLP gRPC endpoint (e.g. "alloy:4317"); empty disables telemetry
	OtelEndpoint string
	// LogLevel is the minimum log level (default: info); controlled by LOG_LEVEL env var
	LogLevel slog.Level
	// VoiceLogLevel is the minimum level for disgo's voice-layer logs, controlled
	// by VOICE_LOG_LEVEL. Defaults to off — see telemetry.VoiceLogger for why.
	VoiceLogLevel slog.Level
	// SessionIdleTimeout is how long every voice channel in a raid may stay empty
	// of non-bot users (nobody connected) before the session auto-stops.
	// Default: 10m. Set SESSION_IDLE_TIMEOUT=0 to disable.
	SessionIdleTimeout time.Duration
	// Test holds optional test/debug audio overrides
	Test TestConfig
	// RvR holds the announcement, priority speaker and report settings.
	RvR RvRConfig
}

// RvRConfig holds the settings added for RvR raids.
type RvRConfig struct {
	// PrioritySpeaker makes the bots speak as Discord priority speakers, so
	// squad chatter is lowered while the relayed call plays. PRIORITY_SPEAKER,
	// default true.
	PrioritySpeaker bool
	// SoundsDir holds the Ogg Opus announcement files. SOUNDS_DIR, default /sounds.
	SoundsDir string
	// SoundOnStart is played when a raid starts. SOUND_ON_START, default "debut";
	// "none" disables it.
	SoundOnStart string
	// SoundOnStop is played before the bots leave on /stop. SOUND_ON_STOP,
	// default "fin"; "none" disables it.
	SoundOnStop string
	// ReportsDir receives the JSON quality reports. REPORTS_DIR, default
	// "reports" next to the store file; "none" disables saving.
	ReportsDir string
	// ReportLang is the language of the report posted in Discord (fr or en).
	// REPORT_LANG, default fr.
	ReportLang string
	// ReportToDiscord posts the quality report in the channel where /start
	// was used. REPORT_TO_DISCORD, default true.
	ReportToDiscord bool
	// DiagDir receives one detailed JSON Lines diagnostic file per raid.
	// DIAG_DIR, default "diagnostics" next to the store file; "none" disables
	// the files (health log lines are kept).
	DiagDir string
	// DiagInterval is the time between two diagnostic snapshots. DIAG_INTERVAL,
	// default 10s.
	DiagInterval time.Duration
	// SquadLeadRole is the role (name or ID) whose members call the raid leads
	// from their squad channel. SQUAD_LEAD_ROLE, default "Chef d'escouade";
	// "none" disables squad calls.
	SquadLeadRole string
	// RaidLeadRole is the role (name or ID) of the raid leads: the only members
	// who can join the raid lead channel and whose voice is relayed. Used when
	// no capture role was chosen with /setup. RAID_LEAD_ROLE, default
	// "Raid Lead"; "none" disables the fallback.
	RaidLeadRole string
	// SquadLeadKey: "priority" relays squad leads only with Discord's priority
	// push-to-talk key, "always" whenever they speak. SQUAD_LEAD_KEY, default priority.
	SquadLeadKey string
	// SquadLeadsToSquads also relays squad leads to the other squad channels
	// (each squad hears the raid leads and every other squad lead, never
	// itself), not only to the raid lead channel. SQUAD_LEADS_TO_SQUADS,
	// default false.
	SquadLeadsToSquads bool
	// Channels configures the raid channels created at each start.
	Channels ChannelsConfig
}

// ChannelsConfig holds the dynamic raid channel settings. One relay instance
// serves one faction.
type ChannelsConfig struct {
	// Enabled creates the category and voice channels when a raid starts and
	// deletes them when it stops. DYNAMIC_CHANNELS, default false.
	Enabled bool
	// Faction is the faction name used in the category name. FACTION_NAME.
	Faction string
	// FactionRole is the role (name or ID) that sees the channels; empty makes
	// them visible to everyone. FACTION_ROLE, default the faction name.
	FactionRole string
	// EventLabel names the event in category names, user commands and
	// messages, for games where "RvR" means nothing. EVENT_LABEL, default "RvR".
	EventLabel string
	// Category is the category name. RVR_CATEGORY, default "<label> <faction>".
	Category string
	// After is the existing category (name or ID) the raid category is placed
	// right below. RVR_CATEGORY_AFTER, default empty (bottom of the list).
	After string
	// RaidLeadName is the raid lead channel name. RAID_LEAD_CHANNEL, default "Raid Lead".
	RaidLeadName string
	// SquadName prefixes the numbered squad channels. SQUAD_CHANNEL, default "Escouade".
	SquadName string
	// SquadCount is the number of squad channels, capped by the speaker bots.
	// SQUAD_COUNT, default 10.
	SquadCount int
	// Region pins the Discord voice region of every channel (for example
	// "rotterdam"); empty lets Discord choose. VOICE_REGION.
	Region string
	// StateFile remembers the channels created, next to the store file.
	// AccessRole (name or ID), when set, replaces FactionRole as the role that
	// sees and joins the raid channels: only members vetted by the RvR board.
	// ACCESS_ROLE, default none.
	AccessRole string
	// BoardRole (name or ID) may grant and revoke AccessRole from Discord
	// (right click on a member > Apps). BOARD_ROLE, default none.
	BoardRole string
	// AccessChannel (name or ID) is a text channel where the board grants
	// access by mentioning players or pasting their usernames ("retirer" first
	// to revoke). Needs the Message Content intent. ACCESS_CHANNEL, default none.
	AccessChannel string
	StateFile     string
	// ClassRoles lists the class roles (names or IDs, comma separated) used
	// to sort waiting players into the squads. CLASS_ROLES, default none:
	// class sorting is off.
	ClassRoles []string
	// WaitingChannel is the voice channel (name or ID) players wait in before
	// being sorted; created with the raid channels when Enabled. WAITING_CHANNEL,
	// default none: class sorting is off.
	WaitingChannel string
	// ClassRule is the composition rule (see classsort.ParseRule). CLASS_RULE,
	// default "balanced".
	ClassRule string
}

const defaultSessionIdleTimeout = 10 * time.Minute

var speakerTokenPattern = regexp.MustCompile(`^DISCORD_SPEAKER_BOT_TOKEN_(\d+)$`)

// Load reads configuration from a .env file (if present) and then from environment variables.
// Environment variables already set in the process take precedence over the .env file.
//
// Owner bot:
//
//	DISCORD_OWNER_BOT_TOKEN  (required)
//
// Speaker pool (at least one recommended):
//
//	DISCORD_SPEAKER_BOT_TOKEN_1
//	DISCORD_SPEAKER_BOT_TOKEN_2
//	... (any numeric suffix; gaps in numbering are supported)
func Load() (*Config, error) {
	// Load .env file if it exists; silently skip if not found.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("error reading .env file: %w", err)
	}

	ownerToken := os.Getenv("DISCORD_OWNER_BOT_TOKEN")
	if ownerToken == "" {
		return nil, fmt.Errorf("DISCORD_OWNER_BOT_TOKEN environment variable is required")
	}

	speakerTokens := loadSpeakerTokens()
	if len(speakerTokens) == 0 {
		slog.Warn("no speaker tokens configured; voice relay will not work — set DISCORD_SPEAKER_1_BOT_TOKEN (and _2, _3 …)")
	}

	return &Config{
		OwnerBotToken:      ownerToken,
		SpeakerTokens:      speakerTokens,
		StorePath:          storePath(),
		OtelEndpoint:       os.Getenv("OTEL_ENDPOINT"),
		LogLevel:           parseLogLevel(os.Getenv("LOG_LEVEL")),
		VoiceLogLevel:      parseVoiceLogLevel(os.Getenv("VOICE_LOG_LEVEL")),
		SessionIdleTimeout: parseSessionIdleTimeout(os.Getenv("SESSION_IDLE_TIMEOUT")),
		Test: TestConfig{
			AllowBots: os.Getenv("TEST_ALLOW_BOTS") == "true",
		},
		RvR: loadRvR(storePath()),
	}, nil
}

func loadRvR(store string) RvRConfig {
	reports := envOr("REPORTS_DIR", filepath.Join(filepath.Dir(store), "reports"))
	diagDir := envOr("DIAG_DIR", filepath.Join(filepath.Dir(store), "diagnostics"))
	diagInterval := 10 * time.Second
	if v := strings.TrimSpace(os.Getenv("DIAG_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Second {
			diagInterval = d
		} else {
			slog.Warn("invalid DIAG_INTERVAL; using default", slog.String("value", v), slog.Duration("default", diagInterval))
		}
	}
	return RvRConfig{
		PrioritySpeaker:    parseBool("PRIORITY_SPEAKER", true),
		SoundsDir:          envOr("SOUNDS_DIR", "/sounds"),
		SoundOnStart:       noneToEmpty(envOr("SOUND_ON_START", "debut")),
		SoundOnStop:        noneToEmpty(envOr("SOUND_ON_STOP", "fin")),
		ReportsDir:         noneToEmpty(reports),
		ReportLang:         strings.ToLower(envOr("REPORT_LANG", "fr")),
		ReportToDiscord:    parseBool("REPORT_TO_DISCORD", true),
		DiagDir:            noneToEmpty(diagDir),
		DiagInterval:       diagInterval,
		SquadLeadRole:      noneToEmpty(envOr("SQUAD_LEAD_ROLE", "Chef d'escouade")),
		RaidLeadRole:       noneToEmpty(envOr("RAID_LEAD_ROLE", "Raid Lead")),
		SquadLeadKey:       squadKey(envOr("SQUAD_LEAD_KEY", "priority")),
		SquadLeadsToSquads: parseBool("SQUAD_LEADS_TO_SQUADS", false),
		Channels:           loadChannels(store),
	}
}

func loadChannels(store string) ChannelsConfig {
	faction := envOr("FACTION_NAME", "")
	label := envOr("EVENT_LABEL", "RvR")
	category := envOr("RVR_CATEGORY", strings.TrimSpace(label+" "+faction))
	count := 10
	if v := strings.TrimSpace(os.Getenv("SQUAD_COUNT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 45 {
			count = n
		} else {
			slog.Warn("invalid SQUAD_COUNT (1 to 45); using default", slog.String("value", v), slog.Int("default", count))
		}
	}
	return ChannelsConfig{
		Enabled:       parseBool("DYNAMIC_CHANNELS", false),
		Faction:       faction,
		EventLabel:    label,
		FactionRole:   noneToEmpty(envOr("FACTION_ROLE", faction)),
		Category:      category,
		After:         noneToEmpty(envOr("RVR_CATEGORY_AFTER", "")),
		RaidLeadName:  envOr("RAID_LEAD_CHANNEL", "Raid Lead"),
		SquadName:     envOr("SQUAD_CHANNEL", "Escouade"),
		SquadCount:    count,
		Region:        noneToEmpty(envOr("VOICE_REGION", "")),
		StateFile:     filepath.Join(filepath.Dir(store), "rvr-channels.json"),
		AccessRole:    noneToEmpty(envOr("ACCESS_ROLE", "")),
		BoardRole:     noneToEmpty(envOr("BOARD_ROLE", "")),
		AccessChannel: noneToEmpty(strings.TrimPrefix(envOr("ACCESS_CHANNEL", ""), "#")),

		ClassRoles:     splitList(noneToEmpty(envOr("CLASS_ROLES", ""))),
		WaitingChannel: noneToEmpty(strings.TrimPrefix(envOr("WAITING_CHANNEL", ""), "#")),
		ClassRule:      envOr("CLASS_RULE", "balanced"),
	}
}

// splitList splits a comma or semicolon separated list, dropping blanks.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func squadKey(v string) string {
	switch strings.ToLower(v) {
	case "always", "toujours":
		return "always"
	case "priority", "prioritaire":
		return "priority"
	default:
		slog.Warn("invalid SQUAD_LEAD_KEY; using priority", slog.String("value", v))
		return "priority"
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func noneToEmpty(v string) string {
	if strings.EqualFold(v, "none") || strings.EqualFold(v, "off") {
		return ""
	}
	return v
}

// parseBool reads a boolean variable; empty or invalid input keeps def.
func parseBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		slog.Warn("invalid boolean setting; using default", slog.String("key", key), slog.String("value", v), slog.Bool("default", def))
		return def
	}
	return b
}

// parseSessionIdleTimeout parses SESSION_IDLE_TIMEOUT (e.g. "10m", "0s") into a
// Duration. Empty defaults to defaultSessionIdleTimeout; "0" disables.
// Invalid input falls back to the default with a warning.
func parseSessionIdleTimeout(s string) time.Duration {
	if s == "" {
		return defaultSessionIdleTimeout
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		slog.Warn("invalid SESSION_IDLE_TIMEOUT; using default",
			slog.String("value", s),
			slog.Duration("default", defaultSessionIdleTimeout),
			slog.Any("err", err),
		)
		return defaultSessionIdleTimeout
	}
	return d
}

// loadSpeakerTokens scans all environment variables for DISCORD_SPEAKER_N_BOT_TOKEN
// keys (any numeric N), sorts them by index, and returns the tokens in order.
// Gaps in numbering (e.g. _1 and _3 with no _2) are silently skipped.
func loadSpeakerTokens() []string {
	type indexedToken struct {
		index int
		token string
	}
	var indexed []indexedToken
	for _, env := range os.Environ() {
		eqIdx := strings.IndexByte(env, '=')
		if eqIdx < 0 {
			continue
		}
		key, val := env[:eqIdx], env[eqIdx+1:]
		if val == "" {
			continue
		}
		m := speakerTokenPattern.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		indexed = append(indexed, indexedToken{idx, val})
	}
	sort.Slice(indexed, func(i, j int) bool {
		return indexed[i].index < indexed[j].index
	})
	tokens := make([]string, 0, len(indexed))
	for _, t := range indexed {
		tokens = append(tokens, t.token)
	}
	return tokens
}

// storePath returns the YAML store file path from STORE_PATH, defaulting to "store.yaml".
func storePath() string {
	if p := os.Getenv("STORE_PATH"); p != "" {
		return p
	}
	return "store.yaml"
}

// parseVoiceLogLevel parses VOICE_LOG_LEVEL (debug, info, warn, error, or off)
// into a level for disgo's voice logger. Empty, "off" and invalid input all
// disable it; the per-packet bursts it emits cost far more than they are worth
// unless someone is actively debugging audio.
func parseVoiceLogLevel(s string) slog.Level {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || strings.EqualFold(trimmed, "off") || strings.EqualFold(trimmed, "none") {
		return telemetry.LevelVoiceOff
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(trimmed))); err != nil {
		slog.Warn("invalid VOICE_LOG_LEVEL; voice logs stay disabled",
			slog.String("value", s),
		)
		return telemetry.LevelVoiceOff
	}
	return l
}

// parseLogLevel parses a log level string (debug, info, warn, error) into slog.Level.
// Defaults to LevelInfo on empty or invalid input.
func parseLogLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(s))); err != nil {
		return slog.LevelInfo
	}
	return l
}
