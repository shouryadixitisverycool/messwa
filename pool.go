package main

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/market"
	"messwa/internal/repost"
	"messwa/internal/store"
)

// Pools. Running tasks that sell the same meal at the same mess and diet
// (a meal of your own and a friend's, say) and that negotiate without
// handing off sell as one pool, the way people post "kadamba non-veg
// dinner x2":
//
//   - The oldest active task leads. Only it posts, reposts and takes
//     replies, and its post says how many meals the pool has. With
//     show_price on, it shows the average of the tasks' max prices, which
//     is also where the bot opens.
//   - The bot haggles down to the lowest floor in the pool. A buyer who
//     agrees goes to the oldest task whose own floor takes the price,
//     skipping tasks someone is paying for right now when another fits.
//     That task asks them to pay and sends its own QR.
//   - When a task ends, the buyers it still has in play move on: agreed
//     ones to the oldest task their price fits, the rest to the next
//     leader, who also takes over the posts. Only buyers no task can take
//     hear "sold sorry".
//   - While a copy is under postEditWindow old, the leader edits it when
//     the count or the shown price changes. Older copies keep their text;
//     the next repost shows the new one. Once the pool's last task ends
//     and a meal sold, every copy gets ✅.
//
// A pool of one task behaves exactly like a task on its own.

// postEditWindow is how long after posting a copy may be edited. WhatsApp
// allows 20 minutes; this leaves room for a slow send.
const postEditWindow = 15 * time.Minute

// pooled reports whether t can share a pool with other tasks.
func pooled(t store.SellTask) bool {
	return negotiates(t) && (t.Handoff == "" || t.Handoff == string(config.HandoffNever)) && t.Mess != ""
}

// poolView is a task's pool on its date.
type poolView struct {
	all    []store.SellTask // every task in it, whatever its state, oldest first
	active []store.SellTask // the active ones, oldest first
}

func (m *sellManager) pool(ctx context.Context, id int64) (poolView, error) {
	all, err := m.st.SellTaskPool(ctx, id)
	if err != nil {
		return poolView{}, err
	}
	p := poolView{all: all}
	for _, t := range all {
		if t.State == store.TaskActive {
			p.active = append(p.active, t)
		}
	}
	return p, nil
}

// mates are the active tasks besides id.
func (p poolView) mates(id int64) []store.SellTask {
	var out []store.SellTask
	for _, t := range p.active {
		if t.ID != id {
			out = append(out, t)
		}
	}
	return out
}

// epoch is the run of tasks whose posts are the pool's posts now: the
// active ones and seed, plus every ended task that was still selling when
// one of those started, and so on back. A task started after the pool
// sold out begins a new run with posts of its own.
func (p poolView) epoch(seed ...int64) []store.SellTask {
	in := map[int64]bool{}
	for _, t := range p.active {
		in[t.ID] = true
	}
	for _, id := range seed {
		in[id] = true
	}
	for grew := true; grew; {
		grew = false
		var start time.Time
		for _, t := range p.all {
			if in[t.ID] && (start.IsZero() || t.CreatedAt.Before(start)) {
				start = t.CreatedAt
			}
		}
		for _, t := range p.all {
			if !in[t.ID] && !t.EndedAt.IsZero() && !t.EndedAt.Before(start) {
				in[t.ID] = true
				grew = true
			}
		}
	}
	var out []store.SellTask
	for _, t := range p.all {
		if in[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

// poolPrices are the pool's floor and opening price, in paise: the lowest
// floor, and the average max price rounded to the price unit, never above
// the highest max or below the floor. One task keeps its own range.
func poolPrices(active []store.SellTask) (floor, ask int64) {
	if len(active) == 0 {
		return 0, 0
	}
	if len(active) == 1 {
		return active[0].MinPaise, active[0].MaxPaise
	}
	floor, top := active[0].MinPaise, active[0].MaxPaise
	var sum int64
	for _, t := range active {
		floor, top = min(floor, t.MinPaise), max(top, t.MaxPaise)
		sum += t.MaxPaise
	}
	avg := float64(sum) / float64(len(active)) / 100
	unit := float64(market.PriceUnit(int(avg)))
	ask = int64(math.Round(avg/unit)*unit) * 100
	return floor, min(max(ask, floor), top)
}

// offerShows is what the leader's post says: the pool's opening price and
// how many meals it has. A task alone shows its own max price.
func (m *sellManager) offerShows(ctx context.Context, t store.SellTask) (pricePaise int64, count int) {
	if p, err := m.pool(ctx, t.ID); err == nil && len(p.active) > 0 {
		_, ask := poolPrices(p.active)
		return ask, len(p.active)
	}
	if cur, err := m.st.GetSellTask(ctx, t.ID); err == nil && cur != nil {
		return cur.MaxPaise, 1
	}
	return t.MaxPaise, 1
}

// leads reports whether t is its pool's oldest active task, which posts
// for it. A task that can't pool always leads.
func (m *sellManager) leads(ctx context.Context, t store.SellTask) (bool, error) {
	if !pooled(t) {
		return true, nil
	}
	p, err := m.pool(ctx, t.ID)
	if err != nil {
		return false, err
	}
	return len(p.active) > 0 && p.active[0].ID == t.ID, nil
}

// lead makes rt post for its pool, once.
func (m *sellManager) lead(rt *runningTask) {
	m.mu.Lock()
	start := rt.startLead
	m.mu.Unlock()
	if start != nil && rt.leading.CompareAndSwap(false, true) {
		start()
	}
}

// kick asks rt's post sync to look at its copies now.
func (rt *runningTask) kick() {
	select {
	case rt.kicks <- struct{}{}:
	default:
	}
}

func (m *sellManager) runningTask(id int64) *runningTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[id]
}

// poolChanged runs after a task in t's pool started, ended or changed its
// prices: every running task haggles on the new range, the oldest one
// leads, and its copies are brought up to date.
func (m *sellManager) poolChanged(t store.SellTask) {
	if !pooled(t) {
		return
	}
	ctx := context.WithoutCancel(m.base)
	p, err := m.pool(ctx, t.ID)
	if err != nil {
		m.log.Warn("could not read the task's pool", "task", t.ID, "err", err)
		return
	}
	for _, a := range p.active {
		if rt := m.runningTask(a.ID); rt != nil {
			if n := rt.neg.Load(); n != nil {
				if err := n.refreshRange(ctx); err != nil {
					m.log.Warn("could not update the pool's range", "task", a.ID, "err", err)
				}
			}
		}
	}
	if len(p.active) == 0 {
		return
	}
	if rt := m.runningTask(p.active[0].ID); rt != nil {
		m.lead(rt)
		rt.kick()
	}
}

// sellerFor picks the task that sells to a buyer who agreed paise in t's
// pool: the oldest active one whose own floor takes the price and that
// nobody is paying for, else the oldest whose floor takes it. skip is left
// out. ok is false when no task takes the price.
func (m *sellManager) sellerFor(ctx context.Context, t store.SellTask, paise, skip int64) (int64, bool) {
	if !pooled(t) {
		return 0, false
	}
	p, err := m.pool(ctx, t.ID)
	if err != nil {
		m.log.Warn("could not read the task's pool", "task", t.ID, "err", err)
		return 0, false
	}
	var first int64
	for _, c := range p.active {
		if c.ID == skip || c.MinPaise > paise {
			continue
		}
		if first == 0 {
			first = c.ID
		}
		if rt := m.runningTask(c.ID); rt != nil {
			if n := rt.neg.Load(); n != nil && !n.busy() {
				return c.ID, true
			}
		}
	}
	return first, first != 0
}

// passOn hands t's buyers still in play to the rest of its pool: agreed
// ones to the task their price fits (see sellerFor), the rest to the
// pool's oldest task. It returns who moved, by peer.
func (m *sellManager) passOn(ctx context.Context, t store.SellTask) map[string]bool {
	moved := map[string]bool{}
	if !pooled(t) {
		return moved
	}
	p, err := m.pool(ctx, t.ID)
	if err != nil {
		m.log.Warn("could not read the task's pool; its buyers stay with it", "task", t.ID, "err", err)
		return moved
	}
	mates := p.mates(t.ID)
	if len(mates) == 0 {
		return moved
	}
	buyers, err := m.st.SellTaskBuyers(ctx, t.ID)
	if err != nil {
		m.log.Warn("could not read the task's buyers; they stay with it", "task", t.ID, "err", err)
		return moved
	}
	for _, b := range buyers {
		to := mates[0].ID
		switch b.State {
		case store.BuyerQueued, store.BuyerNegotiating, store.BuyerTimedOut:
		case store.BuyerAgreed:
			id, ok := m.sellerFor(ctx, t, paise(b.Agreed), t.ID)
			if !ok {
				continue
			}
			to = id
		default:
			continue
		}
		ok, err := m.st.PassSellTaskBuyer(ctx, t.ID, to, b.Peer, "", store.Negotiation{}, time.Now())
		if err != nil || !ok {
			m.log.Warn("could not pass a buyer on in the pool", "task", t.ID, "to", to, "buyer", b.Peer, "err", err)
			continue
		}
		moved[b.Peer] = true
		var agreed int64
		if b.State == store.BuyerAgreed {
			agreed = paise(b.Agreed)
		}
		m.notePassed(ctx, t.ID, to, b.Peer, agreed)
		m.adoptPassed(ctx, to, b.Peer)
	}
	return moved
}

// notePassed records a buyer moving from one task in a pool to another.
func (m *sellManager) notePassed(ctx context.Context, from, to int64, peer string, agreed int64) {
	m.log.Info("buyer passed on in the pool", "task", from, "to", to, "buyer", peer)
	m.event(ctx, store.SellEvent{TaskID: from, Peer: peer, Kind: store.EventPooled, N: agreed, Text: fmt.Sprintf("to %d", to)})
	m.event(ctx, store.SellEvent{TaskID: to, Peer: peer, Kind: store.EventPooled, N: agreed, Text: fmt.Sprintf("from %d", from)})
}

// adoptPassed gives a running task the buyer just passed to it. A task not
// running yet finds them in the store when it starts.
func (m *sellManager) adoptPassed(ctx context.Context, to int64, peer string) {
	rt := m.runningTask(to)
	if rt == nil {
		return
	}
	n := rt.neg.Load()
	if n == nil {
		return
	}
	buyers, err := m.st.SellTaskBuyers(ctx, to)
	if err != nil {
		m.log.Warn("could not read a passed buyer", "task", to, "err", err)
		return
	}
	for _, b := range buyers {
		if b.Peer == peer {
			n.adopt(b)
			return
		}
	}
}

// poolCopy is one copy of a pool's offer and the task that posted it.
type poolCopy struct {
	store.SellOffer
	TaskID int64
}

// poolCopies lists the copies of t's pool's offer, oldest first, numbered
// per group across the tasks that posted them.
func (m *sellManager) poolCopies(ctx context.Context, t store.SellTask) ([]poolCopy, error) {
	tasks := []store.SellTask{t}
	if pooled(t) {
		p, err := m.pool(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		tasks = p.epoch(t.ID)
	}
	var out []poolCopy
	for _, e := range tasks {
		offers, err := m.st.SellTaskOffers(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		for _, o := range offers {
			out = append(out, poolCopy{SellOffer: o, TaskID: e.ID})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.PostedAt.Equal(b.PostedAt) {
			return a.PostedAt.Before(b.PostedAt)
		}
		if a.TaskID != b.TaskID {
			return a.TaskID < b.TaskID
		}
		return a.Seq < b.Seq
	})
	seqs := map[string]int{}
	for i := range out {
		out[i].Seq = seqs[out[i].Group]
		seqs[out[i].Group]++
	}
	return out, nil
}

// repostCopies are a pool's copies for the reposter.
func repostCopies(copies []poolCopy) []repost.Copy {
	out := make([]repost.Copy, 0, len(copies))
	for _, c := range copies {
		out = append(out, repost.Copy{Group: c.Group, Seq: c.Seq, ID: c.ID, At: c.PostedAt, State: c.State})
	}
	return out
}

// syncCopies keeps the leader's copies showing what the post should say
// now, until ctx ends. It looks every repostCheck, and at once when kicked.
func (m *sellManager) syncCopies(ctx context.Context, t store.SellTask, gate *taskGate, text func() string, kicks <-chan struct{}) {
	tick := time.NewTicker(repostCheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-kicks:
		}
		m.editCopies(ctx, t, gate, text())
	}
}

// editCopies edits every posted copy young enough to edit whose text
// isn't want.
func (m *sellManager) editCopies(ctx context.Context, t store.SellTask, gate *taskGate, want string) {
	copies, err := m.poolCopies(ctx, t)
	if err != nil {
		m.log.Warn("could not read the offer's copies to update them", "task", t.ID, "err", err)
		return
	}
	for _, c := range copies {
		if c.State != store.OfferPosted || !c.RevokedAt.IsZero() || c.Text == "" || c.Text == want || time.Since(c.PostedAt) >= postEditWindow {
			continue
		}
		chat, err := types.ParseJID(c.Group)
		if err != nil {
			continue
		}
		if err := gate.edit(ctx, chat, c.TaskID, c.ID, want); err != nil {
			m.log.Warn("could not edit a copy of the offer", "task", t.ID, "group", c.Group, "offer_id", c.ID, "err", err)
			continue
		}
		if err := m.st.SetOfferText(ctx, c.TaskID, c.ID, want); err != nil {
			m.log.Warn("could not record the edited copy", "task", t.ID, "offer_id", c.ID, "err", err)
		}
		now := time.Now()
		if err := m.own.add(ctx, store.LogEntry{Chat: c.Group, ID: fmt.Sprintf("edit-%s-%d", c.ID, now.UnixNano()), Type: "edit", TS: now, Target: c.ID, Text: want}); err != nil {
			m.log.Warn("could not add the edit to the group log", "offer_id", c.ID, "err", err)
		}
		m.log.Info("edited a copy of the offer", "task", t.ID, "group", c.Group, "offer_id", c.ID, "text", want)
		m.event(ctx, store.SellEvent{TaskID: t.ID, Peer: c.Group, Kind: store.EventEdited, Text: want})
	}
}

// edit changes the text of one of the pool's posted copies. Only an active
// task may, and only a copy its pool posted.
func (g *taskGate) edit(ctx context.Context, chat types.JID, owner int64, id, text string) error {
	if !g.active.Load() {
		return errTaskInactive
	}
	t, err := g.m.st.GetSellTask(ctx, g.id)
	if err != nil || t == nil {
		return fmt.Errorf("sell task is gone: %w", err)
	}
	copies, err := g.m.poolCopies(ctx, *t)
	if err != nil {
		return err
	}
	known := false
	for _, c := range copies {
		known = known || (c.TaskID == owner && c.ID == id && c.Group == chat.String() && c.State == store.OfferPosted && c.RevokedAt.IsZero())
	}
	if !known {
		return fmt.Errorf("not one of the pool's posted copies")
	}
	if err := g.m.limits.wait(ctx, g.id); err != nil {
		return err
	}
	if !g.active.Load() {
		return errTaskInactive
	}
	_, err = g.m.send(ctx, chat, g.m.conn.BuildEdit(chat, id, &waE2E.Message{Conversation: proto.String(text)}))
	return err
}

// endPool runs once t has ended in its final state. With tasks left in the
// pool, t's posts stay up for them and replies to the posts go to them.
// Otherwise the pool is done: its copies get ✅ if any of its meals sold,
// or are deleted if t was cancelled with revoke.
func (m *sellManager) endPool(t store.SellTask, final string, revoke bool) {
	ctx := context.WithoutCancel(m.base)
	t.State = final
	p, err := m.pool(ctx, t.ID)
	if err != nil {
		m.log.Warn("could not read the task's pool", "task", t.ID, "err", err)
	}
	if mates := p.mates(t.ID); err == nil && len(mates) > 0 {
		if revoke {
			m.log.Info("not deleting the cancelled task's posts: the rest of its pool still sells with them", "task", t.ID, "pool", len(mates))
		}
		m.retire(t)
		m.passOn(ctx, t)
		m.poolChanged(t)
		return
	}
	epoch := []store.SellTask{t}
	if pooled(t) && err == nil {
		epoch = p.epoch(t.ID)
	}
	switch {
	case final == store.TaskCancelled && revoke:
		for _, e := range epoch {
			if e.State != store.TaskSold {
				m.revokeCopies(e)
			}
		}
	default:
		m.tickPool(t, epoch)
	}
	if final == store.TaskCancelled {
		// Posts claimed since Cancel looked count too.
		m.retire(t)
	} else {
		m.forget(t.ID)
	}
}

// tickPool reacts ✅ to every recorded offer post of the run's meals once
// one of them sold, like deliver-qr does, so the board shows the listing
// as sold. It also ticks posts from an earlier deliver-qr for those meals.
func (m *sellManager) tickPool(t store.SellTask, epoch []store.SellTask) {
	ctx, stop := context.WithTimeout(context.WithoutCancel(m.base), time.Minute)
	defer stop()
	sold := false
	for _, e := range epoch {
		if _, state, err := m.st.TaskDelivery(ctx, e.ID); err == nil && state == "sent" {
			sold = true
		}
	}
	if !sold {
		return
	}
	if !m.connected(30 * time.Second) {
		m.log.Warn("WhatsApp is not connected; mark the offer posts sold by hand", "task", t.ID)
		return
	}
	type slot struct{ date, meal string }
	var slots []slot
	var ours []store.OfferPost
	for _, e := range epoch {
		sl := slot{e.Date, e.Meal}
		if slices.Contains(slots, sl) {
			continue
		}
		slots = append(slots, sl)
		posts, err := m.st.OfferPosts(ctx, e.Date, e.Meal)
		if err != nil {
			m.log.Warn("could not read the offer posts to mark them sold", "task", t.ID, "err", err)
			continue
		}
		ours = append(ours, posts...)
	}
	react := func(ctx context.Context, chat types.JID, target, emoji string) (string, time.Time, error) {
		known := false
		for _, p := range ours {
			known = known || (p.Chat == chat.String() && p.ID == target)
		}
		if !known {
			return "", time.Time{}, fmt.Errorf("not one of the pool's recorded offer posts")
		}
		// No rate limit, as for a single task's ✅. An empty sender marks
		// the target as our own message.
		resp, err := m.send(ctx, chat, m.conn.BuildReaction(chat, types.EmptyJID, target, emoji))
		return resp.ID, resp.Timestamp, err
	}
	done := 0
	for _, sl := range slots {
		n, _, err := reactSold(ctx, m.st, m.own, react, m.log, sl.date, sl.meal)
		if err != nil {
			m.log.Warn("could not read the offer posts to mark them sold", "task", t.ID, "err", err)
		}
		done += n
	}
	if done > 0 {
		m.event(ctx, store.SellEvent{TaskID: t.ID, Kind: store.EventTicked, N: int64(done)})
	}
}
