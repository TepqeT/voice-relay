package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/subscription"
)

// Administration: the operators of the service offer subscriptions to
// Discord accounts (for life or until a date, free of charge) and take them
// back. The account owner then activates it on their servers.

type adminRow struct {
	Sub     subscription.Subscription
	Label   string
	Servers []subscription.Link
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request, sess *session) {
	s.renderAdmin(w, r, sess, "")
}

func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, sess *session, errKey string) {
	lang := langFor(r)
	var rows []adminRow
	for _, sub := range s.subs.List() {
		rows = append(rows, adminRow{Sub: sub, Label: s.planLabel(lang, sub), Servers: s.subs.Guilds(sub.UserID)})
	}
	ok := ""
	switch r.URL.Query().Get("ok") {
	case "granted":
		ok = "admin.granted"
	case "revoked":
		ok = "admin.revoked"
	}
	s.render(w, r, "admin.html", map[string]any{
		"User": sess.userName, "CSRF": sess.csrf, "Admin": true,
		"Rows": rows, "OK": ok, "Error": errKey,
		"Tomorrow": time.Now().AddDate(0, 0, 1).Format("2006-01-02"),
	})
}

func (s *Server) handleGrant(w http.ResponseWriter, r *http.Request, sess *session) {
	id, err := snowflake.Parse(strings.TrimSpace(r.PostFormValue("user_id")))
	if err != nil || id == 0 {
		s.renderAdmin(w, r, sess, "aerr.userid")
		return
	}
	maxGuilds := 0
	if v := strings.TrimSpace(r.PostFormValue("max_guilds")); v != "" {
		if maxGuilds, err = strconv.Atoi(v); err != nil || maxGuilds < 0 || maxGuilds > 1000 {
			s.renderAdmin(w, r, sess, "aerr.max")
			return
		}
	}
	sub := subscription.Subscription{
		UserID: id, UserName: strings.TrimSpace(r.PostFormValue("user_name")), Gift: true,
		GrantedBy: sess.userID, MaxGuilds: maxGuilds, Note: strings.TrimSpace(r.PostFormValue("note")),
	}
	switch r.PostFormValue("plan") {
	case "lifetime":
		sub.Plan = subscription.PlanLifetime
	case "until":
		sub.Plan = subscription.PlanUntil
		day, err := time.ParseInLocation("2006-01-02", r.PostFormValue("until"), time.Local)
		if err != nil {
			s.renderAdmin(w, r, sess, "aerr.date")
			return
		}
		sub.Expires = day.AddDate(0, 0, 1) // the whole chosen day is included
	default:
		s.renderAdmin(w, r, sess, "aerr.plan")
		return
	}
	if err := s.subs.Grant(sub); err != nil {
		key := "aerr.save"
		if errors.Is(err, subscription.ErrInvalid) {
			key = "aerr.past"
		}
		s.renderAdmin(w, r, sess, key)
		return
	}
	slog.Info("web: subscription granted", slog.String("userID", id.String()), slog.String("plan", string(sub.Plan)), slog.String("by", sess.userID.String()))
	http.Redirect(w, r, "/admin?ok=granted", http.StatusSeeOther)
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request, sess *session) {
	id, err := snowflake.Parse(r.PostFormValue("user_id"))
	if err != nil {
		s.renderAdmin(w, r, sess, "aerr.userid")
		return
	}
	if err := s.subs.Revoke(id); err != nil {
		s.renderAdmin(w, r, sess, "aerr.revoke")
		return
	}
	slog.Info("web: subscription revoked", slog.String("userID", id.String()), slog.String("by", sess.userID.String()))
	http.Redirect(w, r, "/admin?ok=revoked", http.StatusSeeOther)
}
