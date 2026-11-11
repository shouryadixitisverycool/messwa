package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeasonOf(t *testing.T) {
	c := Default()
	for _, tc := range []struct{ date, name, from, to string }{
		{"2026-11-05", "Monsoon term", "2026-08-01", "2026-12-07"},
		{"2026-12-07", "Monsoon term", "2026-08-01", "2026-12-07"},
		{"2026-12-08", "Winter break", "2026-12-08", "2026-12-31"},
		{"2027-01-01", "Spring term", "2027-01-01", "2027-05-07"},
		{"2027-06-15", "Summer break", "2027-05-08", "2027-07-31"},
	} {
		sp, err := c.SeasonOf(tc.date)
		if err != nil {
			t.Fatal(err)
		}
		if sp.Name != tc.name || sp.From != tc.from || sp.To != tc.to || sp.Key != tc.from {
			t.Errorf("%s: got %+v", tc.date, sp)
		}
	}
}

func TestSeasonGapsAndNewYear(t *testing.T) {
	// One term that runs over New Year; the rest of the year is a gap.
	c := Default()
	c.Seasons = []Season{{Name: "Long term", Term: true, Starts: "11-01", Ends: "02-28"}}
	sp, _ := c.SeasonOf("2027-01-15")
	if sp.Name != "Long term" || sp.From != "2026-11-01" || sp.To != "2027-02-28" || !sp.Term {
		t.Errorf("over new year: %+v", sp)
	}
	sp, _ = c.SeasonOf("2027-06-01")
	if sp.Name != "Off-season" || sp.Term || sp.From != "2027-03-01" || sp.To != "2027-10-31" {
		t.Errorf("gap: %+v", sp)
	}
}

func TestSeasonSpans(t *testing.T) {
	spans, err := Default().SeasonSpans("2026-11-20", "2027-01-03")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range spans {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "Monsoon term,Winter break,Spring term" {
		t.Errorf("spans %s", got)
	}
}

func TestSeasonsValidate(t *testing.T) {
	for body, want := range map[string]string{
		"[[seasons]]\nname = \"a\"\nstarts = \"01-01\"\nends = \"03-01\"\n[[seasons]]\nname = \"b\"\nstarts = \"02-01\"\nends = \"04-01\"\n": "overlap",
		"[[seasons]]\nname = \"a\"\nstarts = \"13-01\"\nends = \"03-01\"\n":                                                                  "MM-DD",
		"[[seasons]]\nname = \"a\"\nstarts = \"02-29\"\nends = \"03-01\"\n":                                                                  "leap",
	} {
		p := filepath.Join(t.TempDir(), "c.toml")
		os.WriteFile(p, []byte(body), 0o600)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q error, got %v", want, err)
		}
	}
}
