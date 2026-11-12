// Package repost decides when an open offer gets a fresh copy in a group
// because the last one dropped out of view. Sell tasks, deliver-qr and
// sell_task_status all use it, so the status shows the same decision the
// task acts on.
package repost

import (
	"fmt"
	"time"

	"messwa/internal/config"
)

// Rules are the [sell] repost settings after one run's flags.
type Rules struct {
	On bool
	// After reposts once a copy is this old. 0 turns the trigger off.
	After time.Duration
	// Messages reposts once this many posts came in after the copy.
	Messages int
	// Listings reposts once this many other sellers listed the same meal
	// at the same mess after the copy.
	Listings int
	// Max is the most fresh copies one group gets for one offer.
	Max int
}

// FromConfig reads the rules from [sell].
func FromConfig(s config.Sell) Rules {
	return Rules{On: s.Repost, After: s.RepostAfter.Duration, Messages: s.RepostMessages, Listings: s.RepostListings, Max: s.MaxReposts}
}

// Copy states, the same as the store's offer post states.
const (
	Posting = "posting"
	Posted  = "posted"
	Failed  = "failed"
)

// Copy is one copy of the offer in a group. Seq 0 is the first, and each
// repost is one higher.
type Copy struct {
	Group string
	Seq   int
	ID    string
	At    time.Time
	State string
}

// Activity is what happened in the group after a copy, read from the group
// log. Known is false when the copy isn't in the log, as in a group that
// isn't one of the configured market groups; only the time trigger works
// there.
type Activity struct {
	Known    bool
	Messages int
	Listings int
}

// Triggers.
const (
	ByTime     = "time"
	ByMessages = "messages"
	ByListings = "listings"
)

// Next is the repost decision for one group.
type Next struct {
	// Current is the group's latest copy.
	Current Copy
	// Reposts is how many fresh copies the group already got.
	Reposts int
	Activity
	// Due means a fresh copy should go out now.
	Due bool
	// Trigger is why it's due, or why it will be due at At. It is empty,
	// with Stopped empty too, when only a message or listing count can
	// still bring a repost before the cutoff.
	Trigger string
	// At is when the next copy goes out unless a count trigger fires
	// first; with Due, when it became due. Zero when Stopped.
	At time.Time
	// Stopped says why the group gets no more copies.
	Stopped string
}

// Plan decides whether a group's offer needs a fresh copy at now. copies
// are that group's copies, in any order; end is when the meal ends.
func Plan(r Rules, copies []Copy, act Activity, now, end time.Time) Next {
	var n Next
	if len(copies) == 0 {
		n.Stopped = "no copy was posted in this group"
		return n
	}
	n.Current = copies[0]
	for _, c := range copies[1:] {
		if c.Seq > n.Current.Seq {
			n.Current = c
		}
	}
	n.Reposts = n.Current.Seq
	n.Activity = act
	cutoff := end.Add(-config.RepostCutoff)
	switch {
	case !r.On:
		n.Stopped = "reposting is off"
		return n
	case n.Current.State == Failed:
		// A group that refuses our post (WhatsApp error 479, say) would
		// refuse the repost too.
		n.Stopped = "the last copy failed to send, so this group gets no more"
		return n
	case n.Reposts >= r.Max:
		n.Stopped = fmt.Sprintf("reached max_reposts (%d)", r.Max)
		return n
	case !now.Before(cutoff):
		n.Stopped = fmt.Sprintf("no reposts in the last %s before the meal ends", config.RepostCutoff)
		return n
	}
	earliest := n.Current.At.Add(config.MinRepostGap)
	switch {
	case act.Known && r.Messages > 0 && act.Messages >= r.Messages:
		n.Trigger, n.At = ByMessages, earliest
	case act.Known && r.Listings > 0 && act.Listings >= r.Listings:
		n.Trigger, n.At = ByListings, earliest
	case r.After > 0:
		n.Trigger, n.At = ByTime, n.Current.At.Add(r.After)
	}
	if n.Trigger == "" {
		n.Stopped = "no repost trigger is set"
		return n
	}
	if !n.At.Before(cutoff) {
		n.Trigger, n.At = "", time.Time{}
		if earliest.Before(cutoff) && act.Known && (r.Messages > 0 || r.Listings > 0) {
			// Only a count can still bring a repost before the cutoff.
			return n
		}
		n.Stopped = fmt.Sprintf("the next repost would fall in the last %s before the meal ends", config.RepostCutoff)
		return n
	}
	n.Due = !now.Before(n.At)
	return n
}

// Why says in words why the next copy is or will be due.
func (n Next) Why(r Rules) string {
	if n.Stopped != "" {
		return n.Stopped
	}
	gap := ""
	if !n.Due {
		gap = fmt.Sprintf(", once the copy is %s old", config.MinRepostGap)
	}
	switch n.Trigger {
	case ByMessages:
		return fmt.Sprintf("%d messages came in after the copy (repost_messages = %d)%s", n.Messages, r.Messages, gap)
	case ByListings:
		return fmt.Sprintf("%d other sellers listed this meal at this mess after the copy (repost_listings = %d)%s", n.Listings, r.Listings, gap)
	}
	why := fmt.Sprintf("the copy is %s old (repost_after)", r.After)
	switch {
	case n.Trigger == "":
		why = fmt.Sprintf("repost_after would land in the last %s before the meal ends", config.RepostCutoff)
	case !n.Due:
		why = fmt.Sprintf("the copy will be %s old (repost_after)", r.After)
	}
	var sooner []string
	if n.Known && r.Messages > 0 {
		sooner = append(sooner, fmt.Sprintf("%d messages after it (now %d)", r.Messages, n.Messages))
	}
	if n.Known && r.Listings > 0 {
		sooner = append(sooner, fmt.Sprintf("%d same-mess listings after it (now %d)", r.Listings, n.Listings))
	}
	switch {
	case n.Due:
	case len(sooner) == 2:
		why += "; sooner at " + sooner[0] + " or " + sooner[1]
	case len(sooner) == 1:
		why += "; sooner at " + sooner[0]
	case !n.Known && (r.Messages > 0 || r.Listings > 0):
		why += "; the group isn't in the group log, so messages and listings after it can't be counted"
	}
	return why
}
