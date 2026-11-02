// Package avatar gives every Voice Relay bot its profile picture: the main
// bot the plain logo, each relay bot the logo on its own colour with its
// number. Applied by the bots themselves through the API, so nobody has to
// upload pictures in the developer portal.
package avatar

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/statefile"
)

//go:embed img/*.png
var images embed.FS

// Image is the picture of the main bot (n == 0) or of relay bot n (1 to 11),
// nil when there is none for n.
func Image(n int) []byte {
	name := "img/main.png"
	if n > 0 {
		name = fmt.Sprintf("img/relay-%02d.png", n)
	}
	b, err := images.ReadFile(name)
	if err != nil {
		return nil
	}
	return b
}

var relayNumber = regexp.MustCompile(`(\d+)\s*$`)

// RelayNumber reads the relay number at the end of a bot name ("Voice
// Relay 7" gives 7), fallback when the name has none.
func RelayNumber(name string, fallback int) int {
	if m := relayNumber.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// Users is the part of the REST API that changes the bot's own profile.
type Users interface {
	UpdateCurrentUser(u discord.UserUpdate, opts ...rest.RequestOpt) (*discord.OAuth2User, error)
}

// Keeper remembers, per bot, which picture it last set (data/avatars.json),
// so a restart does not upload it again: Discord limits avatar changes.
type Keeper struct {
	file string
	mu   sync.Mutex
	set  map[string]string // bot ID -> sha256 of the picture
}

// NewKeeper loads the record kept in file ("" keeps it in memory).
func NewKeeper(file string) *Keeper {
	k := &Keeper{file: file, set: map[string]string{}}
	if file != "" {
		if b, err := statefile.ReadFile(file); err == nil {
			_ = json.Unmarshal(b, &k.set)
		}
	}
	return k
}

// Apply sets img as botID's avatar unless it is already the one set.
func (k *Keeper) Apply(ctx context.Context, users Users, botID snowflake.ID, img []byte) error {
	if len(img) == 0 {
		return nil
	}
	sum := sha256.Sum256(img)
	h := hex.EncodeToString(sum[:])
	k.mu.Lock()
	done := k.set[botID.String()] == h
	k.mu.Unlock()
	if done {
		return nil
	}
	icon := discord.NewIconRaw(discord.IconTypePNG, img)
	if _, err := users.UpdateCurrentUser(discord.UserUpdate{Avatar: omit.New(icon)}, rest.WithCtx(ctx)); err != nil {
		return err
	}
	k.mu.Lock()
	k.set[botID.String()] = h
	k.save()
	k.mu.Unlock()
	slog.Info("avatar: profile picture set", slog.String("botID", botID.String()))
	return nil
}

func (k *Keeper) save() {
	if k.file == "" {
		return
	}
	b, err := json.Marshal(k.set)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(k.file), statefile.DirPerm); err == nil {
			err = os.WriteFile(k.file, b, 0o600)
		}
	}
	if err != nil {
		slog.Warn("avatar: record not saved", slog.String("file", k.file), slog.Any("err", err))
	}
}
