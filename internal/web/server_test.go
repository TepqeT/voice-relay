package web

import (
	"time"

	"context"
	"encoding/json"
	"github.com/TepqeT/voice-relay/internal/journal"
	"github.com/TepqeT/voice-relay/internal/ratelimit"
	"html"
	"io"
	"io/fs"
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
	facts      map[snowflake.ID]ServerFacts // by guild; default: bot present, holder manages
	canManage  map[snowflake.ID]bool        // absent = unknown
	setups     []snowflake.ID
	removed    []snowflake.ID
	panel      string   // PanelChannel reported for every server
	onboarding string   // Onboarding reported for every server
	onboardOn  bool     // OnboardingOn reported for every server
	unFailed   []string // relay bots the next uninstall reports as stuck
	srv        *Server
	subs       *subscription.Store
}

func (f *fakeBackend) BotGuilds() []GuildRef {
	return []GuildRef{{ID: managedGuild, Name: "Zeta"}, {ID: 444, Name: "Community"}}
}

func (f *fakeBackend) BotName() string { return "Relay Test" }
func (f *fakeBackend) ServerFacts(g, _ snowflake.ID) ServerFacts {
	if sf, ok := f.facts[g]; ok {
		return sf
	}
	return ServerFacts{BotPresent: true, MemberCount: 50, Member: true, Manages: true}
}
func (f *fakeBackend) GuildStatus(id snowflake.ID) GuildStatus {
	return GuildStatus{
		BotPresent:   id == managedGuild,
		BotInviteURL: "https://discord.com/oauth2/authorize?client_id=1&guild_id=" + id.String(),
		RelayBots:    []BotStatus{{Name: "Relay 1", Present: true}, {Name: "Relay 2", InviteURL: "https://discord.com/oauth2/authorize?client_id=2"}},
		Roles:        []RoleStatus{{Purpose: "raid_lead", Name: "Speaker", Exists: true}, {Purpose: "class", Name: "Healer"}},
		PanelChannel: f.panel,
		Onboarding:   f.onboarding,
		OnboardingOn: f.onboardOn,
		Category:     "Voice Relay",
	}
}
func (f *fakeBackend) CanManage(g, _ snowflake.ID) (bool, bool) {
	v, ok := f.canManage[g]
	return v, ok
}
func (f *fakeBackend) SetupRoles(_ context.Context, g snowflake.ID) (SetupReport, error) {
	f.setups = append(f.setups, g)
	return SetupReport{Created: []string{"Healer"}, Existing: []string{"Speaker"}}, nil
}

func (f *fakeBackend) Uninstall(_ context.Context, g snowflake.ID) (UninstallReport, error) {
	f.removed = append(f.removed, g)
	return UninstallReport{Left: []string{"Relay 1"}, Failed: f.unFailed, OwnerLeft: true}, nil
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
	be := &fakeBackend{canManage: map[snowflake.ID]bool{}, facts: map[snowflake.ID]ServerFacts{}}
	var site *httptest.Server
	subs, _ := subscription.Open("")
	s, err := New(Config{Addr: ":0", BaseURL: "http://placeholder", ClientID: 42, ClientSecret: "secret"}, be, subs)
	if err != nil {
		t.Fatal(err)
	}
	be.srv, be.subs = s, subs
	// Tests click much faster than people: lift the visitor limits.
	s.pageLimit, s.actionLimit = ratelimit.New(time.Microsecond, 1000), ratelimit.New(time.Microsecond, 1000)
	site = httptest.NewServer(s.Handler())
	t.Cleanup(site.Close)
	s.oauth.apiBase = discord.URL
	s.oauth.redirectURI = site.URL + "/callback"
	jar, _ := cookiejar.New(nil)
	// Most tests read the French pages: the client asks for French unless a
	// request sets its own Accept-Language.
	client := &http.Client{Jar: jar, Transport: frenchFirst{http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return site, be, client
}

// frenchFirst adds "Accept-Language: fr" to requests that set none.
type frenchFirst struct{ next http.RoundTripper }

func (f frenchFirst) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Accept-Language") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("Accept-Language", "fr")
	}
	return f.next.RoundTrip(r)
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
	if code, body := get(t, c, site.URL+"/servers/111"); code != http.StatusOK || !strings.Contains(body, "Relay 2") || !strings.Contains(body, "Créer les rôles manquants") {
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
	st := GuildStatus{BotPresent: true, RelayBots: []BotStatus{{Present: true}}, Roles: []RoleStatus{{Exists: true}}}
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

// hasInlineScript reports a script tag without src, which the CSP blocks.
func hasInlineScript(body string) bool {
	for _, tag := range regexp.MustCompile(`<script[^>]*>`).FindAllString(body, -1) {
		if !strings.Contains(tag, " src=") {
			return true
		}
	}
	return false
}

// The CSP forbids inline styles and scripts: the animated pages must use
// classes only, and every scene must be present.
func TestPagesRespectCSPAndShowScenes(t *testing.T) {
	site, _, c := newTestSite(t)
	_, index := get(t, c, site.URL+"/")
	signIn(t, site, c, "good")
	_, guild := get(t, c, site.URL+"/servers/111")
	for name, body := range map[string]string{"index": index, "guild": guild} {
		if strings.Contains(body, "style=") || hasInlineScript(body) {
			t.Errorf("%s has inline style or script (blocked by the CSP)", name)
		}
	}
	for _, scene := range []string{"scene-login", "scene-invite", "scene-roles", "scene-assign"} {
		if !strings.Contains(index, `class="scene `+scene+`"`) {
			t.Errorf("index misses %s", scene)
		}
	}
	// The home universe: command map, before/after, figures, motion script.
	for _, want := range []string{`<svg class="cmd"`, `class="vs-scene chaos"`, `class="vs-scene order"`, `class="count" data-to="11"`, `src="/static/motion.js"`} {
		if !strings.Contains(index, want) {
			t.Errorf("index misses %s", want)
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

func TestAdminGrantsAccountSubscription(t *testing.T) {
	site, be, c := newTestSite(t)
	be.srv.AddAdmins(7) // the signed-in user of fakeDiscord
	signIn(t, site, c, "good")
	code, page := get(t, c, site.URL+"/admin")
	if code != http.StatusOK {
		t.Fatalf("/admin = %d", code)
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]

	resp, err := c.PostForm(site.URL+"/admin/grant", url.Values{"csrf": {csrf}, "user_id": {"7"}, "user_name": {"Alice"}, "plan": {"lifetime"}, "note": {"partenaire"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Location") != "/admin?ok=granted" {
		t.Fatalf("grant redirect = %q", resp.Header.Get("Location"))
	}
	sub, ok := be.subs.Get(7)
	if !ok || sub.Plan != subscription.PlanLifetime || !sub.Gift || sub.GrantedBy != 7 || sub.Note != "partenaire" || sub.MaxGuilds != 0 {
		t.Fatalf("subscription = %+v", sub)
	}

	resp, _ = c.PostForm(site.URL+"/admin/grant", url.Values{"csrf": {csrf}, "user_id": {"8"}, "plan": {"until"}, "until": {"2099-12-31"}, "max_guilds": {"2"}})
	if resp.Header.Get("Location") != "/admin?ok=granted" {
		t.Fatalf("until grant redirect = %q", resp.Header.Get("Location"))
	}
	if sub, _ := be.subs.Get(8); sub.Plan != subscription.PlanUntil || sub.MaxGuilds != 2 || sub.LastDay().Format("2006-01-02") != "2099-12-31" {
		t.Fatalf("until = %+v", sub)
	}
	if _, body := get(t, c, site.URL+"/admin"); !strings.Contains(body, "Jusqu&#39;au 31/12/2099 (offert)") || !strings.Contains(body, "À vie (offert)") {
		t.Fatalf("admin list:\n%s", body)
	}

	for _, bad := range []url.Values{
		{"csrf": {csrf}, "user_id": {"abc"}, "plan": {"lifetime"}},
		{"csrf": {csrf}, "user_id": {"9"}, "plan": {"until"}, "until": {"2001-01-01"}},
		{"csrf": {csrf}, "user_id": {"9"}},
		{"csrf": {csrf}, "user_id": {"9"}, "plan": {"lifetime"}, "max_guilds": {"-1"}},
	} {
		resp, _ := c.PostForm(site.URL+"/admin/grant", bad)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" {
			t.Errorf("bad grant %v accepted", bad)
		}
	}
	if _, ok := be.subs.Get(9); ok {
		t.Fatal("invalid grant stored")
	}

	resp, _ = c.PostForm(site.URL+"/admin/revoke", url.Values{"csrf": {csrf}, "user_id": {"8"}})
	if resp.Header.Get("Location") != "/admin?ok=revoked" {
		t.Fatal("revoke failed")
	}
	resp, _ = c.PostForm(site.URL+"/admin/revoke", url.Values{"csrf": {"forged"}, "user_id": {"7"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal("revoke without CSRF token")
	}
}

// A subscriber activates their subscription on several servers they manage.
func TestSubscriberActivatesOnSeveralServers(t *testing.T) {
	site, be, c := newTestSite(t)
	be.srv.cfg.SubscriptionRequired = true
	signIn(t, site, c, "good")
	_, page := get(t, c, site.URL+"/servers")
	if !strings.Contains(page, "Vous n&#39;avez pas d&#39;abonnement.") || strings.Contains(page, "Activer mon abonnement ici") {
		t.Fatalf("no subscription expected:\n%s", page)
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]
	resp, _ := c.PostForm(site.URL+"/servers/111/activate", url.Values{"csrf": {csrf}})
	if resp.Header.Get("Location") != "/servers?err=nosub" {
		t.Fatalf("activation without subscription = %q", resp.Header.Get("Location"))
	}

	_ = be.subs.Grant(subscription.Subscription{UserID: 7, Plan: subscription.PlanLifetime, Gift: true})
	_, page = get(t, c, site.URL+"/servers")
	if strings.Count(page, "Activer mon abonnement ici") != 2 {
		t.Fatalf("both managed servers should offer activation:\n%s", page)
	}
	for _, id := range []string{"111", "333"} {
		resp, _ := c.PostForm(site.URL+"/servers/"+id+"/activate", url.Values{"csrf": {csrf}})
		if resp.Header.Get("Location") != "/servers?ok=activated" {
			t.Fatalf("activate %s = %q", id, resp.Header.Get("Location"))
		}
	}
	if !be.subs.Active(111) || !be.subs.Active(333) {
		t.Fatal("servers not covered")
	}
	if _, page = get(t, c, site.URL+"/servers"); strings.Count(page, "Abonnement activé") != 2 || !strings.Contains(page, "Activé sur 2 serveur(s)") {
		t.Fatalf("servers page:\n%s", page)
	}
	// Not a server they manage.
	resp, _ = c.PostForm(site.URL+"/servers/222/activate", url.Values{"csrf": {csrf}})
	if resp.StatusCode != http.StatusForbidden || be.subs.Active(222) {
		t.Fatal("activation on an unmanaged server")
	}
	// From the server page, back to it.
	resp, _ = c.PostForm(site.URL+"/servers/333/deactivate", url.Values{"csrf": {csrf}, "from": {"guild"}})
	if resp.Header.Get("Location") != "/servers/333?ok=deactivated" || be.subs.Active(333) {
		t.Fatalf("deactivate = %q", resp.Header.Get("Location"))
	}
}

func TestServerCoveredByAnotherAccount(t *testing.T) {
	site, be, c := newTestSite(t)
	be.srv.cfg.SubscriptionRequired = true
	_ = be.subs.Grant(subscription.Subscription{UserID: 99, Plan: subscription.PlanLifetime})
	_ = be.subs.Activate(99, 111, "Zeta")
	_ = be.subs.Grant(subscription.Subscription{UserID: 7, Plan: subscription.PlanLifetime})
	signIn(t, site, c, "good")
	_, page := get(t, c, site.URL+"/servers/111")
	if !strings.Contains(page, "Couvert par un autre compte") || strings.Contains(page, "/servers/111/activate") {
		t.Fatalf("server page:\n%s", page)
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]
	resp, _ := c.PostForm(site.URL+"/servers/111/activate", url.Values{"csrf": {csrf}, "from": {"guild"}})
	if resp.Header.Get("Location") != "/servers/111?err=taken" {
		t.Fatalf("taking = %q", resp.Header.Get("Location"))
	}
}

func TestEnglishSite(t *testing.T) {
	site, _, c := newTestSite(t)
	req, _ := http.NewRequest(http.MethodGet, site.URL+"/", nil)
	req.Header.Set("Accept-Language", "en-GB,en;q=0.9,fr;q=0.5")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1<<16)
	n, _ := resp.Body.Read(b)
	body := string(b[:n])
	if !strings.Contains(body, `lang="en"`) || !strings.Contains(body, "Sign in with Discord") || resp.Header.Get("Content-Language") != "en" {
		t.Fatalf("Accept-Language en ignored:\n%s", body)
	}

	// The switch remembers French over the browser language.
	req, _ = http.NewRequest(http.MethodGet, site.URL+"/lang/fr", nil)
	req.Header.Set("Referer", site.URL+"/servers")
	resp, _ = c.Do(req)
	if resp.Header.Get("Location") != "/servers" {
		t.Fatalf("lang redirect = %q", resp.Header.Get("Location"))
	}
	req, _ = http.NewRequest(http.MethodGet, site.URL+"/", nil)
	req.Header.Set("Accept-Language", "en")
	resp, _ = c.Do(req)
	n, _ = resp.Body.Read(b)
	if !strings.Contains(string(b[:n]), "Se connecter avec Discord") {
		t.Fatal("lang cookie ignored")
	}
	// Foreign referer: back home, never an open redirect.
	req, _ = http.NewRequest(http.MethodGet, site.URL+"/lang/en", nil)
	req.Header.Set("Referer", "https://evil.example/x")
	resp, _ = c.Do(req)
	if resp.Header.Get("Location") != "/" {
		t.Fatalf("open redirect: %q", resp.Header.Get("Location"))
	}
	for ref, want := range map[string]string{
		site.URL + "/servers/111":   "/servers/111",
		site.URL + "/admin":         "/admin",
		site.URL + "//evil.example": "/",
		site.URL + "/servers/../x":  "/",
	} {
		req, _ = http.NewRequest(http.MethodGet, site.URL+"/lang/en", nil)
		req.Header.Set("Referer", ref)
		resp, _ = c.Do(req)
		if got := resp.Header.Get("Location"); got != want {
			t.Errorf("referer %s -> %q, want %q", ref, got, want)
		}
	}
	if code, _ := get(t, c, site.URL+"/lang/de"); code != http.StatusNotFound {
		t.Fatal("unknown language accepted")
	}
}

// The guide is public, speaks both languages, and every screenshot it shows
// is shipped with the site.
func TestGuidePage(t *testing.T) {
	site, _, c := newTestSite(t)
	for lang, title := range map[string]string{"fr": messages["fr"]["guide.title"], "en": messages["en"]["guide.title"]} {
		req, _ := http.NewRequest(http.MethodGet, site.URL+"/guide", nil)
		req.Header.Set("Accept-Language", lang)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body := string(b)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, html.EscapeString(title)) {
			t.Fatalf("%s: guide = %d\n%s", lang, resp.StatusCode, body)
		}
		if regexp.MustCompile(`guide\.[a-z0-9]+\.[a-z]`).MatchString(body) {
			t.Errorf("%s: untranslated key left in the guide", lang)
		}
		imgs := regexp.MustCompile(`src="/static/(guide/[a-z0-9-]+\.jpg)"`).FindAllStringSubmatch(body, -1)
		if len(imgs) < 10 {
			t.Fatalf("%s: %d screenshots, want the whole walkthrough", lang, len(imgs))
		}
		for _, m := range imgs {
			if _, err := fs.Stat(assets, "static/"+m[1]); err != nil {
				t.Errorf("%s: missing %s", lang, m[1])
			}
		}
		if !strings.Contains(body, `src="/static/motion.js"`) || strings.Contains(body, "style=") {
			t.Errorf("%s: guide must load its script and use no inline style (CSP)", lang)
		}
	}
	// The home page links to it.
	if _, body := get(t, c, site.URL+"/"); !strings.Contains(body, `href="/guide"`) {
		t.Error("home page has no link to the guide")
	}
	// Switching language from the guide stays on the guide.
	req, _ := http.NewRequest(http.MethodGet, site.URL+"/lang/en", nil)
	req.Header.Set("Referer", site.URL+"/guide")
	resp, _ := c.Do(req)
	if got := resp.Header.Get("Location"); got != "/guide" {
		t.Errorf("lang switch from the guide -> %q", got)
	}
}

// Both languages carry the same keys, so no page falls back silently.
func TestMessagesParity(t *testing.T) {
	for k := range messages["fr"] {
		if _, ok := messages["en"][k]; !ok {
			t.Errorf("en misses %q", k)
		}
	}
	for k := range messages["en"] {
		if _, ok := messages["fr"][k]; !ok {
			t.Errorf("fr misses %q", k)
		}
	}
}

// Every key used by the templates exists.
func TestTemplateKeysExist(t *testing.T) {
	re := regexp.MustCompile(`\{\{t "([a-z0-9_.]+)"`)
	entries, _ := assets.ReadDir("templates")
	for _, e := range entries {
		b, _ := assets.ReadFile("templates/" + e.Name())
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if _, ok := messages["fr"][m[1]]; !ok {
				t.Errorf("%s uses unknown key %q", e.Name(), m[1])
			}
		}
	}
}

// The admin page shows how many servers each account covers and flags the
// servers its holder does not run, then lets the admin bridle the account.
func TestAdminSpotsAndBridlesLending(t *testing.T) {
	site, be, c := newTestSite(t)
	be.srv.AddAdmins(7)
	_ = be.subs.Grant(subscription.Subscription{UserID: 50, UserName: "Honnête", Plan: subscription.PlanLifetime})
	_ = be.subs.Activate(50, 1001, "Sa guilde")
	_ = be.subs.Grant(subscription.Subscription{UserID: 60, UserName: "Prêteur", Plan: subscription.PlanLifetime})
	for i, name := range []string{"Guilde A", "Guilde B", "Guilde C"} {
		_ = be.subs.Activate(60, snowflake.ID(2001+i), name)
	}
	_ = be.subs.RecordRaid(2001)
	be.facts[2002] = ServerFacts{BotPresent: true, MemberCount: 300, Member: false}
	be.facts[2003] = ServerFacts{BotPresent: true, MemberCount: 120, Member: true, Manages: false}

	signIn(t, site, c, "good")
	_, page := get(t, c, site.URL+"/admin")
	for _, want := range []string{
		"2 compte(s) abonné(s), 4 serveur(s) couvert(s), 2 serveur(s) suspect(s).",
		"titulaire absent du serveur", "titulaire sans « Gérer le serveur »",
		"300 membres", "1 session(s)", "aucune session",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("admin page misses %q", want)
		}
	}
	if strings.Index(page, "Prêteur") > strings.Index(page, "Honnête") {
		t.Error("the flagged account must come first")
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]

	resp, _ := c.PostForm(site.URL+"/admin/limit", url.Values{"csrf": {csrf}, "user_id": {"60"}, "max_guilds": {"1"}})
	if resp.Header.Get("Location") != "/admin?ok=limited" {
		t.Fatalf("limit = %q", resp.Header.Get("Location"))
	}
	if sub, _ := be.subs.Get(60); sub.MaxGuilds != 1 {
		t.Fatalf("max = %d", sub.MaxGuilds)
	}
	if _, page = get(t, c, site.URL+"/admin"); !strings.Contains(page, "Plus de serveurs que la limite") {
		t.Error("over-limit warning missing")
	}
	for _, g := range []string{"2002", "2003"} {
		resp, _ = c.PostForm(site.URL+"/admin/unlink", url.Values{"csrf": {csrf}, "guild_id": {g}})
		if resp.Header.Get("Location") != "/admin?ok=unlinked" {
			t.Fatalf("unlink %s = %q", g, resp.Header.Get("Location"))
		}
	}
	if be.subs.Active(2002) || be.subs.Active(2003) || !be.subs.Active(2001) {
		t.Fatal("unlink result")
	}
	for _, bad := range []url.Values{
		{"csrf": {csrf}, "user_id": {"60"}, "max_guilds": {"-2"}},
		{"csrf": {csrf}, "user_id": {"999"}, "max_guilds": {"1"}},
	} {
		resp, _ = c.PostForm(site.URL+"/admin/limit", bad)
		if resp.Header.Get("Location") != "" {
			t.Errorf("bad limit %v accepted", bad)
		}
	}
	// Non-admins cannot bridle.
	be.srv.admins.Store(&map[snowflake.ID]bool{})
	resp, _ = c.PostForm(site.URL+"/admin/unlink", url.Values{"csrf": {csrf}, "guild_id": {"2001"}})
	if resp.StatusCode != http.StatusForbidden || !be.subs.Active(2001) {
		t.Fatal("non-admin unlinked a server")
	}
}

// Animations never target the site's shared building blocks: a rule on
// ".card" once made every panel of the site fade out in a loop.
func TestAnimationsScopedToScenes(t *testing.T) {
	css, err := assets.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	shared := map[string]bool{".card": true, ".row": true, ".list": true, ".button": true, ".step": true, "main": true, "body": true, "section": true, "html": true}
	for i, line := range strings.Split(string(css), "\n") {
		for _, rule := range strings.Split(line, "}") {
			sel, decl, ok := strings.Cut(rule, "{")
			if !ok || !strings.Contains(decl, "animation:") || strings.Contains(decl, "animation:none") || strings.HasPrefix(strings.TrimSpace(sel), "@") {
				continue
			}
			for _, s := range strings.Split(sel, ",") {
				parts := strings.Fields(s)
				if len(parts) > 0 && shared[parts[len(parts)-1]] {
					t.Errorf("style.css:%d animates the shared %q", i+1, strings.TrimSpace(s))
				}
			}
		}
	}
	// Visitors who ask for less motion get none: one rule stops every
	// animation and transition of the site, whatever it animates.
	stop := regexp.MustCompile(`@media \(prefers-reduced-motion: reduce\) \{\s*\*, \*::before, \*::after \{[^}]*animation:none !important;[^}]*transition:none !important;`)
	if !stop.Match(css) {
		t.Fatal("style.css lacks the global prefers-reduced-motion stop")
	}
}

// Every sub-template a page calls is defined in an embedded file (a file once
// ignored by git, coverage.html, existed only locally).
func TestCalledTemplatesExist(t *testing.T) {
	subs, _ := subscription.Open("")
	s, err := New(Config{BaseURL: "https://relais.example"}, &fakeBackend{}, subs)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\{\{template "([a-z0-9-]+)"`)
	entries, _ := assets.ReadDir("templates")
	for _, e := range entries {
		b, _ := assets.ReadFile("templates/" + e.Name())
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if s.tmpls["fr"].Lookup(m[1]) == nil {
				t.Errorf("%s calls undefined template %q", e.Name(), m[1])
			}
		}
	}
}

func TestUninstall(t *testing.T) {
	site, be, c := newTestSite(t)
	signIn(t, site, c, "good")
	_, page := get(t, c, site.URL+"/servers/111")
	if !strings.Contains(page, `action="/servers/111/uninstall"`) || !strings.Contains(page, "Je confirme vouloir retirer Voice Relay") {
		t.Fatalf("uninstall form missing:\n%s", page)
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]
	_ = be.subs.Grant(subscription.Subscription{UserID: 7, Plan: subscription.PlanLifetime, Gift: true})
	if err := be.subs.Activate(7, 111, "Zeta"); err != nil {
		t.Fatal(err)
	}

	// Box not ticked: nothing happens.
	resp, _ := c.PostForm(site.URL+"/servers/111/uninstall", url.Values{"csrf": {csrf}})
	if resp.Header.Get("Location") != "/servers/111?err=confirm" || len(be.removed) != 0 {
		t.Fatalf("unconfirmed = %q, removed %v", resp.Header.Get("Location"), be.removed)
	}
	// Bad CSRF.
	resp, _ = c.PostForm(site.URL+"/servers/111/uninstall", url.Values{"csrf": {"wrong"}, "confirm": {"yes"}})
	if resp.StatusCode != http.StatusForbidden || len(be.removed) != 0 {
		t.Fatalf("bad csrf = %d", resp.StatusCode)
	}
	// Not a server they manage.
	resp, _ = c.PostForm(site.URL+"/servers/222/uninstall", url.Values{"csrf": {csrf}, "confirm": {"yes"}})
	if resp.StatusCode != http.StatusForbidden || len(be.removed) != 0 {
		t.Fatalf("unmanaged = %d", resp.StatusCode)
	}

	resp, _ = c.PostForm(site.URL+"/servers/111/uninstall", url.Values{"csrf": {csrf}, "confirm": {"yes"}})
	if resp.Header.Get("Location") != "/servers?ok=uninstalled" || len(be.removed) != 1 || be.removed[0] != managedGuild {
		t.Fatalf("uninstall = %q, removed %v", resp.Header.Get("Location"), be.removed)
	}
	if be.subs.Active(111) {
		t.Error("the server must be released from the subscription")
	}
	if _, page = get(t, c, site.URL+"/servers?ok=uninstalled"); !strings.Contains(page, "Voice Relay a été retiré du serveur") {
		t.Fatalf("confirmation missing:\n%s", page)
	}

	be.unFailed = []string{"Relay 2"}
	resp, _ = c.PostForm(site.URL+"/servers/111/uninstall", url.Values{"csrf": {csrf}, "confirm": {"yes"}})
	if resp.Header.Get("Location") != "/servers?ok=uninstalled_partial" {
		t.Fatalf("partial uninstall = %q", resp.Header.Get("Location"))
	}
}

func TestSupportButtonAndLogo(t *testing.T) {
	site, be, c := newTestSite(t)
	_, page := get(t, c, site.URL+"/")
	if strings.Contains(page, "Contacter le support") {
		t.Fatal("no support button before the owner is known")
	}
	if !strings.Contains(page, `href="/static/logo.svg"`) || !strings.Contains(page, `class="logo"`) {
		t.Fatalf("logo or favicon missing:\n%s", page)
	}
	if code, _ := get(t, c, site.URL+"/static/logo.svg"); code != http.StatusOK {
		t.Fatalf("logo = %d", code)
	}
	be.srv.SetSupportUser(123456789012345678)
	if _, page = get(t, c, site.URL+"/"); !strings.Contains(page, `href="https://discord.com/users/123456789012345678"`) {
		t.Fatalf("support link to the owner's profile missing:\n%s", page)
	}
	be.srv.cfg.SupportURL = "https://discord.gg/support"
	be.srv.SetSupportUser(1)
	if _, page = get(t, c, site.URL+"/"); !strings.Contains(page, `href="https://discord.gg/support"`) {
		t.Fatal("SUPPORT_URL must win over the owner's profile")
	}
}

// The relay is free: without SUBSCRIPTION_REQUIRED no page talks about
// subscriptions, and a PayPal.me link becomes a donation button.
func TestFreeSiteWithDonationLink(t *testing.T) {
	site, be, c := newTestSite(t)
	signIn(t, site, c, "good")
	_, page := get(t, c, site.URL+"/servers")
	for _, gone := range []string{"Mon abonnement", "Sans abonnement", "Activer mon abonnement ici", "Faire un don"} {
		if strings.Contains(page, gone) {
			t.Fatalf("free site shows %q:\n%s", gone, page)
		}
	}
	if _, page = get(t, c, site.URL+"/servers/111"); strings.Contains(page, "Abonnement") {
		t.Fatalf("guild page talks about subscriptions:\n%s", page)
	}
	be.srv.cfg.PayPalURL = "https://www.paypal.com/paypalme/test"
	for _, u := range []string{"/servers", "/", "/guide"} {
		_, page = get(t, c, site.URL+u)
		if u == "/" {
			c2 := &http.Client{} // signed out: the home page itself
			_, page = get(t, c2, site.URL+u)
		}
		if !strings.Contains(page, `href="https://www.paypal.com/paypalme/test"`) || !strings.Contains(page, "Soutenir Voice Relay") && !strings.Contains(page, "Support Voice Relay") {
			t.Fatalf("%s lacks the donation link:\n%s", u, page)
		}
	}
	// Subscriptions turned back on: the account card returns.
	be.srv.cfg.SubscriptionRequired = true
	if _, page = get(t, c, site.URL+"/servers"); !strings.Contains(page, "Mon abonnement") || !strings.Contains(page, "Pour un abonnement, contactez le support.") {
		t.Fatalf("subscription card missing when required:\n%s", page)
	}
}

// Without any language hint the site is in English.
func TestEnglishByDefault(t *testing.T) {
	site, _, _ := newTestSite(t)
	resp, err := http.Get(site.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Language") != "en" {
		t.Fatalf("Content-Language = %q, want en", resp.Header.Get("Content-Language"))
	}
}

func TestGuildPageNamesPanelChannel(t *testing.T) {
	site, be, c := newTestSite(t)
	signIn(t, site, c, "good")
	if _, page := get(t, c, site.URL+"/servers/111"); !strings.Contains(page, "/panneau") {
		t.Fatalf("without a panel channel the page must name the command:\n%s", page)
	}
	be.panel = "raid-panel"
	_, page := get(t, c, site.URL+"/servers/111")
	if !strings.Contains(page, "#raid-panel") || strings.Contains(page, "/panneau") {
		t.Fatalf("panel channel not shown:\n%s", page)
	}
}

// A visitor hammering the site gets 429 instead of overloading it.
func TestVisitorLimit(t *testing.T) {
	site, be, c := newTestSite(t)
	be.srv.pageLimit = ratelimit.New(time.Hour, 3)
	codes := []int{}
	for range 5 {
		code, _ := get(t, c, site.URL+"/")
		codes = append(codes, code)
	}
	if codes[2] != http.StatusOK || codes[3] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v, want 3 pages then 429", codes)
	}
	if code, _ := get(t, c, site.URL+"/healthz"); code != http.StatusOK {
		t.Fatal("health checks are never limited")
	}
}

func TestClientIPBehindProxy(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "172.18.0.3:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 172.18.0.3")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("behind caddy = %q", got)
	}
	r.RemoteAddr = "198.51.100.7:5000" // public peer: header ignored, it could be forged
	if got := clientIP(r); got != "198.51.100.7" {
		t.Fatalf("direct = %q", got)
	}
}

func TestSetupJournalShown(t *testing.T) {
	site, be, c := newTestSite(t)
	be.srv.AddAdmins(7)
	signIn(t, site, c, "good")
	journal.Record(journal.Event{GuildID: managedGuild, Guild: "Zeta", Level: journal.Problem, Kind: "missing_permissions", Detail: "main bot lacks: mute"})
	if _, page := get(t, c, site.URL+"/servers/111"); !strings.Contains(page, "missing_permissions") || !strings.Contains(page, "main bot lacks: mute") {
		t.Fatalf("guild page must show its recent problems:\n%s", page)
	}
	_, page := get(t, c, site.URL+"/admin")
	if !strings.Contains(page, "Journal des installations") || !strings.Contains(page, "main bot lacks: mute") {
		t.Fatalf("admin page must show the journal:\n%s", page)
	}
	// The unfinished setup of the page itself was recorded too.
	if !strings.Contains(page, "setup_incomplete") {
		t.Fatal("an unfinished setup must be written in the journal")
	}
}

// The server page offers the next missing relay bot as one button, with the
// progress, and loads the script that moves to the following one.
func TestGuildPageNextRelay(t *testing.T) {
	site, _, c := newTestSite(t)
	signIn(t, site, c, "good")
	_, page := get(t, c, site.URL+"/servers/111")
	for _, want := range []string{`id="next-relay"`, "client_id=2", "Relay 2 (1/2", "/static/guild.js"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page lacks %q:\n%s", want, page)
		}
	}
	if code, _ := get(t, c, site.URL+"/static/guild.js"); code != http.StatusOK {
		t.Fatalf("guild.js = %d", code)
	}
}

func TestGuildStatusNextRelay(t *testing.T) {
	st := GuildStatus{RelayBots: []BotStatus{{Name: "A", Present: true}, {Name: "B"}, {Name: "C"}}}
	if n := st.NextRelay(); n == nil || n.Name != "B" || st.RelayPresent() != 1 || st.RelayCount() != 3 {
		t.Fatalf("next = %+v, present = %d", n, st.RelayPresent())
	}
	st.RelayBots[1].Present, st.RelayBots[2].Present = true, true
	if st.NextRelay() != nil {
		t.Fatal("all relay bots in: nothing to offer")
	}
}

// The server page warns when the panel is not an onboarding default.
func TestGuildPageOnboardingWarning(t *testing.T) {
	site, be, c := newTestSite(t)
	be.panel = "control-panel"
	be.onboarding = "missing"
	signIn(t, site, c, "good")
	_, page := get(t, c, site.URL+"/servers/111")
	if !strings.Contains(page, "Salons par défaut") && !strings.Contains(page, "Default Channels") {
		t.Fatalf("no onboarding warning:\n%s", page)
	}
}

// With onboarding on, the server page walks the administrator through
// following the category, then the test.
func TestGuildPageOnboardingGuide(t *testing.T) {
	site, be, c := newTestSite(t)
	be.panel = "control-panel"
	signIn(t, site, c, "good")
	_, page := get(t, c, site.URL+"/servers/111")
	if strings.Contains(page, "Suivre la catégorie") || strings.Contains(page, "Follow Category") {
		t.Fatal("no onboarding step on a server without onboarding")
	}
	be.onboardOn = true
	_, page = get(t, c, site.URL+"/servers/111")
	if !strings.Contains(page, "Suivre la catégorie") && !strings.Contains(page, "Follow Category") {
		t.Fatalf("onboarding step missing:\n%s", page)
	}
	if !strings.Contains(page, "7.") {
		t.Fatal("the test step must come after the onboarding step")
	}
}
