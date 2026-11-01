package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/disgoorg/snowflake/v2"
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
}

// Server is the administration site.
type Server struct {
	cfg      Config
	backend  Backend
	oauth    *oauth
	sessions *sessionStore
	tmpl     *template.Template
	secure   bool // cookies marked Secure (HTTPS base URL)
	mux      *http.ServeMux
}

// New builds the site.
func New(cfg Config, backend Backend) (*Server, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, errors.New("WEB_BASE_URL must be an absolute URL")
	}
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"purpose": purposeLabel,
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:      cfg,
		backend:  backend,
		oauth:    newOAuth(cfg.ClientID, cfg.ClientSecret, base+"/callback"),
		sessions: newSessionStore(),
		tmpl:     tmpl,
		secure:   u.Scheme == "https",
		mux:      http.NewServeMux(),
	}
	static, _ := fs.Sub(assets, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /login", s.handleLogin)
	s.mux.HandleFunc("GET /callback", s.handleCallback)
	s.mux.HandleFunc("POST /logout", s.withSession(s.handleLogout))
	s.mux.HandleFunc("GET /servers", s.withSession(s.handleServers))
	s.mux.HandleFunc("GET /servers/{id}", s.withSession(s.handleGuild))
	s.mux.HandleFunc("POST /servers/{id}/setup", s.withSession(s.handleSetup))
	return s, nil
}

// Handler returns the site with its security headers.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self' https://cdn.discordapp.com; form-action 'self' https://discord.com; base-uri 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
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
				http.Error(w, "jeton de formulaire invalide, rechargez la page", http.StatusForbidden)
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
	s.render(w, "index.html", map[string]any{"Bot": s.backend.BotName(), "Error": r.URL.Query().Get("erreur")})
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
		http.Redirect(w, r, "/?erreur=session", http.StatusSeeOther)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		http.Redirect(w, r, "/?erreur=refus", http.StatusSeeOther)
		return
	}
	user, guilds, err := s.oauth.exchange(r.Context(), q.Get("code"))
	if err != nil {
		slog.Warn("web: discord sign-in failed", slog.Any("err", err))
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
}

func (s *Server) handleServers(w http.ResponseWriter, _ *http.Request, sess *session) {
	rows := make([]guildRow, 0, len(sess.guilds))
	for _, g := range sess.guilds {
		st := s.backend.GuildStatus(g.ID)
		rows = append(rows, guildRow{ID: g.ID, Name: g.Name, BotPresent: st.BotPresent, Ready: st.Ready()})
	}
	s.render(w, "servers.html", map[string]any{"User": sess.userName, "CSRF": sess.csrf, "Guilds": rows, "Bot": s.backend.BotName()})
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
		http.Error(w, "vous ne gérez pas ce serveur", http.StatusForbidden)
		return discordGuild{}, false
	}
	if allowed, known := s.backend.CanManage(id, sess.userID); known && !allowed {
		http.Error(w, "vous n'avez plus la permission Gérer le serveur sur ce serveur", http.StatusForbidden)
		return discordGuild{}, false
	}
	return g, true
}

func (s *Server) handleGuild(w http.ResponseWriter, r *http.Request, sess *session) {
	g, ok := s.authorizedGuild(w, r, sess)
	if !ok {
		return
	}
	s.renderGuild(w, sess, g, nil, "")
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
	s.renderGuild(w, sess, g, &rep, msg)
}

func (s *Server) renderGuild(w http.ResponseWriter, sess *session, g discordGuild, rep *SetupReport, errMsg string) {
	st := s.backend.GuildStatus(g.ID)
	s.render(w, "guild.html", map[string]any{
		"User": sess.userName, "CSRF": sess.csrf, "Guild": g, "Status": st,
		"Report": rep, "Error": errMsg, "Bot": s.backend.BotName(),
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Warn("web: render failed", slog.String("template", name), slog.Any("err", err))
	}
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	path := "/"
	if name == stateCookie {
		path = "/callback"
	}
	// #nosec G124 -- Secure follows the public URL: always on behind HTTPS, off only for local http tests
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
}

// purposeLabel names a role purpose in French.
func purposeLabel(p string) string {
	switch p {
	case "raid_lead":
		return "Raid Lead"
	case "squad_lead":
		return "Chef d'escouade"
	case "faction":
		return "Faction"
	case "access":
		return "Accès aux salons"
	case "board":
		return "Board (accorde l'accès)"
	case "class":
		return "Classe"
	}
	return p
}
