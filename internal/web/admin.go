package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/subscription"
)

// Administration: the operators of the service offer subscriptions (for
// life or until a date) to servers, free of charge, and take them back.

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request, sess *session) {
	s.renderAdmin(w, sess, r.URL.Query().Get("ok"), "")
}

func (s *Server) renderAdmin(w http.ResponseWriter, sess *session, ok, errMsg string) {
	guilds := s.backend.BotGuilds()
	names := make(map[snowflake.ID]string, len(guilds))
	for _, g := range guilds {
		names[g.ID] = g.Name
	}
	s.render(w, "admin.html", map[string]any{
		"User": sess.userName, "CSRF": sess.csrf, "Admin": true,
		"Subs": s.subs.List(), "BotGuilds": guilds, "OK": ok, "Error": errMsg,
		"Tomorrow": time.Now().AddDate(0, 0, 1).Format("2006-01-02"),
	})
}

func (s *Server) handleGrant(w http.ResponseWriter, r *http.Request, sess *session) {
	raw := strings.TrimSpace(r.PostFormValue("guild_id"))
	if raw == "" {
		raw = strings.TrimSpace(r.PostFormValue("guild_pick"))
	}
	id, err := snowflake.Parse(raw)
	if err != nil || id == 0 {
		s.renderAdmin(w, sess, "", "Identifiant de serveur invalide.")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("guild_name"))
	if name == "" {
		for _, g := range s.backend.BotGuilds() {
			if g.ID == id {
				name = g.Name
			}
		}
	}
	sub := subscription.Subscription{
		GuildID: id, GuildName: name, Gift: true, GrantedBy: sess.userID,
		Note: strings.TrimSpace(r.PostFormValue("note")),
	}
	switch r.PostFormValue("plan") {
	case "lifetime":
		sub.Plan = subscription.PlanLifetime
	case "until":
		sub.Plan = subscription.PlanUntil
		day, err := time.ParseInLocation("2006-01-02", r.PostFormValue("until"), time.Local)
		if err != nil {
			s.renderAdmin(w, sess, "", "Date de fin invalide.")
			return
		}
		sub.Expires = day.AddDate(0, 0, 1) // valid through the whole chosen day
	default:
		s.renderAdmin(w, sess, "", "Choisissez une durée.")
		return
	}
	if err := s.subs.Grant(sub); err != nil {
		msg := "Enregistrement impossible : " + err.Error()
		if errors.Is(err, subscription.ErrInvalid) {
			msg = "Abonnement refusé : la date de fin doit être dans le futur."
		}
		s.renderAdmin(w, sess, "", msg)
		return
	}
	slog.Info("web: subscription granted", slog.String("guildID", id.String()), slog.String("plan", string(sub.Plan)), slog.String("by", sess.userID.String()))
	http.Redirect(w, r, "/admin?ok=offert", http.StatusSeeOther)
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request, sess *session) {
	id, err := snowflake.Parse(r.PostFormValue("guild_id"))
	if err != nil {
		s.renderAdmin(w, sess, "", "Identifiant de serveur invalide.")
		return
	}
	if err := s.subs.Revoke(id); err != nil {
		s.renderAdmin(w, sess, "", "Retrait impossible : "+err.Error())
		return
	}
	slog.Info("web: subscription revoked", slog.String("guildID", id.String()), slog.String("by", sess.userID.String()))
	http.Redirect(w, r, "/admin?ok=retire", http.StatusSeeOther)
}
