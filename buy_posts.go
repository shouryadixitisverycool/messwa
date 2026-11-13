package main

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/store"
)

// Buy posts. With [sell] message_buy_posts on, a sell task also goes after
// people who posted in the groups that they want its meal, instead of
// waiting for them to spot the offer. A buy post counts when:
//
//   - it is a buy listing (not an exchange) for the task's meal and date;
//   - it names the task's mess or no mess, and its diet or no diet;
//   - it is still open: not sold, withdrawn or deleted, and the meal hasn't
//     ended;
//   - its poster isn't already one of the task's buyers, and passes the
//     [sell] buyers check.
//
// Each poster is recorded as a buyer first, so the task gate lets the bot
// message them, then gets one private reply to their post naming the meal
// (sayHave), then joins the queue as if they had replied to the offer: a
// negotiating task quotes them its price, a fixed-price one asks them to
// pay. Being a buyer already is what stops a second message, across
// restarts too. A task that hands off on the first reply skips this, since
// nobody it messages first has replied.

const (
	// buyPostSweep is how often a running task looks for new buy posts.
	buyPostSweep = time.Minute
	// buyPostLookback is how far back a buy post may have been made.
	// People post tomorrow's meal the evening before.
	buyPostLookback = 24 * time.Hour
	// maxBuyPosts caps how many posters one run of a task messages, so a
	// busy group doesn't spend the task's sends before anyone replies.
	maxBuyPosts = 10
)

// buyPostsWanted reports whether task t messages buy posters.
func (m *sellManager) buyPostsWanted(t store.SellTask) bool {
	return m.cfg.Live().Sell.MessageBuyPosts && t.Handoff != string(config.HandoffOnReply)
}

// messageBuyPosts sweeps for buy posts now and every buyPostSweep until ctx
// ends or the queue closes.
func (m *sellManager) messageBuyPosts(ctx context.Context, t store.SellTask, s *sale, gate *taskGate, queue *offerQueue) {
	sent := 0
	for {
		if !m.buyPostsWanted(t) {
			return
		}
		sent += m.sweepBuyPosts(ctx, t, s, gate, queue, maxBuyPosts-sent)
		if sent >= maxBuyPosts || queue.isClosed() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(buyPostSweep):
		}
	}
}

// sweepBuyPosts messages up to room posters of open buy posts for t's meal
// and queues them. It returns how many it messaged.
func (m *sellManager) sweepBuyPosts(ctx context.Context, t store.SellTask, s *sale, gate *taskGate, queue *offerQueue, room int) int {
	now := time.Now()
	sold := m.cfg.Live().Sold
	rule := store.SoldRule{Emojis: sold.Emojis, ClosedEmojis: sold.ClosedEmojis, AdminsCount: sold.AdminsCount}
	ls, err := m.st.Listings(ctx, rule, store.ListingFilter{
		Since: now.Add(-buyPostLookback), EndsAfter: now, Kind: "buy", Meal: baseMeal(t.Meal),
	})
	if err != nil {
		m.log.Warn("could not look for buy posts", "task", t.ID, "err", err)
		return 0
	}
	sent := 0
	// Oldest first: whoever has waited longest hears first.
	for i := len(ls) - 1; i >= 0 && sent < room; i-- {
		l := ls[i]
		if !buyPostMatches(l, t.Date, s.mess, s.diet) {
			continue
		}
		// Leave room for the buyers' replies, as reposts do.
		if m.limits.left(t.ID) <= repostReserve {
			return sent
		}
		if m.messageBuyPost(ctx, t, s, gate, queue, rule, l) {
			sent++
		}
		if ctx.Err() != nil || queue.isClosed() {
			break
		}
	}
	return sent
}

// buyPostMatches reports whether buy listing l asks for the meal on date
// at mess with diet. A post that names no mess or no diet takes any.
func buyPostMatches(l store.Listing, date, mess, diet string) bool {
	switch {
	case l.MealDate != date, l.Deleted, l.SoldText, l.SoldReact, l.Closed:
		return false
	case l.Mess != "" && mess != "" && l.Mess != mess:
		return false
	case l.Diet != "" && diet != "" && l.Diet != diet:
		return false
	}
	return true
}

// messageBuyPost queues l's poster after a private reply to their post. It
// reports whether the reply went out.
func (m *sellManager) messageBuyPost(ctx context.Context, t store.SellTask, s *sale, gate *taskGate, queue *offerQueue, rule store.SoldRule, l store.Listing) bool {
	peer, alt := m.posterJIDs(ctx, l)
	if peer.IsEmpty() || queue.has(peer) {
		return false
	}
	if ok, err := m.st.IsSellTaskBuyer(ctx, t.ID, m.peerForms(ctx, peer)...); err != nil || ok {
		return false
	}
	if gate.turnsAway(ctx, peer, alt) {
		return false
	}
	_, posts, err := m.st.Listing(ctx, rule, l.ID)
	if err != nil {
		m.log.Warn("could not read a buy post", "task", t.ID, "listing", l.ID, "err", err)
		return false
	}
	post, ok := latestPost(posts)
	if !ok {
		return false
	}
	text := sayHave(portalMealLabel(s.mess, s.diet, t.Meal), offerDay(m.cfg, t.Date))[0]
	sent := false
	chat := peer
	if !alt.IsEmpty() {
		chat = alt
	}
	queue.addAfter(peer, chat, l.SellerName, func() bool {
		_, err := gate.send(ctx, peer, quotedReply(text, post))
		if err != nil {
			m.log.Warn("could not message someone with a buy post", "task", t.ID, "buyer", peer, "err", err)
			// Recorded but never told: keep them out of the queue, now
			// and after a restart.
			if _, err := m.st.MoveSellTaskBuyer(context.WithoutCancel(ctx), t.ID, peer.String(), store.BuyerQueued, store.BuyerSkipped, "", time.Time{}); err != nil {
				m.log.Warn("could not drop a buyer the bot failed to message", "task", t.ID, "buyer", peer, "err", err)
			}
			return false
		}
		sent = true
		m.event(ctx, store.SellEvent{TaskID: t.ID, Peer: peer.String(), Kind: store.EventBuyPost, N: l.ID, Text: text})
		m.log.Info("messaged someone with a buy post", "task", t.ID, "buyer", peer, "listing", l.ID)
		return true
	})
	if sent && l.SellerName != "" {
		if err := m.st.NameSellTaskBuyer(ctx, t.ID, peer.String(), l.SellerName); err != nil {
			m.log.Warn("could not record the buyer's name", "task", t.ID, "err", err)
		}
	}
	return sent
}

// posterJIDs is who posted l, by phone number when known, as buyerPeer
// names people who reply, and their other JID.
func (m *sellManager) posterJIDs(ctx context.Context, l store.Listing) (peer, alt types.JID) {
	var pn, lid types.JID
	for _, s := range []string{l.Seller, l.SellerAlt} {
		j, err := types.ParseJID(s)
		if err != nil || s == "" {
			continue
		}
		switch j = j.ToNonAD(); j.Server {
		case types.DefaultUserServer:
			if pn.IsEmpty() {
				pn = j
			}
		case types.HiddenUserServer:
			if lid.IsEmpty() {
				lid = j
			}
		}
	}
	if pn.IsEmpty() && !lid.IsEmpty() {
		pn = m.conn.PNForLID(ctx, lid)
	}
	if pn.IsEmpty() {
		return lid, types.EmptyJID
	}
	return pn, lid
}

// latestPost is the newest copy of a listing that is still up.
func latestPost(posts []store.Post) (store.Post, bool) {
	var out store.Post
	found := false
	for _, p := range posts {
		if !p.Deleted && (!found || p.TS.After(out.TS)) {
			out, found = p, true
		}
	}
	return out, found
}

// quotedReply is text sent privately as a reply to group post p, the way
// WhatsApp's "reply privately" does.
func quotedReply(text string, p store.Post) *waE2E.Message {
	return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String(text),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String(p.ID),
			Participant:   proto.String(p.Sender),
			RemoteJID:     proto.String(p.Chat),
			QuotedMessage: &waE2E.Message{Conversation: proto.String(p.Text)},
		},
	}}
}
