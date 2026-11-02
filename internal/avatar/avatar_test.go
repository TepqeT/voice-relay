package avatar

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
)

type fakeUsers struct{ calls []discord.UserUpdate }

func (f *fakeUsers) UpdateCurrentUser(u discord.UserUpdate, _ ...rest.RequestOpt) (*discord.OAuth2User, error) {
	f.calls = append(f.calls, u)
	return &discord.OAuth2User{}, nil
}

func TestImages(t *testing.T) {
	png := []byte("\x89PNG")
	for n := 0; n <= 11; n++ {
		if b := Image(n); !bytes.HasPrefix(b, png) {
			t.Errorf("no PNG for bot %d", n)
		}
	}
	if Image(12) != nil {
		t.Error("no picture beyond 11 relay bots")
	}
}

func TestRelayNumber(t *testing.T) {
	for name, want := range map[string]int{"Voice Relay 7": 7, "Voice Relay 11": 11, "Voice Relay": 3} {
		if got := RelayNumber(name, 3); got != want {
			t.Errorf("RelayNumber(%q) = %d, want %d", name, got, want)
		}
	}
}

// The picture is uploaded once, even across restarts.
func TestApplyOnce(t *testing.T) {
	file := filepath.Join(t.TempDir(), "avatars.json")
	u := &fakeUsers{}
	if err := NewKeeper(file).Apply(context.Background(), u, 42, Image(1)); err != nil {
		t.Fatal(err)
	}
	if len(u.calls) != 1 || u.calls[0].Avatar.IsZero() {
		t.Fatalf("calls %+v: want one avatar update", u.calls)
	}
	if err := NewKeeper(file).Apply(context.Background(), u, 42, Image(1)); err != nil {
		t.Fatal(err)
	}
	if len(u.calls) != 1 {
		t.Fatal("the same picture must not be uploaded again after a restart")
	}
	if err := NewKeeper(file).Apply(context.Background(), u, 42, Image(2)); err != nil || len(u.calls) != 2 {
		t.Fatal("a new picture must be uploaded")
	}
}
