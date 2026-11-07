package config

import (
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/TepqeT/voice-relay/internal/telemetry"
)

func TestParseVoiceLogLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"", telemetry.LevelVoiceOff},
		{"off", telemetry.LevelVoiceOff},
		{"OFF", telemetry.LevelVoiceOff},
		{"none", telemetry.LevelVoiceOff},
		{"nonsense", telemetry.LevelVoiceOff},
		{"warn", slog.LevelWarn},
		{" Error ", slog.LevelError},
		{"debug", slog.LevelDebug},
	}

	for _, tt := range tests {
		if got := parseVoiceLogLevel(tt.in); got != tt.want {
			t.Errorf("parseVoiceLogLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseSessionIdleTimeout(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", defaultSessionIdleTimeout},
		{"0", 0},
		{"90s", 90 * time.Second},
		{"soon", defaultSessionIdleTimeout},
	}

	for _, tt := range tests {
		if got := parseSessionIdleTimeout(tt.in); got != tt.want {
			t.Errorf("parseSessionIdleTimeout(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"nonsense", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"WARN", slog.LevelWarn},
	}

	for _, tt := range tests {
		if got := parseLogLevel(tt.in); got != tt.want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestStorePath(t *testing.T) {
	t.Setenv("STORE_PATH", "")
	if got := storePath(); got != "store.yaml" {
		t.Errorf("storePath() with STORE_PATH unset = %q, want store.yaml", got)
	}

	t.Setenv("STORE_PATH", "/data/store.yaml")
	if got := storePath(); got != "/data/store.yaml" {
		t.Errorf("storePath() = %q, want /data/store.yaml", got)
	}
}

func TestLoadRvRDefaults(t *testing.T) {
	for _, k := range []string{"PRIORITY_SPEAKER", "SOUNDS_DIR", "SOUND_ON_START", "SOUND_ON_STOP", "REPORTS_DIR", "REPORT_LANG", "REPORT_TO_DISCORD", "DIAG_DIR", "DIAG_INTERVAL", "SQUAD_LEAD_ROLE", "SQUAD_LEAD_KEY", "RAID_LEAD_ROLE", "SQUAD_LEADS_TO_SQUADS", "PANEL_CHANNEL",
		"DYNAMIC_CHANNELS", "RVR_CATEGORY", "RAID_LEAD_CHANNEL", "SQUAD_CHANNEL", "SQUAD_COUNT", "VOICE_REGION", "RVR_CATEGORY_AFTER", "ACCESS_ROLE", "BOARD_ROLE", "ACCESS_CHANNEL", "CLASS_ROLES", "WAITING_CHANNEL", "CLASS_RULE", "EVENT_LABEL",
		"SPEAKER_ROLE", "SPEAKER_CHANNEL", "LISTENER_CHANNEL", "LISTENER_COUNT", "CATEGORY", "CATEGORY_AFTER"} {
		t.Setenv(k, "")
	}
	got := loadRvR("/data/store.yaml")
	want := RvRConfig{
		PrioritySpeaker: true,
		SoundsDir:       "/sounds",
		ReportsDir:      "/data/reports",
		ReportLang:      "en",
		ReportToDiscord: true,
		DiagDir:         "/data/diagnostics",
		DiagInterval:    10 * time.Second,
		SquadLeadRole:   "Squad Lead",
		RaidLeadRole:    "Speaker",
		SquadLeadKey:    "always",
		Channels: ChannelsConfig{
			EventLabel:   "Voice Relay",
			Category:     "Voice Relay",
			RaidLeadName: "Speaker",
			SquadName:    "Listener",
			SquadCount:   MaxSquads,
			StateFile:    "/data/rvr-channels.json",
			ClassRule:    "balanced",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadRvR defaults = %+v, want %+v", got, want)
	}
}

func TestLoadRvROverrides(t *testing.T) {
	t.Setenv("PRIORITY_SPEAKER", "false")
	t.Setenv("SOUND_ON_START", "none")
	t.Setenv("SOUND_ON_STOP", "OFF")
	t.Setenv("REPORTS_DIR", "none")
	t.Setenv("REPORT_LANG", "EN")
	t.Setenv("REPORT_TO_DISCORD", "maybe") // invalid: keeps the default
	t.Setenv("DIAG_DIR", "none")
	t.Setenv("DIAG_INTERVAL", "5s")
	t.Setenv("SQUAD_LEAD_ROLE", "none")
	t.Setenv("SQUAD_LEAD_KEY", "Toujours")
	t.Setenv("RAID_LEAD_ROLE", "1234")
	got := loadRvR("store.yaml")
	if got.SquadLeadRole != "" || got.SquadLeadKey != "always" || got.RaidLeadRole != "1234" {
		t.Fatalf("squad overrides not applied: %+v", got)
	}
	if got.PrioritySpeaker || got.SoundOnStart != "" || got.SoundOnStop != "" || got.ReportsDir != "" || got.ReportLang != "en" || !got.ReportToDiscord || got.DiagDir != "" || got.DiagInterval != 5*time.Second {
		t.Fatalf("overrides not applied: %+v", got)
	}
}

func TestLoadChannels(t *testing.T) {
	for _, k := range []string{"EVENT_LABEL", "DYNAMIC_CHANNELS", "RVR_CATEGORY", "RAID_LEAD_CHANNEL", "SQUAD_CHANNEL", "SQUAD_COUNT", "VOICE_REGION",
		"CATEGORY", "SPEAKER_CHANNEL", "LISTENER_CHANNEL", "LISTENER_COUNT"} {
		t.Setenv(k, "")
	}
	c := loadChannels("/data/relay/store.yaml")
	if c.Enabled || c.SquadCount != MaxSquads || c.RaidLeadName != "Speaker" || c.SquadName != "Listener" || c.Category != "Voice Relay" {
		t.Fatalf("defaults = %+v", c)
	}
	if c.StateFile != "/data/relay/rvr-channels.json" {
		t.Fatalf("StateFile = %q", c.StateFile)
	}
	t.Setenv("DYNAMIC_CHANNELS", "true")
	t.Setenv("LISTENER_COUNT", "99")
	if c = loadChannels("store.yaml"); !c.Enabled || c.SquadCount != MaxSquads {
		t.Fatalf("invalid count = %+v", c)
	}
	t.Setenv("LISTENER_COUNT", "all")
	if c = loadChannels("store.yaml"); c.SquadCount != MaxSquads {
		t.Fatalf("all = %d", c.SquadCount)
	}
	t.Setenv("LISTENER_COUNT", "4")
	if c = loadChannels("store.yaml"); c.SquadCount != 4 {
		t.Fatalf("count = %+v", c)
	}
	// Former variable names still work.
	t.Setenv("LISTENER_COUNT", "")
	t.Setenv("SQUAD_COUNT", "3")
	t.Setenv("RAID_LEAD_CHANNEL", "Lead")
	if c = loadChannels("store.yaml"); c.SquadCount != 3 || c.RaidLeadName != "Lead" {
		t.Fatalf("legacy names = %+v", c)
	}
	t.Setenv("SPEAKER_CHANNEL", "Stage")
	if c = loadChannels("store.yaml"); c.RaidLeadName != "Stage" {
		t.Fatal("the new name must win over the former one")
	}
}

func TestLoadClassSort(t *testing.T) {
	for _, k := range []string{"CLASS_ROLES", "WAITING_CHANNEL", "CLASS_RULE"} {
		t.Setenv(k, "")
	}
	c := loadChannels("store.yaml")
	if len(c.ClassRoles) != 0 || c.WaitingChannel != "" || c.ClassRule != "balanced" {
		t.Fatalf("defaults = %+v", c)
	}
	t.Setenv("CLASS_ROLES", " Tank, Healer ;;123456789012345678 ,")
	t.Setenv("WAITING_CHANNEL", "#Waiting")
	t.Setenv("CLASS_RULE", "Healer=2; *=1")
	c = loadChannels("store.yaml")
	if got, want := c.ClassRoles, []string{"Tank", "Healer", "123456789012345678"}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("ClassRoles = %q, want %q", got, want)
	}
	if c.WaitingChannel != "Waiting" || c.ClassRule != "Healer=2; *=1" {
		t.Fatalf("overrides = %+v", c)
	}
	t.Setenv("CLASS_ROLES", "none")
	if c = loadChannels("store.yaml"); len(c.ClassRoles) != 0 {
		t.Fatalf("none = %q", c.ClassRoles)
	}
}

func TestEventLabel(t *testing.T) {
	t.Setenv("RVR_CATEGORY", "")
	t.Setenv("CATEGORY", "")
	t.Setenv("EVENT_LABEL", "")
	if c := loadChannels("store.yaml"); c.EventLabel != "Voice Relay" || c.Category != "Voice Relay" {
		t.Fatalf("default = %q / %q", c.EventLabel, c.Category)
	}
	t.Setenv("EVENT_LABEL", "Briefing")
	if c := loadChannels("store.yaml"); c.EventLabel != "Briefing" || c.Category != "Briefing" {
		t.Fatalf("custom = %q / %q", c.EventLabel, c.Category)
	}
	t.Setenv("CATEGORY", "Ops")
	if c := loadChannels("store.yaml"); c.Category != "Ops" {
		t.Fatalf("CATEGORY must win, got %q", c.Category)
	}
}

func TestLoadWeb(t *testing.T) {
	t.Setenv("SITE_ADMINS", "")
	t.Setenv("SUBSCRIPTION_REQUIRED", "")
	t.Setenv("SUBSCRIPTIONS_FILE", "")
	t.Setenv("WEB_ADDR", "")
	t.Setenv("WEB_BASE_URL", "")
	t.Setenv("DISCORD_CLIENT_SECRET", "")
	if loadWeb("/data/store.yaml").Enabled() {
		t.Fatal("site on by default")
	}
	t.Setenv("WEB_ADDR", ":8080")
	if loadWeb("/data/store.yaml").Enabled() {
		t.Fatal("site on without base URL and secret")
	}
	t.Setenv("WEB_BASE_URL", "https://relais.example.fr/ ")
	t.Setenv("DISCORD_CLIENT_SECRET", "s3cret")
	w := loadWeb("/data/store.yaml")
	if !w.Enabled() || w.BaseURL != "https://relais.example.fr" {
		t.Fatalf("web = %+v", w)
	}
	if w.SubscriptionsFile != "/data/subscriptions.json" || w.SubscriptionRequired || len(w.Admins) != 0 {
		t.Fatalf("subscription defaults = %+v", w)
	}
	t.Setenv("SITE_ADMINS", "111111111111111111, 222222222222222222")
	t.Setenv("SUBSCRIPTION_REQUIRED", "true")
	if w = loadWeb("/data/store.yaml"); len(w.Admins) != 2 || !w.SubscriptionRequired {
		t.Fatalf("subscription settings = %+v", w)
	}
}

func TestLoadWebLinks(t *testing.T) {
	t.Setenv("SUPPORT_URL", "https://discord.com/users/123")
	t.Setenv("PAYPAL_URL", "http://paypal.me/example")
	t.Setenv("DONATION_AMOUNTS", "")
	w := loadWeb("/data/store.yaml")
	if w.SupportURL != "https://discord.com/users/123" || w.PayPalURL != "" || w.DonationAmounts != "3,5,10,20" {
		t.Fatalf("links = %+v (plain http must be dropped)", w)
	}
	if w.CheckoutEnabled() {
		t.Fatal("donations on without PayPal credentials")
	}
	t.Setenv("PAYPAL_CLIENT_ID", "id")
	t.Setenv("PAYPAL_CLIENT_SECRET", "secret")
	t.Setenv("DONATION_AMOUNTS", "2, 4")
	if w = loadWeb("/data/store.yaml"); !w.CheckoutEnabled() || w.DonationAmounts != "2, 4" {
		t.Fatalf("donations = %+v", w)
	}
	t.Setenv("PAYPAL_URL", "https://www.paypal.com/paypalme/example")
	if w = loadWeb("/data/store.yaml"); w.PayPalURL != "https://www.paypal.com/paypalme/example" {
		t.Fatalf("paypal = %q", w.PayPalURL)
	}
	t.Setenv("SUPPORT_URL", "discord.com/users/123")
	if w = loadWeb("/data/store.yaml"); w.SupportURL != "" {
		t.Fatalf("relative link kept: %q", w.SupportURL)
	}
}
