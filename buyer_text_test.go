package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Tests pin the first variant of each reply and send its parts at once.
	pick = func(int) int { return 0 }
	partGap = 0
	os.Exit(m.Run())
}

func TestDayLabel(t *testing.T) {
	loc := time.FixedZone("IST", 5*3600+1800)
	now := time.Date(2026, 11, 12, 23, 30, 0, 0, loc)
	for date, want := range map[string]string{
		"2026-11-12": "",
		"2026-11-13": "tmrw",
		"2026-11-15": "15 nov",
		"2026-12-01": "1 dec",
	} {
		if got := dayLabel(now, date); got != want {
			t.Errorf("dayLabel(%s) = %q, want %q", date, got, want)
		}
	}
}

// Every variant must stay in the groups' style: lowercase, no ₹, nothing
// a real seller wouldn't type.
func TestBuyerTextStyle(t *testing.T) {
	defer func(p func(int) int) { pick = p }(pick)
	for v := 0; v < 4; v++ {
		pick = func(n int) int { return min(v, n-1) }
		for _, r := range []reply{
			sayOpening(60, ""), sayOpening(60, "tmrw"), sayCounter(50, 1), sayCounter(45, 2), sayLowest(40),
			sayWhich("kadamba non-veg", 60), sayRestate(60), sayQueued(50), sayPayOther(), sayNoScreenshots(),
			saySold(), paymentPrompt("30.00", false), paymentPrompt("1.00", true),
		} {
			if len(r) == 0 || len(r) > 3 {
				t.Errorf("%q: want one to three messages", r)
			}
			for _, part := range r {
				for _, bad := range []string{"₹", "negotiable", "Please", "WhatsApp", "QR", ":"} {
					if strings.Contains(part, bad) {
						t.Errorf("%q contains %q", part, bad)
					}
				}
			}
		}
	}
	if got := paymentPrompt("30.00", false); got[0] != "ok 30" {
		t.Errorf("prompt starts %q", got[0])
	}
	if got := sayOpening(30, "tmrw"); len(got) != 1 || !strings.HasSuffix(got[0], ", its for tmrw") {
		t.Errorf("opening for tomorrow = %q", got)
	}
}
