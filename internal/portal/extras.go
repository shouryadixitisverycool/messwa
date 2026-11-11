package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// ExtraRegistration is one extra item, such as a sweet or a drink, that the
// account registered on top of a meal.
type ExtraRegistration struct {
	Date      string  `json:"meal_date"`
	Meal      string  `json:"meal_type"`
	Mess      string  `json:"meal_mess"`
	Name      string  `json:"extra_name"`
	AvailedAt *string `json:"availed_at"`
}

func (e ExtraRegistration) Availed() bool { return e.AvailedAt != nil && *e.AvailedAt != "" }

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
