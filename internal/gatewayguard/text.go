package gatewayguard

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/statefile"
)

// alertText is the message sent to the operator (French: the operator is).
func alertText(halt bool, hour, day, haltAt int, dump string) string {
	where := "aucun diagnostic enregistré"
	if dump != "" {
		where = "état des goroutines enregistré dans " + dump
	}
	if halt {
		return fmt.Sprintf("Voice Relay : ce bot a ouvert %d sessions Discord en 24 h. Sa connexion est coupée pour que Discord ne révoque pas son token. "+
			"Il reviendra au prochain déploiement d'une nouvelle version. %s.", day, where)
	}
	return fmt.Sprintf("Voice Relay : ce bot a ouvert %d sessions Discord en une heure (normal : 0 à 2), sans doute une boucle de reconnexion. "+
		"Au-delà de %d en 24 h (%d pour l'instant), sa connexion sera coupée pour protéger son token. %s.", hour, haltAt, day, where)
}

// WriteGoroutines writes the stack of every goroutine into dir and returns the
// file path. One file per call, named after the bot and the time.
func WriteGoroutines(dir string, botID snowflake.ID, now time.Time) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("no diagnostics directory")
	}
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, statefile.DirPerm); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("goroutines-%s-%s.txt", botID, now.UTC().Format("20060102-150405")))
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
