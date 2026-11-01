package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"github.com/TepqeT/voice-relay/internal/journal"
	"github.com/TepqeT/voice-relay/internal/ratelimit"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/subscription"
)

//go:embed templates/*.html static/*
var assets embed.FS

const (
	sessionCookie = "vr_session"
	stateCookie   = "vr_oauth_state"
)

// Config configures the site.
type Config struct {
	Addr         string       // listen address, e.g. ":8080"
	BaseURL      string       // public URL, e.g. "https://relais.example.fr"
	ClientID     snowflake.ID // owner bot application ID
	ClientSecret string       // OAuth2 client secret of the owner bot application
	// Admins may grant and revoke subscriptions (SITE_ADMINS); the owner of
	// the bot application is added at startup with AddAdmins.
	Admins []snowflake.ID
	// SupportURL is where "Contact support" leads; empty until SetSupportUser
	// points it at a Discord profile.
	SupportURL string
	// PayPalURL is the "Pay with PayPal" link, empty for no button;
	// PriceLabel the price shown next to it.
	PayPalURL  string
	PriceLabel string
	// Checkout takes payments on the site (PayPal Checkout); nil = off.
	Checkout *Checkout
}

// Server is the administration site.
type Server struct {
	cfg      Config
	backend  Backend
	oauth    *oauth
	sessions *sessionStore
	tmpls    map[string]*template.Template // by language
	secure   bool                          // cookies marked Secure (HTTPS base URL)
	mux      *http.ServeMux
	subs     *subscription.Store
	admins   atomic.Pointer[map[snowflake.ID]bool]
	support  atomic.Pointer[string]
	// Request limits per visitor (IP address): pages and files, then the
	// actions (POST), which cost Discord or PayPal calls.
	pageLimit, actionLimit *ratelimit.Limiter
}

// New builds the site. subs records which servers may use the relay.
func New(cfg Config, backend Backend, subs *subscription.Store) (*Server, error) {
	if subs == nil {
		return nil, errors.New("subscription store required")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, errors.New("WEB_BASE_URL must be an absolute URL")
	}
	root, err := template.New("").Funcs(template.FuncMap{
		"t": func(string, ...any) string { return "" }, "purpose": func(string) string { return "" },
		"perm": func(string) string { return "" }, "date": func(time.Time) string { return "" },
		"permList": func([]string) string { return "" }, "join": joinList, "dict": dict,
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	tmpls := make(map[string]*template.Template, len(languages))
	for _, lang := range languages {
		t := tr(lang)
		c, err := root.Clone()
		if err != nil {
			return nil, err
		}
		l := lang
		tmpls[lang] = c.Funcs(template.FuncMap{
			"t":       t,
			"purpose": func(p string) string { return t("purpose." + p) },
			"perm":    func(k string) string { return t("perm." + k) },
			"date":    func(d time.Time) string { return dateFor(l, d) },
			"permList": func(keys []string) string {
				out := make([]string, len(keys))
				for i, k := range keys {
					out[i] = t("perm." + k)
				}
				return joinList(out)
			},
		})
	}
	s := &Server{
		cfg:         cfg,
		backend:     backend,
		oauth:       newOAuth(cfg.ClientID, cfg.ClientSecret, base+"/callback"),
		sessions:    newSessionStore(),
		tmpls:       tmpls,
		secure:      u.Scheme == "https",
		pageLimit:   ratelimit.New(100*time.Millisecond, 60),
		actionLimit: ratelimit.New(time.Second, 6),
		mux:         http.NewServeMux(),
		subs:        subs,
	}
	s.AddAdmins(cfg.Admins...)
	static, _ := fs.Sub(assets, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /lang/{lang}", s.handleLang)
	s.mux.HandleFunc("GET /login", s.handleLogin)
	s.mux.HandleFunc("GET /callback", s.handleCallback)
	s.mux.HandleFunc("POST /logout", s.withSession(s.handleLogout))
	s.mux.HandleFunc("GET /servers", s.withSession(s.handleServers))
	s.mux.HandleFunc("GET /servers/{id}", s.withSession(s.handleGuild))
	s.mux.HandleFunc("POST /servers/{id}/setup", s.withSession(s.handleSetup))
	s.mux.HandleFunc("POST /servers/{id}/activate", s.withSession(s.handleActivate))
	s.mux.HandleFunc("POST /servers/{id}/deactivate", s.withSession(s.handleDeactivate))
	s.mux.HandleFunc("POST /servers/{id}/uninstall", s.withSession(s.handleUninstall))
	s.mux.HandleFunc("POST /pay/order", s.withSession(s.handlePayOrder))
	s.mux.HandleFunc("POST /pay/capture", s.withSession(s.handlePayCapture))
	s.mux.HandleFunc("GET /admin", s.withAdmin(s.handleAdmin))
	s.mux.HandleFunc("POST /admin/grant", s.withAdmin(s.handleGrant))
	s.mux.HandleFunc("POST /admin/revoke", s.withAdmin(s.handleRevoke))
	s.mux.HandleFunc("POST /admin/limit", s.withAdmin(s.handleLimit))
	s.mux.HandleFunc("POST /admin/unlink", s.withAdmin(s.handleUnlink))
	return s, nil
}

// AddAdmins lets ids grant subscriptions. Safe while serving.
// SetSupportUser makes "Contact support" open the Discord profile of userID
// (send a message, add as a friend), unless SUPPORT_URL is set.
func (s *Server) SetSupportUser(userID snowflake.ID) {
	if s.cfg.SupportURL != "" || userID == 0 {
		return
	}
	u := "https://discord.com/users/" + userID.String()
	s.support.Store(&u)
}

// supportURL is the link of the support button, "" when none is known.
func (s *Server) supportURL() string {
	if s.cfg.SupportURL != "" {
		return s.cfg.SupportURL
	}
	if u := s.support.Load(); u != nil {
		return *u
	}
	return ""
}

func (s *Server) AddAdmins(ids ...snowflake.ID) {
	for {
		old := s.admins.Load()
		next := make(map[snowflake.ID]bool)
		if old != nil {
			for k := range *old {
				next[k] = true
			}
		}
		for _, id := range ids {
			if id != 0 {
				next[id] = true
			}
		}
		if s.admins.CompareAndSwap(old, &next) {
			return
		}
	}
}

// allow reports whether r is within its visitor's budget.
func (s *Server) allow(r *http.Request) bool {
	ip := clientIP(r)
	if !s.pageLimit.Allow(ip) {
		return false
	}
	if r.Method == http.MethodPost && !s.actionLimit.Allow(ip) {
		journal.Record(journal.Event{Level: journal.Warning, Kind: "too_many_actions", Detail: "actions slowed down for one visitor (" + r.URL.Path + ")"})
		return false
	}
	return true
}

// clientIP is the visitor's address: the one Caddy forwards when the request
// comes from the local network (the proxy), else the connection's.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first, _, _ := strings.Cut(fwd, ",")
			if f := strings.TrimSpace(first); net.ParseIP(f) != nil {
				return f
			}
		}
	}
	return host
}

func (s *Server) isAdmin(id snowflake.ID) bool {
	m := s.admins.Load()
	return m != nil && (*m)[id]
}

// withAdmin requires a signed-in administrator.
func (s *Server) withAdmin(fn sessionHandler) http.HandlerFunc {
	return s.withSession(func(w http.ResponseWriter, r *http.Request, sess *session) {
		if !s.isAdmin(sess.userID) {
			http.Error(w, tr(langFor(r))("forbidden.admin"), http.StatusForbidden)
			return
		}
		fn(w, r, sess)
	})
}

// Handler returns the site with its security headers.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		nonce := newNonce()
		h.Set("Content-Security-Policy", s.csp(nonce))
		h.Set("X-Content-Type-Options", "nosniff")
		if s.checkoutOn() {
			// PayPal checks the site's origin in the referrer.
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		} else {
			h.Set("Referrer-Policy", "same-origin")
		}
		r = r.WithContext(context.WithValue(r.Context(), nonceKey{}, nonce))
		if r.URL.Path != "/healthz" && !s.allow(r) {
			h.Set("Retry-After", "2")
			http.Error(w, tr(langFor(r))("err.too_fast"), http.StatusTooManyRequests)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		h.Set("X-Frame-Options", "DENY")
		if s.secure {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		s.mux.ServeHTTP(w, r)
	})
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	slog.Info("web site listening", slog.String("addr", s.cfg.Addr), slog.String("baseURL", s.cfg.BaseURL))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess *session)

// withSession requires a signed-in administrator, and a valid CSRF token on
// every POST.
func (s *Server) withSession(fn sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		sess, ok := s.sessions.get(c.Value)
		if !ok {
			s.clearCookie(w, sessionCookie)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost {
			if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(sess.csrf)) != 1 {
				http.Error(w, tr(langFor(r))("forbidden.csrf"), http.StatusForbidden)
				return
			}
		}
		fn(w, r, sess)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, ok := s.sessions.get(c.Value); ok {
			http.Redirect(w, r, "/servers", http.StatusSeeOther)
			return
		}
	}
	s.render(w, r, "index.html", map[string]any{"Bot": s.backend.BotName(), "Error": r.URL.Query().Get("erreur")})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	state := randomToken()
	// #nosec G124 -- Secure follows the public URL: always on behind HTTPS, off only for local http tests
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: "/callback", MaxAge: 600,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, s.oauth.loginURL(state), http.StatusSeeOther)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(stateCookie)
	s.clearCookie(w, stateCookie)
	q := r.URL.Query()
	if err != nil || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(q.Get("state"))) != 1 {
		journal.Record(journal.Event{Level: journal.Warning, Kind: "sign_in_session", Detail: "sign-in came back without a valid session (cookies blocked, link reused or expired)"})
		http.Redirect(w, r, "/?erreur=session", http.StatusSeeOther)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		journal.Record(journal.Event{Kind: "sign_in_refused", Detail: "the user cancelled the Discord authorization (" + q.Get("error") + ")"})
		http.Redirect(w, r, "/?erreur=refus", http.StatusSeeOther)
		return
	}
	user, guilds, err := s.oauth.exchange(r.Context(), q.Get("code"))
	if err != nil {
		journal.Record(journal.Event{Level: journal.Problem, Kind: "sign_in_failed", Detail: "Discord refused the sign-in (client secret or redirect URL?): " + err.Error()})
		http.Redirect(w, r, "/?erreur=discord", http.StatusSeeOther)
		return
	}
	managed := guilds[:0]
	for _, g := range guilds {
		if g.manageable() {
			managed = append(managed, g)
		}
	}
	slices.SortFunc(managed, func(a, b discordGuild) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	sess := s.sessions.create(user.ID, user.display(), managed)
	// #nosec G124 -- Secure follows the public URL: always on behind HTTPS, off only for local http tests
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sess.id, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	slog.Info("web: sign-in", slog.String("userID", user.ID.String()), slog.Int("managedGuilds", len(managed)))
	http.Redirect(w, r, "/servers", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess *session) {
	s.sessions.delete(sess.id)
	s.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type guildRow struct {
	ID         snowflake.ID
	Name       string
	BotPresent bool
	Ready      bool
	Cov        coverageView
}

// coverageView is a server's subscription as shown to the signed-in user.
type coverageView struct {
	Kind        string // mine, other, expired, none
	Label       string // plan of the covering subscription, when shown
	CanActivate bool
}

// accountView is the signed-in user's own subscription.
type accountView struct {
	Has, Active bool
	Label       string
	Count       string
}

func (s *Server) planLabel(lang string, sub subscription.Subscription) string {
	t := tr(lang)
	gift := ""
	if sub.Gift {
		gift = t("sub.gift")
	}
	switch {
	case sub.Plan == subscription.PlanLifetime:
		return t("sub.lifetime") + gift
	case sub.Active(s.subs.Now()):
		return t("sub.until", dateFor(lang, sub.LastDay())) + gift
	default:
		return t("sub.expired", dateFor(lang, sub.LastDay()))
	}
}

func (s *Server) account(lang string, userID snowflake.ID) accountView {
	sub, ok := s.subs.Get(userID)
	if !ok {
		return accountView{}
	}
	v := accountView{Has: true, Active: sub.Active(s.subs.Now()), Label: s.planLabel(lang, sub)}
	n := len(s.subs.Guilds(userID))
	if sub.MaxGuilds > 0 {
		v.Count = tr(lang)("sub.count_max", n, sub.MaxGuilds)
	} else {
		v.Count = tr(lang)("sub.count", n)
	}
	return v
}

func (s *Server) coverage(lang string, userID, guildID snowflake.ID) coverageView {
	c := s.subs.Coverage(guildID)
	mineActive := s.account(lang, userID).Active
	switch {
	case c.State == subscription.StateActive && c.Link.UserID == userID:
		return coverageView{Kind: "mine", Label: s.planLabel(lang, c.Sub)}
	case c.State == subscription.StateActive:
		return coverageView{Kind: "other"}
	case c.State == subscription.StateExpired:
		return coverageView{Kind: "expired", CanActivate: mineActive}
	default:
		return coverageView{Kind: "none", CanActivate: mineActive}
	}
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request, sess *session) {
	lang := langFor(r)
	rows := make([]guildRow, 0, len(sess.guilds))
	for _, g := range sess.guilds {
		st := s.backend.GuildStatus(g.ID)
		rows = append(rows, guildRow{ID: g.ID, Name: g.Name, BotPresent: st.BotPresent, Ready: st.Ready(), Cov: s.coverage(lang, sess.userID, g.ID)})
	}
	s.render(w, r, "servers.html", map[string]any{
		"User": sess.userName, "CSRF": sess.csrf, "Guilds": rows, "Bot": s.backend.BotName(),
		"Admin": s.isAdmin(sess.userID), "Account": s.account(lang, sess.userID),
		"OK": okKey(r), "Err": errKey(r), "UserID": sess.userID,
	})
}

// okKey and errKey read the flash message of a redirect, known keys only.
func okKey(r *http.Request) string {
	switch k := r.URL.Query().Get("ok"); k {
	case "activated", "deactivated", "uninstalled", "uninstalled_partial", "paid":
		return "ok." + k
	}
	return ""
}

func errKey(r *http.Request) string {
	switch k := r.URL.Query().Get("err"); k {
	case "nosub", "limit", "taken", "generic", "confirm", "uninstall":
		return "err." + k
	}
	return ""
}

func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request, sess *session) {
	g, ok := s.authorizedGuild(w, r, sess)
	if !ok {
		return
	}
	dest := backTo(r, g.ID)
	switch err := s.subs.Activate(sess.userID, g.ID, g.Name); {
	case err == nil:
		slog.Info("web: subscription activated", slog.String("guildID", g.ID.String()), slog.String("by", sess.userID.String()))
		http.Redirect(w, r, dest+"?ok=activated", http.StatusSeeOther)
	case errors.Is(err, subscription.ErrNoSubscription):
		http.Redirect(w, r, dest+"?err=nosub", http.StatusSeeOther)
	case errors.Is(err, subscription.ErrLimit):
		http.Redirect(w, r, dest+"?err=limit", http.StatusSeeOther)
	case errors.Is(err, subscription.ErrTaken):
		http.Redirect(w, r, dest+"?err=taken", http.StatusSeeOther)
	default:
		slog.Warn("web: activation failed", slog.Any("err", err))
		http.Redirect(w, r, dest+"?err=generic", http.StatusSeeOther)
	}
}

func (s *Server) handleDeactivate(w http.ResponseWriter, r *http.Request, sess *session) {
	g, ok := s.authorizedGuild(w, r, sess)
	if !ok {
		return
	}
	dest := backTo(r, g.ID)
	if err := s.subs.Deactivate(sess.userID, g.ID); err != nil {
		http.Redirect(w, r, dest+"?err=generic", http.StatusSeeOther)
		return
	}
	slog.Info("web: subscription deactivated", slog.String("guildID", g.ID.String()), slog.String("by", sess.userID.String()))
	http.Redirect(w, r, dest+"?ok=deactivated", http.StatusSeeOther)
}

// handleUninstall removes the tool from the server once the box is ticked,
// and frees the server from the subscription of the person who uninstalls.
func (s *Server) handleUninstall(w http.ResponseWriter, r *http.Request, sess *session) {
	g, ok := s.authorizedGuild(w, r, sess)
	if !ok {
		return
	}
	page := "/servers/" + g.ID.String()
	if r.PostFormValue("confirm") != "yes" {
		http.Redirect(w, r, page+"?err=confirm", http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	rep, err := s.backend.Uninstall(ctx, g.ID)
	if err != nil && !rep.OwnerLeft {
		journal.Record(journal.Event{GuildID: g.ID, Guild: g.Name, UserID: sess.userID, Level: journal.Problem, Kind: "uninstall_failed", Detail: err.Error()})
		http.Redirect(w, r, page+"?err=uninstall", http.StatusSeeOther)
		return
	}
	if err := s.subs.Deactivate(sess.userID, g.ID); err != nil && !errors.Is(err, subscription.ErrNotFound) {
		slog.Warn("web: subscription not released after uninstall", slog.String("guildID", g.ID.String()), slog.Any("err", err))
	}
	lvl, detail := journal.Info, fmt.Sprintf("uninstalled, %d relay bot(s) left", len(rep.Left))
	if len(rep.Failed) > 0 {
		lvl, detail = journal.Warning, detail+"; could not leave: "+strings.Join(rep.Failed, ", ")
	}
	journal.Record(journal.Event{GuildID: g.ID, Guild: g.Name, UserID: sess.userID, Level: lvl, Kind: "uninstalled", Detail: detail})
	if len(rep.Failed) > 0 {
		http.Redirect(w, r, "/servers?ok=uninstalled_partial", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/servers?ok=uninstalled", http.StatusSeeOther)
}

// backTo is where an activation returns: the server page or the list.
func backTo(r *http.Request, id snowflake.ID) string {
	if r.PostFormValue("from") == "guild" {
		return "/servers/" + id.String()
	}
	return "/servers"
}

// authorizedGuild checks that the session may configure the server in the
// URL: listed as managed at sign-in, and still allowed now when the bot can
// tell.
func (s *Server) authorizedGuild(w http.ResponseWriter, r *http.Request, sess *session) (discordGuild, bool) {
	id, err := snowflake.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return discordGuild{}, false
	}
	g, ok := sess.guild(id)
	if !ok {
		http.Error(w, tr(langFor(r))("forbidden.guild"), http.StatusForbidden)
		return discordGuild{}, false
	}
	if allowed, known := s.backend.CanManage(id, sess.userID); known && !allowed {
		http.Error(w, tr(langFor(r))("forbidden.revoked"), http.StatusForbidden)
		return discordGuild{}, false
	}
	return g, true
}

func (s *Server) handleGuild(w http.ResponseWriter, r *http.Request, sess *session) {
	g, ok := s.authorizedGuild(w, r, sess)
	if !ok {
		return
	}
	s.renderGuild(w, r, sess, g, nil, "")
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request, sess *session) {
	g, ok := s.authorizedGuild(w, r, sess)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rep, err := s.backend.SetupRoles(ctx, g.ID)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	slog.Info("web: setup", slog.String("guildID", g.ID.String()), slog.String("by", sess.userID.String()),
		slog.Int("created", len(rep.Created)), slog.Int("failed", len(rep.Failed)))
	s.renderGuild(w, r, sess, g, &rep, msg)
}

func (s *Server) renderGuild(w http.ResponseWriter, r *http.Request, sess *session, g discordGuild, rep *SetupReport, errMsg string) {
	lang := langFor(r)
	st := s.backend.GuildStatus(g.ID)
	noteIncomplete(g, st, sess.userID)
	s.render(w, r, "guild.html", map[string]any{
		"Issues": journal.ForGuildIssues(g.ID, 5),
		"User":   sess.userName, "CSRF": sess.csrf, "Guild": g, "Status": st,
		"Report": rep, "Error": errMsg, "Bot": s.backend.BotName(),
		"Cov": s.coverage(lang, sess.userID, g.ID), "Account": s.account(lang, sess.userID),
		"Admin": s.isAdmin(sess.userID), "OK": okKey(r), "Err": errKey(r), "UserID": sess.userID,
	})
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	lang := langFor(r)
	data["Lang"] = lang
	data["Other"] = map[string]string{"fr": "en", "en": "fr"}[lang]
	data["Support"] = s.supportURL()
	data["PayPal"] = s.cfg.PayPalURL
	data["Price"] = s.cfg.PriceLabel
	if s.checkoutOn() {
		data["Plans"] = s.planViews(lang)
		data["PayClientID"] = s.cfg.Checkout.Client.ClientID
		data["PayCurrency"] = s.cfg.Checkout.Currency
	}
	if n, ok := r.Context().Value(nonceKey{}).(string); ok {
		data["Nonce"] = n
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Language", lang)
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpls[lang].ExecuteTemplate(w, name, data); err != nil {
		slog.Warn("web: render failed", slog.String("template", name), slog.Any("err", err))
	}
}

// handleLang remembers the chosen language and goes back to the page.
func (s *Server) handleLang(w http.ResponseWriter, r *http.Request) {
	lang := r.PathValue("lang")
	if !validLang(lang) {
		http.NotFound(w, r)
		return
	}
	// #nosec G124 -- Secure follows the public URL: always on behind HTTPS, off only for local http tests
	http.SetCookie(w, &http.Cookie{Name: langCookie, Value: lang, Path: "/", MaxAge: 365 * 24 * 3600, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, returnPath(r), http.StatusSeeOther) // #nosec G710 -- returnPath only returns fixed routes or /servers/<numeric id>
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	path := "/"
	if name == stateCookie {
		path = "/callback"
	}
	// #nosec G124 -- Secure follows the public URL: always on behind HTTPS, off only for local http tests
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
}

// returnPath is the page of this site the referer points to, rebuilt from
// known routes only (never an open redirect), "/" otherwise.
func returnPath(r *http.Request) string {
	ref, err := url.Parse(r.Referer())
	if err != nil || ref.Host != r.Host {
		return "/"
	}
	switch ref.Path {
	case "/servers":
		return "/servers"
	case "/admin":
		return "/admin"
	}
	if rest, ok := strings.CutPrefix(ref.Path, "/servers/"); ok {
		if id, err := snowflake.Parse(rest); err == nil {
			return "/servers/" + strconv.FormatUint(uint64(id), 10)
		}
	}
	return "/"
}

func joinList(items []string) string { return strings.Join(items, ", ") }

// dict builds a map for passing several values to a sub-template.
func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("dict: odd number of arguments")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, errors.New("dict: keys must be strings")
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

// incompleteLimit records an unfinished setup at most every 10 minutes per
// server, however often the page is reloaded.
var incompleteLimit = ratelimit.New(10*time.Minute, 1)

// noteIncomplete writes in the setup journal what the server page shows as
// still to do, so the administrator sees why an install does not work.
func noteIncomplete(g discordGuild, st GuildStatus, by snowflake.ID) {
	if st.Ready() {
		return
	}
	var todo []string
	if !st.BotPresent {
		todo = append(todo, "main bot not invited")
	}
	if len(st.MissingPermissions) > 0 {
		todo = append(todo, "missing permissions: "+strings.Join(st.MissingPermissions, ", "))
	}
	missing := 0
	for _, h := range st.RelayBots {
		if !h.Present {
			missing++
		}
	}
	if missing > 0 {
		todo = append(todo, fmt.Sprintf("%d relay bot(s) not invited", missing))
	}
	var roles []string
	for _, r := range st.Roles {
		switch {
		case !r.Exists:
			roles = append(roles, r.Name+" (missing)")
		case r.AboveBot:
			roles = append(roles, r.Name+" (above the bot)")
		}
	}
	if len(roles) > 0 {
		todo = append(todo, "roles: "+strings.Join(roles, ", "))
	}
	if len(todo) == 0 || !incompleteLimit.Allow(g.ID.String()) {
		return
	}
	journal.Record(journal.Event{GuildID: g.ID, Guild: g.Name, UserID: by, Level: journal.Warning, Kind: "setup_incomplete", Detail: strings.Join(todo, "; ")})
}
