package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/subscription"
)

const (
	managedGuild   snowflake.ID = 111
	unmanagedGuild snowflake.ID = 222
	ownedGuild     snowflake.ID = 333
)

type fakeBackend struct {
	canManage map[snowflake.ID]bool // absent = unknown
	setups    []snowflake.ID
	srv       *Server
	subs      *subscription.Store
}

func (f *fakeBackend) BotGuilds() []GuildRef {
	return []GuildRef{{ID: managedGuild, Name: "Zeta"}, {ID: 444, Name: "Test Server"}}
}

func (f *fakeBackend) BotName() string { return "Voix de Test" }
func (f *fakeBackend) GuildStatus(id snowflake.ID) GuildStatus {
	return GuildStatus{
		BotPresent:   id == managedGuild,
		BotInviteURL: "https://discord.com/oauth2/authorize?client_id=1&guild_id=" + id.String(),
		Heralds:      []BotStatus{{Name: "Héraut 1", Present: true}, {Name: "Héraut 2", InviteURL: "https://discord.com/oauth2/authorize?client_id=2"}},
		Roles:        []RoleStatus{{Purpose: "raid_lead", Name: "Raid Lead", Exists: true}, {Purpose: "class", Name: "Clerc"}},
	}
}
func (f *fakeBackend) CanManage(g, _ snowflake.ID) (bool, bool) {
	v, ok := f.canManage[g]
	return v, ok
}
func (f *fakeBackend) SetupRoles(_ context.Context, g snowflake.ID) (SetupReport, error) {
	f.setups = append(f.setups, g)
	return SetupReport{Created: []string{"Clerc"}, Existing: []string{"Raid Lead"}}, nil
}

// fakeDiscord answers the OAuth token, user and guild endpoints.
func fakeDiscord(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if user != "42" || pass != "secret" || r.PostFormValue("code") != "good" || !strings.HasSuffix(r.PostFormValue("redirect_uri"), "/callback") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "Bearer"})
	})
	auth := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer tok" }
	mux.HandleFunc("GET /users/@me", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"7","username":"alice","global_name":"Alice"}`))
	})
	mux.HandleFunc("GET /users/@me/guilds", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"111","name":"Zeta","owner":false,"permissions":"32"},
			{"id":"222","name":"Membre","owner":false,"permissions":"1024"},
			{"id":"333","name":"Alpha","owner":true,"permissions":"0"}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestSite(t *testing.T) (*httptest.Server, *fakeBackend, *http.Client) {
	t.Helper()
	discord := fakeDiscord(t)
	be := &fakeBackend{canManage: map[snowflake.ID]bool{}}
	var site *httptest.Server
	subs, _ := subscription.Open("")
	s, err := New(Config{Addr: ":0", BaseURL: "http://placeholder", ClientID: 42, ClientSecret: "secret"}, be, subs)
	if err != nil {
		t.Fatal(err)
	}
	be.srv, be.subs = s, subs
	site = httptest.NewServer(s.Handler())
	t.Cleanup(site.Close)
	s.oauth.apiBase = discord.URL
	s.oauth.redirectURI = site.URL + "/callback"
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return site, be, client
}

// signIn runs /login then /callback with the state Discord would echo.
func signIn(t *testing.T, site *httptest.Server, c *http.Client, code string) *http.Response {
	t.Helper()
	resp, err := c.Get(site.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	if loc.Host != "discord.com" || q.Get("scope") != "identify guilds" || q.Get("client_id") != "42" || q.Get("state") == "" {
		t.Fatalf("login redirect = %s", loc)
	}
	resp, err = c.Get(site.URL + "/callback?code=" + code + "&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b.String()
}

func TestSignInListsOnlyManagedServers(t *testing.T) {
	site, _, c := newTestSite(t)
	if resp := signIn(t, site, c, "good"); resp.Header.Get("Location") != "/servers" {
		t.Fatalf("callback redirect = %q", resp.Header.Get("Location"))
	}
	code, body := get(t, c, site.URL+"/servers")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "Zeta") || !strings.Contains(body, "Alpha") || strings.Contains(body, "Membre") {
		t.Fatalf("server list:\n%s", body)
	}
	if strings.Index(body, "Alpha") > strings.Index(body, "Zeta") {
		t.Fatal("servers must be sorted by name")
	}
}

func TestCallbackRejectsForgedState(t *testing.T) {
	site, _, c := newTestSite(t)
	if _, err := c.Get(site.URL + "/login"); err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(site.URL + "/callback?code=good&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Location") != "/?erreur=session" {
		t.Fatalf("redirect = %q", resp.Header.Get("Location"))
	}
	if code, _ := get(t, c, site.URL+"/servers"); code != http.StatusSeeOther {
		t.Fatalf("/servers without session = %d", code)
	}
}

func TestCallbackDiscordFailure(t *testing.T) {
	site, _, c := newTestSite(t)
	if resp := signIn(t, site, c, "bad"); resp.Header.Get("Location") != "/?erreur=discord" {
		t.Fatalf("redirect = %q", resp.Header.Get("Location"))
	}
}

func TestGuildPageAuthorization(t *testing.T) {
	site, be, c := newTestSite(t)
	signIn(t, site, c, "good")
	if code, body := get(t, c, site.URL+"/servers/111"); code != http.StatusOK || !strings.Contains(body, "Héraut 2") || !strings.Contains(body, "Créer les rôles manquants") {
		t.Fatalf("managed guild: %d\n%s", code, body)
	}
	if code, _ := get(t, c, site.URL+"/servers/222"); code != http.StatusForbidden {
		t.Fatalf("unmanaged guild = %d, want 403", code)
	}
	be.canManage[managedGuild] = false // permission removed since sign-in
	if code, _ := get(t, c, site.URL+"/servers/111"); code != http.StatusForbidden {
		t.Fatalf("revoked guild = %d, want 403", code)
	}
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func TestSetupRequiresCSRF(t *testing.T) {
	site, be, c := newTestSite(t)
	signIn(t, site, c, "good")
	_, page := get(t, c, site.URL+"/servers/111")
	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no csrf token in page")
	}
	resp, err := c.PostForm(site.URL+"/servers/111/setup", url.Values{"csrf": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || len(be.setups) != 0 {
		t.Fatalf("bad csrf: %d, setups %v", resp.StatusCode, be.setups)
	}
	resp, err = c.PostForm(site.URL+"/servers/111/setup", url.Values{"csrf": {m[1]}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(be.setups) != 1 || be.setups[0] != managedGuild {
		t.Fatalf("setup: %d, setups %v", resp.StatusCode, be.setups)
	}
	resp, _ = c.PostForm(site.URL+"/servers/222/setup", url.Values{"csrf": {m[1]}})
	if resp.StatusCode != http.StatusForbidden || len(be.setups) != 1 {
		t.Fatalf("setup on unmanaged guild: %d", resp.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	site, _, c := newTestSite(t)
	signIn(t, site, c, "good")
	_, page := get(t, c, site.URL+"/servers")
	m := csrfRe.FindStringSubmatch(page)
	if _, err := c.PostForm(site.URL+"/logout", url.Values{"csrf": {m[1]}}); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(t, c, site.URL+"/servers"); code != http.StatusSeeOther {
		t.Fatalf("after logout = %d", code)
	}
}

func TestSecurityHeadersAndHealth(t *testing.T) {
	site, _, c := newTestSite(t)
	resp, err := c.Get(site.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	if code, body := get(t, c, site.URL+"/"); code != http.StatusOK || !strings.Contains(body, "Se connecter avec Discord") {
		t.Fatalf("index %d", code)
	}
	if code, _ := get(t, c, site.URL+"/static/style.css"); code != http.StatusOK {
		t.Fatalf("style.css %d", code)
	}
}

func TestGuildStatusReady(t *testing.T) {
	st := GuildStatus{BotPresent: true, Heralds: []BotStatus{{Present: true}}, Roles: []RoleStatus{{Exists: true}}}
	if !st.Ready() {
		t.Fatal("should be ready")
	}
	st.Roles = append(st.Roles, RoleStatus{Exists: true, AboveBot: true})
	if st.Ready() {
		t.Fatal("a role above the bot is not ready")
	}
	if (GuildStatus{Roles: []RoleStatus{{}, {Exists: true}, {}}}).MissingRoles() != 2 {
		t.Fatal("MissingRoles")
	}
}

func TestNewRejectsRelativeBaseURL(t *testing.T) {
	subs, _ := subscription.Open("")
	if _, err := New(Config{BaseURL: "relais.example"}, &fakeBackend{}, subs); err == nil {
		t.Fatal("relative base URL accepted")
	}
}

// The CSP forbids inline styles and scripts: the animated pages must use
// classes only, and every scene must be present.
func TestPagesRespectCSPAndShowScenes(t *testing.T) {
	site, _, c := newTestSite(t)
	_, index := get(t, c, site.URL+"/")
	signIn(t, site, c, "good")
	_, guild := get(t, c, site.URL+"/servers/111")
	for name, body := range map[string]string{"index": index, "guild": guild} {
		if strings.Contains(body, "style=") || strings.Contains(body, "<script") {
			t.Errorf("%s has inline style or script (blocked by the CSP)", name)
		}
	}
	for _, scene := range []string{"scene-raid", "scene-login", "scene-invite", "scene-roles", "scene-assign"} {
		if !strings.Contains(index, `class="scene `+scene+`"`) {
			t.Errorf("index misses %s", scene)
		}
	}
	for _, scene := range []string{"scene-invite", "scene-roles", "scene-assign"} {
		if !strings.Contains(guild, `class="scene `+scene+`"`) {
			t.Errorf("guild page misses %s", scene)
		}
	}
}

func TestAdminReservedToAdmins(t *testing.T) {
	site, _, c := newTestSite(t)
	signIn(t, site, c, "good")
	if code, _ := get(t, c, site.URL+"/admin"); code != http.StatusForbidden {
		t.Fatalf("/admin for a non-admin = %d", code)
	}
	if code, body := get(t, c, site.URL+"/servers"); code != http.StatusOK || strings.Contains(body, "Administration") {
		t.Fatal("admin link shown to a non-admin")
	}
}

func TestAdminGrantsAndRevokesGiftSubscriptions(t *testing.T) {
	site, be, c := newTestSite(t)
	be.srv.AddAdmins(7) // the signed-in user of fakeDiscord
	signIn(t, site, c, "good")
	code, page := get(t, c, site.URL+"/admin")
	if code != http.StatusOK || !strings.Contains(page, "Test Server") {
		t.Fatalf("/admin = %d", code)
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]

	resp, err := c.PostForm(site.URL+"/admin/grant", url.Values{"csrf": {csrf}, "guild_pick": {"444"}, "plan": {"lifetime"}, "note": {"partenaire"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Location") != "/admin?ok=offert" {
		t.Fatalf("grant redirect = %q", resp.Header.Get("Location"))
	}
	sub, ok := be.subs.Get(444)
	if !ok || sub.Plan != subscription.PlanLifetime || !sub.Gift || sub.GuildName != "Test Server" || sub.GrantedBy != 7 || sub.Note != "partenaire" {
		t.Fatalf("subscription = %+v", sub)
	}

	// Until a date, typed server ID; the whole chosen day is included.
	resp, _ = c.PostForm(site.URL+"/admin/grant", url.Values{"csrf": {csrf}, "guild_id": {"111"}, "plan": {"until"}, "until": {"2099-12-31"}})
	if resp.Header.Get("Location") != "/admin?ok=offert" {
		t.Fatalf("until grant redirect = %q", resp.Header.Get("Location"))
	}
	if sub, _ := be.subs.Get(111); sub.Plan != subscription.PlanUntil || sub.Expires.Format("2006-01-02") != "2100-01-01" {
		t.Fatalf("until = %+v", sub)
	}

	// The server owner sees it on their pages.
	if _, body := get(t, c, site.URL+"/servers"); !strings.Contains(body, "Jusqu&#39;au 31/12/2099 (offert)") {
		t.Fatalf("subscription not shown on /servers:\n%s", body)
	}
	if _, body := get(t, c, site.URL+"/servers/111"); !strings.Contains(body, "(offert)") {
		t.Fatal("subscription not shown on the server page")
	}

	for _, bad := range []url.Values{
		{"csrf": {csrf}, "guild_id": {"abc"}, "plan": {"lifetime"}},
		{"csrf": {csrf}, "guild_id": {"111"}, "plan": {"until"}, "until": {"2001-01-01"}},
		{"csrf": {csrf}, "guild_id": {"111"}},
	} {
		resp, _ := c.PostForm(site.URL+"/admin/grant", bad)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" {
			t.Errorf("bad grant %v accepted", bad)
		}
	}

	resp, _ = c.PostForm(site.URL+"/admin/revoke", url.Values{"csrf": {csrf}, "guild_id": {"444"}})
	if resp.Header.Get("Location") != "/admin?ok=retire" || be.subs.Active(444) {
		t.Fatal("revoke failed")
	}
	resp, _ = c.PostForm(site.URL+"/admin/revoke", url.Values{"csrf": {"forged"}, "guild_id": {"111"}})
	if resp.StatusCode != http.StatusForbidden || !be.subs.Active(111) {
		t.Fatal("revoke without CSRF token")
	}
}

// Animations must stay inside the scenes: a rule on a site class (".card"
// once) made every panel of the site fade out in a loop.
func TestAnimationsScopedToScenes(t *testing.T) {
	css, err := assets.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(css), "\n") {
		if !strings.Contains(line, "animation") {
			continue
		}
		for _, rule := range strings.Split(line, "}") {
			sel, _, ok := strings.Cut(rule, "{")
			if !ok || !strings.Contains(rule, "animation") {
				continue
			}
			sel = strings.TrimSpace(sel)
			if strings.HasPrefix(sel, "@") {
				continue
			}
			for _, s := range strings.Split(sel, ",") {
				if !strings.HasPrefix(strings.TrimSpace(s), ".scene") {
					t.Errorf("style.css:%d animates %q outside .scene", i+1, strings.TrimSpace(s))
				}
			}
		}
	}
}
