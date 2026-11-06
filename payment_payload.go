package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

// shortcut: this prefix was observed in one Android UPI payment, not a stable
// published schema. The remaining Parcel fields and status codes are unknown.
type paymentPayloadSummary struct {
	Currency       string    `json:"currency"`
	Amount         string    `json:"amount"`
	Timestamp      time.Time `json:"timestamp"`
	HeaderCodes    [2]uint32 `json:"unmapped_header_codes"`
	AmountEncoding uint32    `json:"unmapped_amount_encoding"`
	Status         string    `json:"completion_status"`
	BankVerified   bool      `json:"bank_verified"`
	Note           *string   `json:"note,omitempty"`
}

var decimalAmount = regexp.MustCompile(`^[0-9]{1,12}\.[0-9]{2}$`)

var errTruncatedPayment = errors.New("payment prefix truncated")

func decodePaymentPayload(s string) (*paymentPayloadSummary, error) {
	if len(s) > 1024*1024 {
		return nil, errors.New("payment payload too large")
	}
	b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		return nil, errors.New("invalid payment base64")
	}
	if len(b) < 36 {
		return nil, errTruncatedPayment
	}
	currency, next, err := parcelString(b, 16)
	if err != nil || currency != "INR" || next+4 > len(b) {
		return nil, errors.New("unrecognized payment currency/layout")
	}
	encoding := binary.LittleEndian.Uint32(b[next:])
	amount, _, err := parcelString(b, next+4)
	if err != nil || !decimalAmount.MatchString(amount) {
		return nil, errors.New("unrecognized payment amount/layout")
	}
	ms := int64(binary.LittleEndian.Uint64(b[8:16]))
	t := time.UnixMilli(ms).UTC()
	if t.Year() < 2020 || t.Year() > 2100 {
		return nil, errors.New("unrecognized payment timestamp/layout")
	}
	return &paymentPayloadSummary{
		Currency: currency, Amount: amount, Timestamp: t,
		HeaderCodes:    [2]uint32{binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint32(b[4:])},
		AmountEncoding: encoding, Status: "unknown", BankVerified: false,
		Note: paymentInteropNote(b),
	}, nil
}

// Recognize only the exact interopNote list/String-class wrapper observed on
// Android. Unknown wrappers return no note; embedded Java objects are not loaded.
func paymentInteropNote(b []byte) *string {
	wrapper, _ := hex.DecodeString("0100000015000000500000000f0000006a006100760061002e006c0061006e0067002e0043006c00610073007300000025000000aced0005767200106a6176612e6c616e672e537472696e67a0f0a4387a3bb342020000787000000000000000")
	var note *string
	for offset := 0; offset+4 <= len(b); offset += 4 {
		if binary.LittleEndian.Uint32(b[offset:]) != 11 {
			continue
		}
		key, next, err := parcelString(b, offset)
		if err != nil || key != "interopNote" || !bytes.HasPrefix(b[next:], wrapper) {
			continue
		}
		text, _, err := parcelString(b, next+len(wrapper))
		if err != nil {
			continue
		}
		if note != nil {
			return nil
		} // Ambiguous duplicate keys are not resolved by guessing.
		note = &text
	}
	return note
}

// Android Parcel strings have a little-endian UTF-16 character count, a NUL
// terminator, and padding to a four-byte boundary. Never deserialize Java objects.
func parcelString(b []byte, offset int) (string, int, error) {
	if offset < 0 || offset > len(b)-4 {
		return "", 0, errors.New("missing string length")
	}
	n := int64(int32(binary.LittleEndian.Uint32(b[offset:])))
	if n < 0 || n > 4096 {
		return "", 0, errors.New("invalid string length")
	}
	start := offset + 4
	end := start + int(n)*2
	next := (end + 2 + 3) &^ 3
	if next > len(b) || b[end] != 0 || b[end+1] != 0 {
		return "", 0, errors.New("truncated or unterminated string")
	}
	chars := make([]uint16, int(n))
	for i := range chars {
		chars[i] = binary.LittleEndian.Uint16(b[start+i*2:])
	}
	return string(utf16.Decode(chars)), next, nil
}
