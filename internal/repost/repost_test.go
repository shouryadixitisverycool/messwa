package repost

import (
	"strings"
	"testing"
	"time"
)

func TestPlan(t *testing.T) {
	rules := Rules{On: true, After: 10 * time.Minute, Messages: 10, Listings: 3, Max: 2}
	posted := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	end := posted.Add(90 * time.Minute)
	first := Copy{Group: "g", Seq: 0, ID: "a", At: posted, State: Posted}
	known := func(messages, listings int) Activity {
		return Activity{Known: true, Messages: messages, Listings: listings}
	}
	for _, tc := range []struct {
		name    string
		rules   Rules
		copies  []Copy
		act     Activity
		now     time.Time
		due     bool
		trigger string
		at      time.Time
		stopped string
	}{
		{name: "quiet group waits for the interval", copies: []Copy{first}, act: known(2, 0), now: posted.Add(4 * time.Minute),
			trigger: ByTime, at: posted.Add(10 * time.Minute)},
		{name: "interval reached", copies: []Copy{first}, act: known(2, 0), now: posted.Add(10 * time.Minute),
			due: true, trigger: ByTime, at: posted.Add(10 * time.Minute)},
		{name: "scrolled off screen", copies: []Copy{first}, act: known(10, 0), now: posted.Add(6 * time.Minute),
			due: true, trigger: ByMessages, at: posted.Add(5 * time.Minute)},
		{name: "behind other sellers", copies: []Copy{first}, act: known(4, 3), now: posted.Add(6 * time.Minute),
			due: true, trigger: ByListings, at: posted.Add(5 * time.Minute)},
		{name: "busy group still waits the minimum gap", copies: []Copy{first}, act: known(30, 9), now: posted.Add(2 * time.Minute),
			trigger: ByMessages, at: posted.Add(5 * time.Minute)},
		{name: "not in the group log goes by time", copies: []Copy{first}, act: Activity{}, now: posted.Add(9 * time.Minute),
			trigger: ByTime, at: posted.Add(10 * time.Minute)},
		{name: "counts off", rules: Rules{On: true, After: 10 * time.Minute, Max: 2}, copies: []Copy{first}, act: known(50, 9),
			now: posted.Add(6 * time.Minute), trigger: ByTime, at: posted.Add(10 * time.Minute)},
		{name: "latest copy decides", copies: []Copy{{Group: "g", Seq: 1, ID: "b", At: posted.Add(10 * time.Minute), State: Posted}, first},
			act: known(0, 0), now: posted.Add(12 * time.Minute), trigger: ByTime, at: posted.Add(20 * time.Minute)},
		{name: "switched off", rules: Rules{After: 10 * time.Minute, Max: 2}, copies: []Copy{first}, now: posted.Add(time.Hour),
			stopped: "off"},
		{name: "group refused the post", copies: []Copy{{Group: "g", ID: "a", At: posted, State: Failed}}, now: posted.Add(time.Hour),
			stopped: "failed"},
		{name: "cap reached", copies: []Copy{first, {Group: "g", Seq: 1, At: posted, State: Posted}, {Group: "g", Seq: 2, At: posted, State: Posted}},
			now: posted.Add(time.Hour), stopped: "max_reposts"},
		{name: "close to the meal's end", copies: []Copy{first}, act: known(40, 9), now: end.Add(-10 * time.Minute),
			stopped: "last 15m0s"},
		{name: "interval lands after the cutoff, counts can still fire",
			copies: []Copy{{Group: "g", Seq: 0, At: end.Add(-25 * time.Minute), State: Posted}}, act: known(1, 0), now: end.Add(-24 * time.Minute)},
		{name: "interval lands after the cutoff and nothing else can",
			copies: []Copy{{Group: "g", Seq: 0, At: end.Add(-25 * time.Minute), State: Posted}}, now: end.Add(-24 * time.Minute),
			stopped: "would fall"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := rules
			if tc.rules != (Rules{}) {
				r = tc.rules
			}
			n := Plan(r, tc.copies, tc.act, tc.now, end)
			if tc.stopped != "" {
				if !strings.Contains(n.Stopped, tc.stopped) || n.Due {
					t.Fatalf("Plan = %+v, want stopped with %q", n, tc.stopped)
				}
				return
			}
			if n.Stopped != "" || n.Due != tc.due || n.Trigger != tc.trigger || !n.At.Equal(tc.at) {
				t.Fatalf("Plan = %+v, want due %v by %q at %s", n, tc.due, tc.trigger, tc.at)
			}
			if n.Why(r) == "" {
				t.Fatal("no reason given")
			}
		})
	}
}

func TestWhy(t *testing.T) {
	rules := Rules{On: true, After: 10 * time.Minute, Messages: 10, Listings: 3, Max: 6}
	posted := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	end := posted.Add(time.Hour)
	copies := []Copy{{Group: "g", ID: "a", At: posted, State: Posted}}
	n := Plan(rules, copies, Activity{Known: true, Messages: 4, Listings: 1}, posted.Add(3*time.Minute), end)
	if got := n.Why(rules); got != "the copy will be 10m0s old (repost_after); sooner at 10 messages after it (now 4) or 3 same-mess listings after it (now 1)" {
		t.Errorf("waiting: %s", got)
	}
	n = Plan(rules, copies, Activity{Known: true, Messages: 12}, posted.Add(6*time.Minute), end)
	if got := n.Why(rules); got != "12 messages came in after the copy (repost_messages = 10)" {
		t.Errorf("due by messages: %s", got)
	}
	n = Plan(rules, copies, Activity{}, posted.Add(3*time.Minute), end)
	if got := n.Why(rules); !strings.Contains(got, "isn't in the group log") {
		t.Errorf("unlogged group: %s", got)
	}
}
