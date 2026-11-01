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
	for _, k := range []string{"PRIORITY_SPEAKER", "SOUNDS_DIR", "SOUND_ON_START", "SOUND_ON_STOP", "REPORTS_DIR", "REPORT_LANG", "REPORT_TO_DISCORD", "DIAG_DIR", "DIAG_INTERVAL", "SQUAD_LEAD_ROLE", "SQUAD_LEAD_KEY", "RAID_LEAD_ROLE", "SQUAD_LEADS_TO_SQUADS",
		"DYNAMIC_CHANNELS", "FACTION_NAME", "FACTION_ROLE", "RVR_CATEGORY", "RAID_LEAD_CHANNEL", "SQUAD_CHANNEL", "SQUAD_COUNT", "VOICE_REGION", "RVR_CATEGORY_AFTER", "ACCESS_ROLE", "BOARD_ROLE", "ACCESS_CHANNEL", "CLASS_ROLES", "WAITING_CHANNEL", "CLASS_RULE"} {
		t.Setenv(k, "")
	}
	got := loadRvR("/data/store.yaml")
	want := RvRConfig{
		PrioritySpeaker: true,
		SoundsDir:       "/sounds",
		SoundOnStart:    "debut",
		SoundOnStop:     "fin",
		ReportsDir:      "/data/reports",
		ReportLang:      "fr",
		ReportToDiscord: true,
		DiagDir:         "/data/diagnostics",
		DiagInterval:    10 * time.Second,
		SquadLeadRole:   "Chef d'escouade",
		RaidLeadRole:    "Raid Lead",
		SquadLeadKey:    "priority",
		Channels: ChannelsConfig{
			Category:     "RvR",
			RaidLeadName: "Raid Lead",
			SquadName:    "Escouade",
			SquadCount:   10,
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
	for _, k := range []string{"DYNAMIC_CHANNELS", "FACTION_NAME", "FACTION_ROLE", "RVR_CATEGORY", "RAID_LEAD_CHANNEL", "SQUAD_CHANNEL", "SQUAD_COUNT", "VOICE_REGION"} {
		t.Setenv(k, "")
	}
	c := loadChannels("/data/asmos/store.yaml")
	if c.Enabled || c.SquadCount != 10 || c.RaidLeadName != "Raid Lead" || c.SquadName != "Escouade" || c.FactionRole != "" {
		t.Fatalf("defaults = %+v", c)
	}
	if c.StateFile != "/data/asmos/rvr-channels.json" {
		t.Fatalf("StateFile = %q", c.StateFile)
	}

	t.Setenv("DYNAMIC_CHANNELS", "true")
	t.Setenv("FACTION_NAME", "Asmodiens")
	t.Setenv("SQUAD_COUNT", "99")
	c = loadChannels("store.yaml")
	if !c.Enabled || c.FactionRole != "Asmodiens" || c.Category != "RvR Asmodiens" || c.SquadCount != 10 {
		t.Fatalf("faction = %+v", c)
	}
	t.Setenv("FACTION_ROLE", "none")
	t.Setenv("SQUAD_COUNT", "4")
	if c = loadChannels("store.yaml"); c.FactionRole != "" || c.SquadCount != 4 {
		t.Fatalf("overrides = %+v", c)
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
	t.Setenv("CLASS_ROLES", " Templier, Clerc ;;123456789012345678 ,")
	t.Setenv("WAITING_CHANNEL", "#Attente")
	t.Setenv("CLASS_RULE", "Clerc=2; *=1")
	c = loadChannels("store.yaml")
	if got, want := c.ClassRoles, []string{"Templier", "Clerc", "123456789012345678"}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("ClassRoles = %q, want %q", got, want)
	}
	if c.WaitingChannel != "Attente" || c.ClassRule != "Clerc=2; *=1" {
		t.Fatalf("overrides = %+v", c)
	}
	t.Setenv("CLASS_ROLES", "none")
	if c = loadChannels("store.yaml"); len(c.ClassRoles) != 0 {
		t.Fatalf("none = %q", c.ClassRoles)
	}
}
