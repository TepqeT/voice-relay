// Package payment takes payments on the site through PayPal Checkout: the
// buyer pays in a PayPal window opened from the site (PayPal account or card),
// the server creates and captures the order with the REST API, and the money
// lands on the PayPal account that owns the API credentials.
package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bases of the PayPal REST API.
const (
	SandboxAPI = "https://api-m.sandbox.paypal.com"
	LiveAPI    = "https://api-m.paypal.com"
)

// ErrPayPal wraps any refusal or unexpected answer from PayPal.
var ErrPayPal = errors.New("paypal")

// Client calls the PayPal Orders v2 API with app credentials.
type Client struct {
	ClientID string
	Secret   string
	Base     string // SandboxAPI or LiveAPI
	HTTP     *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewClient returns a client for the sandbox (live=false) or production.
func NewClient(clientID, secret string, live bool) *Client {
	base := SandboxAPI
	if live {
		base = LiveAPI
	}
	return &Client{ClientID: clientID, Secret: secret, Base: base, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// accessToken returns a cached OAuth token, renewed a minute before it ends.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/v1/oauth2/token", strings.NewReader(url.Values{"grant_type": {"client_credentials"}}.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.ClientID, c.Secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	c.token = out.AccessToken
	c.expires = time.Now().Add(time.Duration(out.ExpiresIn)*time.Second - time.Minute)
	return c.token, nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPayPal, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%w: HTTP %d: %s", ErrPayPal, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("%w: unreadable answer: %v", ErrPayPal, err)
		}
	}
	return nil
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// Order is what the site asks PayPal to charge.
type Order struct {
	Amount      string // "4.99"
	Currency    string // "EUR"
	Description string // shown to the buyer
	CustomID    string // our reference (account and plan), echoed on capture
}

// CreateOrder registers an order to be approved by the buyer; returns its ID.
func (c *Client) CreateOrder(ctx context.Context, o Order) (string, error) {
	in := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []map[string]any{{
			"amount":      map[string]string{"currency_code": o.Currency, "value": o.Amount},
			"description": o.Description,
			"custom_id":   o.CustomID,
		}},
		"payment_source": map[string]any{"paypal": map[string]any{"experience_context": map[string]string{
			"shipping_preference": "NO_SHIPPING",
			"user_action":         "PAY_NOW",
		}}},
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, "/v2/checkout/orders", in, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("%w: order without ID", ErrPayPal)
	}
	return out.ID, nil
}

// Capture is a captured payment as PayPal reports it.
type Capture struct {
	OrderID   string
	CaptureID string
	Status    string // COMPLETED when the money is received
	Amount    string
	Currency  string
	CustomID  string
	Payer     string // buyer's PayPal e-mail, may be empty
}

// CaptureOrder takes the money of an approved order.
func (c *Client) CaptureOrder(ctx context.Context, orderID string) (Capture, error) {
	if orderID == "" || strings.ContainsAny(orderID, "/?#") {
		return Capture{}, fmt.Errorf("%w: invalid order ID", ErrPayPal)
	}
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Payer  struct {
			Email string `json:"email_address"`
		} `json:"payer"`
		PurchaseUnits []struct {
			Payments struct {
				Captures []struct {
					ID       string `json:"id"`
					Status   string `json:"status"`
					CustomID string `json:"custom_id"`
					Amount   struct {
						Currency string `json:"currency_code"`
						Value    string `json:"value"`
					} `json:"amount"`
				} `json:"captures"`
			} `json:"payments"`
		} `json:"purchase_units"`
	}
	if err := c.call(ctx, http.MethodPost, "/v2/checkout/orders/"+orderID+"/capture", map[string]any{}, &out); err != nil {
		return Capture{}, err
	}
	cp := Capture{OrderID: out.ID, Status: out.Status, Payer: out.Payer.Email}
	if len(out.PurchaseUnits) > 0 && len(out.PurchaseUnits[0].Payments.Captures) > 0 {
		k := out.PurchaseUnits[0].Payments.Captures[0]
		cp.CaptureID, cp.Status, cp.CustomID = k.ID, k.Status, k.CustomID
		cp.Amount, cp.Currency = k.Amount.Value, k.Amount.Currency
	}
	return cp, nil
}

// MaxDonation caps one donation amount, a guard against typos in the
// configuration.
const MaxDonation = 1000

// ParseAmounts reads DONATION_AMOUNTS: "3, 5, 10, 20". Amounts come back
// normalized ("5.00"), in the given order, each once.
func ParseAmounts(s string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		f, err := strconv.ParseFloat(part, 64)
		if err != nil || f < 1 || f > MaxDonation {
			return nil, fmt.Errorf("amount %q: expected a number from 1 to %d", part, MaxDonation)
		}
		a := strconv.FormatFloat(f, 'f', 2, 64)
		if seen[a] {
			return nil, fmt.Errorf("amount %q listed twice", part)
		}
		seen[a] = true
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no donation amount")
	}
	return out, nil
}
