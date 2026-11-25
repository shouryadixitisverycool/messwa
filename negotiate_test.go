package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHaggleWalk(t *testing.T) {
	end := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	h := newHaggle(30, 60, 50, true, true, end, testLines("veg", ""))
	cases := []struct {
		left      time.Duration
		competing bool
		want      int
	}{
		{4 * time.Hour, false, 50},
		{2 * time.Hour, false, 50},
		{90 * time.Minute, false, 45},
		{time.Hour, false, 40},
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
	if h := newHaggle(30, 60, 0, false, true, end, testLines("veg", "")); h.target != 60 || h.walk(end.Add(-4*time.Hour), false) != 60 {
		t.Fatalf("target = %d", h.target)
	}
	// A median outside the range is clamped to it.
	if h := newHaggle(30, 60, 80, true, true, end, testLines("veg", "")); h.target != 60 {
		t.Fatalf("target = %d", h.target)
	}
	if h := newHaggle(30, 60, 20, true, true, end, testLines("veg", "")); h.target != 30 {
		t.Fatalf("target = %d", h.target)
	}
	// Without smart haggle the floor is open all day and the median is
	// ignored.
	plain := newHaggle(30, 60, 50, true, false, end, testLines("veg", ""))
	if plain.target != 60 || plain.walk(end.Add(-4*time.Hour), false) != 30 || plain.walk(end.Add(-4*time.Hour), true) != 30 {
		t.Fatalf("plain target = %d, walk = %d", plain.target, plain.walk(end.Add(-4*time.Hour), false))
	}
	if got := plain.withSmart(true); got.target != 50 || got.walk(end.Add(-4*time.Hour), false) != 50 {
		t.Fatalf("smart target = %d", got.target)
	}
	// Tiny prices concede by ₹1.
	if h := newHaggle(1, 3, 0, false, true, end, testLines("veg", "")); h.unit != 1 {
		t.Fatalf("unit = %d", h.unit)
	}
}

// The chat that started smart_haggle: range 85 to 115, a buyer offers 80
// four hours before lunch ends. Smart haggle held at the market's 115;
// plain haggling counters and goes as low as the floor.
func TestPlainHaggleUsesTheWholeRange(t *testing.T) {
	end := time.Date(2026, 11, 18, 15, 30, 0, 0, time.UTC)
	now := end.Add(-4 * time.Hour)
	smart := newHaggle(85, 115, 120, true, true, end, testLines("nonveg", ""))
	if got := strings.Join(replay(smart, now, "Available?", "80?"), " | "); !strings.Contains(got, "115") || strings.Contains(got, "100") {
		t.Errorf("smart haggle said %q, want it to hold at 115", got)
	}
	plain := newHaggle(85, 115, 120, true, false, end, testLines("nonveg", ""))
	if got := replay(plain, now, "Available?", "80?"); !strings.Contains(got[len(got)-1], "100") {
		t.Errorf("plain haggle said %q, want a counter of 100", got)
	}
	if got := replay(plain, now, "Available?", "80?", "85"); got[len(got)-1] != "AGREE 85" {
		t.Errorf("plain haggle said %q, want to agree at the floor", got)
	}
	if got := replay(plain, now, "Available?", "Okay sorry then"); got[len(got)-1] != "PASS" && !strings.Contains(got[len(got)-1], "100") {
		t.Errorf("plain haggle said %q to a polite no, want a pass or one lower price", got)
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
			[]string{"60", "kadamba veg lunch for 60"}},
		{"pass before any price ends", 30, 60, 50, []string{"nvm"},
			[]string{"PASS"}},
		{"pass at the opening ends", 30, 60, 50, []string{"price?", "nvm"},
			[]string{"60", "PASS"}},
		{"bought elsewhere ends without a counter", 30, 60, 50, []string{"price?", "bought already"},
			[]string{"60", "PASS"}},
		{"bought elsewhere after a counter ends", 30, 100, 80, []string{"price?", "75", "sorry bought"},
			[]string{"100", "how about 90", "PASS"}},
		{"first pass ends", 30, 60, 50, []string{"price?", "nvm"},
			[]string{"60", "PASS"}},
		{"pass after hearing the lowest ends", 30, 60, 45, []string{"price?", "35 final", "nvm"},
			[]string{"60", "45 / final offer", "PASS"}},
		{"polite withdrawal after the lowest does not agree", 115, 120, 115, []string{"Available?", "80?", "Okay sorry then."},
			[]string{"120", "115 / final offer", "PASS"}},
		{"a pass with a price is an offer", 30, 60, 50, []string{"price?", "too much, 55?"},
			[]string{"60", "AGREE 55"}},
		{"pass after an offer ends", 30, 60, 40, []string{"price?", "too much, 35?", "nvm"},
			[]string{"60", "how about 50", "PASS"}},
		{"pay another way parks", 30, 60, 50, []string{"price?", "upi id?"},
			[]string{"60", "PARK"}},
		{"an offer up to 3x the max is a highball", 30, 60, 50, []string{"price?", "180"},
			[]string{"60", "AGREE 180"}},
		{"an offer over 3x the max goes to you", 30, 60, 50, []string{"price?", "181"},
			[]string{"60", "PARK"}},
	}
	for _, c := range cases {
		h := newHaggle(c.min, c.max, c.median, true, true, end, testLines("veg", ""))
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
	h := newHaggle(30, 60, 50, true, true, end, testLines("veg", ""))
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
	h := newHaggle(30, 60, 50, true, true, end, testLines("veg", "tmrw"))
	early := end.Add(-4 * time.Hour)
	if got := replay(h, early, "price?", "nv?"); strings.Join(got, "|") != "60, its for tmrw|kadamba veg lunch for 60" {
		t.Fatalf("opening: %q", got)
	}
	if got := replay(h, early, "45?"); strings.Join(got, "|") != "how about 55, its for tmrw" {
		t.Fatalf("counter first: %q", got)
	}
	if got := replay(h, early, "35 final"); strings.Join(got, "|") != "50 / final offer, its for tmrw" {
		t.Fatalf("lowest first: %q", got)
	}
}

// A decline ends the conversation at any price, including old stored sessions.
func TestHagglePassEndsImmediately(t *testing.T) {
	end := time.Date(2026, 11, 12, 21, 30, 0, 0, time.UTC)
	h := newHaggle(30, 70, 50, true, true, end, testLines("nonveg", "")).forMeal("dinner")
	for _, text := range []string{"No, thank you", "no thanks", "no", "Nop", "nah", "nvm", "not interested", "I'll pass", "don't need lunch", "sorry bought from another person", "sorry, bought from someone else"} {
		for _, st := range []haggleState{
			{ask: 70},
			{ask: 70, botMsgs: 1},
			{ask: 50, botMsgs: 2},
			{ask: 30, botMsgs: 3, saidLowest: true},
			{ask: 40, botMsgs: 2, passCountered: true},
		} {
			result := h.decide(st, parseBuyer(text), end.Add(-time.Hour), false)
			if !result.pass || len(result.reply) != 0 || result.agree != 0 {
				t.Errorf("%q at %+v = %+v", text, st, result)
			}
		}
	}
}

// The 15 Nov chat: a lunch seller messaged someone whose post at 09:32
// was for breakfast.
func TestHaggleOtherMeal(t *testing.T) {
	end := time.Date(2026, 11, 15, 15, 30, 0, 0, time.UTC)
	h := newHaggle(100, 140, 0, false, true, end, testLines("nonveg", "")).forMeal("lunch")
	cases := []struct {
		name string
		msgs []string
		want []string
	}{
		{"names another meal", []string{"price?", "i actually texted for breakfast", "breakfast only"}, []string{"140", "its lunch", "PASS"}},
		{"offers for another meal", []string{"price?", "bf 60?", "bf 60?"}, []string{"140", "its lunch", "PASS"}},
		{"asks for another meal first", []string{"breakfast?", "120?"}, []string{"its lunch, 140", "140 / final offer"}},
		{"turns down the meal", []string{"price?", "dont need lunch"}, []string{"140", "PASS"}},
		{"dont need is a pass", []string{"price?", "dont need"}, []string{"140", "PASS"}},
		{"names the meal on sale", []string{"price?", "lunch for 120?"}, []string{"140", "140 / final offer"}},
		{"names both", []string{"price?", "breakfast or lunch, 120?"}, []string{"140", "140 / final offer"}},
		{"not req is a pass", []string{"price?", "not req"}, []string{"140", "PASS"}},
	}
	for _, c := range cases {
		if got := replay(h, end.Add(-4*time.Hour), c.msgs...); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	// The 15 Nov dinner chat: the buyer read the post as lunch. The bot
	// quoted 30 four times and never said dinner.
	dinner := newHaggle(25, 30, 0, false, true, end.Add(7*time.Hour), saleLines(nil, "kadamba", "veg", "dinner", "", time.Time{}, nil)).forMeal("dinner")
	if got := replay(dinner, end, "Lunch?", "I thought u mistyped\nDont need dinner"); strings.Join(got, "|") != "its dinner, 30|PASS" {
		t.Errorf("dinner chat: got %q", got)
	}
	if got := replay(dinner, end, "Lunch?", "Lunch na?"); strings.Join(got, "|") != "its dinner, 30|PASS" {
		t.Errorf("lunch twice: got %q", got)
	}
	if got := replay(dinner, end, "price?", "veg?"); strings.Join(got, "|") != "30|kadamba veg dinner for 30" {
		t.Errorf("which: got %q", got)
	}
	// A haggle that doesn't know its meal reads meals as before.
	if got := replay(newHaggle(100, 140, 0, false, true, end, testLines("nonveg", "")), end.Add(-4*time.Hour), "price?", "breakfast?"); len(got) != 2 || got[1] == "PASS" {
		t.Errorf("no meal: got %q", got)
	}
}

func TestCombineIntentsMeals(t *testing.T) {
	in := combineIntents([]string{"i texted for breakfast", "price?"})
	if !in.wantsOtherMeal("lunch") || in.wantsOtherMeal("breakfast") {
		t.Errorf("meals = %v", in.meals)
	}
	if in := combineIntents([]string{"bf?", "or lunch"}); in.wantsOtherMeal("lunch") {
		t.Errorf("bf then lunch = %v", in.meals)
	}
}
