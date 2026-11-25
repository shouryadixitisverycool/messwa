package main

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"messwa/internal/store"
)

// A new buyer may reply to a post after its task stopped listening.
// Match the same quoted replies and optional plain DMs as an active offer.
func (m *sellManager) answerSoldReply(ctx context.Context, e *events.Message, peer types.JID) {
	if e.IsEdit || parseBuyer(dmText(e.Message)).kind == intentPass {
		return
	}
	quote := e.Message.GetExtendedTextMessage().GetContextInfo().GetStanzaID()
	if quote == "" && !m.cfg.Live().Sell.AcceptPlainDMs {
		return
	}
	tasks, err := m.st.SellTasks(ctx, time.Now().Add(-lateWindow))
	if err != nil {
		m.log.Warn("could not look up sold offers", "err", err)
		return
	}
	if quote == "" {
		for _, t := range tasks {
			if t.State == store.TaskActive && time.Now().Before(t.EndsAt) && newOfferTopic(m.cfg, t.Mess, baseMeal(t.Meal), "").asksAbout(dmText(e.Message)) {
				return
			}
		}
	}
	for _, t := range tasks {
		if t.State != store.TaskSold && t.State != store.TaskActive {
			continue
		}
		if t.State == store.TaskSold && (t.EndedAt.IsZero() || !time.Now().Before(t.EndedAt.Add(store.SoldReplyWindow))) {
			continue
		}
		offers, err := m.st.SellTaskOffers(ctx, t.ID)
		if err != nil {
			m.log.Warn("could not read offers for a late reply", "task", t.ID, "err", err)
			return
		}
		o := &groupOffer{}
		for _, offer := range offers {
			group, err := types.ParseJID(offer.Group)
			if err == nil {
				o.watch(postedOffer{Chat: group, ID: offer.ID, At: offer.PostedAt})
			}
		}
		if m.cfg.Live().Sell.AcceptPlainDMs {
			o.acceptPlainDMs(newPlainDMs(m.cfg, m.st, m.log, t.Mess, baseMeal(t.Meal), ""))
		}
		if _, matches := o.buyer(ctx, m.conn, e); !matches {
			continue
		}
		// An active offer takes precedence over a sold one for plain DMs.
		if t.State == store.TaskActive || e.Info.Timestamp.Before(t.EndedAt) {
			return
		}
		for _, other := range tasks {
			if other.State == store.TaskActive && time.Now().Before(other.EndsAt) && samePoolTasks(t, other) {
				return
			}
		}
		gate := &taskGate{m: m, id: t.ID}
		if gate.turnsAway(ctx, peer, e.Info.Chat.ToNonAD()) {
			return
		}
		claimed, err := m.st.ClaimNewSoldNotice(ctx, t.ID, peer.String(), e.Info.Chat.ToNonAD().String(), time.Now())
		if err != nil {
			m.log.Warn("could not claim a new buyer's sold notice", "task", t.ID, "err", err)
		}
		if !claimed {
			return
		}
		if err := m.sendLateSold(ctx, t, peer); err != nil {
			m.log.Warn("could not tell a new buyer the meal sold", "task", t.ID, "err", err)
			return
		}
		m.event(ctx, store.SellEvent{TaskID: t.ID, Peer: peer.String(), Kind: store.EventToldSold, Text: "first replied after the meal sold"})
		return
	}
}

func (m *sellManager) sendLateSold(ctx context.Context, t store.SellTask, peer types.JID) error {
	ctx, cancel := context.WithDeadline(ctx, t.EndedAt.Add(store.SoldReplyWindow))
	defer cancel()
	gate := &taskGate{m: m, id: t.ID}
	return sendReply(ctx, gate.sendSoldNotice, peer, reply{"sorry, sold"})
}
