package payment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestParsePlans(t *testing.T) {
	ps, err := ParsePlans(" 30d=4.99 , 365d=39, life=79 ")
	if err != nil || len(ps) != 3 {
		t.Fatalf("plans %+v err %v", ps, err)
	}
	if ps[0].Days != 30 || ps[0].Price != "4.99" || ps[1].Days != 365 || !ps[2].Lifetime() || ps[2].Price != "79.00" {
		t.Fatalf("plans %+v", ps)
	}
	for _, bad := range []string{"30=4", "30d=0", "30d=abc", "30d=4,30d=5", "month=4"} {
		if _, err := ParsePlans(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// fakePayPal answers the token, create and capture calls.
func fakePayPal(t *testing.T, capture map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "id" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("POST /v2/checkout/orders", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.Header.Get("Authorization") != "Bearer tok" || in["intent"] != "CAPTURE" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ORDER1"})
	})
	mux.HandleFunc("POST /v2/checkout/orders/{id}/capture", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(capture)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func completed(custom, amount string) map[string]any {
	return map[string]any{"id": "ORDER1", "status": "COMPLETED", "payer": map[string]any{"email_address": "buyer@example.com"},
		"purchase_units": []any{map[string]any{"payments": map[string]any{"captures": []any{map[string]any{
			"id": "CAP1", "status": "COMPLETED", "custom_id": custom, "amount": map[string]any{"currency_code": "EUR", "value": amount}}}}}}}
}

func TestClientCreateAndCapture(t *testing.T) {
	srv := fakePayPal(t, completed("7:30d", "4.99"))
	c := NewClient("id", "secret", false)
	c.Base = srv.URL
	id, err := c.CreateOrder(context.Background(), Order{Amount: "4.99", Currency: "EUR", CustomID: "7:30d"})
	if err != nil || id != "ORDER1" {
		t.Fatalf("order %q err %v", id, err)
	}
	cp, err := c.CaptureOrder(context.Background(), id)
	if err != nil || cp.Status != "COMPLETED" || cp.CaptureID != "CAP1" || cp.Amount != "4.99" || cp.CustomID != "7:30d" || cp.Payer != "buyer@example.com" {
		t.Fatalf("capture %+v err %v", cp, err)
	}
	if _, err := c.CaptureOrder(context.Background(), "../x"); err == nil {
		t.Fatal("an order ID with a path must be refused")
	}
}

func TestLedgerCountsAnOrderOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payments.jsonl")
	l, err := OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := l.Record(Entry{OrderID: "A", Amount: "4.99"}); !ok || err != nil {
		t.Fatalf("first record %v %v", ok, err)
	}
	if ok, _ := l.Record(Entry{OrderID: "A"}); ok {
		t.Fatal("an order must be recorded once")
	}
	l2, err := OpenLedger(path)
	if err != nil || !l2.Seen("A") || len(l2.Recent(10)) != 1 {
		t.Fatalf("reloaded ledger: seen=%v err=%v", l2.Seen("A"), err)
	}
}
