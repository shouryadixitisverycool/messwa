package main

import (
	"strconv"
	"testing"
	"time"
)

func TestHaggleWalk(t *testing.T) {
	end := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	h := newHaggle(30, 60, 50, true, end, "lunch")
	cases := []struct {
		left      time.Duration
		competing bool
		want      int
	}{
		{4 * time.Hour, false, 50},
		{3 * time.Hour, false, 50},
		{90 * time.Minute, false, 40},
		{10 * time.Minute, false, 35},
		{0, false, 30},
		{10 * time.Minute, true, 50},
	}
	for _, c := range cases {
		if got := h.walk(end.Add(-c.left), c.competing); got != c.want {
			t.Errorf("walk %v before the end (competing %v) = %d, want %d", c.left, c.competing, got, c.want)
		}
	}
	// No market data: hold at max until time runs short.
	if h := newHaggle(30, 60, 0, false, end, "lunch"); h.target != 60 || h.walk(end.Add(-4*time.Hour), false) != 60 {
		t.Fatalf("target = %d", h.target)
	}
	// A median outside the range is clamped to it.
	if h := newHaggle(30, 60, 80, true, end, "lunch"); h.target != 60 {
		t.Fatalf("target = %d", h.target)
	}
	if h := newHaggle(30, 60, 20, true, end, "lunch"); h.target != 30 {
		t.Fatalf("target = %d", h.target)
	}
	// Tiny prices concede by ₹1.
	if h := newHaggle(1, 3, 0, false, end, "lunch"); h.unit != 1 {
		t.Fatalf("unit = %d", h.unit)
	}
}

// replay runs a buyer's messages through decide and returns what the bot
// said, ending with "AGREE n", "PARK" or "PASS".
func replay(h haggle, now time.Time, msgs ...string) []string {
	st := haggleState{ask: h.max}
	var out []string
	for _, m := range msgs {
		s := h.decide(st, parseBuyer(m), now, false)
		switch {
		case s.agree > 0:
			return append(out, "AGREE "+strconv.Itoa(s.agree))
		case s.park != "":
			return append(out, "PARK")
		case s.pass:
			return append(out, "PASS")
		}
		if s.reply != "" {
			out = append(out, s.reply)
			s.next.botMsgs++
		}
		st = s.next
	}
	return out
}

func TestHaggleDecide(t *testing.T) {
	end := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	early := end.Add(-4 * time.Hour)
	cases := []struct {
		name     string
		min, max int
		median   float64
		msgs     []string
		want     []string
	}{
		{"opens then agrees", 30, 60, 50, []string{"available?", "ok"},
			[]string{"₹60 for the lunch, slightly negotiable", "AGREE 60"}},
		{"ok before any price is an opener", 30, 60, 50, []string{"ok"},
			[]string{"₹60 for the lunch, slightly negotiable"}},
		{"same number agrees at once", 30, 60, 50, []string{"same number?"},
			[]string{"AGREE 60"}},
		{"a fair first offer is taken", 30, 60, 50, []string{"Buy @55"},
			[]string{"AGREE 55"}},
		{"offer at or above the ask takes the ask", 30, 60, 50, []string{"price?", "70"},
			[]string{"₹60 for the lunch, slightly negotiable", "AGREE 60"}},
		// Your 2 Apr sale: 55 -> B50? -> 50. Here the bot opens at 60.
		{"counter then meet", 30, 60, 45, []string{"price?", "40?", "45"},
			[]string{"₹60 for the lunch, slightly negotiable", "can do ₹50", "AGREE 45"}},
		// Your 8 Mar sale: 100 -> B80 x4 -> 80. The bot stops at its lowest
		// and hands a buyer who won't move to you.
		{"firm lowballer is parked", 60, 100, 90, []string{"price?", "80", "80", "80", "80"},
			[]string{"₹100 for the lunch, slightly negotiable", "₹90 is the lowest I can do", "PARK"}},
		{"firm offer gets the lowest at once", 30, 60, 45, []string{"price?", "35 final"},
			[]string{"₹60 for the lunch, slightly negotiable", "₹45 is the lowest I can do"}},
		{"a counter that reaches the walk-away price is the lowest", 30, 80, 40, []string{"price?", "20", "25", "30"},
			[]string{"₹80 for the lunch, slightly negotiable", "can do ₹50", "₹40 is the lowest I can do", "PARK"}},
		{"two counters then lowest", 30, 100, 80, []string{"price?", "75", "76", "77"},
			[]string{"₹100 for the lunch, slightly negotiable", "can do ₹90", "can do ₹85", "₹80 is the lowest I can do"}},
		{"lower without a number steps down", 30, 60, 40, []string{"price?", "too much", "too much", "too much", "too much"},
			[]string{"₹60 for the lunch, slightly negotiable", "can do ₹50", "can do ₹45", "₹40 is the lowest I can do", "PARK"}},
		{"question gets the label and price", 30, 60, 50, []string{"price?", "nv?"},
			[]string{"₹60 for the lunch, slightly negotiable", "it's the lunch, ₹60"}},
		{"pass", 30, 60, 50, []string{"price?", "sorry bought"},
			[]string{"₹60 for the lunch, slightly negotiable", "PASS"}},
		{"pay another way parks", 30, 60, 50, []string{"price?", "upi id?"},
			[]string{"₹60 for the lunch, slightly negotiable", "PARK"}},
		{"absurd number is unreadable", 30, 60, 50, []string{"price?", "500", "999"},
			[]string{"₹60 for the lunch, slightly negotiable", "₹60 for the lunch, ok?", "PARK"}},
	}
	for _, c := range cases {
		h := newHaggle(c.min, c.max, c.median, true, end, "lunch")
		got := replay(h, early, c.msgs...)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %q, want %q", c.name, got, c.want)
				break
			}
		}
	}
}

func TestHaggleSoftensNearTheEnd(t *testing.T) {
	end := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	h := newHaggle(30, 60, 50, true, end, "lunch")
	// Four hours out ₹35 is too low; ten minutes out it's fine.
	if got := replay(h, end.Add(-4*time.Hour), "price?", "35"); got[len(got)-1] == "AGREE 35" {
		t.Fatalf("early: %q", got)
	}
	if got := replay(h, end.Add(-10*time.Minute), "price?", "35"); got[len(got)-1] != "AGREE 35" {
		t.Fatalf("late: %q", got)
	}
}
