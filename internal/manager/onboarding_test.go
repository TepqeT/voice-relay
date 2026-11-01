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
	"github.com/disgoorg/disgo/discord"
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

// unknownOnce refuses the first PUT the way Discord does for a deleted
// channel in default_channel_ids.
type unknownOnce struct {
	onboardingRest
	refused bool
	code    string // Discord error code, UNKNOWN_CHANNEL when empty
}

func (r *unknownOnce) Do(e *rest.CompiledEndpoint, rq any, rs any, opts ...rest.RequestOpt) error {
	err := r.onboardingRest.Do(e, rq, rs, opts...)
	if e.Endpoint.Method == http.MethodPut && !r.refused {
		r.refused = true
		code := r.code
		if code == "" {
			code = "UNKNOWN_CHANNEL"
		}
		return &rest.Error{Code: 50035, Message: "Invalid Form Body", Errors: []byte(`{"default_channel_ids":{"_errors":[{"code":"` + code + `"}]}}`)}
	}
	return err
}

// The category is never sent; a deleted channel in the server's list is
// dropped on the retry, the channels the bot sees are kept.
func TestOnboardingSkipsCategoryAndDeletedChannels(t *testing.T) {
	r := &unknownOnce{onboardingRest: onboardingRest{get: `{"enabled":true,"default_channel_ids":["5","7"]}`}}
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	var text discord.GuildTextChannel
	var cat discord.GuildCategoryChannel
	if err := json.Unmarshal([]byte(`{"id":"5","guild_id":"10","name":"general","type":0}`), &text); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"id":"50","guild_id":"10","name":"Voice Relay","type":4}`), &cat); err != nil {
		t.Fatal(err)
	}
	caches.AddChannel(text)
	caches.AddChannel(cat)
	m := NewService(store.NewInMemoryStore(), mapPool{}, &bot.Client{Caches: caches, Rest: r}, 1, config.TestConfig{}, nil)
	m.EnsureOnboardingDefaults(context.Background(), 10, 50, 100)
	if len(r.puts) != 2 {
		t.Fatalf("puts = %v, want a refused one then a retry", r.puts)
	}
	if r.puts[0] != `{"default_channel_ids":["5","7","100"]}` || r.puts[1] != `{"default_channel_ids":["5","100"]}` {
		t.Fatalf("puts = %v", r.puts)
	}
}

// A default channel of the server @everyone can no longer see is dropped on
// the retry; visible ones stay.
func TestOnboardingDropsChannelsHiddenFromEveryone(t *testing.T) {
	r := &unknownOnce{onboardingRest: onboardingRest{get: `{"enabled":true,"default_channel_ids":["5","6"]}`}, code: "DEFAULT_CHANNEL_REQUIRES_EVERYONE_ACCESS"}
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	caches.AddRole(discord.Role{ID: 10, GuildID: 10, Name: "@everyone", Permissions: discord.PermissionViewChannel})
	for _, raw := range []string{
		`{"id":"5","guild_id":"10","name":"rules","type":0}`,
		`{"id":"6","guild_id":"10","name":"staff","type":0,"permission_overwrites":[{"id":"10","type":0,"allow":"0","deny":"1024"}]}`,
	} {
		var ch discord.GuildTextChannel
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatal(err)
		}
		caches.AddChannel(ch)
	}
	m := NewService(store.NewInMemoryStore(), mapPool{}, &bot.Client{Caches: caches, Rest: r}, 1, config.TestConfig{}, nil)
	m.EnsureOnboardingDefaults(context.Background(), 10, 100)
	if len(r.puts) != 2 || r.puts[1] != `{"default_channel_ids":["5","100"]}` {
		t.Fatalf("puts = %v, want the hidden channel 6 dropped on the retry", r.puts)
	}
}
