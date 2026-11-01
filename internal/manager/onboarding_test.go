package manager

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/config"
	"github.com/TepqeT/voice-relay/internal/store"
)

type onboardingRest struct {
	stubRest
	ob      *discord.GuildOnboarding
	getErr  error
	gets    int
	updates []discord.GuildOnboardingUpdate
}

func (r *onboardingRest) GetGuildOnboarding(snowflake.ID, ...rest.RequestOpt) (*discord.GuildOnboarding, error) {
	r.gets++
	return r.ob, r.getErr
}

func (r *onboardingRest) UpdateGuildOnboarding(_ snowflake.ID, u discord.GuildOnboardingUpdate, _ ...rest.RequestOpt) (*discord.GuildOnboarding, error) {
	r.updates = append(r.updates, u)
	return r.ob, nil
}

func onboardingService(r *onboardingRest) *Service {
	c := &bot.Client{Caches: cache.New(cache.WithCaches(cache.FlagsAll)), Rest: r}
	return NewService(store.NewInMemoryStore(), mapPool{}, c, 1, config.TestConfig{}, nil)
}

func TestOnboardingAddsMissingChannelsOnce(t *testing.T) {
	r := &onboardingRest{ob: &discord.GuildOnboarding{Enabled: true, DefaultChannelIDs: []snowflake.ID{5, 7}}}
	m := onboardingService(r)
	m.EnsureOnboardingDefaults(context.Background(), 10, 7, 100, 0, 101)
	if len(r.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(r.updates))
	}
	u := r.updates[0]
	if u.DefaultChannelIDs == nil || !reflect.DeepEqual(*u.DefaultChannelIDs, []snowflake.ID{5, 7, 100, 101}) {
		t.Fatalf("defaults = %v, want the server's plus 100 and 101", u.DefaultChannelIDs)
	}
	if u.Prompts != nil || u.Enabled != nil || u.Mode != nil {
		t.Fatal("only the default channels may be sent")
	}
	m.EnsureOnboardingDefaults(context.Background(), 10, 7, 100, 0, 101)
	if r.gets != 1 || len(r.updates) != 1 {
		t.Fatalf("second call must cost nothing: gets=%d updates=%d", r.gets, len(r.updates))
	}
}

func TestOnboardingLeavesServersWithoutIt(t *testing.T) {
	r := &onboardingRest{ob: &discord.GuildOnboarding{Enabled: false}}
	onboardingService(r).EnsureOnboardingDefaults(context.Background(), 10, 100)
	r2 := &onboardingRest{getErr: errors.New("missing access")}
	onboardingService(r2).EnsureOnboardingDefaults(context.Background(), 10, 100)
	r3 := &onboardingRest{ob: &discord.GuildOnboarding{Enabled: true, DefaultChannelIDs: []snowflake.ID{100}}}
	onboardingService(r3).EnsureOnboardingDefaults(context.Background(), 10, 100)
	if len(r.updates)+len(r2.updates)+len(r3.updates) != 0 {
		t.Fatal("no update when onboarding is off, unreadable or already complete")
	}
}
