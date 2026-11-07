package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/TepqeT/voice-relay/internal/journal"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/TepqeT/voice-relay/internal/payment"
)

// Donations: Voice Relay is free; whoever wants to support it picks an amount
// on the site and pays in the PayPal window (PayPal account or card). The
// server creates and captures the order and writes it down in the ledger.
// Money goes to the PayPal account of the API credentials. Signing in is not
// needed: a donation buys nothing, so the only guard is the same-origin check
// against forged requests.

// Checkout configures donations on the site; nil Client disables them.
type Checkout struct {
	Client   *payment.Client
	Amounts  []string // donation amounts offered, normalized ("5.00")
	Currency string   // "EUR"
	Ledger   *payment.Ledger
	Live     bool // production PayPal (else sandbox)
}

// donationCustom marks the orders of this site in PayPal (custom_id).
const donationCustom = "donation"

func (s *Server) checkoutOn() bool {
	c := s.cfg.Checkout
	return c != nil && c.Client != nil && len(c.Amounts) > 0 && c.Ledger != nil
}

type nonceKey struct{}

// newNonce is a per-response CSP nonce for the PayPal script.
func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawStdEncoding.EncodeToString(b)
}

// csp is the Content-Security-Policy; PayPal's script, frames and calls are
// allowed only when donations are on, the script tag carrying nonce.
func (s *Server) csp(nonce string) string {
	if !s.checkoutOn() {
		return "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' https://cdn.discordapp.com; form-action 'self' https://discord.com; base-uri 'none'; frame-ancestors 'none'"
	}
	pp := "https://*.paypal.com https://*.paypalobjects.com"
	return "default-src 'none'; " +
		"script-src 'self' 'nonce-" + nonce + "' " + pp + "; " +
		"style-src 'self' 'nonce-" + nonce + "' " + pp + "; " +
		"img-src 'self' data: https://cdn.discordapp.com " + pp + "; " +
		"frame-src " + pp + "; connect-src 'self' " + pp + "; " +
		"form-action 'self' https://discord.com; base-uri 'none'; frame-ancestors 'none'"
}

// amountView is a donation amount as shown on the site.
type amountView struct {
	Value, Label string
}

func (s *Server) amountViews(lang string) []amountView {
	if !s.checkoutOn() {
		return nil
	}
	c := s.cfg.Checkout
	out := make([]amountView, 0, len(c.Amounts))
	for _, a := range c.Amounts {
		out = append(out, amountView{Value: a, Label: priceLabel(lang, a, c.Currency)})
	}
	return out
}

// priceLabel shows an amount the way lang writes it; whole amounts lose
// their cents ("5 €", "5,50 €").
func priceLabel(lang, price, currency string) string {
	price = strings.TrimSuffix(price, ".00")
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// sameOrigin refuses requests sent by another site: the browser's Origin
// header (or, without it, Sec-Fetch-Site) must name this site.
func sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// donor is the signed-in visitor, if any: user ID and name, else zero.
func (s *Server) donor(r *http.Request) (string, string) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if sess, ok := s.sessions.get(c.Value); ok {
			return sess.userID.String(), sess.userName
		}
	}
	return "", ""
}

// donationGuard answers the refusals common to both donation steps; false
// means the request was answered.
func (s *Server) donationGuard(w http.ResponseWriter, r *http.Request) bool {
	t := tr(langFor(r))
	switch {
	case !s.checkoutOn():
		writeJSON(w, http.StatusNotFound, map[string]string{"error": t("pay.err.off")})
	case !sameOrigin(r):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": t("forbidden.csrf")})
	default:
		return true
	}
	return false
}

// handleDonateOrder creates the PayPal order of the chosen amount.
func (s *Server) handleDonateOrder(w http.ResponseWriter, r *http.Request) {
	if !s.donationGuard(w, r) {
		return
	}
	t := tr(langFor(r))
	amount := r.PostFormValue("amount")
	if !slices.Contains(s.cfg.Checkout.Amounts, amount) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": t("pay.err.amount")})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	id, err := s.cfg.Checkout.Client.CreateOrder(ctx, payment.Order{
		Amount:      amount,
		Currency:    s.cfg.Checkout.Currency,
		Description: "Voice Relay, " + t("donate.item"),
		CustomID:    donationCustom,
	})
	if err != nil {
		journal.Record(journal.Event{Level: journal.Problem, Kind: "donation_order_failed", Detail: err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": t("pay.err.paypal")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// handleDonateCapture takes the money of an approved order and writes the
// donation down. Status, amount, currency and origin are checked against what
// PayPal reports, and each order counts once.
func (s *Server) handleDonateCapture(w http.ResponseWriter, r *http.Request) {
	if !s.donationGuard(w, r) {
		return
	}
	t := tr(langFor(r))
	orderID := strings.TrimSpace(r.PostFormValue("order"))
	if orderID == "" || s.cfg.Checkout.Ledger.Seen(orderID) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": t("pay.err.done")})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cp, err := s.cfg.Checkout.Client.CaptureOrder(ctx, orderID)
	if err != nil {
		journal.Record(journal.Event{Level: journal.Problem, Kind: "donation_capture_failed", Detail: "order " + orderID + ": " + err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": t("pay.err.paypal")})
		return
	}
	if err := checkDonation(cp, s.cfg.Checkout.Amounts, s.cfg.Checkout.Currency); err != nil {
		slog.Error("web: paypal capture not recorded as a donation", slog.String("order", orderID),
			slog.String("status", cp.Status), slog.String("amount", cp.Amount+" "+cp.Currency), slog.String("custom", cp.CustomID), slog.Any("err", err))
		writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": t("pay.err.check")})
		return
	}
	userID, userName := s.donor(r)
	fresh, err := s.cfg.Checkout.Ledger.Record(payment.Entry{
		At: s.subs.Now().UTC(), UserID: userID, UserName: userName, Plan: donationCustom,
		Amount: cp.Amount, Currency: cp.Currency, OrderID: orderID, CaptureID: cp.CaptureID, Payer: cp.Payer,
	})
	if err != nil {
		// Received but not written down: the money is there, only the ledger misses it.
		slog.Error("web: donation received but not recorded", slog.String("order", orderID), slog.String("capture", cp.CaptureID), slog.Any("err", err))
	}
	if !fresh && err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": t("pay.err.done")})
		return
	}
	slog.Info("web: donation received", slog.String("amount", cp.Amount+" "+cp.Currency), slog.String("capture", cp.CaptureID))
	s.stats.Count(statDonation)
	writeJSON(w, http.StatusOK, map[string]string{"thanks": t("donate.thanks")})
}

// checkDonation refuses a capture that is not one of this site's donations.
func checkDonation(cp payment.Capture, amounts []string, currency string) error {
	switch {
	case cp.Status != "COMPLETED":
		return fmt.Errorf("status %s", cp.Status)
	case cp.CustomID != donationCustom:
		return errors.New("not a donation of this site")
	case cp.Currency != currency || !slices.Contains(amounts, cp.Amount):
		return errors.New("amount differs from the offered ones")
	}
	return nil
}
