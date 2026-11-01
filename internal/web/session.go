package web

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

const (
	sessionTTL  = 12 * time.Hour
	maxSessions = 10000
)

// session is a signed-in administrator. Kept in memory only: a restart signs
// everyone out, which is acceptable for an admin site.
type session struct {
	id       string
	userID   snowflake.ID
	userName string
	guilds   []discordGuild // servers they manage, from the OAuth list at sign-in
	csrf     string
	expires  time.Time
}

func (s *session) guild(id snowflake.ID) (discordGuild, bool) {
	for _, g := range s.guilds {
		if g.ID == id {
			return g, true
		}
	}
	return discordGuild{}, false
}

type sessionStore struct {
	mu  sync.Mutex
	m   map[string]*session
	now func() time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{m: make(map[string]*session), now: time.Now}
}

func (st *sessionStore) create(userID snowflake.ID, name string, guilds []discordGuild) *session {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.purgeLocked()
	if len(st.m) >= maxSessions {
		for k := range st.m { // drop an arbitrary one rather than refuse sign-in
			delete(st.m, k)
			break
		}
	}
	s := &session{id: randomToken(), userID: userID, userName: name, guilds: guilds, csrf: randomToken(), expires: st.now().Add(sessionTTL)}
	st.m[s.id] = s
	return s
}

func (st *sessionStore) get(id string) (*session, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.m[id]
	if !ok || st.now().After(s.expires) {
		delete(st.m, id)
		return nil, false
	}
	return s, true
}

func (st *sessionStore) delete(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.m, id)
}

func (st *sessionStore) purgeLocked() {
	now := st.now()
	for k, s := range st.m {
		if now.After(s.expires) {
			delete(st.m, k)
		}
	}
}

// randomToken returns 32 random bytes, URL-safe.
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error()) // never happens on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
