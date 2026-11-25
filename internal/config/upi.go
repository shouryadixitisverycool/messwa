package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// upiID is a UPI ID (VPA) such as "name@okaxis": a handle, "@", and the
// bank's short name.
var upiID = regexp.MustCompile(`^[a-zA-Z0-9._-]{2,256}@[a-zA-Z][a-zA-Z0-9.-]{1,64}$`)

// ParseUPI reads a UPI ID, or the "upi://pay?pa=..." text inside a UPI QR,
// and returns the UPI ID in lower case. Empty stays empty: no UPI QR.
func ParseUPI(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(strings.ToLower(s), "upi://") {
		u, err := url.Parse(s)
		if err != nil || !strings.EqualFold(u.Host, "pay") {
			return "", fmt.Errorf("that QR isn't a UPI payment QR")
		}
		s = strings.TrimSpace(u.Query().Get("pa"))
		if s == "" {
			return "", fmt.Errorf("that UPI QR has no UPI ID in it")
		}
	}
	if !upiID.MatchString(s) {
		return "", fmt.Errorf("%q isn't a UPI ID like name@okaxis", s)
	}
	return strings.ToLower(s), nil
}

// UPIPayURI is the text of a UPI QR that pays amount rupees ("30.00") to
// the UPI ID upi, with note shown to both sides. UPI apps fill in the
// amount, but most let the buyer change it before paying, so a screenshot
// can still show less. An empty amount leaves it to the buyer.
func UPIPayURI(upi, amount, note string) string {
	q := url.Values{}
	q.Set("pa", upi)
	if amount != "" {
		q.Set("am", amount)
	}
	q.Set("cu", "INR")
	if note != "" {
		q.Set("tn", note)
	}
	// UPI apps expect %20, not +, for spaces.
	return "upi://pay?" + strings.ReplaceAll(q.Encode(), "+", "%20")
}

// ParsePayeeName tidies the name on a bank account, as UPI apps print it
// under "Banking name". Empty is allowed: it turns the payee check off.
func ParsePayeeName(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 80 {
		return "", fmt.Errorf("%q is too long for a name", s)
	}
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if s != "" && letters < 2 {
		return "", fmt.Errorf("%q isn't a name", s)
	}
	return s, nil
}
