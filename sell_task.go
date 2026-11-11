package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/store"
)

// Sell tasks sell one registered meal from inside run, on run's WhatsApp
// connection, so the board keeps working during a sale. They reuse the
// sale engine deliver-qr uses. The rules below are enforced here, not left
// to the agent that starts the task:
//
//   - nothing is sent unless the task is active;
//   - besides the group post, messages go only to people who replied
//     privately to that post, or with [sell] accept_plain_dms, DMed about
//     it (see plain_dm.go);
//   - no buyer is asked, and no delivery armed, below the floor price;
//   - sends are rate-limited per task and across all tasks;
//   - a task ends when its meal ends.
//
// Every send is claimed in the store first, so a restart resumes a task
// without posting, prompting or saying "sorry sold" twice.

// Send limits. A sale needs the group post, one prompt per buyer, the QR
// and a "sorry sold" to each buyer still waiting, so these stop a runaway
// loop without slowing a normal sale much.
const (
	taskSendsPerMinute   = 6
	globalSendsPerMinute = 12
	taskSendCap          = 40
)

// resumeGrace is how long a resumed buyer's turn lasts at least, so a
// payment sent while run was down still counts.
const resumeGrace = 30 * time.Second

var errTaskInactive = errors.New("sell task is not active; nothing was sent")
var errCancelledByUser = errors.New("cancelled")

// taskSellSettings is how a task posts its offer: the [sell] section, in
// the first configured group when [sell] picks none. With show_price the
// post shows the task's max price.
func taskSellSettings(cfg *config.Config) (sellSettings, error) {
	s, err := resolveSellSettings(cfg, sellFlags{})
	if err == nil && len(s.Groups) == 0 && len(cfg.Groups) > 0 {
		s.Groups = cfg.Groups[:1]
	}
	return s, err
}

// handedOffError ends a task that handed its first buyer to you. detail
// says who, for sell_task_status.
type handedOffError struct{ detail string }

func (e handedOffError) Error() string { return "handed off: " + e.detail }
func (e handedOffError) Unwrap() error { return errHandedOff }

// negotiator decides what to ask a buyer, in paise. This is the seam for
// negotiation: a negotiator can talk to the buyer and return the price
// they accepted. Whatever it returns, the task refuses to ask for or arm
// a delivery below the task's floor, and the payment must match exactly.
type negotiator func(ctx context.Context, t store.SellTask, buyer types.JID) (int64, error)

// openAtMax asks every buyer the task's max price. It stands in until
// negotiation lands.
func openAtMax(_ context.Context, t store.SellTask, _ types.JID) (int64, error) {
	return t.MaxPaise, nil
}

type sellManager struct {
	cfg  *config.Config
	log  *slog.Logger
	st   *store.Store
	conn saleConn
	// send is the raw WhatsApp send. Only taskGate calls it.
	send      sendFunc
	connected func(time.Duration) bool
	portalFor func(ctx context.Context, userID int64) (mealPortal, error)
	negotiate negotiator
	limits    *sendLimits
	// own writes the task's offer posts and ✅ into the group log, since
	// run never gets events for messages it sent itself.
	own ownGroupLog

	base    context.Context // run's context; tasks stop with it and resume next start
	mu      sync.Mutex
	running map[int64]*runningTask
}

type runningTask struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
	gate   *taskGate
}

func newSellManager(ctx context.Context, cfg *config.Config, log *slog.Logger, st *store.Store, conn saleConn, send sendFunc,
	connected func(time.Duration) bool, portalFor func(context.Context, int64) (mealPortal, error), own ownGroupLog) *sellManager {
	return &sellManager{
		cfg: cfg, log: log.With("component", "sell"), st: st, conn: conn, send: send, connected: connected,
		portalFor: portalFor, negotiate: openAtMax, limits: newSendLimits(), own: own, base: ctx, running: map[int64]*runningTask{},
	}
}

// Start checks the registration, records the task and starts selling in
// the background. Prices are whole rupees. handoff is "never" (or empty)
// or "on_reply", which ends the task at the first private reply and hands
// that buyer's chat to you.
func (m *sellManager) Start(ctx context.Context, userID int64, date, meal string, minRupees, maxRupees int, handoff string) (int64, error) {
	if minRupees <= 0 || maxRupees < minRupees || maxRupees > 10000 {
		return 0, errors.New("min_price must be at least 1 and max_price between min_price and 10000")
	}
	h, err := config.ParseHandoff(handoff)
	if err != nil {
		return 0, err
	}
	slot, err := findSlot(m.cfg, date, meal)
	if err != nil {
		return 0, err
	}
	if !time.Now().Before(slot.End) {
		return 0, errors.New("meal has already ended")
	}
	if settings, err := taskSellSettings(m.cfg); err != nil {
		return 0, err
	} else if len(settings.Groups) == 0 {
		return 0, errors.New("no group to post the offer in; add the mess groups to config.toml")
	}
	p, err := m.portalFor(ctx, userID)
	if err != nil {
		return 0, err
	}
	s := m.newSale(date, meal, slot.End)
	if err := s.usePortal(ctx, p, m.cfg.VegOnly); err != nil {
		return 0, err
	}
	t := store.SellTask{Date: date, Meal: meal, MinPaise: int64(minRupees) * 100, MaxPaise: int64(maxRupees) * 100,
		UserID: userID, AccountHash: s.accountHash, Mess: s.mess, Diet: s.diet, Handoff: string(h), CreatedAt: time.Now(), EndsAt: slot.End}
	if t.ID, err = m.st.CreateSellTask(ctx, t); err != nil {
		return 0, err
	}
	t.State = store.TaskActive
	m.launch(t, s, nil)
	m.log.Info("sell task started", "task", t.ID, "date", date, "meal", meal, "min", minRupees, "max", maxRupees, "handoff", h)
	return t.ID, nil
}

// Cancel stops a task. A QR already being delivered for a payment still
// goes out, and the task then ends as sold.
func (m *sellManager) Cancel(ctx context.Context, id int64) error {
	m.mu.Lock()
	rt := m.running[id]
	m.mu.Unlock()
	if rt == nil {
		t, err := m.st.GetSellTask(ctx, id)
		if err != nil {
			return err
		}
		if t == nil || t.State != store.TaskActive {
			return fmt.Errorf("sell task %d is not active", id)
		}
		m.end(*t, store.TaskCancelled, "cancelled")
		return nil
	}
	rt.gate.active.Store(false)
	rt.cancel(errCancelledByUser)
	select {
	case <-rt.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Resume restarts the tasks that were active when run last stopped. Call
// it before connecting, so payments and replies that arrived while run was
// down reach the tasks. It returns once every resumed task is listening,
// or after a few seconds. With sell tasks turned off, it cancels them.
func (m *sellManager) Resume(ctx context.Context, enabled bool) error {
	tasks, err := m.st.SellTasks(ctx, time.Now())
	if err != nil {
		return err
	}
	var listening []chan struct{}
	for _, t := range tasks {
		if t.State != store.TaskActive {
			continue
		}
		switch {
		case !enabled:
			m.end(t, store.TaskCancelled, "sell tasks are turned off in config.toml")
		case !time.Now().Before(t.EndsAt):
			m.end(t, store.TaskExpired, "the meal ended while messwa was stopped")
		default:
			ready := make(chan struct{})
			listening = append(listening, ready)
			m.log.Info("resuming sell task", "task", t.ID, "date", t.Date, "meal", t.Meal)
			m.launch(t, nil, ready)
		}
	}
	timeout := time.After(10 * time.Second)
	for _, ready := range listening {
		select {
		case <-ready:
		case <-timeout:
			m.log.Warn("a resumed sell task is slow to start listening; connecting anyway")
			return nil
		}
	}
	return nil
}

func (m *sellManager) newSale(date, meal string, end time.Time) *sale {
	return &sale{log: m.log, loc: m.cfg.Location(), conn: m.conn, st: m.st, date: date, meal: meal, end: end}
}

func (m *sellManager) launch(t store.SellTask, s *sale, ready chan struct{}) {
	ctx, cancel := context.WithCancelCause(m.base)
	ctx, stop := context.WithDeadline(ctx, t.EndsAt)
	gate := &taskGate{m: m, id: t.ID}
	gate.active.Store(true)
	rt := &runningTask{cancel: cancel, done: make(chan struct{}), gate: gate}
	m.mu.Lock()
	m.running[t.ID] = rt
	m.mu.Unlock()
	var once sync.Once
	signal := func() {
		if ready != nil {
			once.Do(func() { close(ready) })
		}
	}
	go func() {
		defer close(rt.done)
		defer stop()
		defer signal()
		// Cancel finds the task in running until its final state is
		// written, so it can't record a different one.
		defer func() {
			m.mu.Lock()
			delete(m.running, t.ID)
			m.mu.Unlock()
		}()
		err := m.runTask(ctx, t, s, gate, signal)
		gate.active.Store(false)
		state, detail := m.outcome(ctx, err)
		if state == "" {
			m.log.Info("sell task paused until run restarts", "task", t.ID)
			return
		}
		if m.end(t, state, detail) == store.TaskSold {
			m.tickOffers(t, gate)
		}
	}()
}

// outcome maps how runTask ended to the task's final state. An empty state
// means run is shutting down and the task resumes on the next start.
func (m *sellManager) outcome(ctx context.Context, err error) (string, string) {
	var handedOff handedOffError
	switch cause := context.Cause(ctx); {
	case err == nil:
		return store.TaskSold, "QR delivered after a matching payment"
	case errors.As(err, &handedOff):
		return store.TaskHandedOff, handedOff.detail
	case errors.Is(cause, errCancelledByUser):
		return store.TaskCancelled, "cancelled"
	case m.base.Err() != nil:
		return "", ""
	case errors.Is(cause, context.DeadlineExceeded):
		return store.TaskExpired, "the meal ended unsold"
	default:
		return store.TaskFailed, err.Error()
	}
}

// end releases an unpaid reservation and records the task's final state,
// which it returns. A delivery left mid-send makes the task failed, since
// only the buyer's chat can tell whether the QR went out.
func (m *sellManager) end(t store.SellTask, state, detail string) string {
	ctx := context.WithoutCancel(m.base)
	if state != store.TaskSold {
		d, dstate, err := m.st.TaskDelivery(ctx, t.ID)
		switch {
		case err != nil:
			m.log.Error("read sell task delivery", "task", t.ID, "err", err)
		case d != nil && dstate == "armed":
			if err := m.st.ReleaseUnpaidDelivery(ctx, *d); err != nil {
				m.log.Error("release sell task reservation", "task", t.ID, "err", err)
			}
		case d != nil && dstate == "sending":
			state, detail = store.TaskFailed, "a payment was claimed but the QR send is uncertain; check the buyer's chat"
		case d != nil && dstate == "sent":
			state, detail = store.TaskSold, "QR delivered after a matching payment"
		}
	}
	if _, err := m.st.EndSellTask(ctx, t.ID, state, detail, time.Now()); err != nil {
		m.log.Error("end sell task", "task", t.ID, "err", err)
		return ""
	}
	m.log.Info("sell task ended", "task", t.ID, "state", state, "detail", detail)
	return state
}

// tickOffers reacts ✅ to the meal's offer posts once its QR is out, like
// deliver-qr does, so the board shows the listing as sold. It also ticks
// posts from an earlier deliver-qr for the same meal.
func (m *sellManager) tickOffers(t store.SellTask, gate *taskGate) {
	if !m.connected(30 * time.Second) {
		m.log.Warn("WhatsApp is not connected; mark the offer posts sold by hand", "task", t.ID)
		return
	}
	ctx, stop := context.WithTimeout(context.WithoutCancel(m.base), time.Minute)
	defer stop()
	if _, _, err := reactSold(ctx, m.st, m.own, gate.react, m.log, t.Date, t.Meal); err != nil {
		m.log.Warn("could not read the offer posts to mark them sold", "task", t.ID, "err", err)
	}
}

// runTask posts the offer (unless it already went out) and sells to the
// buyers who reply, picking up where a previous run stopped. listening is
// called once replies and any resumed payment are being watched.
func (m *sellManager) runTask(ctx context.Context, t store.SellTask, s *sale, gate *taskGate, listening func()) error {
	switch _, dstate, err := m.st.TaskDelivery(ctx, t.ID); {
	case err != nil:
		return err
	case dstate == "sent":
		return nil
	case dstate == "sending":
		return errors.New("a payment was claimed but the QR send is uncertain; check the buyer's chat")
	}
	if s == nil {
		p, err := m.portalFor(ctx, t.UserID)
		if err != nil {
			return err
		}
		s = m.newSale(t.Date, t.Meal, t.EndsAt)
		// Binds to the account the task started with.
		s.accountHash = t.AccountHash
		if err := s.usePortal(ctx, p, m.cfg.VegOnly); err != nil {
			return err
		}
	}
	s.send, s.sendPaid = gate.send, gate.sendPaid
	j := &taskJournal{st: m.st, t: t, negotiate: m.negotiate}
	settings, err := taskSellSettings(m.cfg)
	if err != nil {
		return err
	}
	queue := newOfferQueue()
	queue.join = func(peer, chat types.JID) bool {
		added, err := m.st.AddSellTaskBuyer(ctx, t.ID, peer.String(), chat.String(), time.Now())
		if err != nil {
			m.log.Error("record sell task buyer", "task", t.ID, "err", err)
		}
		return added
	}
	resume, err := m.restore(ctx, t, queue)
	if err != nil {
		return err
	}
	offers, err := m.st.SellTaskOffers(ctx, t.ID)
	if err != nil {
		return err
	}
	posting := watchGroupOffer(ctx, m.conn, queue)
	defer posting.close()
	if settings.AcceptPlainDMs {
		posting.acceptPlainDMs(newPlainDMs(m.cfg, m.st, m.log.With("task", t.ID), s.mess, t.Meal, ""))
	}
	claimed := map[string]bool{}
	for _, o := range offers {
		claimed[o.Group] = true
		group, err := types.ParseJID(o.Group)
		if err != nil {
			continue
		}
		// A failed send may still have reached the group, so replies to
		// it count, as they did before the restart.
		posting.watch(postedOffer{Chat: group, ID: o.ID, At: o.PostedAt})
	}
	if resume == nil {
		listening()
	}
	if err := m.postCopies(ctx, t, s, gate, posting, settings, claimed); err != nil {
		return err
	}
	if resume != nil {
		resume.listening = listening
	}
	s.sold = func() { posting.stop() }
	var detail string
	if t.Handoff == string(config.HandoffOnReply) {
		s.handoff = func(ctx context.Context, buyer types.JID, d store.Delivery, others []types.JID) error {
			posting.stop()
			var err error
			detail, err = m.handOff(ctx, t, buyer, d, queue.name(buyer), others)
			return err
		}
	}
	err = s.sellToQueue(ctx, context.WithoutCancel(ctx), queue, j, 0, resume)
	if errors.Is(err, errHandedOff) {
		return handedOffError{detail: detail}
	}
	return err
}

// postCopies posts the offer to every [sell] group that has no copy from
// this task yet, post_gap apart, in the background. When no copy went out
// before, it waits for the first one and fails if none posts.
func (m *sellManager) postCopies(ctx context.Context, t store.SellTask, s *sale, gate *taskGate, posting *groupOffer, settings sellSettings, claimed map[string]bool) error {
	first := len(claimed) == 0
	if !m.connected(30 * time.Second) {
		if first {
			return errors.New("WhatsApp is not connected; the offer was not posted")
		}
		m.log.Warn("WhatsApp is not connected; posting no further copies", "task", t.ID)
		return nil
	}
	joined, err := m.conn.GetJoinedGroups(ctx)
	if err != nil {
		if first {
			return err
		}
		m.log.Warn("could not list groups; posting no further copies", "task", t.ID, "err", err)
		return nil
	}
	var targets []offerTarget
	for _, g := range settings.Groups {
		target, err := pickGroup(joined, g)
		if err != nil {
			m.log.Warn("not posting the offer to this group", "task", t.ID, "group", g, "err", err)
			continue
		}
		if !claimed[target.JID.String()] {
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		if first {
			return errors.New("the offer could not be posted in any group")
		}
		return nil
	}
	var extras []string
	if ep, ok := s.portal.(extrasPortal); ok && settings.MentionExtras {
		extras = readExtras(ctx, ep, m.log, t.Date, t.Meal, s.mess)
	}
	text := offerText(offerDetails{Mess: s.mess, Diet: s.diet, Meal: t.Meal, Price: rupees(store.FormatPaise(t.MaxPaise)),
		ShowPrice: settings.ShowPrice, Day: offerDay(m.cfg, t.Date), Extras: extras})
	record := func(p postedOffer) { recordOffers(ctx, m.st, m.own, m.log, t.Date, t.Meal, []postedOffer{p}) }
	var delay time.Duration
	if !first {
		delay = settings.PostGap
	}
	posting.post(ctx, m.log, targets, delay, settings.PostGap, m.taskCopy(t, gate, text), record)
	if !first {
		return nil
	}
	if err := posting.waitPosted(); err != nil {
		return fmt.Errorf("the offer could not be posted in any group: %w", err)
	}
	return nil
}

// taskCopy claims each copy in the store before the gate sends it, so a
// restart never posts to the same group twice.
func (m *sellManager) taskCopy(t store.SellTask, gate *taskGate, text string) sendCopy {
	return func(ctx context.Context, o *groupOffer, g offerTarget) (postedOffer, error) {
		p := postedOffer{Chat: g.JID, GroupName: g.Name, ID: m.conn.GenerateMessageID(), At: time.Now().Truncate(time.Second), Text: text}
		if err := m.st.ClaimOfferPost(ctx, t.ID, g.JID.String(), p.ID, p.At); err != nil {
			return p, err
		}
		o.watch(p)
		resp, err := gate.send(ctx, g.JID, &waE2E.Message{Conversation: proto.String(text)}, whatsmeow.SendRequestExtra{ID: p.ID})
		// The copy's state must be written even if posting stopped mid-send.
		stateCtx := context.WithoutCancel(ctx)
		if err != nil {
			// It may still have reached WhatsApp, so replies keep counting,
			// but it is never sent again.
			if serr := m.st.SetOfferState(stateCtx, t.ID, p.ID, store.OfferFailed); serr != nil {
				m.log.Warn("could not record the failed offer post", "task", t.ID, "err", serr)
			}
			return p, fmt.Errorf("group offer send failed or is uncertain: %w", err)
		}
		if err := m.st.SetOfferState(stateCtx, t.ID, p.ID, store.OfferPosted); err != nil {
			m.log.Warn("could not record the offer post", "task", t.ID, "err", err)
		}
		p.At = firstTime(resp.Timestamp, p.At)
		m.log.Info("sell task offer posted", "task", t.ID, "group", g.Name, "offer_id", p.ID)
		return p, nil
	}
}

// handOff records that the task hands buyer to you and returns what
// sell_task_status shows about it. The bot has sent the buyer nothing.
func (m *sellManager) handOff(ctx context.Context, t store.SellTask, buyer types.JID, d store.Delivery, name string, others []types.JID) (string, error) {
	if err := m.st.HandOffTaskDelivery(ctx, t.ID, d, name); err != nil {
		return "", err
	}
	if _, err := m.st.MoveSellTaskBuyer(ctx, t.ID, buyer.String(), store.BuyerQueued, store.BuyerHandedOff, d.Amount, time.Time{}); err != nil {
		m.log.Warn("could not record the handed-off buyer", "task", t.ID, "err", err)
	}
	who := buyer.String()
	if buyer.Server == types.DefaultUserServer {
		who = "+" + buyer.User
	}
	if name != "" {
		who = name + " (" + who + ")"
	}
	detail := fmt.Sprintf("handed the chat with %s to you; the bot sent them nothing. The offer asked INR %s.", who, d.Amount)
	if len(others) > 0 {
		detail += fmt.Sprintf(" %d other buyer(s) replied too.", len(others))
	}
	m.log.Info("sell task handed the sale to you", "task", t.ID, "buyer", d.Peer, "name", name, "asked", d.Amount)
	return detail, nil
}

// restore rebuilds the queue from the store and finds a buyer whose turn
// was under way. A buyer whose prompt was claimed is never prompted again.
func (m *sellManager) restore(ctx context.Context, t store.SellTask, queue *offerQueue) (*resumedTurn, error) {
	buyers, err := m.st.SellTaskBuyers(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	d, dstate, err := m.st.TaskDelivery(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	var resume *resumedTurn
	for _, b := range buyers {
		peer, err := types.ParseJID(b.Peer)
		if err != nil {
			continue
		}
		switch b.State {
		case store.BuyerQueued:
			queue.restore(peer, true)
			continue
		case store.BuyerOffered:
			if d != nil && d.Peer == b.Peer && d.Amount == b.Amount {
				// A payment sent while run was down arrives once it
				// reconnects, maybe after the buyer's deadline. Give them
				// a short grace period; nobody else has been asked yet.
				payUntil := b.PayUntil
				if grace := time.Now().Add(resumeGrace).Truncate(time.Second); payUntil.Before(grace) {
					payUntil = grace
				}
				resume = &resumedTurn{buyer: peer, d: *d, payUntil: payUntil}
				d = nil
			} else if _, err := m.st.MoveSellTaskBuyer(ctx, t.ID, b.Peer, store.BuyerOffered, store.BuyerTimedOut, "", time.Time{}); err != nil {
				return nil, err
			}
		}
		queue.restore(peer, false)
	}
	// Armed for a buyer who was never prompted: drop the reservation and
	// let them take their turn from the queue.
	if d != nil && dstate == "armed" {
		if err := m.st.ReleaseUnpaidDelivery(ctx, *d); err != nil {
			return nil, err
		}
	}
	return resume, nil
}

// taskJournal persists a task's queue so restarts don't repeat sends.
type taskJournal struct {
	st        *store.Store
	t         store.SellTask
	negotiate negotiator
}

func (j *taskJournal) price(ctx context.Context, buyer types.JID) (string, error) {
	p, err := j.negotiate(ctx, j.t, buyer)
	if err != nil {
		return "", err
	}
	if p < j.t.MinPaise {
		return "", fmt.Errorf("INR %s is below the floor of INR %s", store.FormatPaise(p), store.FormatPaise(j.t.MinPaise))
	}
	return store.FormatPaise(p), nil
}

func (j *taskJournal) arm(ctx context.Context, d store.Delivery) (time.Time, error) {
	return j.st.ArmTaskDelivery(ctx, j.t.ID, d)
}

func (j *taskJournal) prompting(ctx context.Context, buyer types.JID, amount string, payUntil time.Time) (bool, error) {
	ok, err := j.st.MoveSellTaskBuyer(ctx, j.t.ID, buyer.String(), store.BuyerQueued, store.BuyerOffered, amount, payUntil)
	if err == nil && !ok {
		err = errors.New("buyer was already prompted, or the task ended")
	}
	return ok, err
}

func (j *taskJournal) turnEnded(ctx context.Context, buyer types.JID, state string) error {
	from := store.BuyerOffered
	if state == store.BuyerSkipped {
		from = store.BuyerQueued
	}
	_, err := j.st.MoveSellTaskBuyer(ctx, j.t.ID, buyer.String(), from, state, "", time.Time{})
	return err
}

func (j *taskJournal) tellingSold(ctx context.Context, buyer types.JID) (bool, error) {
	return j.st.MoveSellTaskBuyer(ctx, j.t.ID, buyer.String(), store.BuyerQueued, store.BuyerToldSold, "", time.Time{})
}

// taskGate is the only way a task sends. It checks the rules on every
// message, whatever the sale engine or a future negotiator asks for.
type taskGate struct {
	m      *sellManager
	id     int64
	active atomic.Bool
}

func (g *taskGate) send(ctx context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	return g.deliver(ctx, false, to, msg, extra...)
}

// sendPaid sends the QR for a payment claimed while the task was active.
// It skips only the active check: a buyer who paid gets the QR even if the
// task was cancelled a moment later.
func (g *taskGate) sendPaid(ctx context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	if msg.GetImageMessage() == nil {
		return whatsmeow.SendResponse{}, errors.New("only the meal QR can be sent after a payment")
	}
	d, state, err := g.m.st.TaskDelivery(ctx, g.id)
	if err != nil {
		return whatsmeow.SendResponse{}, err
	}
	if d == nil || state != "sending" {
		return whatsmeow.SendResponse{}, errors.New("no claimed payment for this task")
	}
	return g.deliver(ctx, true, to, msg, extra...)
}

// react sends a ✅ to one of the meal's recorded offer posts. Like sendPaid
// it skips the active check, but only once the task's QR went out.
func (g *taskGate) react(ctx context.Context, chat types.JID, target, emoji string) (string, time.Time, error) {
	d, state, err := g.m.st.TaskDelivery(ctx, g.id)
	if err != nil {
		return "", time.Time{}, err
	}
	if d == nil || state != "sent" {
		return "", time.Time{}, errors.New("a task reacts to offer posts only after its QR went out")
	}
	posts, err := g.m.st.OfferPosts(ctx, d.Date, d.Meal)
	if err != nil {
		return "", time.Time{}, err
	}
	known := false
	for _, p := range posts {
		known = known || (p.Chat == chat.String() && p.ID == target)
	}
	if !known {
		return "", time.Time{}, errors.New("not one of this meal's recorded offer posts")
	}
	// No rate limit: there is one reaction per recorded post, after the
	// sale, and waiting behind the sale's own sends could time them out.
	// An empty sender marks the target as our own message.
	resp, err := g.m.send(ctx, chat, g.m.conn.BuildReaction(chat, types.EmptyJID, target, emoji))
	return resp.ID, resp.Timestamp, err
}

func (g *taskGate) deliver(ctx context.Context, paid bool, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	if !paid && !g.active.Load() {
		return whatsmeow.SendResponse{}, errTaskInactive
	}
	if err := g.allowed(ctx, to, extra); err != nil {
		return whatsmeow.SendResponse{}, err
	}
	if err := g.m.limits.wait(ctx, g.id); err != nil {
		return whatsmeow.SendResponse{}, err
	}
	if !paid && !g.active.Load() {
		return whatsmeow.SendResponse{}, errTaskInactive
	}
	return g.m.send(ctx, to, msg, extra...)
}

// allowed lets a task post only the group offer it claimed, and message
// only the buyers its offer queued: people who replied privately to it, or
// DMed about it with [sell] accept_plain_dms on.
func (g *taskGate) allowed(ctx context.Context, to types.JID, extra []whatsmeow.SendRequestExtra) error {
	to = to.ToNonAD()
	if to.Server == types.GroupServer {
		if len(extra) != 1 || extra[0].ID == "" {
			return errors.New("a group message must be the task's claimed offer post")
		}
		ok, err := g.m.st.OfferClaimed(ctx, g.id, to.String(), extra[0].ID)
		if err == nil && !ok {
			err = errors.New("this group post was not claimed by the task, or already went out")
		}
		return err
	}
	if to.Server != types.DefaultUserServer && to.Server != types.HiddenUserServer {
		return errors.New("a task can only message a group or a person")
	}
	peers := []string{to.String()}
	if to.Server == types.HiddenUserServer {
		if pn := g.m.conn.PNForLID(ctx, to); !pn.IsEmpty() {
			peers = append(peers, pn.String())
		}
	}
	ok, err := g.m.st.IsSellTaskBuyer(ctx, g.id, peers...)
	if err == nil && !ok {
		err = errors.New("recipient did not reply to the task's offer")
	}
	return err
}

// sendLimits allows taskSendsPerMinute per task and globalSendsPerMinute
// in all, over a sliding minute, and taskSendCap per task in total.
type sendLimits struct {
	mu     sync.Mutex
	global []time.Time
	tasks  map[int64][]time.Time
	totals map[int64]int
}

func newSendLimits() *sendLimits {
	return &sendLimits{tasks: map[int64][]time.Time{}, totals: map[int64]int{}}
}

// wait blocks until both limits have room, then records one send.
func (l *sendLimits) wait(ctx context.Context, task int64) error {
	for {
		l.mu.Lock()
		if l.totals[task] >= taskSendCap {
			l.mu.Unlock()
			return fmt.Errorf("sell task reached its limit of %d messages", taskSendCap)
		}
		now := time.Now()
		l.global = recent(l.global, now)
		l.tasks[task] = recent(l.tasks[task], now)
		delay := max(room(l.global, globalSendsPerMinute, now), room(l.tasks[task], taskSendsPerMinute, now))
		if delay == 0 {
			l.global = append(l.global, now)
			l.tasks[task] = append(l.tasks[task], now)
			l.totals[task]++
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// recent drops sends older than a minute.
func recent(sent []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(sent) && !sent[i].After(now.Add(-time.Minute)) {
		i++
	}
	return sent[i:]
}

// room is how long until another send fits under limit.
func room(sent []time.Time, limit int, now time.Time) time.Duration {
	if len(sent) < limit {
		return 0
	}
	return sent[len(sent)-limit].Add(time.Minute).Sub(now)
}
