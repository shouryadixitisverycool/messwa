package main

import (
	"context"
	"errors"
	"strconv"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"messwa/internal/store"
)

// Cleaning up after a cancelled task. Cancel deletes the task's group posts
// for everyone unless asked to keep them (revokeCopies). Until the meal
// ends, a private reply quoting one of its posts goes to another active
// task selling the same meal at the same mess and diet, as if the buyer had
// replied to that task's post (adoptTarget). With no such task the reply
// gets nothing, like a reply to a post whose meal already sold.

const (
	// revokeWindow is how old a post may be and still be deleted for
	// everyone. WhatsApp allows about two and a half days; this stays
	// under that so a late revoke isn't sent only to be refused.
	revokeWindow = 48 * time.Hour
	// revokeTimeout bounds each revoke send.
	revokeTimeout = 20 * time.Second
	// groupPostTimeout bounds a group post that a cancel lets finish.
	groupPostTimeout = time.Minute
)

// deadPost is a post of a cancelled task whose meal hasn't ended.
type deadPost struct {
	task store.SellTask
	chat types.JID
	id   string
	at   time.Time
}

// retire notes t's posts as dead, so replies to them can go to another
// task. Calling it again adds posts claimed since.
func (m *sellManager) retire(t store.SellTask) {
	offers, err := m.st.SellTaskOffers(context.WithoutCancel(m.base), t.ID)
	if err != nil {
		m.log.Warn("could not read a cancelled task's posts; replies to them will be ignored", "task", t.ID, "err", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, d := range m.dead {
		if !now.Before(d.task.EndsAt) {
			delete(m.dead, id)
		}
	}
	for _, o := range offers {
		chat, err := types.ParseJID(o.Group)
		if err != nil {
			continue
		}
		m.dead[o.ID] = deadPost{task: t, chat: chat, id: o.ID, at: o.PostedAt}
	}
}

// forget drops task id's posts from the dead ones, for a task that Cancel
// retired but that ended sold after all.
func (m *sellManager) forget(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, d := range m.dead {
		if d.task.ID == id {
			delete(m.dead, k)
		}
	}
}

// sameMeal reports whether a and b sell the same thing: one date, one mess
// and diet, and one meal, whatever slot suffix (dinner#2) either carries.
func sameMeal(a, b store.SellTask) bool {
	return a.Mess != "" && a.Date == b.Date && a.Mess == b.Mess && a.Diet == b.Diet && baseMeal(a.Meal) == baseMeal(b.Meal)
}

// adoptTarget finds who takes e, a private reply quoting a dead post: the
// oldest active task selling the same meal. ok is false when e doesn't
// quote a dead post or no task sells that meal.
func (m *sellManager) adoptTarget(e *events.Message) (from deadPost, to int64, ok bool) {
	quote := e.Message.GetExtendedTextMessage().GetContextInfo().GetStanzaID()
	if quote == "" {
		return deadPost{}, 0, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, found := m.dead[quote]
	if !found || !time.Now().Before(d.task.EndsAt) || !privateOfferReply(e, d.chat, d.id, d.at) {
		return deadPost{}, 0, false
	}
	for id, rt := range m.running {
		if id == d.task.ID || !rt.gate.active.Load() || !sameMeal(rt.task, d.task) {
			continue
		}
		if to == 0 || id < to {
			to = id
		}
	}
	return d, to, to != 0
}

// adopter is how task t takes replies to dead posts. Exactly one running
// task answers true for a given reply.
func (m *sellManager) adopter(t store.SellTask) func(*events.Message) bool {
	return func(e *events.Message) bool {
		d, to, ok := m.adoptTarget(e)
		if !ok || to != t.ID {
			return false
		}
		m.log.Info("a private reply quotes a cancelled task's post; this task takes the buyer", "task", t.ID,
			"cancelled_task", d.task.ID, "group", d.chat, "offer_id", d.id, "message_id", e.Info.ID)
		m.event(m.base, store.SellEvent{TaskID: t.ID, Peer: e.Info.Chat.ToNonAD().String(), Kind: store.EventAdopted,
			Text: strconv.FormatInt(d.task.ID, 10)})
		return true
	}
}

// revokeCopies deletes every copy a cancelled task posted, for everyone.
// The IDs come from the task's own claimed posts, so it only ever revokes
// the bot's messages. A copy that provably never went out, one too old to
// delete, or one whose revoke fails stays up, and the reason is recorded
// on the copy and logged. Every revoke is logged.
func (m *sellManager) revokeCopies(t store.SellTask) {
	ctx := context.WithoutCancel(m.base)
	offers, err := m.st.SellTaskOffers(ctx, t.ID)
	if err != nil {
		m.log.Error("could not read the cancelled task's posts; delete them by hand", "task", t.ID, "err", err)
		return
	}
	var pending []store.SellOffer
	for _, o := range offers {
		switch {
		case !o.RevokedAt.IsZero():
		case o.State == store.OfferFailed && o.Error == errTaskInactive.Error():
			m.log.Debug("not revoking a copy that was never sent", "task", t.ID, "group", o.Group, "offer_id", o.ID)
		default:
			pending = append(pending, o)
		}
	}
	if len(pending) == 0 {
		return
	}
	fail := func(o store.SellOffer, why string) {
		m.log.Warn("could not delete the cancelled task's post; delete it by hand", "task", t.ID, "group", o.Group,
			"offer_id", o.ID, "state", o.State, "why", why)
		if err := m.st.SetOfferRevokeError(ctx, t.ID, o.ID, why); err != nil {
			m.log.Warn("could not record the failed revoke", "task", t.ID, "err", err)
		}
		m.event(ctx, store.SellEvent{TaskID: t.ID, Peer: o.Group, Kind: store.EventRevokeFailed, Text: why})
	}
	if !m.connected(30 * time.Second) {
		for _, o := range pending {
			fail(o, "WhatsApp is not connected")
		}
		return
	}
	for _, o := range pending {
		if err := m.revokeCopy(ctx, t, o); err != nil {
			fail(o, err.Error())
		}
	}
}

// revokeCopy deletes one copy for everyone and drops it from the meal's
// offer posts and the group log.
func (m *sellManager) revokeCopy(ctx context.Context, t store.SellTask, o store.SellOffer) error {
	group, err := types.ParseJID(o.Group)
	if err != nil || group.Server != types.GroupServer {
		return errors.New("the recorded group is not a group JID")
	}
	if age := time.Since(o.PostedAt); age > revokeWindow {
		return errors.New("posted " + age.Truncate(time.Minute).String() + " ago, too long to delete for everyone")
	}
	sendCtx, stop := context.WithTimeout(ctx, revokeTimeout)
	defer stop()
	// No rate limit, as with the sale's ✅: one revoke per post, after
	// the task ended. An empty sender marks the target as our own message.
	resp, err := m.send(sendCtx, group, m.conn.BuildRevoke(group, types.EmptyJID, o.ID))
	if err != nil {
		return err
	}
	at := firstTime(resp.Timestamp, time.Now())
	m.log.Info("deleted the cancelled task's post for everyone", "task", t.ID, "group", o.Group, "offer_id", o.ID, "copy", o.Seq, "state", o.State)
	if err := m.st.SetOfferRevoked(ctx, t.ID, o.ID, at); err != nil {
		m.log.Warn("could not record the revoke", "task", t.ID, "offer_id", o.ID, "err", err)
	}
	if err := m.st.DropOfferPost(ctx, o.Group, o.ID); err != nil {
		m.log.Warn("could not drop the revoked post from the meal's offer posts", "task", t.ID, "offer_id", o.ID, "err", err)
	}
	id := resp.ID
	if id == "" {
		id = "revoke-" + o.ID
	}
	if err := m.own.add(ctx, store.LogEntry{Chat: o.Group, ID: id, Type: "delete", TS: at, Target: o.ID}); err != nil {
		m.log.Warn("could not add the revoke to the group log", "offer_id", o.ID, "err", err)
	}
	m.event(ctx, store.SellEvent{TaskID: t.ID, Peer: o.Group, Kind: store.EventRevoked, Text: o.ID})
	return nil
}
