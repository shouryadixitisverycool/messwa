package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/repost"
	"messwa/internal/store"
)

// Sell tasks sell one registered meal from inside run, on run's WhatsApp
// connection, so the board keeps working during a sale. They reuse the
// sale engine deliver-qr uses. The rules below are enforced here, not left
// to the agent that starts the task:
//
//   - nothing is sent unless the task is active;
//   - besides the group post, messages go only to people who replied
//     privately to that post, with [sell] accept_plain_dms DMed about it
//     (see plain_dm.go), or with [sell] message_buy_posts posted that they
//     want the meal (see buy_posts.go);
//   - with [sell] buyers = "unsaved_only", saved contacts never join the
//     queue and get nothing;
//   - no buyer is quoted, agreed with, asked, or armed for below the floor
//     price (see negotiate.go for how a task haggles);
//   - sends are rate-limited per task and across all tasks;
//   - a task ends when its meal ends.
//
// Every send is claimed in the store first, so a restart resumes a task
// without posting, prompting or saying "sorry sold" twice.

// Send limits. A sale needs the group post, a few negotiation messages per
// buyer, one prompt per paying buyer, the QR and a "sorry sold" to each
// buyer still waiting. Buyers message several sellers at once and leave
// within a minute, so a burst of replies needs quick quotes; these still
// stop a runaway loop.
const (
	taskSendsPerMinute   = 12
	globalSendsPerMinute = 15
	taskSendCap          = 80
)

// resumeGrace is how long a resumed buyer's turn lasts at least, so a
// payment sent while run was down still counts.
const resumeGrace = 30 * time.Second

var errTaskInactive = errors.New("sell task is not active; nothing was sent")
var errCancelledByUser = errors.New("cancelled")
var errSavedContact = errors.New(`recipient is a saved contact and [sell] buyers is "unsaved_only"`)

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

type sellManager struct {
	cfg  *config.Config
	log  *slog.Logger
	st   *store.Store
	conn saleConn
	// send is the raw WhatsApp send. Only taskGate calls it.
	send      sendFunc
	connected func(time.Duration) bool
	portalFor func(ctx context.Context, userID int64) (mealPortal, error)
	// prices reads asking prices for the negotiation's target; tests fake it.
	prices func(ctx context.Context, from, to string) ([]store.ServingPrices, error)
	limits *sendLimits
	// own writes the task's offer posts and ✅ into the group log, since
	// run never gets events for messages it sent itself.
	own ownGroupLog

	// notify tells the dashboard a sale changed. Nil when nothing watches.
	notify func()

	base    context.Context // run's context; tasks stop with it and resume next start
	mu      sync.Mutex
	running map[int64]*runningTask
	// dead are the posts of cancelled tasks whose meal hasn't ended, by
	// message ID, so a reply to one can go to a task still selling that
	// meal. See adoptTarget.
	dead map[string]deadPost
	// pricesMu serializes SetPrices.
	pricesMu sync.Mutex
}

type runningTask struct {
	task   store.SellTask
	cancel context.CancelCauseFunc
	done   chan struct{}
	gate   *taskGate
	// revoke is set by a cancel that deletes the task's posts once it ends.
	revoke atomic.Bool
	// Guarded by sellManager.mu. add queues a buyer you added, once the
	// task listens for replies; manual is a sale you recorded by hand.
	add    func(ctx context.Context, peer types.JID, name string) error
	manual *manualSale
	// neg is the task's negotiation once it runs, so a range edit reaches
	// it; nil for a task that doesn't negotiate.
	neg atomic.Pointer[negotiation]
}

func newSellManager(ctx context.Context, cfg *config.Config, log *slog.Logger, st *store.Store, conn saleConn, send sendFunc,
	connected func(time.Duration) bool, portalFor func(context.Context, int64) (mealPortal, error), own ownGroupLog) *sellManager {
	m := &sellManager{
		cfg: cfg, log: log.With("component", "sell"), st: st, conn: conn, send: send, connected: connected,
		portalFor: portalFor, prices: st.Prices, limits: newSendLimits(), own: own, base: ctx, running: map[int64]*runningTask{},
		dead: map[string]deadPost{},
	}
	if conn != nil {
		id := conn.AddEventHandler(m.lateReply)
		context.AfterFunc(ctx, func() { conn.RemoveEventHandler(id) })
	}
	return m
}

// lateWindow is how far back lateReply looks for a task the writer timed
// out of. Only tasks whose meal hasn't ended count anyway.
const lateWindow = 24 * time.Hour

// lateReply answers a buyer who timed out of a negotiating task and
// writes after the task stopped. A running task revives them itself (see
// negotiation.revive). Once the task sold the meal, they hear "sold sorry"
// once, the line everyone still talking to the bot got at the sale. After
// a cancelled, expired or failed task the bot stays quiet: the meal may
// not be gone, and you may be selling it by hand. Nothing is sent once the
// meal is over, or while the writer is a buyer in any active task, whose
// own session answers them.
func (m *sellManager) lateReply(evt any) {
	e, ok := evt.(*events.Message)
	if !ok || e.Info.IsFromMe || e.Info.IsGroup {
		return
	}
	if e.Info.Chat.Server != types.DefaultUserServer && e.Info.Chat.Server != types.HiddenUserServer {
		return
	}
	if strings.TrimSpace(messageText(e.Message)) == "" {
		return
	}
	go m.answerLate(e.Info)
}

func (m *sellManager) answerLate(info types.MessageInfo) {
	ctx, stop := context.WithTimeout(m.base, 2*time.Minute)
	defer stop()
	peer := buyerPeer(ctx, m.conn, info)
	late, active, err := m.st.TimedOutTasks(ctx, time.Now().Add(-lateWindow), peer.String(), info.Chat.ToNonAD().String())
	if err != nil {
		m.log.Warn("could not look up a late reply", "err", err)
		return
	}
	if active {
		return
	}
	for _, lb := range late {
		t := lb.Task
		if !negotiates(t) || !time.Now().Before(t.EndsAt) {
			// A fixed-price task never revives anyone, so its buyers
			// expect nothing after their turn lapsed.
			continue
		}
		ok, err := m.st.ToldSoldLate(ctx, t.ID, lb.Peer)
		if err != nil {
			m.log.Warn("could not claim the sold notice", "task", t.ID, "err", err)
		}
		if !ok {
			m.log.Info("buyer wrote after timing out of a task that ended unsold; leaving them to you", "task", t.ID, "buyer", lb.Peer, "state", t.State)
			return
		}
		to, err := types.ParseJID(lb.Peer)
		if err != nil {
			return
		}
		gate := &taskGate{m: m, id: t.ID}
		if err := sendReply(ctx, gate.sendSold, to, saySold()); err != nil {
			m.log.Warn("could not tell a late buyer the meal sold", "task", t.ID, "err", err)
			return
		}
		m.log.Info("told a buyer who wrote after timing out that the meal sold", "task", t.ID, "buyer", lb.Peer)
		m.event(ctx, store.SellEvent{TaskID: t.ID, Peer: lb.Peer, Kind: store.EventToldSold, Text: "wrote after timing out"})
		return
	}
}

// Start checks the registration, records the task and starts selling in
// the background. Prices are whole rupees; with max above min the task
// negotiates. handoff is "never" (or empty); "on_reply", which ends the
// task at the first private reply and hands that buyer's chat to you; or
// "on_agreement", which hands you the first buyer who agrees a price.
func (m *sellManager) Start(ctx context.Context, userID int64, date, meal string, minRupees, maxRupees int, handoff string) (int64, error) {
	if minRupees <= 0 || maxRupees < minRupees || maxRupees > 10000 {
		return 0, errors.New("min_price must be at least 1 and max_price between min_price and 10000")
	}
	h, err := config.ParseTaskHandoff(handoff)
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

// event records one step of a task's sale for the dashboard, then tells it.
// A failed write only loses the step from the dashboard, never the sale.
func (m *sellManager) event(ctx context.Context, e store.SellEvent) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if err := m.st.AddSellTaskEvent(context.WithoutCancel(ctx), e); err != nil {
		m.log.Warn("could not record a sale event", "task", e.TaskID, "kind", e.Kind, "err", err)
	}
	if m.notify != nil {
		m.notify()
	}
}

// saleEvents lets a task's sale record its steps: prompts, payments, the
// QR, and the buyers whose turn ended.
func (m *sellManager) saleEvents(id int64) func(buyer types.JID, kind string, n int64, text string) {
	return func(buyer types.JID, kind string, n int64, text string) {
		m.event(m.base, store.SellEvent{TaskID: id, Peer: buyer.String(), Kind: kind, N: n, Text: text})
	}
}

// Cancel stops a task. A QR already being delivered for a payment still
// goes out, and the task then ends as sold. A group post in flight is
// allowed to finish. With revoke, once the task has ended as cancelled its
// group posts are deleted for everyone (see revokeCopies); Cancel returns
// after that. Either way, replies to its posts can go to another task
// selling the same meal.
func (m *sellManager) Cancel(ctx context.Context, id int64, revoke bool) error {
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
		m.retire(*t)
		if m.end(*t, store.TaskCancelled, "cancelled") != store.TaskCancelled {
			m.forget(t.ID)
			return nil
		}
		if revoke {
			m.revokeCopies(*t)
		}
		return nil
	}
	// Replies that arrive while the task winds down already go elsewhere.
	m.retire(rt.task)
	rt.revoke.Store(revoke)
	rt.gate.active.Store(false)
	rt.cancel(errCancelledByUser)
	select {
	case <-rt.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetPrices changes a running task's floor and opening price, in whole
// rupees, without reposting or restarting it. Buyers mid-haggle get the new
// range on their next turn. Prices the bot already quoted or agreed stand
// even below a raised floor, and an ask is never raised. Whether the task
// negotiates can't change: a negotiating task keeps max above min, and a
// fixed-price one keeps them equal.
//
// When the floor drops, buyers who left the haggle with a last offer at or
// above the new floor are listed in needs_you, and a WhatsApp Pay payment
// of that offer from them gets the meal. The bot doesn't message them.
func (m *sellManager) SetPrices(ctx context.Context, id int64, minRupees, maxRupees int) error {
	if minRupees <= 0 || maxRupees < minRupees || maxRupees > 10000 {
		return errors.New("min_price must be at least 1 and max_price between min_price and 10000")
	}
	m.pricesMu.Lock()
	defer m.pricesMu.Unlock()
	t, err := m.st.GetSellTask(ctx, id)
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("no sell task %d", id)
	}
	if t.State != store.TaskActive {
		return fmt.Errorf("sell task %d is %s, not active", id, t.State)
	}
	switch {
	case negotiates(*t) && maxRupees == minRupees:
		return errors.New("this task negotiates, so max_price must stay above min_price; cancel it and start a fixed-price task instead")
	case !negotiates(*t) && t.Handoff != string(config.HandoffOnReply) && maxRupees != minRupees:
		return errors.New("this task asks one fixed price, so min_price and max_price must be equal; cancel it and start a negotiating task instead")
	}
	minPaise, maxPaise := int64(minRupees)*100, int64(maxRupees)*100
	if minPaise == t.MinPaise && maxPaise == t.MaxPaise {
		return nil
	}
	old, err := m.st.SetSellTaskPrices(ctx, id, minPaise, maxPaise)
	if err != nil {
		return err
	}
	m.log.Info("sell task prices changed", "task", id, "min", minRupees, "max", maxRupees,
		"was_min", old.MinPaise/100, "was_max", old.MaxPaise/100)
	m.event(ctx, store.SellEvent{TaskID: id, Kind: store.EventPrices, N: minPaise,
		Text: fmt.Sprintf("%s-%s was %s-%s", store.FormatPaise(minPaise), store.FormatPaise(maxPaise),
			store.FormatPaise(old.MinPaise), store.FormatPaise(old.MaxPaise))})
	m.mu.Lock()
	rt := m.running[id]
	m.mu.Unlock()
	if rt != nil {
		if n := rt.neg.Load(); n != nil {
			if err := n.refreshRange(ctx); err != nil {
				m.log.Warn("the running negotiation did not take the new prices", "task", id, "err", err)
			}
		}
	}
	if minPaise < old.MinPaise {
		m.flagOffersAtFloor(ctx, id, minPaise)
	}
	return nil
}

// flagOffersAtFloor lists in needs_you the buyers whose last offer the new,
// lower floor now meets but who are no longer haggling: they timed out,
// passed, or were parked. Their earlier note stays in the event log.
func (m *sellManager) flagOffersAtFloor(ctx context.Context, id, floor int64) {
	buyers, err := m.st.SellTaskBuyers(ctx, id)
	if err != nil {
		m.log.Warn("could not look for buyers the new floor meets", "task", id, "err", err)
		return
	}
	for _, b := range buyers {
		if !offerMeetsFloor(b, floor) {
			continue
		}
		offer := strings.TrimSuffix(store.FormatPaise(b.LastOffer), ".00")
		note := fmt.Sprintf("offered %s, which the new floor of %s now meets; message them if you want to sell at %s. A WhatsApp Pay payment of %s from them gets the QR. If a timed-out buyer writes again, the bot haggles with them at the new range",
			offer, strings.TrimSuffix(store.FormatPaise(floor), ".00"), offer, offer)
		if err := m.st.ParkSellTaskBuyer(ctx, id, b.Peer, note); err != nil {
			m.log.Warn("could not list a buyer the new floor meets", "task", id, "buyer", b.Peer, "err", err)
			continue
		}
		m.event(ctx, store.SellEvent{TaskID: id, Peer: b.Peer, Kind: store.EventParked, N: b.LastOffer, Text: note})
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
		if t.State == store.TaskCancelled && time.Now().Before(t.EndsAt) {
			m.retire(t)
		}
		if t.State != store.TaskActive {
			continue
		}
		switch {
		case !enabled:
			m.end(t, store.TaskCancelled, "sell tasks are turned off in settings")
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

// repostRules are the repost settings as saved now. Running tasks read them
// every round, and so does sell_task_status, so both agree.
func (m *sellManager) repostRules() repost.Rules { return repost.FromConfig(m.cfg.Live().Sell) }

func (m *sellManager) newSale(date, meal string, end time.Time) *sale {
	return &sale{log: m.log, loc: m.cfg.Location(), conn: m.conn, st: m.st, date: date, meal: meal, end: end}
}

func (m *sellManager) launch(t store.SellTask, s *sale, ready chan struct{}) {
	ctx, cancel := context.WithCancelCause(m.base)
	ctx, stop := context.WithDeadline(ctx, t.EndsAt)
	gate := &taskGate{m: m, id: t.ID}
	gate.active.Store(true)
	rt := &runningTask{task: t, cancel: cancel, done: make(chan struct{}), gate: gate}
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
		err := m.runTask(ctx, t, s, rt, signal)
		if errors.Is(context.Cause(ctx), errSoldByYou) {
			err = m.finishManualSale(ctx, t, gate)
		}
		gate.active.Store(false)
		state, detail := m.outcome(ctx, err)
		if state == store.TaskSold {
			if kind, err := m.st.PaymentKind(context.WithoutCancel(ctx), t.Date, t.Meal); err == nil && kind == store.PaidByImage {
				detail = "QR sent for an image the buyer sent after the payment prompt; the payment is NOT verified, check your UPI app"
			}
		}
		if state == "" {
			m.log.Info("sell task paused until run restarts", "task", t.ID)
			return
		}
		switch final := m.end(t, state, detail); {
		case final == store.TaskSold:
			m.forget(t.ID)
			m.tickOffers(t, gate)
		case final == store.TaskCancelled:
			// Posts claimed since Cancel looked count too.
			m.retire(t)
			if rt.revoke.Load() {
				m.revokeCopies(t)
			}
		default:
			m.forget(t.ID)
		}
	}()
}

// outcome maps how runTask ended to the task's final state. An empty state
// means run is shutting down and the task resumes on the next start.
func (m *sellManager) outcome(ctx context.Context, err error) (string, string) {
	var handedOff handedOffError
	var manual manualSoldError
	switch cause := context.Cause(ctx); {
	case err == nil:
		return store.TaskSold, "QR delivered after a matching payment"
	case errors.As(err, &manual):
		return store.TaskSold, manual.detail
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
			state = store.TaskFailed
			detail = "a payment was claimed but the QR send is uncertain; check the buyer's chat: " + detail
		case d != nil && dstate == "sent":
			state, detail = store.TaskSold, "QR delivered after a matching payment"
		case d != nil && dstate == "handed_off" && state == store.TaskExpired:
			// A task that kept selling after a handoff ran out the meal.
			state, detail = store.TaskHandedOff, fmt.Sprintf("the meal ended while %s was handed to you at INR %s", describeBuyer(d.Peer, ""), strings.TrimSuffix(d.Amount, ".00"))
		}
	}
	if _, err := m.st.EndSellTask(ctx, t.ID, state, detail, time.Now()); err != nil {
		m.log.Error("end sell task", "task", t.ID, "err", err)
		return ""
	}
	m.log.Info("sell task ended", "task", t.ID, "state", state, "detail", detail)
	m.event(ctx, store.SellEvent{TaskID: t.ID, Kind: store.EventEnded, Text: state})
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
	done, _, err := reactSold(ctx, m.st, m.own, gate.react, m.log, t.Date, t.Meal)
	if err != nil {
		m.log.Warn("could not read the offer posts to mark them sold", "task", t.ID, "err", err)
	}
	if done > 0 {
		m.event(ctx, store.SellEvent{TaskID: t.ID, Kind: store.EventTicked, N: int64(done)})
	}
}

// runTask posts the offer (unless it already went out) and sells to the
// buyers who reply, picking up where a previous run stopped. listening is
// called once replies and any resumed payment are being watched.
func (m *sellManager) runTask(ctx context.Context, t store.SellTask, s *sale, rt *runningTask, listening func()) error {
	gate := rt.gate
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
	s.muted = gate.muted
	s.event = m.saleEvents(t.ID)
	j := &taskJournal{st: m.st, t: t}
	settings, err := taskSellSettings(m.cfg)
	if err != nil {
		return err
	}
	s.acceptImages = settings.AcceptPaymentImages
	queue := newOfferQueue()
	queue.join = func(peer, chat types.JID) bool {
		if gate.turnsAway(ctx, peer, chat) {
			logSavedSkip(m.log, peer, "task", t.ID)
			return false
		}
		added, err := m.st.AddSellTaskBuyer(ctx, t.ID, peer.String(), chat.String(), time.Now())
		if err != nil {
			m.log.Error("record sell task buyer", "task", t.ID, "err", err)
		}
		if added {
			m.event(ctx, store.SellEvent{TaskID: t.ID, Peer: peer.String(), Kind: store.EventJoined})
		}
		return added
	}
	var n *negotiation
	if negotiates(t) {
		n = newNegotiation(ctx, m, t, s, gate, queue)
		rt.neg.Store(n)
		// A range edit made while the task was starting reached the store
		// but not n.
		if err := n.refreshRange(ctx); err != nil {
			return err
		}
	}
	m.acceptAdded(t, gate, queue)
	resume, err := m.restore(ctx, t, queue, gate, n)
	if err != nil {
		return err
	}
	offers, err := m.st.SellTaskOffers(ctx, t.ID)
	if err != nil {
		return err
	}
	posting := watchGroupOffer(ctx, m.conn, queue)
	defer posting.close()
	posting.adoptReplies(m.adopter(t))
	if settings.AcceptPlainDMs {
		posting.acceptPlainDMs(newPlainDMs(m.cfg, m.st, m.log.With("task", t.ID), s.mess, baseMeal(t.Meal), ""))
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
	if t.Handoff == string(config.HandoffOnReply) || t.Handoff == string(config.HandoffOnAgreement) {
		// A negotiating task that hands off on agreement keeps posting and
		// haggling, so a higher offer can still take the handoff.
		s.holdHandoff = n != nil && t.Handoff == string(config.HandoffOnAgreement)
		s.handoff = func(ctx context.Context, buyer types.JID, d store.Delivery, others []types.JID) error {
			if !s.holdHandoff {
				posting.stop()
			}
			var err error
			detail, err = m.handOff(ctx, t, buyer, d, queue.name(buyer), others)
			return err
		}
	}
	if m.buyPostsWanted(t) {
		go m.messageBuyPosts(ctx, t, s, gate, queue)
	}
	if n != nil {
		err = m.sellNegotiated(ctx, n, s, j, resume)
	} else {
		err = s.sellToQueue(ctx, context.WithoutCancel(ctx), queue, j, 0, resume)
	}
	if errors.Is(err, errHandedOff) {
		return handedOffError{detail: detail}
	}
	return err
}

// postCopies posts the offer to every [sell] group that has no copy from
// this task yet, post_gap apart, in the background, then keeps the copies
// in view with reposts. When no copy went out before, it waits for the
// first one and fails if none posts.
func (m *sellManager) postCopies(ctx context.Context, t store.SellTask, s *sale, gate *taskGate, posting *groupOffer, settings sellSettings, claimed map[string]bool) error {
	first := len(claimed) == 0
	var groups []offerTarget
	var listed bool
	// targets are the [sell] groups we're in, listed once.
	targets := func(ctx context.Context) ([]offerTarget, error) {
		if listed {
			return groups, nil
		}
		joined, err := m.conn.GetJoinedGroups(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range settings.Groups {
			target, err := pickGroup(joined, g)
			if err != nil {
				m.log.Warn("not posting the offer to this group", "task", t.ID, "group", g, "err", err)
				continue
			}
			groups = append(groups, target)
		}
		listed = true
		return groups, nil
	}
	extras := sync.OnceValue(func() []string {
		// Extras typed in for someone else's meal are meant to be posted.
		if ep, ok := s.portal.(extrasPortal); ok && (settings.MentionExtras || t.Meal != baseMeal(t.Meal)) {
			return readExtras(ctx, ep, m.log, t.Date, t.Meal, s.mess)
		}
		return nil
	})
	// Each copy shows the opening price as it is now, after any range edit.
	text := func() string {
		price := t.MaxPaise
		if cur, err := m.st.GetSellTask(ctx, t.ID); err == nil && cur != nil {
			price = cur.MaxPaise
		}
		return offerText(offerDetails{Mess: s.mess, Diet: s.diet, Meal: t.Meal, Price: rupees(store.FormatPaise(price)),
			ShowPrice: settings.ShowPrice, Day: offerDay(m.cfg, t.Date), Extras: extras()})
	}
	send := m.taskCopy(t, gate, text)
	record := func(p postedOffer) { recordOffers(ctx, m.st, m.own, m.log, t.Date, t.Meal, []postedOffer{p}) }
	keepInView := func() {
		posting.keepInView(ctx, &reposter{
			log: m.log.With("task", t.ID), st: m.st, live: m.repostRules, end: t.EndsAt, gap: settings.PostGap,
			groups: targets,
			copies: func(ctx context.Context) ([]repost.Copy, error) {
				offers, err := m.st.SellTaskOffers(ctx, t.ID)
				return storedCopies(offers), err
			},
			send: send, record: record,
			room: func() bool { return m.limits.left(t.ID) > repostReserve },
		})
	}
	if !m.connected(30 * time.Second) {
		if first {
			return errors.New("WhatsApp is not connected; the offer was not posted")
		}
		m.log.Warn("WhatsApp is not connected; posting no further copies for now", "task", t.ID)
		keepInView()
		return nil
	}
	all, err := targets(ctx)
	if err != nil {
		if first {
			return err
		}
		m.log.Warn("could not list groups; posting no further copies for now", "task", t.ID, "err", err)
		keepInView()
		return nil
	}
	var fresh []offerTarget
	for _, target := range all {
		if !claimed[target.JID.String()] {
			fresh = append(fresh, target)
		}
	}
	if len(fresh) == 0 {
		if first {
			return errors.New("the offer could not be posted in any group")
		}
		keepInView()
		return nil
	}
	var delay time.Duration
	if !first {
		delay = settings.PostGap
	}
	posting.post(ctx, m.log, fresh, delay, settings.PostGap, send, record)
	keepInView()
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
func (m *sellManager) taskCopy(t store.SellTask, gate *taskGate, text func() string) sendCopy {
	return func(ctx context.Context, o *groupOffer, g offerTarget, seq int) (postedOffer, error) {
		p := postedOffer{Chat: g.JID, GroupName: g.Name, ID: m.conn.GenerateMessageID(), At: time.Now().Truncate(time.Second), Text: text()}
		if err := m.st.ClaimOfferPost(ctx, t.ID, g.JID.String(), p.ID, seq, p.At); err != nil {
			return p, err
		}
		o.watch(p)
		resp, err := gate.send(ctx, g.JID, &waE2E.Message{Conversation: proto.String(p.Text)}, whatsmeow.SendRequestExtra{ID: p.ID})
		// The copy's state must be written even if posting stopped mid-send.
		stateCtx := context.WithoutCancel(ctx)
		if err != nil {
			// It may still have reached WhatsApp, so replies keep counting,
			// but it is never sent again.
			if serr := m.st.SetOfferFailed(stateCtx, t.ID, p.ID, err.Error()); serr != nil {
				m.log.Warn("could not record the failed offer post", "task", t.ID, "err", serr)
			}
			m.event(stateCtx, store.SellEvent{TaskID: t.ID, Peer: g.JID.String(), Kind: store.EventPostFailed, Text: err.Error()})
			return p, fmt.Errorf("group offer send failed or is uncertain: %w", err)
		}
		if err := m.st.SetOfferState(stateCtx, t.ID, p.ID, store.OfferPosted); err != nil {
			m.log.Warn("could not record the offer post", "task", t.ID, "err", err)
		}
		m.event(stateCtx, store.SellEvent{TaskID: t.ID, Peer: g.JID.String(), Kind: store.EventPosted})
		p.At = firstTime(resp.Timestamp, p.At)
		m.log.Info("sell task offer posted", "task", t.ID, "group", g.Name, "offer_id", p.ID, "copy", seq)
		return p, nil
	}
}

// handOff records that the task hands buyer to you and returns what
// sell_task_status shows about it. The bot has sent the buyer nothing.
func (m *sellManager) handOff(ctx context.Context, t store.SellTask, buyer types.JID, d store.Delivery, name string, others []types.JID) (string, error) {
	if err := m.st.HandOffTaskDelivery(ctx, t.ID, d, name); err != nil {
		return "", err
	}
	if _, err := m.st.MoveSellTaskBuyerFrom(ctx, t.ID, buyer.String(), []string{store.BuyerQueued, store.BuyerAgreed}, store.BuyerHandedOff); err != nil {
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
	if t.Handoff == string(config.HandoffOnAgreement) {
		detail = fmt.Sprintf("handed the chat with %s to you. %s", who, describeAgreement(d.Amount))
	}
	if len(others) > 0 {
		detail += fmt.Sprintf(" %d other buyer(s) replied too.", len(others))
	}
	m.log.Info("sell task handed the sale to you", "task", t.ID, "buyer", d.Peer, "name", name, "asked", d.Amount)
	m.event(ctx, store.SellEvent{TaskID: t.ID, Peer: buyer.String(), Kind: store.EventHandedOff, N: paise(d.Amount), Text: detail})
	return detail, nil
}

// restore rebuilds the queue from the store and finds a buyer whose turn
// was under way. A buyer whose prompt was claimed is never prompted again.
// With n, buyers who were negotiating or waiting to pay get their sessions
// back. A queued buyer the buyers setting now turns away is skipped.
func (m *sellManager) restore(ctx context.Context, t store.SellTask, queue *offerQueue, gate *taskGate, n *negotiation) (*resumedTurn, error) {
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
			alt, _ := types.ParseJID(b.Alt)
			if !gate.turnsAway(ctx, peer, alt) {
				queue.restore(peer, true)
				continue
			}
			logSavedSkip(m.log, peer, "task", t.ID)
			if _, err := m.st.MoveSellTaskBuyer(ctx, t.ID, b.Peer, store.BuyerQueued, store.BuyerSkipped, "", time.Time{}); err != nil {
				return nil, err
			}
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
				if n != nil {
					n.restoreBuyer(b, true)
				}
			} else if _, err := m.st.MoveSellTaskBuyer(ctx, t.ID, b.Peer, store.BuyerOffered, store.BuyerTimedOut, "", time.Time{}); err != nil {
				return nil, err
			} else if n != nil {
				n.restoreBuyer(b, false) // ended, with their haggle
			}
		case store.BuyerHandedOff:
			// A task that keeps selling after a handoff: the buyer still
			// holds it.
			if n != nil && d != nil && dstate == "handed_off" && d.Peer == b.Peer {
				resume = &resumedTurn{buyer: peer, d: *d, handedOff: true}
				d = nil
				n.restoreBuyer(b, true)
			}
		case store.BuyerNegotiating, store.BuyerAgreed, store.BuyerTimedOut:
			// A timed-out buyer comes back ended, ready to be revived by
			// a late reply.
			if n != nil {
				n.restoreBuyer(b, false)
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
	st *store.Store
	t  store.SellTask
}

// price is the task's max price as it is now, after any range edit: a task
// that doesn't negotiate asks it of every buyer. A negotiating task uses
// negotiatedJournal instead.
func (j *taskJournal) price(ctx context.Context, buyer types.JID) (string, error) {
	if j.muted(ctx, buyer) {
		return "", errBuyerMuted
	}
	t, err := j.st.GetSellTask(ctx, j.t.ID)
	if err != nil || t == nil {
		return "", errors.Join(err, errors.New("sell task is gone"))
	}
	if t.MaxPaise < t.MinPaise {
		return "", fmt.Errorf("INR %s is below the floor of INR %s", store.FormatPaise(t.MaxPaise), store.FormatPaise(t.MinPaise))
	}
	return store.FormatPaise(t.MaxPaise), nil
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

func (j *taskJournal) claimLine(ctx context.Context, buyer types.JID, kind string) (bool, error) {
	return j.st.ClaimBuyerLine(ctx, j.t.ID, buyer.String(), kind)
}

// tellingSold claims "sorry sold" for a buyer still in the running. A
// muted buyer is never claimed.
func (j *taskJournal) tellingSold(ctx context.Context, buyer types.JID) (bool, error) {
	return j.st.ClaimSoldNotice(ctx, j.t.ID, buyer.String())
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

// sendSold tells one of the task's buyers that the meal sold. Like
// sendPaid it skips the active check, and only after the task sold the
// meal; the message must be one of saySold's lines.
func (g *taskGate) sendSold(ctx context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	if !slices.Contains(soldLines, msg.GetConversation()) {
		return whatsmeow.SendResponse{}, errors.New("an ended task may only say the meal sold")
	}
	t, err := g.m.st.GetSellTask(ctx, g.id)
	if err != nil {
		return whatsmeow.SendResponse{}, err
	}
	_, state, err := g.m.st.TaskDelivery(ctx, g.id)
	if err != nil {
		return whatsmeow.SendResponse{}, err
	}
	if t == nil || (t.State != store.TaskSold && state != "sent") {
		return whatsmeow.SendResponse{}, errors.New("the task did not sell the meal")
	}
	if err := g.allowed(ctx, to, extra); err != nil {
		return whatsmeow.SendResponse{}, err
	}
	if g.turnsAway(ctx, to) {
		return whatsmeow.SendResponse{}, errSavedContact
	}
	if err := g.m.limits.wait(ctx, g.id); err != nil {
		return whatsmeow.SendResponse{}, err
	}
	return g.m.send(ctx, to, msg, extra...)
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

// turnsAway is the [sell] buyers check for this task. Every way a buyer
// joins the queue calls it, and so does every DM the task sends.
func (g *taskGate) turnsAway(ctx context.Context, peers ...types.JID) bool {
	return turnsAway(ctx, g.m.conn, g.m.cfg.Live().Sell.Buyers, peers...)
}

func (g *taskGate) deliver(ctx context.Context, paid bool, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	if !paid && !g.active.Load() {
		return whatsmeow.SendResponse{}, errTaskInactive
	}
	if err := g.allowed(ctx, to, extra); err != nil {
		return whatsmeow.SendResponse{}, err
	}
	// A buyer who already paid gets the QR, even if the setting changed
	// across a restart during their turn.
	if !paid && to.Server != types.GroupServer && g.turnsAway(ctx, to) {
		return whatsmeow.SendResponse{}, errSavedContact
	}
	// A muted buyer receives no task messages.
	if to.Server != types.GroupServer && g.muted(ctx, to) {
		return whatsmeow.SendResponse{}, errBuyerMuted
	}
	if err := g.m.limits.wait(ctx, g.id); err != nil {
		return whatsmeow.SendResponse{}, err
	}
	if !paid && !g.active.Load() {
		return whatsmeow.SendResponse{}, errTaskInactive
	}
	if to.Server == types.GroupServer {
		// A cancel must not abort a group post halfway: whatsmeow may
		// have sent it to some members already, and a revoke needs to
		// know it went out. The post finishes or times out, and a
		// cancelled task revokes it with the rest.
		sendCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), groupPostTimeout)
		defer stop()
		return g.m.send(sendCtx, to, msg, extra...)
	}
	if to.Server != types.GroupServer && g.muted(ctx, to) {
		return whatsmeow.SendResponse{}, errBuyerMuted
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

// left is how many more messages the task may send in total.
func (l *sendLimits) left(task int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return taskSendCap - l.totals[task]
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
