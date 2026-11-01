package quality

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// keepReports is how many report files are kept on disk.
const keepReports = 200

type texts struct {
	title, duration, audio, noAudio, drops, latency, latencyDirect, dave, daveNone,
	links, logs, findings string
	net, playout, mix, overlap, servers, cpu, file string
	decimalComma                                   bool
	verdict                                        map[Level]string
	findingT                                       map[string]string
	modes                                          map[string]string
}

var french = texts{
	title:         "**Rapport RvR** : %s",
	duration:      "Durée : %s (%s)",
	audio:         "Voix captée : %s de parole (%d paquets)",
	noAudio:       "Voix captée : aucune",
	drops:         "Pertes : %s %% (%d paquets)",
	latency:       "Latence ajoutée par le relais : p50 %s ms, p95 %s ms",
	latencyDirect: "Latence ajoutée par le relais : relais direct, sans mixage",
	dave:          "Chiffrement Discord (DAVE) : %s %% déchiffré",
	daveNone:      "Chiffrement Discord (DAVE) : aucun paquet",
	links:         "Connexions : %d reconnexion(s), %d échec(s), latence Discord en fin de raid %s ms",
	logs:          "Logs : %d erreur(s), %d avertissement(s)",
	findings:      "À regarder :",
	net:           "Réseau des Raid Leads : %s %% perdus, %s %% dans le désordre, irrégularité max %s ms",
	playout:       "Diffusion : %d micro-coupure(s) sur %d salon(s)",
	mix:           "Mélange actif (plusieurs Raid Leads présents) : %s (%d fois)",
	overlap:       "Voix superposées : %s, jusqu'à %d en même temps",
	servers:       "Serveurs vocaux Discord : %d, région(s) : %d",
	cpu:           "Machine : processeur %s %% max, %s %% en moyenne",
	file:          "Diagnostic détaillé : %s",
	decimalComma:  true,
	verdict:       map[Level]string{LevelOK: "OK", LevelWatch: "à surveiller", LevelBad: "problème"},
	findingT: map[string]string{
		"no_audio":           "aucune voix captée : vérifier le rôle Raid Lead et le salon du bot caller",
		"drops":              "pertes audio élevées : machine ou connexion saturée",
		"dave":               "échecs de déchiffrement DAVE : relancer le raid si la voix grésille ou coupe",
		"latency":            "latence du relais élevée : machine trop chargée",
		"reconnects":         "un bot s'est reconnecté pendant le raid",
		"reconnect_failures": "un bot n'a pas pu se reconnecter : vérifier les logs",
		"gateway_latency":    "latence élevée vers Discord : connexion internet",
		"errors":             "erreurs dans les logs : docker compose logs",
		"warnings":           "beaucoup d'avertissements dans les logs",
		"network_loss":       "un Raid Lead perd des paquets : sa connexion internet est en cause",
		"reorder":            "paquets d'un Raid Lead arrivés dans le désordre : connexion instable",
		"jitter":             "arrivée irrégulière de la voix d'un Raid Lead : risque de hachures",
		"stalls":             "micro-coupures fréquentes dans les salons : machine ou connexion du bot",
		"voice_servers":      "les salons sont dans des régions Discord différentes : fixer la même région sur tous les salons",
		"cpu":                "processeur de la machine très chargé",
	},
	modes: map[string]string{"one_caller": "un caller", "guild_caller": "multi-caller", "one_many_guild_caller": "étoile"},
}

var english = texts{
	title:         "**RvR report**: %s",
	duration:      "Duration: %s (%s)",
	audio:         "Captured voice: %s of speech (%d packets)",
	noAudio:       "Captured voice: none",
	drops:         "Losses: %s %% (%d packets)",
	latency:       "Relay added latency: p50 %s ms, p95 %s ms",
	latencyDirect: "Relay added latency: direct relay, no mixing",
	dave:          "Discord encryption (DAVE): %s %% decrypted",
	daveNone:      "Discord encryption (DAVE): no packet",
	links:         "Connections: %d reconnect(s), %d failure(s), Discord latency at the end %s ms",
	logs:          "Logs: %d error(s), %d warning(s)",
	findings:      "To check:",
	net:           "Raid lead network: %s %% lost, %s %% out of order, max jitter %s ms",
	playout:       "Playout: %d micro-cut(s) over %d channel(s)",
	mix:           "Mix mode (several raid leads present): %s (%d times)",
	overlap:       "Overlapping voices: %s, up to %d at once",
	servers:       "Discord voice servers: %d, region(s): %d",
	cpu:           "Host: CPU %s %% max, %s %% average",
	file:          "Detailed diagnostic: %s",
	verdict:       map[Level]string{LevelOK: "OK", LevelWatch: "watch", LevelBad: "problem"},
	findingT: map[string]string{
		"no_audio":           "no voice captured: check the caller role and the caller bot channel",
		"drops":              "high audio loss: host or network saturated",
		"dave":               "DAVE decrypt failures: restart the raid if voice crackles or cuts",
		"latency":            "high relay latency: host overloaded",
		"reconnects":         "a bot reconnected during the raid",
		"reconnect_failures": "a bot failed to reconnect: check the logs",
		"gateway_latency":    "high latency to Discord: internet connection",
		"errors":             "errors in the logs: docker compose logs",
		"warnings":           "many warnings in the logs",
		"network_loss":       "a raid lead is losing packets: their internet connection",
		"reorder":            "a raid lead's packets arrive out of order: unstable connection",
		"jitter":             "a raid lead's voice arrives irregularly: risk of choppy audio",
		"stalls":             "frequent micro-cuts in the channels: bot host or connection",
		"voice_servers":      "channels are in different Discord regions: set the same region on every channel",
		"cpu":                "host CPU heavily loaded",
	},
	modes: map[string]string{"one_caller": "one caller", "guild_caller": "multi-caller", "one_many_guild_caller": "star"},
}

// Render formats the report as a Discord message. lang "en" selects English,
// anything else French.
func Render(r Report, lang string) string { return RenderWithLabel(r, lang, "RvR") }

// RenderWithLabel is Render with the event name of EVENT_LABEL in the title.
func RenderWithLabel(r Report, lang, label string) string {
	t := french
	if strings.HasPrefix(strings.ToLower(lang), "en") {
		t = english
	}
	num := func(v float64, prec int) string {
		s := fmt.Sprintf("%.*f", prec, v)
		if t.decimalComma {
			s = strings.ReplaceAll(s, ".", ",")
		}
		return s
	}
	var b strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
		b.WriteByte('\n')
	}
	if label = strings.TrimSpace(label); label == "" {
		label = "RvR"
	}
	line(strings.Replace(t.title, "RvR", label, 1), t.verdict[r.Verdict])
	mode := t.modes[r.Mode]
	if mode == "" {
		mode = r.Mode
	}
	line(t.duration, humanDuration(time.Duration(r.DurationSec*float64(time.Second))), mode)
	if r.FramesReceived > 0 {
		line(t.audio, humanDuration(time.Duration(r.VoiceSec*float64(time.Second))), r.FramesReceived)
		line(t.drops, num(r.DropPct, 2), r.FramesDropped)
	} else {
		line("%s", t.noAudio)
	}
	if r.LatencySamples > 0 {
		line(t.latency, num(r.LatencyP50Ms, 0), num(r.LatencyP95Ms, 0))
	} else {
		line("%s", t.latencyDirect)
	}
	if attempts := r.DaveSuccess + r.DaveDropped + r.DaveFailure; attempts > 0 {
		line(t.dave, num(100-r.DaveFailPct, 2))
	} else {
		line("%s", t.daveNone)
	}
	line(t.links, r.Reconnects, r.ReconnectFailures, num(r.GatewayLatencyEndMs, 0))
	line(t.logs, r.Errors, r.Warnings)
	if d := r.Diag; d != nil {
		if w, ok := d.WorstSource(); ok {
			line(t.net, num(w.LostPct, 2), num(w.ReorderPct, 2), num(d.MaxJitterMs(), 0))
		}
		if len(d.Outputs) > 0 {
			line(t.playout, d.Stalls(), len(d.Outputs))
		}
		if d.MixSwitches > 0 {
			line(t.mix, humanDuration(time.Duration(d.MixSeconds*float64(time.Second))), d.MixSwitches)
		}
		if d.OverlapSeconds > 0 {
			line(t.overlap, humanDuration(time.Duration(d.OverlapSeconds*float64(time.Second))), d.MaxSimultaneous)
		}
		if d.DistinctVoiceServers > 0 {
			line(t.servers, d.DistinctVoiceServers, d.DistinctRegions)
		}
		line(t.cpu, num(d.CPUMaxPct, 0), num(d.CPUAvgPct, 0))
		if d.File != "" {
			line(t.file, filepath.Base(d.File))
		}
	}
	if len(r.Findings) > 0 {
		line("%s", t.findings)
		for _, f := range r.Findings {
			line("- %s", t.findingT[f.Code])
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%d h %02d min", h, m)
	case m > 0:
		return fmt.Sprintf("%d min %02d s", m, s)
	default:
		return fmt.Sprintf("%d s", s)
	}
}

// Save writes the report as JSON into dir and keeps only the most recent
// files. It returns the written path.
func Save(dir string, r Report) (string, error) {
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create reports directory: %w", err)
	}
	name := fmt.Sprintf("rvr-%s-%s.json", r.End.UTC().Format("20060102-150405"), r.GuildID)
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write report: %w", err)
	}
	prune(dir)
	return path, nil
}

func prune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "rvr-") && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, e.Name())
		}
	}
	if len(files) <= keepReports {
		return
	}
	sort.Strings(files) // names start with the UTC timestamp
	for _, f := range files[:len(files)-keepReports] {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			slog.Warn("quality: failed to prune old report", slog.String("file", f), slog.Any("err", err))
		}
	}
}

// LogAttrs returns the report as structured log attributes, for the one-line
// summary written when a raid ends.
func (r Report) LogAttrs() []any {
	return []any{
		slog.String("guildID", r.GuildID),
		slog.String("verdict", string(r.Verdict)),
		slog.Float64("durationSec", r.DurationSec),
		slog.Int64("framesReceived", r.FramesReceived),
		slog.Int64("framesDropped", r.FramesDropped),
		slog.Int("outputs", r.Outputs),
		slog.Float64("dropPct", r.DropPct),
		slog.Float64("latencyP95Ms", r.LatencyP95Ms),
		slog.Float64("daveFailPct", r.DaveFailPct),
		slog.Int64("reconnects", r.Reconnects),
		slog.Int64("errors", r.Errors),
		slog.Int64("warnings", r.Warnings),
	}
}
