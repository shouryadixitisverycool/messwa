package config

import (
	"fmt"
	"sort"
	"time"
)

// Season is a part of the academic year that repeats every year. During a
// term everyone is registered with a mess, so the market is many times
// busier than in a break, when only people who registered themselves eat
// there. The insights page never averages across the two.
type Season struct {
	Name string `toml:"name" json:"name"`
	// Term marks a semester. Everything else is a break.
	Term bool `toml:"term" json:"term"`
	// Starts and Ends are "MM-DD", both inclusive. A season whose end comes
	// before its start runs over New Year and belongs to the year it starts.
	Starts string `toml:"starts" json:"starts"`
	Ends   string `toml:"ends" json:"ends"`
}

// SeasonSpan is one dated run of a season. Days no season covers make an
// "Off-season" break.
type SeasonSpan struct {
	// Key is From, which no other span shares.
	Key  string `json:"key"`
	Name string `json:"name"`
	Term bool   `json:"term"`
	From string `json:"from"`
	To   string `json:"to"`
}

const dateFmt = "2006-01-02"

func defaultSeasons() []Season {
	return []Season{
		{Name: "Spring term", Term: true, Starts: "01-01", Ends: "05-07"},
		{Name: "Summer break", Starts: "05-08", Ends: "07-31"},
		{Name: "Monsoon term", Term: true, Starts: "08-01", Ends: "12-07"},
		{Name: "Winter break", Starts: "12-08", Ends: "12-31"},
	}
}

func parseMonthDay(s string) (time.Month, int, error) {
	t, err := time.Parse("01-02", s)
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not MM-DD", s)
	}
	if t.Month() == time.February && t.Day() == 29 {
		return 0, 0, fmt.Errorf("%q only exists in leap years", s)
	}
	return t.Month(), t.Day(), nil
}

// in returns the span of season s that starts in year y.
func (s Season) in(y int) (from, to time.Time) {
	sm, sd, _ := parseMonthDay(s.Starts)
	em, ed, _ := parseMonthDay(s.Ends)
	from = time.Date(y, sm, sd, 0, 0, 0, 0, time.UTC)
	to = time.Date(y, em, ed, 0, 0, 0, 0, time.UTC)
	if to.Before(from) {
		to = to.AddDate(1, 0, 0)
	}
	return from, to
}

func validateSeasons(ss []Season) error {
	// Mark every day of a non-leap year, so two seasons that share one show up.
	var seen [366]string
	for _, s := range ss {
		if s.Name == "" {
			return fmt.Errorf("season without a name")
		}
		if _, _, err := parseMonthDay(s.Starts); err != nil {
			return fmt.Errorf("season %s starts: %w", s.Name, err)
		}
		if _, _, err := parseMonthDay(s.Ends); err != nil {
			return fmt.Errorf("season %s ends: %w", s.Name, err)
		}
		from, to := s.in(2001)
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			i := d.YearDay()
			if d.Year() > 2001 {
				i += 365
			}
			i = (i - 1) % 365
			if seen[i] != "" {
				return fmt.Errorf("seasons %s and %s overlap on %s", seen[i], s.Name, d.Format("01-02"))
			}
			seen[i] = s.Name
		}
	}
	return nil
}

// SeasonOf returns the span holding date ("YYYY-MM-DD").
func (c *Config) SeasonOf(date string) (SeasonSpan, error) {
	d, err := time.Parse(dateFmt, date)
	if err != nil {
		return SeasonSpan{}, err
	}
	if len(c.Seasons) == 0 {
		return SeasonSpan{Key: "0001-01-01", Name: "All year", From: "0001-01-01", To: "9999-12-31"}, nil
	}
	type run struct {
		from, to time.Time
		s        Season
	}
	var runs []run
	for _, s := range c.Seasons {
		for y := d.Year() - 1; y <= d.Year()+1; y++ {
			from, to := s.in(y)
			runs = append(runs, run{from, to, s})
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].from.Before(runs[j].from) })
	span := func(from, to time.Time, s Season) SeasonSpan {
		f := from.Format(dateFmt)
		return SeasonSpan{Key: f, Name: s.Name, Term: s.Term, From: f, To: to.Format(dateFmt)}
	}
	// The gap before the first run that starts after d, if d is in no run.
	gapFrom := time.Time{}
	for _, r := range runs {
		if !d.Before(r.from) && !d.After(r.to) {
			return span(r.from, r.to, r.s), nil
		}
		if r.to.Before(d) {
			gapFrom = r.to.AddDate(0, 0, 1)
			continue
		}
		return span(gapFrom, r.from.AddDate(0, 0, -1), Season{Name: "Off-season"}), nil
	}
	// Unreachable: runs span three years around d.
	return SeasonSpan{}, fmt.Errorf("no season for %s", date)
}

// SeasonSpans returns the spans that cover from..to, oldest first.
func (c *Config) SeasonSpans(from, to string) ([]SeasonSpan, error) {
	var out []SeasonSpan
	for d := from; d <= to; {
		sp, err := c.SeasonOf(d)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
		if sp.To >= to {
			break
		}
		next, _ := time.Parse(dateFmt, sp.To)
		d = next.AddDate(0, 0, 1).Format(dateFmt)
	}
	return out, nil
}
