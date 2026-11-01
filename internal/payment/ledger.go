package payment

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one payment received, kept for the accounts.
type Entry struct {
	At        time.Time `json:"at"`
	UserID    string    `json:"user_id"`
	UserName  string    `json:"user_name,omitempty"`
	Plan      string    `json:"plan"`
	Amount    string    `json:"amount"`
	Currency  string    `json:"currency"`
	OrderID   string    `json:"order_id"`
	CaptureID string    `json:"capture_id"`
	Payer     string    `json:"payer,omitempty"`
}

// Ledger appends payments to a JSON lines file and remembers the orders
// already credited, so an order is never credited twice.
type Ledger struct {
	path string
	mu   sync.Mutex
	seen map[string]bool
	list []Entry
}

// OpenLedger loads path (created on first payment). "" keeps it in memory.
func OpenLedger(path string) (*Ledger, error) {
	l := &Ledger{path: path, seen: map[string]bool{}}
	if path == "" {
		return l, nil
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.OrderID != "" {
			l.seen[e.OrderID] = true
			l.list = append(l.list, e)
		}
	}
	return l, sc.Err()
}

// Seen reports whether orderID was already credited.
func (l *Ledger) Seen(orderID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen[orderID]
}

// Record appends e; false when its order was already recorded.
func (l *Ledger) Record(e Entry) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[e.OrderID] {
		return false, nil
	}
	if l.path != "" {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
			return false, err
		}
		f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return false, err
		}
		b, _ := json.Marshal(e)
		_, werr := fmt.Fprintf(f, "%s\n", b)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return false, werr
		}
	}
	l.seen[e.OrderID] = true
	l.list = append(l.list, e)
	return true, nil
}

// Recent returns up to n payments, newest first.
func (l *Ledger) Recent(n int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, 0, min(n, len(l.list)))
	for i := len(l.list) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, l.list[i])
	}
	return out
}
