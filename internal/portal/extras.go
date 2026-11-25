package portal

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ExtraRate accepts the portal's extra_rate whether returned as a JSON string
// ("4000"), number (4000), or null.
type ExtraRate string

func (r *ExtraRate) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*r = ""
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var u string
		if err := json.Unmarshal(b, &u); err != nil {
			return err
		}
		*r = ExtraRate(strings.TrimSpace(u))
		return nil
	}
	*r = ExtraRate(s)
	return nil
}

// Paise parses the portal rate. Integer strings ("1500", "4000") are treated
// as paise (matching the portal's /registrations/extras contract), while
// decimal rupee strings ("36.00") are converted to paise. Returns 0 when unset
// or unparseable.
func (r ExtraRate) Paise() int64 {
	s := strings.TrimSpace(string(r))
	if s == "" {
		return 0
	}
	if strings.Contains(s, ".") {
		if f, err := strconv.ParseFloat(s, 64); err == nil && f >= 0 {
			return int64(math.Round(f * 100))
		}
		return 0
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil && v >= 0 {
		return v
	}
	return 0
}

// ExtraRegistration is one extra item, such as an egg or a sweet, that the
// account registered on top of a meal.
type ExtraRegistration struct {
	ID        string    `json:"id"`
	Extra     string    `json:"extra"`
	Date      string    `json:"meal_date"`
	Meal      string    `json:"meal_type"`
	Mess      string    `json:"meal_mess"`
	Name      string    `json:"extra_name"`
	Rate      ExtraRate `json:"extra_rate"`
	AvailedAt *string   `json:"availed_at"`
}

func (e ExtraRegistration) Availed() bool { return e.AvailedAt != nil && *e.AvailedAt != "" }

// parseAvailedTime parses an extra registration's availed_at timestamp.
// Naive timestamps without a zone offset are interpreted in loc (falling back
// to UTC if loc is nil).
func parseAvailedTime(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	if loc == nil {
		loc = time.UTC
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// AvailableExtras computes how many registered units of extraName are still
// free to allocate or deliver, reconciling live portal rows against already-
// sold deliveries (by their QR send time) and other committed slots.
//
// An availed row can only explain a sold delivery if availed_at is at or after
// the delivery's sent_at. Deliveries whose QR has not finished sending yet
// (zero sent_at) never match an availed row. Note: because the portal shares
// one account QR across seller and buyers, an extra scanned by the owner
// *after* a QR was sent cannot be distinguished from the buyer's scan by
// timestamp alone.
func AvailableExtras(rows []ExtraRegistration, extraName string, soldSentAt []time.Time, otherCommitted int, loc *time.Location) (total, availed, unavailed, available int) {
	want := NormalizeExtraName(extraName)
	var availedTimes []time.Time
	for _, r := range rows {
		if !strings.EqualFold(NormalizeExtraName(r.Name), want) {
			continue
		}
		total++
		if r.Availed() {
			availed++
			if t, ok := parseAvailedTime(*r.AvailedAt, loc); ok {
				availedTimes = append(availedTimes, t)
			}
		}
	}
	unavailed = total - availed
	// Greedy 1-to-1 matching: each sold delivery (with a known send time) can
	// only match an unmatched availed_at strictly after sent_at. Send times
	// are whole seconds, so a scan in the same second may be the owner's and
	// is never credited to a buyer.
	sortedSold := make([]time.Time, 0, len(soldSentAt))
	for _, t := range soldSentAt {
		if !t.IsZero() {
			sortedSold = append(sortedSold, t)
		}
	}
	sort.Slice(sortedSold, func(i, j int) bool { return sortedSold[i].Before(sortedSold[j]) })
	sort.Slice(availedTimes, func(i, j int) bool { return availedTimes[i].Before(availedTimes[j]) })
	matched := 0
	ai := 0
	for _, st := range sortedSold {
		for ai < len(availedTimes) && !availedTimes[ai].After(st) {
			ai++
		}
		if ai < len(availedTimes) {
			matched++
			ai++
		}
	}
	unscannedSold := len(soldSentAt) - matched
	free := min(unavailed-unscannedSold, total-len(soldSentAt)) - otherCommitted
	if free < 0 {
		free = 0
	}
	return total, availed, unavailed, free
}

// NormalizeExtraName collapses whitespace in an extra's display name.
func NormalizeExtraName(name string) string {
	return strings.Join(strings.Fields(name), " ")
}

// ExtraSummary groups one meal's extra registrations by item name.
type ExtraSummary struct {
	Name      string
	Mess      string // mess name without diet, e.g. "kadamba"
	RatePaise int64
	Total     int
	Availed   int
	Unavailed int
}

// SummarizeExtras groups rows by normalized extra name (case-insensitive),
// preserving first-seen order and casing.
func SummarizeExtras(rows []ExtraRegistration) []ExtraSummary {
	var out []ExtraSummary
	idx := map[string]int{}
	for _, r := range rows {
		name := NormalizeExtraName(r.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		i, ok := idx[key]
		if !ok {
			i = len(out)
			idx[key] = i
			mess, _ := (Registration{Mess: r.Mess}).MessDiet()
			out = append(out, ExtraSummary{Name: name, Mess: mess, RatePaise: r.Rate.Paise()})
		}
		out[i].Total++
		if r.Availed() {
			out[i].Availed++
		} else {
			out[i].Unavailed++
		}
		if out[i].Mess == "" {
			out[i].Mess, _ = (Registration{Mess: r.Mess}).MessDiet()
		}
		if out[i].RatePaise == 0 {
			out[i].RatePaise = r.Rate.Paise()
		}
	}
	return out
}

// ExtraRegistrations reads the extras registered for one meal, the same read
// as IIITH-mess-MCP's get_extra_registrations: GET /registrations/extras with
// meal and date.
func (c *Client) ExtraRegistrations(ctx context.Context, date, meal string) ([]ExtraRegistration, error) {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return nil, errors.New("date must be YYYY-MM-DD")
	}
	if meal != "breakfast" && meal != "lunch" && meal != "snacks" && meal != "dinner" {
		return nil, errors.New("meal must be breakfast, lunch, snacks or dinner")
	}
	data, err := c.getData(ctx, "/registrations/extras", url.Values{"meal": {meal}, "date": {date}})
	if err != nil {
		return nil, err
	}
	var rows []ExtraRegistration
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, errors.New("invalid extras response: expected extra registration array")
	}
	out := []ExtraRegistration{}
	for _, r := range rows {
		r.Name = strings.TrimSpace(r.Name)
		if r.Name == "" {
			return nil, errors.New("invalid extras response: extra registration without a name")
		}
		// The query already filters by meal and date. Skip rows that say
		// otherwise, in case the portal widens the filter, but accept a
		// timestamp in place of a bare date.
		if (r.Date != "" && !strings.HasPrefix(r.Date, date)) || (r.Meal != "" && r.Meal != meal) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}
