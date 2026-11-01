package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/rest"

	"github.com/TepqeT/voice-relay/internal/config"
	"github.com/TepqeT/voice-relay/internal/store"
)

// onboardingRest answers GET and PUT /guilds/{id}/onboarding with raw JSON.
type onboardingRest struct {
	stubRest
	get    string // JSON returned by GET
	getErr error
	gets   int
	puts   []string // JSON bodies sent by PUT
}

func (r *onboardingRest) Do(e *rest.CompiledEndpoint, rq any, rs any, _ ...rest.RequestOpt) error {
	switch e.Endpoint.Method {
	case http.MethodGet:
		r.gets++
		if r.getErr != nil {
			return r.getErr
		}
		return json.Unmarshal([]byte(r.get), rs)
	case http.MethodPut:
		b, _ := json.Marshal(rq)
		r.puts = append(r.puts, string(b))
		return nil
	}
	return errors.New("unexpected " + e.Endpoint.Method)
}

func onboardingService(r *onboardingRest) *Service {
	c := &bot.Client{Caches: cache.New(cache.WithCaches(cache.FlagsAll)), Rest: r}
	return NewService(store.NewInMemoryStore(), mapPool{}, c, 1, config.TestConfig{}, nil)
}

// A real answer carries prompts with emojis the bot must neither decode nor
// send back.
const onboardingOn = `{"guild_id":"10","enabled":true,"mode":0,"default_channel_ids":["5","7"],
 "prompts":[{"id":"1","title":"Class","options":[{"id":"2","title":"Tank","emoji":{"id":null,"name":"x"},"channel_ids":[],"role_ids":[]}]}]}`

func TestOnboardingAddsMissingChannelsOnce(t *testing.T) {
	r := &onboardingRest{get: onboardingOn}
	m := onboardingService(r)
	m.EnsureOnboardingDefaults(context.Background(), 10, 7, 100, 0, 101)
	if len(r.puts) != 1 {
		t.Fatalf("puts = %d, want 1", len(r.puts))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(r.puts[0]), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"default_channel_ids": []any{"5", "7", "100", "101"}}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("PUT body = %s, want only the default channels with 100 and 101", r.puts[0])
	}
	m.EnsureOnboardingDefaults(context.Background(), 10, 7, 100, 0, 101)
	if r.gets != 1 || len(r.puts) != 1 {
		t.Fatalf("second call must cost nothing: gets=%d puts=%d", r.gets, len(r.puts))
	}
}

func TestOnboardingLeavesServersWithoutIt(t *testing.T) {
	r := &onboardingRest{get: `{"enabled":false,"default_channel_ids":[]}`}
	onboardingService(r).EnsureOnboardingDefaults(context.Background(), 10, 100)
	r2 := &onboardingRest{getErr: errors.New("missing access")}
	onboardingService(r2).EnsureOnboardingDefaults(context.Background(), 10, 100)
	r3 := &onboardingRest{get: `{"enabled":true,"default_channel_ids":["100"]}`}
	onboardingService(r3).EnsureOnboardingDefaults(context.Background(), 10, 100)
	if len(r.puts)+len(r2.puts)+len(r3.puts) != 0 {
		t.Fatal("no update when onboarding is off, unreadable or already complete")
	}
}
