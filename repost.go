package main

import (
	"context"
	"log/slog"
	"time"

	"messwa/internal/repost"
	"messwa/internal/store"
)

// repostCheck is how often a reposter looks at its groups.
const repostCheck = 20 * time.Second

// repostReserve is how many sends a task keeps for its buyers: a repost
// never leaves the task fewer than this under taskSendCap.
const repostReserve = 20

// reposter keeps an open offer in view. Every repostCheck it asks
// repost.Plan about each group's latest copy and posts a fresh copy where
// one is due, post_gap apart. Replies to every copy keep feeding the same
// queue, since the send watches each copy it posts.
type reposter struct {
	log   *slog.Logger
	st    *store.Store
	rules repost.Rules
	// live, when set, replaces rules: it is read every round, so a sell
	// task follows repost settings saved on the dashboard while it runs.
	live func() repost.Rules
	// end is when the meal ends.
	end time.Time
	// gap is the pause between two reposts in one round.
	gap time.Duration
	// groups are the groups the offer may be reposted in. A group with no
	// copy yet is left to the first round of posting.
	groups func(context.Context) ([]offerTarget, error)
	// copies lists every copy so far, with its state.
	copies func(context.Context) ([]repost.Copy, error)
	send   sendCopy
	// record runs for each repost WhatsApp acknowledged.
	record func(postedOffer)
	// room reports whether a repost may use one of the task's sends. Nil
	// means always.
	room func() bool
}

// current is the rules for this round.
func (r *reposter) current() repost.Rules {
	if r.live != nil {
		return r.live()
	}
	return r.rules
}

// enabled reports whether r may ever repost: with live rules, reposting
// can be switched on while the offer is open.
func (r *reposter) enabled() bool { return r.live != nil || r.rules.On }

// run reposts until ctx ends or no group can get another copy.
func (r *reposter) run(ctx context.Context, o *groupOffer) {
	tick := time.NewTicker(repostCheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if !r.round(ctx, o) {
			r.log.Info("no group gets another copy of the offer; reposting is done")
			return
		}
	}
}

// round posts a fresh copy in every group where one is due. It reports
// whether any group may still get one later.
func (r *reposter) round(ctx context.Context, o *groupOffer) bool {
	if !time.Now().Before(r.end) {
		return false
	}
	rules := r.current()
	if !rules.On {
		// Only live rules get here; wait for reposting to come back on.
		return true
	}
	groups, err := r.groups(ctx)
	if err != nil {
		r.log.Warn("could not list groups; not reposting this round", "err", err)
		return true
	}
	copies, err := r.copies(ctx)
	if err != nil {
		r.log.Warn("could not read the offer's copies; not reposting this round", "err", err)
		return true
	}
	byGroup := map[string][]repost.Copy{}
	for _, c := range copies {
		byGroup[c.Group] = append(byGroup[c.Group], c)
	}
	sent := 0
	alive := false
	for _, g := range groups {
		n, ok := r.plan(ctx, rules, byGroup[g.JID.String()])
		if !ok || n.Stopped != "" {
			continue
		}
		if !n.Due {
			alive = true
			continue
		}
		if r.room != nil && !r.room() {
			r.log.Info("not reposting: the task keeps its remaining sends for buyers", "group", g.Name)
			return false
		}
		if sent > 0 {
			timer := time.NewTimer(r.gap)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false
			case <-timer.C:
			}
		}
		sent++
		why := n.Why(rules)
		g = refreshGroup(ctx, o.conn, r.log, g)
		p, err := r.send(ctx, o, g, n.Current.Seq+1)
		if err != nil {
			r.log.Warn("repost failed or is uncertain; replies to it still count, and this group gets no more",
				append(append([]any{"group", g.Name, "jid", g.JID, "err", err}, groupAttrs("listed_", g.listed)...), groupAttrs("", g.fresh)...)...)
			continue
		}
		alive = alive || n.Current.Seq+1 < rules.Max
		o.addPosted(p)
		if r.record != nil {
			r.record(p)
		}
		r.log.Info("reposted the offer", "group", g.Name, "offer_id", p.ID, "repost", n.Current.Seq+1, "why", why)
	}
	return alive
}

// plan is repost.Plan for one group's copies, with the activity after the
// latest one read from the group log.
func (r *reposter) plan(ctx context.Context, rules repost.Rules, copies []repost.Copy) (repost.Next, bool) {
	if len(copies) == 0 {
		return repost.Next{}, false
	}
	current := copies[0]
	for _, c := range copies {
		if c.Seq > current.Seq {
			current = c
		}
	}
	act, err := groupActivity(ctx, r.st, current)
	if err != nil {
		r.log.Warn("could not count messages after the offer; going by time only", "group", current.Group, "err", err)
	}
	return repost.Plan(rules, copies, act, time.Now(), r.end), true
}

// groupActivity reads what came into the group after c from the group log.
func groupActivity(ctx context.Context, st *store.Store, c repost.Copy) (repost.Activity, error) {
	if st == nil || c.State == repost.Failed {
		return repost.Activity{}, nil
	}
	a, known, err := st.After(ctx, c.Group, c.ID)
	if err != nil {
		return repost.Activity{}, err
	}
	return repost.Activity{Known: known, Messages: a.Messages, Listings: a.Listings}, nil
}
