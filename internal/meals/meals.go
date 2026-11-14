// Package meals maps timestamps to meal slots.
package meals

import (
	"sort"
	"time"

	"messwa/internal/config"
)

type Schedule struct {
	loc   *time.Location
	meals []meal // sorted by end time
}

type meal struct {
	name       string
	start, end int // minutes after midnight
}

// Slot is one serving of one meal on one date.
type Slot struct {
	Meal  string    `json:"meal"`
	Date  string    `json:"date"` // YYYY-MM-DD in the schedule's timezone
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func New(cfg *config.Config) *Schedule {
	s := &Schedule{loc: cfg.Location()}
	for _, m := range cfg.Meals {
		st, _ := config.ParseClock(m.Starts)
		en, _ := config.ParseClock(m.Ends)
		s.meals = append(s.meals, meal{m.Name, st, en})
	}
	sort.Slice(s.meals, func(i, j int) bool { return s.meals[i].end < s.meals[j].end })
	return s
}

func (s *Schedule) Location() *time.Location { return s.loc }

// Next returns the first slot that hasn't ended at t. If name is non-empty,
// only that meal counts. A post at 10:00 with no meal named is for today's
// lunch; a post saying "lunch" at 15:00 is for tomorrow's.
func (s *Schedule) Next(t time.Time, name string) (Slot, bool) {
	local := t.In(s.loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.loc)
	for d := 0; d < 2; d++ {
		date := day.AddDate(0, 0, d)
		for _, m := range s.meals {
			if name != "" && m.name != name {
				continue
			}
			end := date.Add(time.Duration(m.end) * time.Minute)
			if end.After(local) {
				return Slot{
					Meal:  m.name,
					Date:  date.Format("2006-01-02"),
					Start: date.Add(time.Duration(m.start) * time.Minute),
					End:   end,
				}, true
			}
		}
	}
	return Slot{}, false
}

// Current returns the slot being served at t, if any.
func (s *Schedule) Current(t time.Time) (Slot, bool) {
	slot, ok := s.Next(t, "")
	if ok && !t.Before(slot.Start) {
		return slot, true
	}
	return Slot{}, false
}

// Windows lists every meal slot for the date containing t.
func (s *Schedule) Windows(t time.Time) []Slot {
	local := t.In(s.loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.loc)
	var out []Slot
	for _, m := range s.meals {
		out = append(out, Slot{
			Meal:  m.name,
			Date:  day.Format("2006-01-02"),
			Start: day.Add(time.Duration(m.start) * time.Minute),
			End:   day.Add(time.Duration(m.end) * time.Minute),
		})
	}
	return out
}

// Without returns the schedule minus the meals hide reports true for. It
// returns s itself when nothing is hidden.
func (s *Schedule) Without(hide func(string) bool) *Schedule {
	out := &Schedule{loc: s.loc}
	for _, m := range s.meals {
		if !hide(m.name) {
			out.meals = append(out.meals, m)
		}
	}
	if len(out.meals) == len(s.meals) {
		return s
	}
	return out
}
