package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/TepqeT/voice-relay/internal/payment"
)

// paypalStub is a fake PayPal API whose capture answer the test sets.
func paypalStub(t *testing.T, capture *map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("POST /v2/checkout/orders", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ORDER1"})
	})
	mux.HandleFunc("POST /v2/checkout/orders/{id}/capture", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(*capture)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func captured(order, custom, amount string) map[string]any {
	return map[string]any{"id": order, "status": "COMPLETED",
		"purchase_units": []any{map[string]any{"payments": map[string]any{"captures": []any{map[string]any{
			"id": "CAP-" + order, "status": "COMPLETED", "custom_id": custom, "amount": map[string]any{"currency_code": "EUR", "value": amount}}}}}}}
}

// postFrom posts a form as the site's own page would (Origin = the site).
func postFrom(t *testing.T, c *http.Client, origin, u string, v url.Values) (int, map[string]string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, u, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestDonationOnTheSite(t *testing.T) {
	site, be, _ := newTestSite(t)
	c := &http.Client{} // a visitor who never signed in
	var capture map[string]any
	pp := paypalStub(t, &capture)
	client := payment.NewClient("client-id", "secret", false)
	client.Base = pp.URL
	amounts, _ := payment.ParseAmounts("3,5,10")
	ledger, _ := payment.OpenLedger("")
	be.srv.cfg.Checkout = &Checkout{Client: client, Amounts: amounts, Currency: "EUR", Ledger: ledger}

	req, _ := http.NewRequest(http.MethodGet, site.URL+"/", nil)
	req.Header.Set("Accept-Language", "fr")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	for _, want := range []string{"Soutenir Voice Relay", "Choisissez un montant", "3 €", "5 €", "10 €", "https://www.paypal.com/sdk/js?client-id=client-id", "/static/donate.js", `id="paypal-buttons"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("home page lacks %q:\n%s", want, page)
		}
	}
	if !strings.Contains(csp, "script-src 'self' 'nonce-") || !strings.Contains(csp, "https://*.paypal.com") {
		t.Fatalf("CSP must allow PayPal with a nonce: %s", csp)
	}

	// Order: same origin only, offered amounts only.
	if code, _ := postFrom(t, c, "https://evil.example", site.URL+"/donate/order", url.Values{"amount": {"5.00"}}); code != http.StatusForbidden {
		t.Fatalf("cross-site order = %d", code)
	}
	if code, _ := postFrom(t, c, "", site.URL+"/donate/order", url.Values{"amount": {"5.00"}}); code != http.StatusForbidden {
		t.Fatalf("order without origin = %d", code)
	}
	if code, _ := postFrom(t, c, site.URL, site.URL+"/donate/order", url.Values{"amount": {"0.01"}}); code != http.StatusBadRequest {
		t.Fatalf("unknown amount = %d", code)
	}
	if code, out := postFrom(t, c, site.URL, site.URL+"/donate/order", url.Values{"amount": {"5.00"}}); code != 200 || out["id"] != "ORDER1" {
		t.Fatalf("order = %d %v", code, out)
	}

	// Tampered amount or a foreign order: not recorded.
	capture = captured("BAD", donationCustom, "0.01")
	if code, _ := postFrom(t, c, site.URL, site.URL+"/donate/capture", url.Values{"order": {"BAD"}}); code != http.StatusPaymentRequired {
		t.Fatalf("tampered amount = %d", code)
	}
	capture = captured("OTHER", "7:30d", "5.00")
	if code, _ := postFrom(t, c, site.URL, site.URL+"/donate/capture", url.Values{"order": {"OTHER"}}); code != http.StatusPaymentRequired {
		t.Fatalf("foreign order = %d", code)
	}
	if got := ledger.Recent(5); len(got) != 0 {
		t.Fatalf("refused captures recorded: %+v", got)
	}

	// The real one: thanked, written down, nothing else changes.
	capture = captured("ORDER1", donationCustom, "5.00")
	code, out := postFrom(t, c, site.URL, site.URL+"/donate/capture", url.Values{"order": {"ORDER1"}})
	if code != 200 || out["thanks"] != messages["en"]["donate.thanks"] {
		t.Fatalf("capture = %d %v", code, out)
	}
	if got := ledger.Recent(5); len(got) != 1 || got[0].CaptureID != "CAP-ORDER1" || got[0].Amount != "5.00" || got[0].Plan != donationCustom || got[0].UserID != "" {
		t.Fatalf("ledger %+v", got)
	}
	if _, ok := be.subs.Get(7); ok {
		t.Fatal("a donation must not create a subscription")
	}
	// Replayed: counted once.
	if code, _ := postFrom(t, c, site.URL, site.URL+"/donate/capture", url.Values{"order": {"ORDER1"}}); code != http.StatusConflict {
		t.Fatalf("replayed capture = %d", code)
	}
}

func TestOwnScriptsOnlyWithoutCheckout(t *testing.T) {
	site, _, c := newTestSite(t)
	signIn(t, site, c, "good")
	resp, err := c.Get(site.URL + "/servers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Only the site's own files: no inline script, no third party.
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self';") || strings.Contains(csp, "nonce") || strings.Contains(csp, "paypal") {
		t.Fatalf("only same-origin scripts allowed without checkout: %s", csp)
	}
	if code, _ := postFrom(t, c, site.URL, site.URL+"/donate/order", url.Values{"amount": {"5.00"}}); code != http.StatusNotFound {
		t.Fatalf("donation without checkout = %d", code)
	}
}
