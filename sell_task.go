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
//     privately to that post;
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

// sellOptions decides how a task posts its offer. The [sell] section of
// config.toml will set these; until it does, a task posts in the first
// configured group and leaves the price out, so buyers ask in a DM.
type sellOptions struct {
	Groups       []string // group JIDs or exact names
	MentionPrice bool
}

func sellOptionsFrom(cfg *config.Config) sellOptions {
	var o sellOptions
	if len(cfg.Groups) > 0 {
		o.Groups = cfg.Groups[:1]
	}
	return o
}

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
	connected func(time.Duration) bool, portalFor func(context.Context, int64) (mealPortal, error)) *sellManager {
	return &sellManager{
		cfg: cfg, log: log.With("component", "sell"), st: st, conn: conn, send: send, connected: connected,
		portalFor: portalFor, negotiate: openAtMax, limits: newSendLimits(), base: ctx, running: map[int64]*runningTask{},
	}
}

// Start checks the registration, records the task and starts selling in
// the background. Prices are whole rupees.
func (m *sellManager) Start(ctx context.Context, userID int64, date, meal string, minRupees, maxRupees int) (int64, error) {
	if minRupees <= 0 || maxRupees < minRupees || maxRupees > 10000 {
		return 0, errors.New("min_price must be at least 1 and max_price between min_price and 10000")
	}
	slot, err := findSlot(m.cfg, date, meal)
	if err != nil {
		return 0, err
	}
	if !time.Now().Before(slot.End) {
		return 0, errors.New("meal has already ended")
	}
	if len(sellOptionsFrom(m.cfg).Groups) == 0 {
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
		UserID: userID, AccountHash: s.accountHash, Mess: s.mess, Diet: s.diet, CreatedAt: time.Now(), EndsAt: slot.End}
	if t.ID, err = m.st.CreateSellTask(ctx, t); err != nil {
		return 0, err
	}
	t.State = store.TaskActive
	m.launch(t, s, nil)
	m.log.Info("sell task started", "task", t.ID, "date", date, "meal", meal, "min", minRupees, "max", maxRupees)
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
	return &sale{log: m.log, loc: m.cfg.Location(), conn: m.conn, st: m.st, date: date, meal: meal, end: end, label: meal}
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
		m.end(t, state, detail)
	}()
}

// outcome maps how runTask ended to the task's final state. An empty state
// means run is shutting down and the task resumes on the next start.
func (m *sellManager) outcome(ctx context.Context, err error) (string, string) {
	switch cause := context.Cause(ctx); {
	case err == nil:
		return store.TaskSold, "QR delivered after a matching payment"
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

// end releases an unpaid reservation and records the task's final state.
// A delivery left mid-send makes the task failed, since only the buyer's
// chat can tell whether the QR went out.
func (m *sellManager) end(t store.SellTask, state, detail string) {
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
		return
	}
	m.log.Info("sell task ended", "task", t.ID, "state", state, "detail", detail)
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
	for _, o := range offers {
		group, err := types.ParseJID(o.Group)
		if err != nil || o.State == store.OfferFailed {
			continue
		}
		stop := watchOfferReplies(ctx, m.conn, queue, []postedOffer{{group: group, id: o.ID, postedAt: o.PostedAt}})
		defer stop()
	}
	if resume == nil {
		listening()
	}
	if len(offers) == 0 {
		if !m.connected(30 * time.Second) {
			return errors.New("WhatsApp is not connected; the offer was not posted")
		}
		text := offerText(m.cfg, s.label, t.Date, store.FormatPaise(t.MaxPaise), sellOptionsFrom(m.cfg).MentionPrice)
		posted := 0
		for _, g := range sellOptionsFrom(m.cfg).Groups {
			stop, err := m.post(ctx, t, gate, queue, g, text)
			if err != nil {
				m.log.Warn("offer post failed", "task", t.ID, "group", g, "err", err)
				continue
			}
			defer stop()
			posted++
		}
		if posted == 0 {
			return errors.New("the offer could not be posted in any group")
		}
	}
	if resume != nil {
		resume.listening = listening
	}
	return s.sellToQueue(ctx, context.WithoutCancel(ctx), queue, j, 0, resume)
}

// post claims, watches and sends one group offer.
func (m *sellManager) post(ctx context.Context, t store.SellTask, gate *taskGate, queue *offerQueue, group, text string) (func(), error) {
	jid, name, err := findGroup(ctx, m.conn, group)
	if err != nil {
		return nil, err
	}
	o := postedOffer{group: jid, id: m.conn.GenerateMessageID(), postedAt: time.Now().Truncate(time.Second)}
	if err := m.st.ClaimOfferPost(ctx, t.ID, jid.String(), o.id, o.postedAt); err != nil {
		return nil, err
	}
	stop := watchOfferReplies(ctx, m.conn, queue, []postedOffer{o})
	if _, err := gate.send(ctx, jid, &waE2E.Message{Conversation: proto.String(text)}, whatsmeow.SendRequestExtra{ID: o.id}); err != nil {
		// A failed send may still have reached WhatsApp, so keep watching
		// for replies but never post again.
		m.st.SetOfferState(ctx, t.ID, o.id, store.OfferFailed)
		return stop, fmt.Errorf("group offer send failed or is uncertain: %w", err)
	}
	if err := m.st.SetOfferState(ctx, t.ID, o.id, store.OfferPosted); err != nil {
		m.log.Warn("could not record the offer post", "task", t.ID, "err", err)
	}
	m.log.Info("sell task offer posted", "task", t.ID, "group", name, "offer_id", o.id)
	return stop, nil
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
// only people who replied privately to that offer.
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
