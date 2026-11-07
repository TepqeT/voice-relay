package web

import (
	"cmp"
	"errors"
	"github.com/TepqeT/voice-relay/internal/journal"
	"github.com/TepqeT/voice-relay/internal/payment"
	"log/slog"
	"net/http"
	"slices"
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
	Servers []adminServer
	Flagged int  // servers that look lent
	Over    bool // more servers than the limit (limit lowered afterwards)
}

// adminServer is one server covered by a subscription, with the signs of
// lending: the subscriber no longer in it, or without Manage Server there.
type adminServer struct {
	Link  subscription.Link
	Facts ServerFacts
	Flags []string // message keys
}

func (s *Server) adminRows() (rows []adminRow, servers, flagged int) {
	for _, sub := range s.subs.List() {
		row := adminRow{Sub: sub}
		for _, l := range s.subs.Guilds(sub.UserID) {
			f := s.backend.ServerFacts(l.GuildID, sub.UserID)
			srv := adminServer{Link: l, Facts: f}
			switch {
			case !f.BotPresent:
				srv.Flags = append(srv.Flags, "flag.nobot")
			case !f.Member:
				srv.Flags = append(srv.Flags, "flag.notmember")
			case !f.Manages:
				srv.Flags = append(srv.Flags, "flag.notmanager")
			}
			if f.BotPresent && (!f.Member || !f.Manages) {
				row.Flagged++
			}
			row.Servers = append(row.Servers, srv)
		}
		row.Over = sub.MaxGuilds > 0 && len(row.Servers) > sub.MaxGuilds
		servers += len(row.Servers)
		flagged += row.Flagged
		rows = append(rows, row)
	}
	// The accounts most likely to lend first: flagged servers, then count.
	slices.SortStableFunc(rows, func(a, b adminRow) int {
		return cmp.Or(cmp.Compare(b.Flagged, a.Flagged), cmp.Compare(len(b.Servers), len(a.Servers)))
	})
	return rows, servers, flagged
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request, sess *session) {
	s.renderAdmin(w, r, sess, "")
}

func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, sess *session, errKey string) {
	lang := langFor(r)
	rows, servers, flagged := s.adminRows()
	for i := range rows {
		rows[i].Label = s.planLabel(lang, rows[i].Sub)
	}
	ok := ""
	switch k := r.URL.Query().Get("ok"); k {
	case "granted", "revoked", "limited", "unlinked":
		ok = "admin." + k
	}
	s.render(w, r, "admin.html", map[string]any{
		"User": sess.userName, "CSRF": sess.csrf, "Admin": true,
		"Rows": rows, "OK": ok, "Error": errKey,
		"Accounts": len(rows), "Servers": servers, "Flagged": flagged,
		"Tomorrow": time.Now().AddDate(0, 0, 1).Format("2006-01-02"),
		"Payments": s.recentPayments(),
		"Stats":    s.statRows(),
		"Journal":  journal.Recent(60, false),
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

// handleLimit changes how many servers an account may cover.
func (s *Server) handleLimit(w http.ResponseWriter, r *http.Request, sess *session) {
	id, err := snowflake.Parse(r.PostFormValue("user_id"))
	if err != nil {
		s.renderAdmin(w, r, sess, "aerr.userid")
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("max_guilds")))
	if err != nil || n < 0 || n > 1000 {
		s.renderAdmin(w, r, sess, "aerr.max")
		return
	}
	if err := s.subs.SetMaxGuilds(id, n); err != nil {
		s.renderAdmin(w, r, sess, "aerr.save")
		return
	}
	slog.Info("web: subscription limit changed", slog.String("userID", id.String()), slog.Int("maxGuilds", n), slog.String("by", sess.userID.String()))
	http.Redirect(w, r, "/admin?ok=limited", http.StatusSeeOther)
}

// handleUnlink removes one server from the subscription covering it.
func (s *Server) handleUnlink(w http.ResponseWriter, r *http.Request, sess *session) {
	id, err := snowflake.Parse(r.PostFormValue("guild_id"))
	if err != nil {
		s.renderAdmin(w, r, sess, "aerr.userid")
		return
	}
	if err := s.subs.Unlink(id); err != nil {
		s.renderAdmin(w, r, sess, "aerr.save")
		return
	}
	slog.Info("web: server unlinked by an administrator", slog.String("guildID", id.String()), slog.String("by", sess.userID.String()))
	http.Redirect(w, r, "/admin?ok=unlinked", http.StatusSeeOther)
}

// recentPayments lists the last payments received on the site, newest first.
func (s *Server) recentPayments() []payment.Entry {
	if !s.checkoutOn() {
		return nil
	}
	return s.cfg.Checkout.Ledger.Recent(50)
}
