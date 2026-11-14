package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHaggleWalk(t *testing.T) {
	end := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	h := newHaggle(30, 60, 50, true, end, "kadamba veg", "")
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
	if h := newHaggle(30, 60, 0, false, end, "kadamba veg", ""); h.target != 60 || h.walk(end.Add(-4*time.Hour), false) != 60 {
		t.Fatalf("target = %d", h.target)
	}
	// A median outside the range is clamped to it.
	if h := newHaggle(30, 60, 80, true, end, "kadamba veg", ""); h.target != 60 {
		t.Fatalf("target = %d", h.target)
	}
	if h := newHaggle(30, 60, 20, true, end, "kadamba veg", ""); h.target != 30 {
		t.Fatalf("target = %d", h.target)
	}
	// Tiny prices concede by ₹1.
	if h := newHaggle(1, 3, 0, false, end, "kadamba veg", ""); h.unit != 1 {
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
		if len(s.reply) > 0 {
			out = append(out, strings.Join(s.reply, " / "))
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
			[]string{"60", "AGREE 60"}},
		{"ok before any price is an opener", 30, 60, 50, []string{"ok"},
			[]string{"60"}},
		{"same number agrees at once", 30, 60, 50, []string{"same number?"},
			[]string{"AGREE 60"}},
		{"a fair first offer is taken", 30, 60, 50, []string{"Buy @55"},
			[]string{"AGREE 55"}},
		{"offer above the ask is taken", 30, 60, 50, []string{"price?", "70"},
			[]string{"60", "AGREE 70"}},
		{"highball before any price is taken", 50, 70, 60, []string{"buy 200"},
			[]string{"AGREE 200"}},
		{"a first offer over 3x the max goes to you", 50, 70, 60, []string{"buy 300"},
			[]string{"PARK"}},
		// Your 2 Apr sale: 55 -> B50? -> 50. Here the bot opens at 60.
		{"counter then meet", 30, 60, 45, []string{"price?", "40?", "45"},
			[]string{"60", "how about 50", "AGREE 45"}},
		// Your 8 Mar sale: 100 -> B80 x4 -> 80. The bot stops at its lowest
		// and hands a buyer who won't move to you.
		{"firm lowballer is parked", 60, 100, 90, []string{"price?", "80", "80", "80", "80"},
			[]string{"100", "90 / final offer", "PARK"}},
		{"firm offer gets the lowest at once", 30, 60, 45, []string{"price?", "35 final"},
			[]string{"60", "45 / final offer"}},
		{"a counter that reaches the walk-away price is the lowest", 30, 80, 40, []string{"price?", "20", "25", "30"},
			[]string{"80", "how about 50", "40 / final offer", "PARK"}},
		{"two counters then lowest", 30, 100, 80, []string{"price?", "75", "76", "77"},
			[]string{"100", "how about 90", "85?", "80 / final offer"}},
		{"lower without a number steps down", 30, 60, 40, []string{"price?", "too much", "too much", "too much", "too much"},
			[]string{"60", "how about 50", "45?", "40 / final offer", "PARK"}},
		{"question gets the label and price", 30, 60, 50, []string{"price?", "nv?"},
			[]string{"60", "kadamba veg for 60"}},
		{"pass before any price ends", 30, 60, 50, []string{"nvm"},
			[]string{"PASS"}},
		{"pass at the opening gets the target once", 30, 60, 50, []string{"price?", "nvm"},
			[]string{"60", "how about 50"}},
		{"bought elsewhere ends without a counter", 30, 60, 50, []string{"price?", "bought already"},
			[]string{"60", "PASS"}},
		{"bought elsewhere after a counter ends", 30, 100, 80, []string{"price?", "75", "sorry bought"},
			[]string{"100", "how about 90", "PASS"}},
		{"second pass ends", 30, 60, 50, []string{"price?", "nvm", "nvm"},
			[]string{"60", "how about 50", "PASS"}},
		{"pass after hearing the lowest ends", 30, 60, 45, []string{"price?", "35 final", "nvm"},
			[]string{"60", "45 / final offer", "PASS"}},
		{"a pass with a price is an offer", 30, 60, 50, []string{"price?", "too much, 55?"},
			[]string{"60", "AGREE 55"}},
		{"an offer doesn't use up the pass counter", 30, 60, 40, []string{"price?", "too much, 35?", "nvm", "nvm"},
			[]string{"60", "how about 50", "40?", "PASS"}},
		{"pay another way parks", 30, 60, 50, []string{"price?", "upi id?"},
			[]string{"60", "PARK"}},
		{"an offer up to 3x the max is a highball", 30, 60, 50, []string{"price?", "180"},
			[]string{"60", "AGREE 180"}},
		{"an offer over 3x the max goes to you", 30, 60, 50, []string{"price?", "181"},
			[]string{"60", "PARK"}},
	}
	for _, c := range cases {
		h := newHaggle(c.min, c.max, c.median, true, end, "kadamba veg", "")
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
	h := newHaggle(30, 60, 50, true, end, "kadamba veg", "")
	// Four hours out ₹35 is too low; ten minutes out it's fine.
	if got := replay(h, end.Add(-4*time.Hour), "price?", "35"); got[len(got)-1] == "AGREE 35" {
		t.Fatalf("early: %q", got)
	}
	if got := replay(h, end.Add(-10*time.Minute), "price?", "35"); got[len(got)-1] != "AGREE 35" {
		t.Fatalf("late: %q", got)
	}
}

// A meal that isn't today's says so with the first price, whichever reply
// carries it.
func TestHaggleNamesTheDay(t *testing.T) {
	end := time.Date(2026, 11, 12, 14, 30, 0, 0, time.UTC)
	h := newHaggle(30, 60, 50, true, end, "kadamba veg", "tmrw")
	early := end.Add(-4 * time.Hour)
	if got := replay(h, early, "price?", "nv?"); strings.Join(got, "|") != "60, its for tmrw|kadamba veg for 60" {
		t.Fatalf("opening: %q", got)
	}
	if got := replay(h, early, "45?"); strings.Join(got, "|") != "how about 55, its for tmrw" {
		t.Fatalf("counter first: %q", got)
	}
	if got := replay(h, early, "35 final"); strings.Join(got, "|") != "50 / final offer, its for tmrw" {
		t.Fatalf("lowest first: %q", got)
	}
}

// The 12 Nov dinner run, task 7: kadamba non-veg, ₹60 to ₹70, no market
// median, so the target is ₹70. Three buyers passed at the opening (or at
// the floor) and the bot let each one go.
func TestHagglePassGetsOneCounter(t *testing.T) {
	end := time.Date(2026, 11, 12, 21, 30, 0, 0, time.UTC)
	early := end.Add(-90 * time.Minute)
	h := newHaggle(60, 70, 0, false, end, "kadamba non-veg", "")

	// At the target: halfway to the floor.
	if got := strings.Join(replay(h, early, "price?", "nvm", "nvm"), "|"); got != "70|how about 65|PASS" {
		t.Fatalf("pass at the opening: %q", got)
	}

	// Above the target: the target.
	h2 := newHaggle(50, 70, 60, true, end, "kadamba non-veg", "")
	if got := strings.Join(replay(h2, early, "price?", "no"), "|"); got != "70|how about 60" {
		t.Fatalf("pass above the target: %q", got)
	}

	// Already at the floor: nothing lower to offer.
	s := h.decide(haggleState{ask: 60, botMsgs: 2, quoted: []int{70, 60}}, parseBuyer("nvm"), early, false)
	if !s.pass || len(s.reply) > 0 {
		t.Fatalf("pass at the floor: %+v", s)
	}

	// Halfway that rounds back to the ask still comes down one unit.
	h3 := newHaggle(50, 55, 0, false, end, "kadamba non-veg", "")
	if got := strings.Join(replay(h3, early, "price?", "nvm"), "|"); got != "55|how about 50" {
		t.Fatalf("one unit above the floor: %q", got)
	}

	// No messages left for this buyer: let them go.
	s = h.decide(haggleState{ask: 70, botMsgs: maxBotMsgs}, parseBuyer("nvm"), early, false)
	if !s.pass {
		t.Fatalf("pass at the message cap: %+v", s)
	}
}
