package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/TepqeT/voice-relay/internal/payment"
	"github.com/TepqeT/voice-relay/internal/subscription"
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

func postJSON(t *testing.T, c *http.Client, u string, v url.Values) (int, map[string]string) {
	t.Helper()
	resp, err := c.PostForm(u, v)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestCheckoutOnTheSite(t *testing.T) {
	site, be, c := newTestSite(t)
	var capture map[string]any
	pp := paypalStub(t, &capture)
	client := payment.NewClient("client-id", "secret", false)
	client.Base = pp.URL
	plans, _ := payment.ParsePlans("30d=4.99,life=79")
	ledger, _ := payment.OpenLedger("")
	be.srv.cfg.Checkout = &Checkout{Client: client, Plans: plans, Currency: "EUR", Ledger: ledger}

	signIn(t, site, c, "good")
	resp, err := c.Get(site.URL + "/servers")
	if err != nil {
		t.Fatal(err)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	resp.Body.Close()
	_, page := get(t, c, site.URL+"/servers")
	for _, want := range []string{"Choisissez votre formule", "1 mois", "4,99 €", "À vie", "79,00 €", "https://www.paypal.com/sdk/js?client-id=client-id", "/static/pay.js", `id="paypal-buttons"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("checkout page lacks %q:\n%s", want, page)
		}
	}
	if !strings.Contains(csp, "script-src 'self' 'nonce-") || !strings.Contains(csp, "https://*.paypal.com") {
		t.Fatalf("CSP must allow PayPal with a nonce: %s", csp)
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]

	// Order: needs the CSRF token and a known plan.
	if code, _ := postJSON(t, c, site.URL+"/pay/order", url.Values{"plan": {"30d"}}); code != http.StatusForbidden {
		t.Fatalf("order without csrf = %d", code)
	}
	if code, _ := postJSON(t, c, site.URL+"/pay/order", url.Values{"csrf": {csrf}, "plan": {"999d"}}); code != http.StatusBadRequest {
		t.Fatalf("unknown plan = %d", code)
	}
	if code, out := postJSON(t, c, site.URL+"/pay/order", url.Values{"csrf": {csrf}, "plan": {"30d"}}); code != 200 || out["id"] != "ORDER1" {
		t.Fatalf("order = %d %v", code, out)
	}

	// Wrong amount: not credited.
	capture = captured("BAD", "7:30d", "0.01")
	if code, _ := postJSON(t, c, site.URL+"/pay/capture", url.Values{"csrf": {csrf}, "order": {"BAD"}}); code != http.StatusPaymentRequired || be.subs.Active(111) {
		t.Fatalf("tampered amount = %d", code)
	}
	if _, ok := be.subs.Get(7); ok {
		t.Fatal("a tampered payment must not credit anything")
	}
	// Order made for another account: not credited to this one.
	capture = captured("OTHER", "8:30d", "4.99")
	if code, _ := postJSON(t, c, site.URL+"/pay/capture", url.Values{"csrf": {csrf}, "order": {"OTHER"}}); code != http.StatusPaymentRequired {
		t.Fatalf("someone else's order = %d", code)
	}

	// The real one.
	capture = captured("ORDER1", "7:30d", "4.99")
	code, out := postJSON(t, c, site.URL+"/pay/capture", url.Values{"csrf": {csrf}, "order": {"ORDER1"}})
	if code != 200 || out["redirect"] != "/servers?ok=paid" {
		t.Fatalf("capture = %d %v", code, out)
	}
	sub, ok := be.subs.Get(7)
	if !ok || sub.Plan != subscription.PlanUntil || !sub.Active(be.subs.Now()) || sub.Gift {
		t.Fatalf("subscription after payment: %+v", sub)
	}
	if got := ledger.Recent(5); len(got) != 1 || got[0].CaptureID != "CAP-ORDER1" || got[0].Amount != "4.99" {
		t.Fatalf("ledger %+v", got)
	}
	// Replayed: counted once.
	if code, _ := postJSON(t, c, site.URL+"/pay/capture", url.Values{"csrf": {csrf}, "order": {"ORDER1"}}); code != http.StatusConflict {
		t.Fatalf("replayed capture = %d", code)
	}
	if _, page = get(t, c, site.URL+"/servers?ok=paid"); !strings.Contains(page, "paiement reçu") {
		t.Fatalf("thank-you message missing:\n%s", page)
	}
}

func TestNoScriptWithoutCheckout(t *testing.T) {
	site, _, c := newTestSite(t)
	signIn(t, site, c, "good")
	resp, err := c.Get(site.URL + "/servers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if csp := resp.Header.Get("Content-Security-Policy"); strings.Contains(csp, "script-src") {
		t.Fatalf("no script allowed without checkout: %s", csp)
	}
}
