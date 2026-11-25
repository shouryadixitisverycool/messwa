// Package payshot reads a UPI payment screenshot (PhonePe, GPay, Paytm and
// the like) with a Gemini vision model and returns what it shows: the
// amount, the status, who was paid and the bank reference.
//
// The model only transcribes. Every decision (is the amount enough, is the
// payee right, was this UTR used before) is plain Go on the result, so a
// misread can be traced to one field.
package payshot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// DefaultModels are tried in order. On the free tier the newest Flash
// models are often overloaded (503s, or no answer for a minute), so a busy
// model passes the image to the next one instead of failing the read.
var DefaultModels = []string{"gemini-3.6-flash", "gemini-3.5-flash", "gemini-flash-lite-latest"}

// perModel bounds one model's attempt, so a hung model leaves time for the
// next. Reading a receipt normally takes 1 to 3 seconds.
const perModel = 20 * time.Second

const endpoint = "https://generativelanguage.googleapis.com/v1beta/models/"

// Receipt is what one screenshot shows. Fields the model couldn't read are
// empty.
type Receipt struct {
	// IsReceipt is false for anything that isn't a payment confirmation
	// screen: a photo, a QR, a chat, a bank balance.
	IsReceipt bool `json:"is_payment_receipt"`
	// Status is "success", "pending", "failed" or "unknown".
	Status string `json:"status"`
	// Amount is the amount paid as printed, without the ₹ sign ("3",
	// "32.50"). AmountPaise is the same value parsed.
	Amount      string `json:"amount"`
	AmountPaise int64  `json:"-"`
	PayeeName   string `json:"payee_name"`
	// PayeeUPI is the payee's UPI ID as shown, often masked
	// ("••••••9322@upi").
	PayeeUPI string `json:"payee_upi"`
	// UTR is the 12-digit bank reference (UPI Ref No / UTR).
	UTR string `json:"utr"`
	// TxnID is the app's own transaction ID, such as PhonePe's T2611....
	TxnID string `json:"transaction_id"`
	// PaidAt is the date and time as printed. PaidDate is its date as
	// YYYY-MM-DD, empty when the screen shows none.
	PaidAt   string `json:"paid_at"`
	PaidDate string `json:"paid_date"`
	App      string `json:"app"`
}

// Reader reads screenshots through the Gemini API.
type Reader struct {
	Key    string
	Models []string // DefaultModels when empty
	HTTP   *http.Client
	// Endpoint overrides the API base URL, for tests.
	Endpoint string
}

const prompt = `This image was sent by a buyer as proof of a UPI payment. Transcribe what it shows.

Rules:
- amount: the amount that was PAID in this transaction, digits and decimal point only, no ₹ sign or commas (e.g. "3", "35", "32.50"). Not a balance, not a date, not an ID. If several amounts are shown for the same payment (e.g. "Paid to" and "Debited from"), they are the same payment; give it once. Empty if unreadable.
- status: "success" for a completed payment, "pending" or "failed" if the screen says so, "unknown" if it doesn't say.
- payee_name / payee_upi: who RECEIVED the money, exactly as shown, keeping masking characters like •••••• or XXXXXX.
- utr: the 12-digit UPI reference / UTR / UPI Ref No / RRN, digits only. BHIM labels it "Transaction ID": an all-digit 12-digit transaction ID is the UTR. Empty if not shown.
- transaction_id: the app's own transaction ID if shown.
- paid_at: the date and time of the payment exactly as printed.
- paid_date: the payment's date as YYYY-MM-DD (a two-digit year like "26" is 2026). Empty if no date is shown.
- app: the payment app (PhonePe, Google Pay, Paytm, BHIM, ...).
- is_payment_receipt: false if this is not a payment confirmation screen.
Never guess a value that is not visible; leave it empty.`

var schema = map[string]any{
	"type": "OBJECT",
	"properties": map[string]any{
		"is_payment_receipt": map[string]any{"type": "BOOLEAN"},
		"status":             map[string]any{"type": "STRING", "enum": []string{"success", "pending", "failed", "unknown"}},
		"amount":             map[string]any{"type": "STRING"},
		"payee_name":         map[string]any{"type": "STRING"},
		"payee_upi":          map[string]any{"type": "STRING"},
		"utr":                map[string]any{"type": "STRING"},
		"transaction_id":     map[string]any{"type": "STRING"},
		"paid_at":            map[string]any{"type": "STRING"},
		"paid_date":          map[string]any{"type": "STRING"},
		"app":                map[string]any{"type": "STRING"},
	},
	"required": []string{"is_payment_receipt", "status", "amount", "payee_name", "payee_upi", "utr", "transaction_id", "paid_at", "paid_date", "app"},
}

// busyError is a model being overloaded or rate limited, which the next
// model may not be.
type busyError struct{ err error }

func (e busyError) Error() string { return e.err.Error() }
func (e busyError) Unwrap() error { return e.err }

// Read sends one image to the models in turn and returns what the first
// one to answer read, and which model that was. mime is the image type,
// such as "image/jpeg", as WhatsApp reports it.
func (r *Reader) Read(ctx context.Context, img []byte, mime string) (Receipt, string, error) {
	if r.Key == "" {
		return Receipt{}, "", errors.New("payshot: no API key")
	}
	if mime == "" {
		mime = http.DetectContentType(img)
	}
	models := r.Models
	if len(models) == 0 {
		models = DefaultModels
	}
	var errs []error
	for _, m := range models {
		rc, err := r.readWith(ctx, m, img, mime)
		if err == nil {
			return rc, m, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", m, err))
		var busy busyError
		if ctx.Err() != nil || !errors.As(err, &busy) {
			break
		}
	}
	return Receipt{}, "", fmt.Errorf("payshot: %w", errors.Join(errs...))
}

func (r *Reader) readWith(ctx context.Context, model string, img []byte, mime string) (Receipt, error) {
	ctx, cancel := context.WithTimeout(ctx, perModel)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"contents": []any{map[string]any{"parts": []any{
			map[string]any{"inline_data": map[string]any{"mime_type": mime, "data": base64.StdEncoding.EncodeToString(img)}},
			map[string]any{"text": prompt},
		}}},
		"generationConfig": map[string]any{
			"temperature":      0,
			"responseMimeType": "application/json",
			"responseSchema":   schema,
		},
	})
	if err != nil {
		return Receipt{}, err
	}
	base := r.Endpoint
	if base == "" {
		base = endpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+model+":generateContent", bytes.NewReader(body))
	if err != nil {
		return Receipt{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", r.Key)
	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Receipt{}, busyError{err}
		}
		return Receipt{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		if ctx.Err() != nil {
			return Receipt{}, busyError{err}
		}
		return Receipt{}, err
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return Receipt{}, busyError{err}
		}
		return Receipt{}, err
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Receipt{}, fmt.Errorf("decoding response: %w", err)
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return Receipt{}, fmt.Errorf("empty response: %s", strings.TrimSpace(string(raw)))
	}
	rc, err := parse(out.Candidates[0].Content.Parts[0].Text)
	if err != nil {
		// A garbled answer is this model's problem; the next may do better.
		return Receipt{}, busyError{err}
	}
	return rc, nil
}

func parse(text string) (Receipt, error) {
	var rc Receipt
	if err := json.Unmarshal([]byte(text), &rc); err != nil {
		return Receipt{}, fmt.Errorf("model returned %q: %w", text, err)
	}
	rc.UTR = digitsOnly(rc.UTR)
	if rc.Amount != "" {
		p, err := ParsePaise(rc.Amount)
		if err != nil {
			return Receipt{}, fmt.Errorf("amount %q: %w", rc.Amount, err)
		}
		rc.AmountPaise = p
	}
	return rc, nil
}

var amountRE = regexp.MustCompile(`^(\d+)(?:\.(\d{1,2}))?$`)

// ParsePaise turns "32", "32.5" or "₹1,032.50" into paise.
func ParsePaise(s string) (int64, error) {
	s = strings.NewReplacer("₹", "", ",", "", " ", "", "Rs.", "", "Rs", "").Replace(strings.TrimSpace(s))
	m := amountRE.FindStringSubmatch(s)
	if m == nil {
		return 0, errors.New("not an amount")
	}
	rupees, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, err
	}
	frac := m[2]
	if len(frac) == 1 {
		frac += "0"
	}
	var p int64
	if frac != "" {
		p, _ = strconv.ParseInt(frac, 10, 64)
	}
	return rupees*100 + p, nil
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// PayeeMatches reports whether shown, a UPI ID as a payment app prints it,
// can be upi. Apps mask the front ("••••••9322@upi", "XXXXXX9322@upi"),
// so the handle after @ must match and the visible characters before it
// must end upi's name. At least 4 must be visible.
func PayeeMatches(shown, upi string) bool {
	shown, upi = strings.ToLower(strings.TrimSpace(shown)), strings.ToLower(strings.TrimSpace(upi))
	sName, sHandle, ok1 := strings.Cut(shown, "@")
	uName, uHandle, ok2 := strings.Cut(upi, "@")
	if !ok1 || !ok2 || sHandle != uHandle {
		return false
	}
	visible := strings.TrimLeftFunc(sName, func(r rune) bool { return r == '•' || r == '*' || r == 'x' || r == '.' || r == '·' })
	if visible == sName {
		// Nothing masked: the whole ID is there to compare.
		return sName == uName
	}
	return len(visible) >= 4 && strings.HasSuffix(uName, visible)
}

// NameMatches reports whether shown, a payee name as a payment app prints
// it, names want. Case, spacing, punctuation and word order don't matter;
// every word of want must be there.
func NameMatches(shown, want string) bool {
	have := map[string]bool{}
	for _, w := range nameWords(shown) {
		have[w] = true
	}
	words := nameWords(want)
	for _, w := range words {
		if !have[w] {
			return false
		}
	}
	return len(words) > 0
}

func nameWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// PaidTo reports whether rc paid you. The banking name decides when the
// receipt shows one: apps print it on nearly every receipt, while the UPI
// ID may be another of yours, masked, or misread. A receipt that names
// someone else fails even if a masked UPI ID fits yours, since other IDs
// end in the same digits. Only a receipt with no name falls back to the
// UPI ID. With no name set it's true: the UPI ID alone would turn away real
// payments to your other IDs, so only the amount is checked.
func PaidTo(rc Receipt, upi, name string) bool {
	if name == "" {
		return true
	}
	if strings.TrimSpace(rc.PayeeName) != "" {
		return NameMatches(rc.PayeeName, name)
	}
	return upi != "" && PayeeMatches(rc.PayeeUPI, upi)
}

// PaidBefore reports whether the receipt is dated before day's date, as
// an old receipt from an earlier sale would be. A receipt without a
// readable date is not.
func (rc Receipt) PaidBefore(day time.Time) bool {
	d, err := time.Parse("2006-01-02", rc.PaidDate)
	if err != nil {
		return false
	}
	return d.Format("2006-01-02") < day.Format("2006-01-02")
}
