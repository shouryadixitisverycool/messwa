package portal

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"rsc.io/qr"
)

type CurrentQR struct {
	PNG []byte
	// Stable account binding, not the resettable token or raw account ID.
	AccountHash string
	// The registration QRForMeal checked. Empty from QR.
	Registration Registration
}

// QR matches the portal frontend: encode auth/me.data.token directly, with no
// JSON wrapper or account ID. Never cache or log the token.
func (c *Client) QR(ctx context.Context) (CurrentQR, error) {
	data, err := c.getData(ctx, "/auth/me", nil)
	if err != nil {
		return CurrentQR{}, err
	}
	var profile struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &profile); err != nil || strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Token) == "" || len(profile.Token) > 2048 {
		return CurrentQR{}, errors.New("portal profile has no valid account ID or QR token")
	}
	code, err := qr.Encode(profile.Token, qr.M)
	if err != nil {
		return CurrentQR{}, errors.New("portal QR token could not be encoded")
	}
	code.Scale = 8
	return CurrentQR{PNG: code.PNG(), AccountHash: fmt.Sprintf("%x", sha256.Sum256([]byte(profile.ID)))}, nil
}

// RequireRegistration is deliberately conservative for real-QR delivery.
func (c *Client) RequireRegistration(ctx context.Context, date, meal string) (Registration, error) {
	rows, err := c.Registrations(ctx, date, date)
	if err != nil {
		return Registration{}, err
	}
	var found []Registration
	for _, r := range rows {
		if r.Meal != meal {
			continue
		}
		if r.Category != "registered" || r.Cancelled() || r.Availed() {
			return Registration{}, errors.New("meal is cancelled, used or not a regular registration")
		}
		found = append(found, r)
	}
	if len(found) != 1 {
		return Registration{}, errors.New("expected exactly one active registration for this meal")
	}
	return found[0], nil
}

func (c *Client) QRForMeal(ctx context.Context, date, meal, accountHash string) (CurrentQR, error) {
	reg, err := c.RequireRegistration(ctx, date, meal)
	if err != nil {
		return CurrentQR{}, err
	}
	image, err := c.QR(ctx)
	if err != nil {
		return CurrentQR{}, err
	}
	if accountHash != "" && image.AccountHash != accountHash {
		return CurrentQR{}, errors.New("portal account changed since this delivery was armed")
	}
	image.Registration = reg
	return image, nil
}

// RequireExtra checks that the account has at least need unavailed
// registrations of extraName (case-insensitive) for date and meal. Unlike
// RequireRegistration, it does not fail when the main meal itself was already
// availed or cancelled, because extras are scanned independently. It returns
// a representative unavailed row and the total unavailed count for extraName.
func (c *Client) RequireExtra(ctx context.Context, date, meal, extraName string, need int) (ExtraRegistration, int, error) {
	want := NormalizeExtraName(extraName)
	if want == "" {
		return ExtraRegistration{}, 0, errors.New("extra name must not be empty")
	}
	if need < 1 {
		need = 1
	}
	rows, err := c.ExtraRegistrations(ctx, date, meal)
	if err != nil {
		return ExtraRegistration{}, 0, err
	}
	var first ExtraRegistration
	total, unavailed := 0, 0
	for _, r := range rows {
		if !strings.EqualFold(NormalizeExtraName(r.Name), want) {
			continue
		}
		total++
		if r.Availed() {
			continue
		}
		if unavailed == 0 {
			first = r
			first.Name = NormalizeExtraName(r.Name)
		}
		unavailed++
	}
	if total == 0 {
		return ExtraRegistration{}, 0, fmt.Errorf("no %s registered for %s %s on the mess portal", want, date, meal)
	}
	if unavailed < need {
		return ExtraRegistration{}, unavailed, fmt.Errorf("only %d unavailed %s registered on the mess portal (need %d)", unavailed, want, need)
	}
	if strings.TrimSpace(first.Mess) == "" {
		if regs, err := c.Registrations(ctx, date, date); err == nil {
			for _, r := range regs {
				if r.Meal == meal && !r.Cancelled() && r.Mess != "" {
					first.Mess = r.Mess
					break
				}
			}
		}
	}
	return first, unavailed, nil
}

// QRForExtra verifies at least need unavailed extra registrations and fetches
// the account's current QR. Registration.Mess carries the mess name without a
// diet so extra sales stay distinct from veg/non-veg meal sales.
func (c *Client) QRForExtra(ctx context.Context, date, meal, extraName string, need int, accountHash string) (CurrentQR, int, error) {
	row, unavailed, err := c.RequireExtra(ctx, date, meal, extraName, need)
	if err != nil {
		return CurrentQR{}, unavailed, err
	}
	image, err := c.QR(ctx)
	if err != nil {
		return CurrentQR{}, unavailed, err
	}
	if accountHash != "" && image.AccountHash != accountHash {
		return CurrentQR{}, unavailed, errors.New("portal account changed since this delivery was armed")
	}
	mess, _ := (Registration{Mess: row.Mess}).MessDiet()
	image.Registration = Registration{Date: date, Meal: meal, Mess: mess, Category: "extra"}
	return image, unavailed, nil
}
