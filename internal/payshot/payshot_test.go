package payshot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParsePaise(t *testing.T) {
	for in, want := range map[string]int64{"3": 300, "35": 3500, "32.5": 3250, "32.50": 3250, "₹1,032.05": 103205, " Rs. 40 ": 4000} {
		got, err := ParsePaise(in)
		if err != nil || got != want {
			t.Errorf("ParsePaise(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "3.505", "-3", "3..5"} {
		if _, err := ParsePaise(in); err == nil {
			t.Errorf("ParsePaise(%q) succeeded", in)
		}
	}
}

func TestPayeeMatches(t *testing.T) {
	const upi = "9599119322@upi"
	for shown, want := range map[string]bool{
		"••••••9322@upi":  true, // PhonePe "Paid to"
		"XXXXXX9322@upi":  true, // PhonePe "Sent to"
		"9599119322@UPI":  true,
		"••••••1234@upi":  false,
		"••••••9322@ybl":  false, // same digits, other handle
		"••••••322@upi":   false, // too few visible to trust
		"9322":            false,
		"":                false,
		"someone9322@upi": false,
		"19322@upi":       false, // unmasked, so it must be the whole ID
	} {
		if got := PayeeMatches(shown, upi); got != want {
			t.Errorf("PayeeMatches(%q) = %v, want %v", shown, got, want)
		}
	}
}

// fakeGemini answers generateContent per model: a status code, or a
// receipt as the model's JSON text.
func fakeGemini(t *testing.T, answers map[string]any) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		model := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ":generateContent")
		mu.Lock()
		calls = append(calls, model)
		mu.Unlock()
		if r.Header.Get("x-goog-api-key") != "k" {
			t.Errorf("missing API key header")
		}
		switch a := answers[model].(type) {
		case int:
			w.WriteHeader(a)
		case string:
			json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": a}}}}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

const rs3 = `{"is_payment_receipt":true,"status":"success","amount":"3","payee_name":"S","payee_upi":"••••••9322@upi","utr":"2772 9858 1603","transaction_id":"T1","paid_at":"07:39 pm","app":"PhonePe"}`

func TestReadFallsBackWhenBusy(t *testing.T) {
	srv, calls := fakeGemini(t, map[string]any{"busy": http.StatusServiceUnavailable, "limited": http.StatusTooManyRequests, "good": rs3})
	r := &Reader{Key: "k", Models: []string{"busy", "limited", "good"}, Endpoint: srv.URL + "/"}
	rc, model, err := r.Read(context.Background(), []byte("\x89PNG\r\n\x1a\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if model != "good" || rc.AmountPaise != 300 || rc.UTR != "277298581603" || !rc.IsReceipt {
		t.Fatalf("got %q %+v", model, rc)
	}
	if len(*calls) != 3 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestReadStopsOnHardError(t *testing.T) {
	srv, calls := fakeGemini(t, map[string]any{"bad": http.StatusBadRequest, "good": rs3})
	r := &Reader{Key: "k", Models: []string{"bad", "good"}, Endpoint: srv.URL + "/"}
	if _, _, err := r.Read(context.Background(), []byte("img"), "image/png"); err == nil {
		t.Fatal("a 400 should fail the read, not try the next model")
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestReadRejectsBadAmount(t *testing.T) {
	srv, _ := fakeGemini(t, map[string]any{"m": strings.Replace(rs3, `"amount":"3"`, `"amount":"three"`, 1)})
	r := &Reader{Key: "k", Models: []string{"m"}, Endpoint: srv.URL + "/"}
	if _, _, err := r.Read(context.Background(), []byte("img"), "image/png"); err == nil {
		t.Fatal("an unparseable amount should be an error, not ₹0")
	}
}

func TestNameMatches(t *testing.T) {
	for shown, want := range map[string]bool{
		"SHOURYA DIXIT":   true,
		"Shourya Dixit":   true,
		"Dixit, Shourya":  true,
		"SHOURYA DIXIT S": true,
		"Shourya":         false,
		"Shourya Dikshit": false,
		"":                false,
	} {
		if got := NameMatches(shown, "Shourya Dixit"); got != want {
			t.Errorf("NameMatches(%q) = %v, want %v", shown, got, want)
		}
	}
}

// TestPaidTo covers the eval set's payees: the name decides, the UPI ID
// alone also passes, and no name set skips the check.
func TestPaidTo(t *testing.T) {
	const upi, name = "9599119322@upi", "Shourya Dixit"
	for _, c := range []struct {
		rc        Receipt
		name      string
		want      bool
		situation string
	}{
		{Receipt{PayeeName: "SHOURYA DIXIT", PayeeUPI: "shouryadixitisverycool@okhdfcbank"}, name, true, "other UPI ID, right name"},
		{Receipt{PayeeName: "SHOURYA DIXIT"}, name, true, "name only"},
		{Receipt{PayeeName: "SHOURYA DIXIT", PayeeUPI: "959919322@upi"}, name, true, "misread UPI ID, right name"},
		{Receipt{PayeeUPI: "XXXXXX9322@upi"}, name, true, "your UPI ID, no name"},
		{Receipt{PayeeName: "Someone Else", PayeeUPI: "someone@ybl"}, name, false, "someone else"},
		{Receipt{PayeeName: "Someone Else", PayeeUPI: "XXXXXX9322@upi"}, name, false, "someone else, masked ID ending like yours"},
		{Receipt{PayeeName: "Someone Else"}, "", true, "no name set"},
	} {
		if got := PaidTo(c.rc, upi, c.name); got != c.want {
			t.Errorf("%s: PaidTo = %v, want %v", c.situation, got, c.want)
		}
	}
}

// TestReadFallsBackOnGarbledAnswer tries the next model when one answers
// with an amount that doesn't parse.
func TestReadFallsBackOnGarbledAnswer(t *testing.T) {
	srv, calls := fakeGemini(t, map[string]any{"garbled": strings.Replace(rs3, `"amount":"3"`, `"amount":"3 INR"`, 1), "good": rs3})
	r := &Reader{Key: "k", Models: []string{"garbled", "good"}, Endpoint: srv.URL + "/"}
	rc, model, err := r.Read(context.Background(), []byte("img"), "image/png")
	if err != nil || model != "good" || rc.AmountPaise != 300 || len(*calls) != 2 {
		t.Fatalf("got %q %+v %v after %v", model, rc, err, *calls)
	}
}

func TestPaidBefore(t *testing.T) {
	day := time.Date(2026, 11, 24, 19, 39, 0, 0, time.UTC)
	for date, want := range map[string]bool{"2026-11-23": true, "2026-11-24": false, "2026-11-25": false, "": false, "24 Nov": false} {
		if got := (Receipt{PaidDate: date}).PaidBefore(day); got != want {
			t.Errorf("PaidBefore(%q) = %v, want %v", date, got, want)
		}
	}
}
