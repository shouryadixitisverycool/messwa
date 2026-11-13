package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"messwa/internal/store"
)

// Three things you can do to a running sell task by hand, from the
// dashboard or an MCP tool, instead of editing the database:
//
//   - MarkSold: you sold the meal yourself. The task stops, the sale goes
//     in the ledger as manual, buyers still in the running hear "sorry
//     sold" as after a bot sale, and the offer posts get their ✅. The
//     buyer you sold to gets nothing from the bot.
//   - AddBuyer: queue someone as if they had replied privately to the
//     offer. The bot quotes them like any other buyer.
//   - MuteBuyer: the bot sends a buyer nothing more for the task, not even
//     "sorry sold", and ignores what they write. Their record stays;
//     payments while muted do not trigger an automatic QR.
//
// Each one writes an event to the task's log.

var (
	errBuyerMuted = errors.New("buyer is muted for this task")
	errSoldByYou  = errors.New("sold by you")
)

// manualSale is a sale MarkSold hands to the running task.
type manualSale struct {
	peer, name, amount, note string
	at                       time.Time
	// recorded gets the ledger write's result, once.
	recorded chan error
}

// manualSoldError ends a task you sold by hand; detail is for the status.
type manualSoldError struct{ detail string }

func (e manualSoldError) Error() string { return "sold by you: " + e.detail }

// soldBuyer names the buyer of a manual sale. It returns the peer to
// record ("" for a name alone) and the name. A phone number or JID that
// matches one of the task's buyers records them as stored.
func (m *sellManager) soldBuyer(ctx context.Context, id int64, who, name string) (string, string, error) {
	who, name = strings.TrimSpace(who), strings.TrimSpace(name)
	if who == "" {
		if name == "" {
			return "", "", errors.New("say who bought it: one of the task's buyers, a phone number, or a name")
		}
		return "", name, nil
	}
	jid, err := parseBuyerJID(who)
	if err != nil {
		return "", "", errors.New(strings.Replace(err.Error(), "-peer", "buyer", 1))
	}
	peers := m.peerForms(ctx, jid)
	buyers, err := m.st.SellTaskBuyers(ctx, id)
	if err != nil {
		return "", "", err
	}
	for _, b := range buyers {
		for _, p := range peers {
			if p == b.Peer || (b.Alt != "" && p == b.Alt) {
				if name == "" {
					name = firstOf(b.Name, m.st.PeerName(ctx, b.Peer, b.Alt))
				}
				return b.Peer, name, nil
			}
		}
	}
	return jid.String(), name, nil
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// peerForms are the JIDs a buyer may be stored under: jid, and the phone
// number behind a LID.
func (m *sellManager) peerForms(ctx context.Context, jid types.JID) []string {
	jid = jid.ToNonAD()
	out := []string{jid.String()}
	if jid.Server == types.HiddenUserServer {
		if pn := m.conn.PNForLID(ctx, jid); !pn.IsEmpty() {
			out = append(out, pn.String())
		}
	}
	return out
}

// MarkSold records that you sold task id's meal yourself to who (a phone
// number or JID, one of the task's buyers or not) or to name, for
// priceRupees. It returns once the sale is in the ledger; "sorry sold"
// notices and the ✅ follow in the background.
func (m *sellManager) MarkSold(ctx context.Context, id int64, who, name string, priceRupees int, note string) error {
	if priceRupees <= 0 || priceRupees > 10000 {
		return errors.New("price must be between 1 and 10000 rupees")
	}
	rt, err := m.runningActive(ctx, id)
	if err != nil {
		return err
	}
	switch _, state, err := m.st.TaskDelivery(ctx, id); {
	case err != nil:
		return err
	case state == "sending" || state == "sent":
		return errors.New("the bot already sold this meal; check sell_task_status")
	}
	peer, name, err := m.soldBuyer(ctx, id, who, name)
	if err != nil {
		return err
	}
	ms := &manualSale{peer: peer, name: name, amount: store.FormatPaise(int64(priceRupees) * 100),
		note: strings.TrimSpace(note), at: time.Now(), recorded: make(chan error, 1)}
	m.mu.Lock()
	if rt.manual != nil {
		m.mu.Unlock()
		return errors.New("a manual sale for this task is already being recorded")
	}
	rt.manual = ms
	m.mu.Unlock()
	// Nothing else goes out from here: the task's own sends stop now, and
	// only "sorry sold" goes out once the sale is recorded.
	rt.gate.active.Store(false)
	rt.cancel(errSoldByYou)
	select {
	case err := <-ms.recorded:
		return err
	case <-rt.done:
		select {
		case err := <-ms.recorded:
			return err
		default:
			return errors.New("the task ended before the sale could be recorded; check sell_task_status")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runningActive is task id while it runs in this process.
func (m *sellManager) runningActive(ctx context.Context, id int64) (*runningTask, error) {
	m.mu.Lock()
	rt := m.running[id]
	m.mu.Unlock()
	if rt != nil {
		return rt, nil
	}
	t, err := m.st.GetSellTask(ctx, id)
	switch {
	case err != nil:
		return nil, err
	case t == nil:
		return nil, fmt.Errorf("no sell task %d", id)
	case t.State != store.TaskActive:
		return nil, fmt.Errorf("sell task %d is %s", id, t.State)
	}
	return nil, fmt.Errorf("sell task %d is not running in this messwa; try again once run is up", id)
}

// finishManualSale runs in the task once MarkSold stopped it, however the
// task's selling returned: it records the sale, then tells buyers still in
// the running that it sold, as after a bot sale. It returns the error that
// ends the task. If the bot sold the meal in the meantime, recording fails
// and end sees the bot's sale.
func (m *sellManager) finishManualSale(ctx context.Context, t store.SellTask, gate *taskGate) error {
	m.mu.Lock()
	ms := m.running[t.ID].manual
	m.mu.Unlock()
	notifyCtx := context.WithoutCancel(ctx)
	err := m.st.RecordTaskManualSale(notifyCtx, t.ID, store.TaskManualSale{Peer: ms.peer, BuyerName: ms.name, Amount: ms.amount, At: ms.at})
	ms.recorded <- err
	if err != nil {
		m.log.Warn("could not record your sale", "task", t.ID, "err", err)
		return fmt.Errorf("could not record your sale: %w", err)
	}
	who := describeBuyer(ms.peer, ms.name)
	detail := fmt.Sprintf("sold by you to %s for INR %s; the bot sent them nothing", who, strings.TrimSuffix(ms.amount, ".00"))
	if ms.note != "" {
		detail += ". Note: " + ms.note
	}
	m.log.Info("sell task sold by you", "task", t.ID, "buyer", ms.peer, "name", ms.name, "amount", ms.amount)
	m.event(notifyCtx, store.SellEvent{TaskID: t.ID, Peer: ms.peer, Kind: store.EventSoldByYou, N: paise(ms.amount), Text: ms.note})

	buyers, err := m.st.SellTaskBuyers(notifyCtx, t.ID)
	if err != nil {
		m.log.Warn("could not list buyers to tell it sold", "task", t.ID, "err", err)
	}
	var waiting []types.JID
	for _, b := range buyers {
		if peer, err := types.ParseJID(b.Peer); err == nil && b.Peer != ms.peer {
			waiting = append(waiting, peer)
		}
	}
	// The gate is closed to the task's own sends; these go through
	// sendSoldNotice, which only lets out the "sorry sold" claimed for them.
	s := m.newSale(t.Date, t.Meal, t.EndsAt)
	s.send, s.event = gate.sendSoldNotice, m.saleEvents(t.ID)
	s.tellSold(notifyCtx, &taskJournal{st: m.st, t: t}, waiting)
	return manualSoldError{detail: detail}
}

func describeBuyer(peer, name string) string {
	who := peer
	if user, ok := strings.CutSuffix(peer, "@"+types.DefaultUserServer); ok {
		who = "+" + user
	}
	switch {
	case who == "":
		return name
	case name != "":
		return name + " (" + who + ")"
	}
	return who
}

// sendSoldNotice sends "sorry sold" after you sold the meal by hand, once
// the task's own sends have stopped. It skips only the active check, and
// only for a buyer the store moved to told_sold.
func (g *taskGate) sendSoldNotice(ctx context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	if to.Server == types.GroupServer {
		return whatsmeow.SendResponse{}, errors.New("a sold notice goes to a buyer, not a group")
	}
	if err := g.allowed(ctx, to, extra); err != nil {
		return whatsmeow.SendResponse{}, err
	}
	if g.turnsAway(ctx, to) {
		return whatsmeow.SendResponse{}, errSavedContact
	}
	if g.muted(ctx, to) {
		return whatsmeow.SendResponse{}, errBuyerMuted
	}
	told := false
	if buyers, err := g.m.st.SellTaskBuyers(ctx, g.id); err == nil {
		forms := g.m.peerForms(ctx, to)
		for _, b := range buyers {
			for _, p := range forms {
				told = told || (b.State == store.BuyerToldSold && (p == b.Peer || p == b.Alt))
			}
		}
	}
	if !told {
		return whatsmeow.SendResponse{}, errors.New("no sold notice was claimed for this buyer")
	}
	if err := g.m.limits.wait(ctx, g.id); err != nil {
		return whatsmeow.SendResponse{}, err
	}
	if g.muted(ctx, to) {
		return whatsmeow.SendResponse{}, errBuyerMuted
	}
	return g.m.send(ctx, to, msg, extra...)
}

// muted reports whether the task's buyer to is muted. A failed read counts
// as muted: better to miss a message than send one you stopped.
func (g *taskGate) muted(ctx context.Context, to types.JID) bool {
	muted, err := g.m.st.SellTaskBuyerMuted(ctx, g.id, g.m.peerForms(ctx, to)...)
	if err != nil {
		g.m.log.Warn("could not read whether a buyer is muted; not messaging them", "task", g.id, "err", err)
		return true
	}
	return muted
}

func (j *taskJournal) muted(ctx context.Context, buyer types.JID) bool {
	muted, err := j.st.SellTaskBuyerMuted(ctx, j.t.ID, buyer.String())
	return err != nil || muted
}

// acceptAdded lets AddBuyer queue buyers on task t once it listens.
func (m *sellManager) acceptAdded(t store.SellTask, gate *taskGate, queue *offerQueue) {
	add := func(ctx context.Context, peer types.JID, name string) error {
		if gate.turnsAway(ctx, peer) {
			return errSavedContact
		}
		if ok, err := m.st.IsSellTaskBuyer(ctx, t.ID, m.peerForms(ctx, peer)...); err != nil {
			return err
		} else if ok || queue.has(peer) {
			return errors.New("they are already one of this task's buyers")
		}
		// As if they had replied to the offer: join records them, and a
		// negotiating task opens with its price, since there is no first
		// message to answer.
		if !queue.addFrom(peer, peer, name) {
			return errors.New("could not add them; they already joined or the task ended")
		}
		if ok, err := m.st.IsSellTaskBuyer(ctx, t.ID, peer.String()); err != nil || !ok {
			return errors.Join(err, errors.New("could not add them; the task may have just ended"))
		}
		if name != "" {
			if err := m.st.NameSellTaskBuyer(ctx, t.ID, peer.String(), name); err != nil {
				m.log.Warn("could not record the buyer's name", "task", t.ID, "err", err)
			}
		}
		m.event(ctx, store.SellEvent{TaskID: t.ID, Peer: peer.String(), Kind: store.EventAddedByYou, Text: name})
		m.log.Info("buyer added by you", "task", t.ID, "buyer", peer)
		return nil
	}
	m.mu.Lock()
	if rt := m.running[t.ID]; rt != nil {
		rt.add = add
	}
	m.mu.Unlock()
}

// AddBuyer queues who (a phone number with country code, or a WhatsApp
// JID) on task id as if they had replied privately to its offer. The bot
// then quotes them like anyone who replied. name is optional.
func (m *sellManager) AddBuyer(ctx context.Context, id int64, who, name string) error {
	jid, err := parseBuyerJID(who)
	if err != nil {
		return errors.New(strings.Replace(err.Error(), "-peer", "buyer", 1))
	}
	rt, err := m.runningActive(ctx, id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	add := rt.add
	m.mu.Unlock()
	if add == nil || !rt.gate.active.Load() {
		return errors.New("the task is not taking buyers right now; try again in a moment")
	}
	return add(ctx, jid, strings.TrimSpace(name))
}

// MuteBuyer mutes or unmutes who, one of task id's buyers. While muted the
// bot sends them nothing for the task and ignores their messages; their
// record stays as it was.
func (m *sellManager) MuteBuyer(ctx context.Context, id int64, who string, muted bool) error {
	jid, err := parseBuyerJID(who)
	if err != nil {
		return errors.New(strings.Replace(err.Error(), "-peer", "buyer", 1))
	}
	t, err := m.st.GetSellTask(ctx, id)
	switch {
	case err != nil:
		return err
	case t == nil:
		return fmt.Errorf("no sell task %d", id)
	case t.State != store.TaskActive:
		return fmt.Errorf("sell task %d is %s", id, t.State)
	}
	if muted {
		d, state, err := m.st.TaskDelivery(ctx, id)
		if err != nil {
			return err
		}
		if d != nil && state == "sending" {
			for _, p := range m.peerForms(ctx, jid) {
				if p == d.Peer {
					return errors.New("this buyer's paid QR is already being delivered; try again after delivery finishes")
				}
			}
		}
	}
	peer, changed, err := m.st.MuteSellTaskBuyer(ctx, id, muted, time.Now(), m.peerForms(ctx, jid)...)
	if err != nil || !changed {
		return err
	}
	kind := store.EventMuted
	if !muted {
		kind = store.EventUnmuted
	}
	m.event(ctx, store.SellEvent{TaskID: id, Peer: peer, Kind: kind})
	m.log.Info("buyer "+kind+" by you", "task", id, "buyer", peer)
	return nil
}
