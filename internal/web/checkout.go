package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/payment"
)

// Checkout: the buyer chooses a plan on the site and pays in the PayPal
// window (PayPal account or card); the server creates and captures the order
// and credits the subscription at once. Money goes to the PayPal account of
// the API credentials.

// Checkout configures payments on the site; nil Client disables them.
type Checkout struct {
	Client   *payment.Client
	Plans    []payment.Plan
	Currency string // "EUR"
	Ledger   *payment.Ledger
	Live     bool // production PayPal (else sandbox)
}

func (s *Server) checkoutOn() bool {
	c := s.cfg.Checkout
	return c != nil && c.Client != nil && len(c.Plans) > 0 && c.Ledger != nil
}

type nonceKey struct{}

// newNonce is a per-response CSP nonce for the PayPal script.
func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawStdEncoding.EncodeToString(b)
}

// csp is the Content-Security-Policy; PayPal's script, frames and calls are
// allowed only when payments are on, the script tag carrying nonce.
func (s *Server) csp(nonce string) string {
	if !s.checkoutOn() {
		return "default-src 'none'; style-src 'self'; img-src 'self' https://cdn.discordapp.com; form-action 'self' https://discord.com; base-uri 'none'; frame-ancestors 'none'"
	}
	pp := "https://*.paypal.com https://*.paypalobjects.com"
	return "default-src 'none'; " +
		"script-src 'self' 'nonce-" + nonce + "' " + pp + "; " +
		"style-src 'self' 'nonce-" + nonce + "' " + pp + "; " +
		"img-src 'self' data: https://cdn.discordapp.com " + pp + "; " +
		"frame-src " + pp + "; connect-src 'self' " + pp + "; " +
		"form-action 'self' https://discord.com; base-uri 'none'; frame-ancestors 'none'"
}

// planView is a plan as shown on the site.
type planView struct {
	ID, Label, Price string
}

func (s *Server) planViews(lang string) []planView {
	if !s.checkoutOn() {
		return nil
	}
	c := s.cfg.Checkout
	out := make([]planView, 0, len(c.Plans))
	for _, p := range c.Plans {
		out = append(out, planView{ID: p.ID, Label: planLabel(lang, p), Price: priceLabel(lang, p.Price, c.Currency)})
	}
	return out
}

func planLabel(lang string, p payment.Plan) string {
	t := tr(lang)
	switch {
	case p.Lifetime():
		return t("pay.plan.life")
	case p.Days == 365:
		return t("pay.plan.year")
	case p.Days%365 == 0:
		return t("pay.plan.years", p.Days/365)
	case p.Days == 30:
		return t("pay.plan.month")
	case p.Days%30 == 0:
		return t("pay.plan.months", p.Days/30)
	}
	return t("pay.plan.days", p.Days)
}

func priceLabel(lang, price, currency string) string {
	sym := map[string]string{"EUR": "€", "USD": "$", "GBP": "£"}[currency]
	if sym == "" {
		sym = currency
	}
	if lang == "fr" {
		return strings.Replace(price, ".", ",", 1) + " " + sym
	}
	if currency == "EUR" {
		return price + " €"
	}
	return sym + price
}

func (s *Server) plan(id string) (payment.Plan, bool) {
	for _, p := range s.cfg.Checkout.Plans {
		if p.ID == id {
			return p, true
		}
	}
	return payment.Plan{}, false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handlePayOrder creates the PayPal order of the chosen plan.
func (s *Server) handlePayOrder(w http.ResponseWriter, r *http.Request, sess *session) {
	lang := langFor(r)
	if !s.checkoutOn() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": tr(lang)("pay.err.off")})
		return
	}
	p, ok := s.plan(r.PostFormValue("plan"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": tr(lang)("pay.err.plan")})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	id, err := s.cfg.Checkout.Client.CreateOrder(ctx, payment.Order{
		Amount:      p.Price,
		Currency:    s.cfg.Checkout.Currency,
		Description: "Voice Relay, " + planLabel(lang, p),
		CustomID:    sess.userID.String() + ":" + p.ID,
	})
	if err != nil {
		slog.Warn("web: paypal order refused", slog.String("by", sess.userID.String()), slog.Any("err", err))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": tr(lang)("pay.err.paypal")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// handlePayCapture takes the money of an approved order and credits the
// subscription of the signed-in account. The amount, currency and account
// are checked against what PayPal reports, and each order counts once.
func (s *Server) handlePayCapture(w http.ResponseWriter, r *http.Request, sess *session) {
	lang := langFor(r)
	t := tr(lang)
	if !s.checkoutOn() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": t("pay.err.off")})
		return
	}
	orderID := strings.TrimSpace(r.PostFormValue("order"))
	if orderID == "" || s.cfg.Checkout.Ledger.Seen(orderID) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": t("pay.err.done")})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cp, err := s.cfg.Checkout.Client.CaptureOrder(ctx, orderID)
	if err != nil {
		slog.Warn("web: paypal capture failed", slog.String("by", sess.userID.String()), slog.String("order", orderID), slog.Any("err", err))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": t("pay.err.paypal")})
		return
	}
	userPart, planID, _ := strings.Cut(cp.CustomID, ":")
	buyer, _ := snowflake.Parse(userPart)
	p, ok := s.plan(planID)
	if err := checkCapture(cp, ok, p, s.cfg.Checkout.Currency, buyer, sess.userID); err != nil {
		slog.Error("web: paypal capture not credited", slog.String("by", sess.userID.String()), slog.String("order", orderID),
			slog.String("status", cp.Status), slog.String("amount", cp.Amount+" "+cp.Currency), slog.String("custom", cp.CustomID), slog.Any("err", err))
		writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": t("pay.err.check")})
		return
	}
	fresh, err := s.cfg.Checkout.Ledger.Record(payment.Entry{
		At: s.subs.Now().UTC(), UserID: sess.userID.String(), UserName: sess.userName, Plan: p.ID,
		Amount: cp.Amount, Currency: cp.Currency, OrderID: orderID, CaptureID: cp.CaptureID, Payer: cp.Payer,
	})
	if err != nil {
		// Paid but not written down: say so loudly, the admin credits by hand.
		slog.Error("web: payment received but not recorded", slog.String("order", orderID), slog.String("capture", cp.CaptureID), slog.Any("err", err))
	}
	if !fresh && err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": t("pay.err.done")})
		return
	}
	if _, err := s.subs.Extend(sess.userID, sess.userName, p.Days, "PayPal "+cp.CaptureID); err != nil {
		slog.Error("web: payment received, subscription not credited", slog.String("order", orderID), slog.String("user", sess.userID.String()), slog.Any("err", err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": t("pay.err.credit")})
		return
	}
	slog.Info("web: payment received", slog.String("user", sess.userID.String()), slog.String("plan", p.ID),
		slog.String("amount", cp.Amount+" "+cp.Currency), slog.String("capture", cp.CaptureID))
	writeJSON(w, http.StatusOK, map[string]string{"redirect": "/servers?ok=paid"})
}

// checkCapture refuses a capture that is not exactly what the site sold to
// this account.
func checkCapture(cp payment.Capture, planOK bool, p payment.Plan, currency string, buyer, user snowflake.ID) error {
	switch {
	case cp.Status != "COMPLETED":
		return fmt.Errorf("status %s", cp.Status)
	case !planOK:
		return errors.New("unknown plan")
	case buyer != user:
		return errors.New("order made for another account")
	case cp.Currency != currency || cp.Amount != p.Price:
		return errors.New("amount differs from the plan")
	}
	return nil
}
