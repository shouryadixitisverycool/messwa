package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"messwa/internal/config"
	"messwa/internal/market"
	"messwa/internal/store"
)

// A sell task with max_price above min_price negotiates. Every buyer who
// replies to a post gets their own session at once: the bot quotes, reads
// each reply with the fixed rules in negotiate_parse.go and answers from
// templates. A buyer who agrees joins the pay queue, and buyers pay one at
// a time through the same turns as before (sellToQueue), so two of them
// never both think they can pay. Once a buyer holds the turn (and no other
// meal in the pool is free), the listing is reserved for them: other
// sessions pause without quoting, nudging or timing out, and nobody can
// outbid the active holder. If the holder pays, waiting buyers hear "sold
// sorry"; if the turn lapses or the holder backs out, paused sessions
// resume cleanly. Whatever a session decides, the store refuses to quote,
// agree or arm below the task's floor.

const (
	maxSessions   = 10              // buyers negotiating at once
	maxCounters   = 2               // counter-offers before "the lowest I can do"
	maxBotMsgs    = 6               // replies a session sends one buyer; a two-message reply counts once
	replyDebounce = 2 * time.Second // a burst of messages gets one reply
	sessionLimit  = config.NegotiationLimit
	paidGrace     = 60 * time.Second // "paid" with no WhatsApp Pay payment yet
	// screenshotGrace replaces paidGrace when screenshots count as payment,
	// so a buyer asked for one has time to take and send it.
	screenshotGrace = 3 * time.Minute
	maxOfferFactor  = 3 // an offer above 3x max_price goes to you instead of being agreed
)

// nudgeAfter is how long a buyer may stay quiet after the bot's first
// message before it asks "yes or no?", and again after that before it
// drops the price once and times them out. A reply to either revives them.
var nudgeAfter = time.Minute

var errSoldDirect = errors.New("a buyer paid before their turn and got the meal")

// haggle prices one task's negotiation, in whole rupees. It has no I/O.
type haggle struct {
	min, max int
	// target is what the meal usually goes for: the median ask, clamped
	// to [min, max]. The bot holds there until time runs short.
	target int
	// median is the market's median ask, kept so a range edit can clamp
	// it again; haveMedian is false when the market had none.
	median     float64
	haveMedian bool
	// smart is [sell] smart_haggle. Off, the target is max_price and the
	// bot takes anything down to min at any time.
	smart bool
	unit  int // concessions round to this: ₹5, or ₹1 for tiny prices
	end   time.Time
	meal  string // "lunch"; a buyer who names only other meals is out
	// l says the replies. The session adds the buyer's name before decide.
	l lines
}

func newHaggle(minRupees, maxRupees int, median float64, haveMedian bool, smart bool, end time.Time, l lines) haggle {
	h := haggle{end: end, l: l, median: median, haveMedian: haveMedian, smart: smart}
	return h.withRange(minRupees, maxRupees)
}

// withSmart is h with smart haggle on or off, and the target worked out
// again for it.
func (h haggle) withSmart(on bool) haggle {
	if h.smart == on {
		return h
	}
	h.smart = on
	return h.withRange(h.min, h.max)
}

// withRange is h with a new floor and opening price, and the target and
// price unit worked out again for them.
func (h haggle) withRange(minRupees, maxRupees int) haggle {
	h.min, h.max, h.target, h.unit = minRupees, maxRupees, maxRupees, market.PriceUnit(maxRupees)
	if h.smart && h.haveMedian {
		h.target = min(max(int(math.Round(h.median)), minRupees), maxRupees)
	}
	return h
}

// forMeal is h selling meal.
func (h haggle) forMeal(meal string) haggle {
	h.meal = meal
	return h
}

func (h haggle) round(x float64) int {
	return int(math.Round(x/float64(h.unit))) * h.unit
}

// walk is the lowest price the bot accepts at now; see market.Walk. Without
// smart haggle it is the floor.
func (h haggle) walk(now time.Time, competing bool) int {
	if !h.smart {
		return h.min
	}
	return market.Walk(h.min, h.target, h.unit, h.end, now, competing)
}

// haggleState is one buyer's side of the negotiation, in rupees.
type haggleState struct {
	ask, rounds, botMsgs, unknowns, lastOffer int
	quoted                                    []int
	saidLowest                                bool
	passCountered                             bool // retained for stored sessions from older versions
	toldMeal                                  bool // the buyer named another meal and heard which one this is
	nudges                                    int  // see quietNudge
}

func (s haggleState) store(started time.Time) store.Negotiation {
	n := store.Negotiation{Ask: int64(s.ask) * 100, Rounds: s.rounds, BotMsgs: s.botMsgs, Unknowns: s.unknowns,
		LastOffer: int64(s.lastOffer) * 100, SaidLowest: s.saidLowest, PassCountered: s.passCountered, ToldMeal: s.toldMeal, Nudges: s.nudges, StartedAt: started}
	for _, q := range s.quoted {
		n.Quoted = append(n.Quoted, int64(q)*100)
	}
	return n
}

func haggleFromStore(n store.Negotiation, maxRupees int) haggleState {
	s := haggleState{ask: int(n.Ask / 100), rounds: n.Rounds, botMsgs: n.BotMsgs, unknowns: n.Unknowns,
		lastOffer: int(n.LastOffer / 100), saidLowest: n.SaidLowest, passCountered: n.PassCountered, toldMeal: n.ToldMeal, nudges: n.Nudges}
	if s.ask == 0 {
		s.ask = maxRupees
	}
	for _, q := range n.Quoted {
		s.quoted = append(s.quoted, int(q/100))
	}
	return s
}

// step is what one burst of buyer messages leads to.
type step struct {
	next      haggleState
	reply     reply  // to send; empty sends nothing
	agree     int    // > 0: agreed at this price
	pass      bool   // the buyer is out
	why       string // for a pass the bot reads into the message, why
	park      string // non-empty: stop and hand the buyer to you, saying why
	paidClaim bool   // the buyer says they paid
}

// quietDrop is the lower price a buyer hears after staying quiet through
// the bot's first message and the nudge. Above the target it is the target;
// otherwise it is halfway to the floor, at least one unit lower. It reports
// false when the ask is already at the floor or was called the lowest.
func (h haggle) quietDrop(st haggleState) (int, bool) {
	a := min(st.ask, h.max)
	if a <= h.min || st.saidLowest {
		return 0, false
	}
	if a > h.target {
		return h.target, true
	}
	return max(h.min, min(a-h.unit, h.round(float64(a+h.min)/2))), true
}

// decide answers in, given st. The buyer has heard a price once the bot
// has sent them anything.
//
// After a range edit, a buyer's ask stays where it was unless the new max
// is lower: the bot never asks more than it already said, and a raised
// floor only stops it going lower than that ask.
func (h haggle) decide(st haggleState, in intent, now time.Time, competing bool) step {
	priced := st.botMsgs > 0
	a := min(st.ask, h.max)
	if !priced {
		a = h.max
	}
	w := min(h.walk(now, competing), a)
	next := st
	next.quoted = slices.Clone(st.quoted)
	say := func(price int, text reply) step {
		if day := h.l.today(); !priced && day != "" && in.kind == intentOffer && !strings.Contains(strings.Join(text, "\n"), day) {
			// A counter as the first price: still say which day.
			text = append(text[:len(text)-1:len(text)-1], text[len(text)-1]+", its for "+day)
		}
		next.ask = price
		if !slices.Contains(next.quoted, price) {
			next.quoted = append(next.quoted, price)
		}
		return step{next: next, reply: text}
	}
	opening := func() step {
		return say(a, h.l.opening(a))
	}
	lowest := func() step {
		next.saidLowest = true
		return say(w, h.l.lowest(w, a))
	}
	counter := func(k int) step {
		if k <= w {
			return lowest()
		}
		next.rounds++
		return say(k, h.l.counter(k, a, next.rounds))
	}
	agree := func(price int) step {
		next.ask = price
		return step{next: next, agree: price}
	}

	if in.kind != intentPass && in.kind != intentPaid && in.kind != intentAccept && in.wantsOtherMeal(h.meal) {
		// "lunch?" to a dinner seller: they may have misread the post, so
		// say which meal this is once. Asking for another meal again ends
		// it; no price changes that.
		if st.toldMeal {
			return step{next: st, pass: true, why: "wants another meal"}
		}
		next.toldMeal = true
		return say(a, h.l.otherMeal(a, priced))
	}
	switch in.kind {
	case intentPass:
		if in.bought {
			return step{next: st, pass: true, why: "bought elsewhere"}
		}
		if len(in.meals) > 0 {
			// "dont need dinner": they turned down the meal, not the price.
			return step{next: st, pass: true, why: "doesn't want this meal"}
		}
		return step{next: st, pass: true}
	case intentPaid:
		return step{next: st, paidClaim: true}
	case intentPayOther:
		return step{next: st, park: "wants to pay some way other than WhatsApp Pay"}
	case intentAccept:
		return agree(a)
	case intentOK:
		if priced {
			return agree(a)
		}
		return opening()
	case intentOffer:
		c := in.offer
		next.lastOffer = c
		switch {
		case c > maxOfferFactor*h.max:
			return step{next: next, park: fmt.Sprintf("offered ₹%d, more than %dx the ₹%d max price", c, maxOfferFactor, h.max)}
		case c >= w:
			// Take the offer as made, including one above the ask.
			return agree(c)
		case st.saidLowest:
			return step{next: next, park: fmt.Sprintf("offered ₹%d after the bot's lowest of ₹%d", c, a)}
		case in.firm || st.rounds >= maxCounters:
			return lowest()
		}
		return counter(max(w, min(a-h.unit, h.round(float64(a+c)/2))))
	case intentLower:
		switch {
		case !priced:
			return opening()
		case a <= w && st.saidLowest:
			return step{next: next, park: fmt.Sprintf("still wants it below the bot's lowest of ₹%d", a)}
		case a <= w, st.rounds >= maxCounters:
			return lowest()
		}
		return counter(max(w, a-max(h.unit, h.round(float64(a-w)/2))))
	case intentOpener, intentQuestion:
		if !priced {
			return opening()
		}
		return say(a, h.l.which(a))
	}
	if !priced {
		return opening()
	}
	if st.unknowns > 0 {
		return step{next: next, park: "sent messages the bot couldn't read"}
	}
	next.unknowns++
	return say(a, h.l.restate(a))
}

// Session states, in memory. The store keeps the same story in
// sell_task_buyers.
const (
	sessionWaiting     = ""
	sessionNegotiating = "negotiating"
	sessionAgreed      = "agreed"
	sessionEnded       = "ended"
)

type buyerMsg struct{ id, text string }

// session is one buyer's negotiation.
type session struct {
	n       *negotiation
	buyer   types.JID
	inbox   chan buyerMsg
	firstID string // the reply that queued them; it may also reach inbox
	first   string // the text to answer first when a revived session starts
	// heard is set once the buyer writes after the session started, which
	// stops the nudges. A revived buyer has written.
	heard bool
	// repriced wakes run after a range edit lowered the opening price; see
	// reprice.
	repriced chan struct{}

	// Guarded by n.mu.
	state   string
	agreed  string // INR amount, once agreed
	hs      haggleState
	joined  time.Time
	started time.Time // first bot message
	revived time.Time // last time a late reply revived them; zero if never
	// imaged is set when the buyer sends an image after agreeing; the
	// payment turn judges it, so "sent" with it needs no screenshot ask.
	imaged bool
}

// negotiation runs the sessions for one task and feeds agreed buyers to the
// pay queue.
type negotiation struct {
	m    *sellManager
	t    store.SellTask
	s    *sale
	gate *taskGate
	h    haggle
	log  *slog.Logger
	join *offerQueue // everyone who replied to a post
	pay  *offerQueue // buyers who agreed, in agreement order

	// ctx runs the pay queue and the sessions; a payment claimed outside a
	// turn cancels it. talk stops only the sessions, once the meal sells.
	ctx    context.Context
	cancel context.CancelCauseFunc
	talk   context.Context
	hush   context.CancelFunc
	slots  chan struct{}
	wg     sync.WaitGroup
	// silence is [sell] negotiation_silence as it was when the task
	// started: how long a buyer may be quiet before their session ends.
	silence time.Duration
	// revived carries buyers who wrote after timing out to reviveLoop.
	revived  chan *session
	reviveMu sync.Mutex

	// rangeMu serializes refreshRange.
	rangeMu sync.Mutex
	// agreeMu serializes checking reservation state against claiming a
	// negotiation reply or agreement, so concurrent sessions cannot quote
	// or agree after another buyer just reserved the last free meal. Never
	// held across WhatsApp sends.
	agreeMu sync.Mutex

	mu sync.Mutex
	// h is guarded by mu once sessions run; read it with prices.
	sessions map[types.JID]*session
	resumed  []*session
	holder   types.JID // the buyer whose payment turn is under way
	// dropped are turn holders autopilot skipped, or who backed out of
	// their deal, with why. Their turn ends through dropC and droppedTurn
	// drops them for good.
	dropped map[types.JID]droppedTurn
	// dropC fires when holder is dropped early (backout or autopilot skip).
	dropC chan struct{}
	// paused is true while this task (and every active mate in its pool) is
	// reserved for a buyer. pauseCh is closed and replaced on every pause
	// transition or turn end.
	paused  bool
	pauseCh chan struct{}
	sold    bool
	// direct gets the outcome of a sale claimed outside a turn: a buyer
	// paid a quoted price while nobody held the reservation.
	direct      chan error
	directBuyer types.JID
	remove      func()
	// ownMin is the task's own floor in rupees. In a pool h.min is the
	// pool's lowest floor, which the bot may quote but this task never
	// charges below.
	ownMin int
	// moved are buyers passed to another task in the pool; their messages
	// are that task's now.
	moved map[types.JID]bool
}

func newNegotiation(ctx context.Context, m *sellManager, t store.SellTask, s *sale, gate *taskGate, join *offerQueue) *negotiation {
	var median float64
	var have bool
	if day, err := time.ParseInLocation("2006-01-02", t.Date, m.cfg.Location()); err == nil {
		from := day.Add(-market.Window).Format("2006-01-02")
		if ps, err := m.prices(ctx, from, t.Date); err != nil {
			m.log.Warn("could not read market prices; negotiating toward max_price", "task", t.ID, "err", err)
		} else {
			median, have = market.MedianAsk(ps, t.Date, baseMeal(t.Meal), s.mess, s.diet)
		}
	}
	n := &negotiation{
		m: m, t: t, s: s, gate: gate, log: m.log.With("task", t.ID), join: join, pay: newOfferQueue(),
		h:     newHaggle(int(t.MinPaise/100), int(t.MaxPaise/100), median, have, m.cfg.Live().Sell.SmartHaggle, t.EndsAt, s.lines(types.EmptyJID)).forMeal(baseMeal(t.Meal)),
		slots: make(chan struct{}, maxSessions), sessions: map[types.JID]*session{}, direct: make(chan error, 1),
		silence: m.cfg.Live().Sell.NegotiationSilence.Duration, revived: make(chan *session, maxSessions),
		ownMin: int(t.MinPaise / 100), moved: map[types.JID]bool{}, pauseCh: make(chan struct{}),
	}
	if n.silence <= 0 {
		n.silence = config.Default().Sell.NegotiationSilence.Duration
	}
	n.ctx, n.cancel = context.WithCancelCause(ctx)
	n.talk, n.hush = context.WithCancel(n.ctx)
	n.log.Info("negotiating", "min", n.h.min, "max", n.h.max, "target", n.h.target, "median_known", have, "smart", n.h.smart)
	if err := m.st.SetSellTaskTarget(ctx, t.ID, int64(n.h.target)*100); err != nil {
		n.log.Warn("could not record the target price", "err", err)
	}
	return n
}

// lines are the bot's lines for this task, said to buyer.
func (n *negotiation) lines(buyer types.JID) lines {
	return n.prices().l.forBuyer(n.join.name(buyer))
}

// prices returns the haggle as it stands now. A range edit or the smart
// haggle setting can change it while sessions run, so they read it once
// per turn.
func (n *negotiation) prices() haggle {
	smart := n.m.cfg.Live().Sell.SmartHaggle
	n.mu.Lock()
	defer n.mu.Unlock()
	n.h = n.h.withSmart(smart)
	return n.h
}

// refreshRange reloads the task's floor and opening price from the store,
// works out the target again, and records it. Every session's next turn
// uses them. When the opening price drops below what a haggling buyer was
// last asked, the bot offers them the new price right away (see reprice). Calls are serialized and always read the store, so the last
// edit wins whatever order they arrive in. In a pool the range is the
// pool's (see poolPrices).
func (n *negotiation) refreshRange(ctx context.Context) error {
	n.rangeMu.Lock()
	defer n.rangeMu.Unlock()
	t, err := n.m.st.GetSellTask(ctx, n.t.ID)
	if err != nil || t == nil {
		return errors.Join(err, errors.New("sell task is gone"))
	}
	lo, hi := t.MinPaise, t.MaxPaise
	if pooled(*t) && t.State == store.TaskActive {
		if p, err := n.m.pool(ctx, t.ID); err != nil {
			n.log.Warn("could not read the task's pool; haggling on its own range", "err", err)
		} else if len(p.active) > 1 {
			lo, hi = poolPrices(p.active)
		}
	}
	n.mu.Lock()
	old := n.h
	n.h = n.h.withRange(int(lo/100), int(hi/100))
	n.ownMin = int(t.MinPaise / 100)
	h := n.h
	if h.max < old.max {
		for _, ss := range n.sessions {
			if ss.state == sessionNegotiating && ss.hs.botMsgs > 0 && ss.hs.ask > h.max {
				select {
				case ss.repriced <- struct{}{}:
				default:
				}
			}
		}
	}
	n.mu.Unlock()
	if h.min == old.min && h.max == old.max {
		return nil
	}
	n.log.Info("price range changed", "min", h.min, "max", h.max, "target", h.target)
	return n.m.st.SetSellTaskTarget(ctx, n.t.ID, int64(h.target)*100)
}

// negotiates reports whether a task haggles: it has room between min and
// max, and doesn't hand the first reply straight to you.
func negotiates(t store.SellTask) bool {
	return t.MaxPaise > t.MinPaise && t.Handoff != string(config.HandoffOnReply)
}

// session returns peer's session, creating it (waiting) if needed.
func (n *negotiation) session(peer types.JID) *session {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sessionLocked(peer)
}

func (n *negotiation) sessionLocked(peer types.JID) *session {
	ss := n.sessions[peer]
	if ss == nil {
		ss = &session{n: n, buyer: peer, inbox: make(chan buyerMsg, 32), joined: time.Now(),
			hs: haggleState{ask: n.h.max}, repriced: make(chan struct{}, 1)}
		n.sessions[peer] = ss
	}
	return ss
}

// restoreBuyer puts back a buyer's session after a restart. holder marks
// the buyer whose payment turn resumes. A buyer who timed out comes back
// ended but with their haggle, so a late reply can revive them.
func (n *negotiation) restoreBuyer(b store.SellBuyer, holder bool) {
	peer, err := types.ParseJID(b.Peer)
	if err != nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	ss := n.sessionLocked(peer)
	ss.joined, ss.started = b.JoinedAt, b.StartedAt
	ss.hs = haggleFromStore(b.Negotiation, n.h.max)
	switch {
	case holder:
		// A handed-off buyer was never asked to pay an amount.
		ss.state, ss.agreed = sessionAgreed, cmp.Or(b.Amount, b.Agreed)
		n.setHolderLocked(peer)
		n.pay.restore(peer, false)
	case b.State == store.BuyerAgreed:
		ss.state, ss.agreed = sessionAgreed, b.Agreed
	case b.State == store.BuyerNegotiating:
		ss.state = sessionNegotiating
	default:
		ss.state = sessionEnded
		return
	}
	n.resumed = append(n.resumed, ss)
}

// start listens to buyers and opens a session for each one who replies.
func (n *negotiation) start() {
	id := n.m.conn.AddEventHandler(n.handle)
	n.remove = func() { n.m.conn.RemoveEventHandler(id) }
	n.mu.Lock()
	resumed := n.resumed
	n.resumed = nil
	n.mu.Unlock()
	// Agreed buyers queue to pay in the order they agreed.
	sort.SliceStable(resumed, func(i, j int) bool { return resumed[i].joined.Before(resumed[j].joined) })
	var agreed []store.SellBuyer
	if buyers, err := n.m.st.SellTaskBuyers(n.ctx, n.t.ID); err == nil {
		for _, b := range buyers {
			if b.State == store.BuyerAgreed {
				agreed = append(agreed, b)
			}
		}
	}
	sort.SliceStable(agreed, func(i, j int) bool { return agreed[i].AgreedAt.Before(agreed[j].AgreedAt) })
	for _, b := range agreed {
		if peer, err := types.ParseJID(b.Peer); err == nil {
			if p, err := store.ParsePaise(b.Agreed); err == nil {
				n.pay.setPriority(peer, p)
			}
			n.pay.add(peer)
		}
	}
	n.m.syncPoolPause(n.t)
	for _, ss := range resumed {
		slot := false
		if ss.state == sessionNegotiating {
			select {
			case n.slots <- struct{}{}:
				slot = true
			default:
			}
		}
		n.wg.Add(1)
		go ss.run("", slot)
	}
	n.wg.Add(2)
	go n.loop()
	go n.reviveLoop()
}

// stop ends every session and waits for them.
func (n *negotiation) stop() {
	n.cancel(context.Canceled)
	if n.remove != nil {
		n.remove()
	}
	n.wg.Wait()
}

// halt stops the sessions once the meal sold, before "sorry sold" goes out.
func (n *negotiation) halt() {
	n.mu.Lock()
	n.sold = true
	n.wakeLocked()
	n.mu.Unlock()
	n.hush()
	n.m.syncPoolPause(n.t)
}

func (n *negotiation) loop() {
	defer n.wg.Done()
	for {
		buyer, err := n.join.next(n.talk)
		if err != nil {
			return
		}
		select {
		case n.slots <- struct{}{}:
		case <-n.talk.Done():
			return
		}
		firstID, first := n.join.first(buyer)
		n.mu.Lock()
		ss := n.sessionLocked(buyer)
		ss.firstID = firstID
		if ss.state == sessionWaiting {
			ss.state = sessionNegotiating
		}
		n.mu.Unlock()
		n.wg.Add(1)
		go ss.run(first, true)
	}
}

// reviveLoop starts a session again for each buyer revive put back into
// negotiation, once a slot is free.
func (n *negotiation) reviveLoop() {
	defer n.wg.Done()
	for {
		var ss *session
		select {
		case ss = <-n.revived:
		case <-n.talk.Done():
			return
		}
		select {
		case n.slots <- struct{}{}:
		case <-n.talk.Done():
			return
		}
		n.mu.Lock()
		first := ss.first
		ss.first = ""
		n.mu.Unlock()
		n.wg.Add(1)
		go ss.run(first, true)
	}
}

// revive puts a buyer who timed out back into negotiation when they write
// again, while the meal is unsold and the task active, and answers m as
// their next message. The store keeps their haggle, so the bot carries on
// from its last ask and doesn't contradict a price it already quoted.
// Buyers who passed, were parked, skipped or bought stay ended: the store
// moves only a timed_out buyer. It reports whether m was taken.
func (n *negotiation) revive(ss *session, m buyerMsg) bool {
	n.reviveMu.Lock()
	defer n.reviveMu.Unlock()
	n.mu.Lock()
	state, sold := ss.state, n.sold
	n.mu.Unlock()
	if sold || n.talk.Err() != nil {
		return false
	}
	if state != sessionEnded {
		// Revived by an earlier message in the same burst.
		return n.deliver(ss, m)
	}
	ok, err := n.m.st.ReviveSellTaskBuyer(n.talk, n.t.ID, ss.buyer.String())
	if err != nil {
		n.log.Warn("could not revive a buyer who wrote after timing out", "buyer", ss.buyer, "err", err)
	}
	if !ok {
		return false
	}
	n.mu.Lock()
	ss.state, ss.firstID, ss.first, ss.revived = sessionNegotiating, m.id, m.text, time.Now()
	ask := ss.hs.ask
	n.mu.Unlock()
	n.log.Info("buyer wrote after timing out; negotiating again", "buyer", ss.buyer, "ask", ask)
	n.event(ss.buyer, store.EventRevived, ask, "wrote after timing out")
	select {
	case n.revived <- ss:
	case <-n.talk.Done():
	}
	return true
}

// deliver hands m to a session that is reading its inbox.
func (n *negotiation) deliver(ss *session, m buyerMsg) bool {
	select {
	case ss.inbox <- m:
		return true
	default:
		n.log.Warn("buyer is sending faster than the session reads; dropping a message", "buyer", ss.buyer)
		return false
	}
}

// handle routes a buyer's DM to their session, and their payments to
// payment. Messages from anyone who never replied to a post are ignored.
func (n *negotiation) handle(evt any) {
	e, ok := evt.(*events.Message)
	if !ok || e.Info.IsFromMe || e.Info.IsGroup || n.talk.Err() != nil {
		return
	}
	if e.Info.Chat.Server != types.DefaultUserServer && e.Info.Chat.Server != types.HiddenUserServer {
		return
	}
	peer := buyerPeer(n.ctx, n.m.conn, e.Info)
	if !n.join.has(peer) || n.isMoved(peer) {
		return
	}
	if n.gate.muted(n.ctx, peer) {
		return
	}
	if e.Message.GetSendPaymentMessage() != nil {
		go n.payment(peer, e)
		return
	}
	if e.Message.GetImageMessage() != nil {
		n.mu.Lock()
		if ss := n.sessions[peer]; ss != nil && ss.state == sessionAgreed {
			ss.imaged = true
		}
		n.mu.Unlock()
	}
	text := messageText(e.Message)
	// Reactions, protocol updates and media have no negotiation text.
	// Payment images are handled separately by the payment turn.
	if strings.TrimSpace(text) == "" {
		return
	}
	n.mu.Lock()
	ss := n.sessionLocked(peer)
	ended := ss.state == sessionEnded
	n.mu.Unlock()
	m := buyerMsg{id: e.Info.ID, text: text}
	if ended {
		n.revive(ss, m)
		return
	}
	n.deliver(ss, m)
}

func messageText(m *waE2E.Message) string {
	if t := m.GetConversation(); t != "" {
		return t
	}
	return m.GetExtendedTextMessage().GetText()
}

// competing reports whether two or more buyers are negotiating or waiting
// to pay.
func (n *negotiation) competing() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	active := 0
	for _, ss := range n.sessions {
		if ss.state == sessionNegotiating || ss.state == sessionAgreed {
			active++
		}
	}
	return market.Competing(active)
}

// others are everyone besides the buyer who should hear "sorry sold". The
// store skips whoever passed, timed out or already heard it.
func (n *negotiation) others() []types.JID {
	out := n.join.closeAndDrain()
	n.mu.Lock()
	var ss []*session
	for _, s := range n.sessions {
		ss = append(ss, s)
	}
	n.mu.Unlock()
	sort.Slice(ss, func(i, j int) bool { return ss[i].joined.Before(ss[j].joined) })
	for _, s := range ss {
		if !slices.Contains(out, s.buyer) {
			out = append(out, s.buyer)
		}
	}
	return out
}

// wakeLocked broadcasts a state or pause change to all running sessions.
// n.mu must be held.
func (n *negotiation) wakeLocked() {
	close(n.pauseCh)
	n.pauseCh = make(chan struct{})
}

// setPaused updates whether non-holder sessions should pause while every
// active meal in the pool is reserved.
func (n *negotiation) setPaused(p bool) {
	n.mu.Lock()
	if n.paused == p {
		n.mu.Unlock()
		return
	}
	n.paused = p
	n.wakeLocked()
	n.mu.Unlock()
}

// isPausedFor reports whether buyer's negotiating session must wait because
// all meals in the pool are reserved for someone else.
func (n *negotiation) isPausedFor(buyer types.JID) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.paused && n.holder != buyer
}

// loopState returns ss's current state, whether it is paused, and the
// channel closed on the next pause or turn-end change.
func (ss *session) loopState() (string, bool, <-chan struct{}) {
	n := ss.n
	n.mu.Lock()
	defer n.mu.Unlock()
	return ss.state, ss.state == sessionNegotiating && n.holder != ss.buyer && n.paused, n.pauseCh
}

// agreedPrice is what buyer agreed to pay, if they're still waiting to.
// The pay queue calls it when their turn comes, so it also marks them as
// the one holding the reservation.
func (n *negotiation) agreedPrice(buyer types.JID) (string, bool) {
	n.mu.Lock()
	ss := n.sessions[buyer]
	if ss == nil || ss.state != sessionAgreed {
		n.mu.Unlock()
		return "", false
	}
	n.setHolderLocked(buyer)
	agreed := ss.agreed
	n.mu.Unlock()
	n.m.syncPoolPause(n.t)
	return agreed, true
}

// setHolderLocked makes buyer the one holding the turn, with a fresh drop
// signal. n.mu must be held.
func (n *negotiation) setHolderLocked(buyer types.JID) {
	n.holder = buyer
	n.dropC = make(chan struct{}, 1)
}

// watchDrop is the signal for buyer's turn: it fires when buyer is dropped
// early (for example after backing out or being skipped on autopilot). It
// is nil unless buyer holds the turn.
func (n *negotiation) watchDrop(buyer types.JID) <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.holder != buyer {
		return nil
	}
	return n.dropC
}

// droppedTurn ends the turn of a holder who was dropped early (backout or
// autopilot skip) and unpauses waiting sessions if no meal remains busy.
func (n *negotiation) droppedTurn(ctx context.Context, buyer types.JID, handedOff bool) error {
	n.mu.Lock()
	d, ok := n.dropped[buyer]
	if ok {
		delete(n.dropped, buyer)
	}
	n.mu.Unlock()
	if ok {
		if err := n.dropTurn(ctx, buyer, d); err != nil {
			return err
		}
	}
	n.turnOver(buyer)
	return nil
}

// turnOver ends buyer's payment turn and unpauses waiting sessions if a
// meal is now free.
func (n *negotiation) turnOver(buyer types.JID) {
	n.mu.Lock()
	if n.holder == buyer {
		n.holder, n.dropC = types.EmptyJID, nil
	}
	if ss := n.sessions[buyer]; ss != nil {
		ss.state = sessionEnded
	}
	n.wakeLocked()
	n.mu.Unlock()
	n.m.syncPoolPause(n.t)
}

// accepts are the prices buyer may pay besides agreed: anything higher the
// bot quoted them. A buyer who pays the opening ₹60 after agreeing ₹50
// still gets the meal.
func (n *negotiation) accepts(buyer types.JID, agreed string) []string {
	paise, err := store.ParsePaise(agreed)
	if err != nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	ss := n.sessions[buyer]
	if ss == nil {
		return nil
	}
	var out []string
	for _, q := range ss.hs.quoted {
		if p := int64(q) * 100; p > paise {
			out = append(out, store.FormatPaise(p))
		}
	}
	return out
}

// payment handles a WhatsApp Pay payment from a buyer who doesn't hold the
// reservation. If nobody holds it and the amount is one the bot quoted
// them (or the posted price), the buyer gets the meal. Otherwise the
// payment is recorded for you to refund.
func (n *negotiation) payment(peer types.JID, e *events.Message) {
	n.mu.Lock()
	if n.sold || n.holder == peer {
		// The holder's turn handles their payment.
		n.mu.Unlock()
		return
	}
	ss := n.sessionLocked(peer)
	// The posted price, before and after any range edit.
	amounts := []string{store.FormatPaise(n.t.MaxPaise), store.FormatPaise(int64(n.h.max) * 100)}
	for _, q := range ss.hs.quoted {
		amounts = append(amounts, store.FormatPaise(int64(q)*100))
	}
	if ss.agreed != "" {
		amounts = append(amounts, ss.agreed)
	}
	// ClaimTaskSale checks this task's floor and its previously valid
	// quotes before accepting a payment, including prices from the pool.
	floor := int64(n.ownMin) * 100
	ended := ss.state == sessionEnded || ss.state == sessionWaiting
	since := ss.joined.Truncate(time.Second)
	n.mu.Unlock()
	if ended {
		// A buyer who left the haggle with an offer a lowered floor now
		// meets is listed in needs_you; paying that offer gets the meal.
		if offer := n.endedOffer(peer, floor); offer != "" {
			amounts = append(amounts, offer)
		}
	}

	amount, err := paymentMatch(e, amounts, since, n.t.EndsAt, time.Now())
	ctx := context.WithoutCancel(n.ctx)
	if err != nil {
		note := "sent a payment that matches no price the bot quoted them; check the chat and refund it if it went through"
		if errors.Is(err, errTruncatedPayment) {
			note = "sent a WhatsApp Pay payment the bot couldn't read; check the chat"
		}
		n.log.Warn("payment outside a turn not honoured", "buyer", peer, "reason", err)
		n.park(ctx, ss, note)
		return
	}
	d := n.s.reservation(peer, amount)
	msgID := n.m.conn.GenerateMessageID()
	paid := store.Payment{ID: e.Info.ID, At: e.Info.Timestamp, BuyerName: e.Info.PushName}
	claimed, err := n.m.st.ClaimTaskSale(ctx, n.t.ID, d, paid, msgID)
	if err != nil || !claimed {
		n.log.Warn("payment arrived while the meal was reserved for someone else", "buyer", peer, "amount", amount, "err", err)
		if rerr := n.m.st.RefundSellTaskBuyer(ctx, n.t.ID, peer.String(), amount,
			fmt.Sprintf("paid INR %s while the meal was reserved for someone else; refund them", amount)); rerr != nil {
			n.log.Error("could not record the payment to refund", "err", rerr)
		}
		n.m.event(ctx, store.SellEvent{TaskID: n.t.ID, Peer: peer.String(), Kind: store.EventParked, N: paise(amount),
			Text: fmt.Sprintf("paid INR %s while the meal was reserved for someone else; refund them", amount)})
		n.mu.Lock()
		ss.state = sessionEnded
		n.wakeLocked()
		n.mu.Unlock()
		return
	}
	n.mu.Lock()
	n.sold, n.directBuyer = true, peer
	n.mu.Unlock()
	n.log.Info("buyer paid before their turn; sending the QR", "buyer", peer, "amount", amount)
	n.m.event(ctx, store.SellEvent{TaskID: n.t.ID, Peer: peer.String(), Kind: store.EventPayment, N: paise(amount)})
	n.cancel(errSoldDirect)
	sendCtx, stop := context.WithTimeout(ctx, time.Minute)
	defer stop()
	err = n.s.sendQR(sendCtx, e.Info.Chat, msgID, d, true)
	if err == nil {
		err = n.m.st.FinishDelivery(sendCtx, d, msgID)
	}
	if err == nil {
		n.m.event(ctx, store.SellEvent{TaskID: n.t.ID, Peer: peer.String(), Kind: store.EventQRSent, N: paise(amount)})
	}
	n.direct <- err
}

// endedOffer is the last offer, as an INR amount, of a buyer who timed out,
// passed or was parked, when it is at or above floor; otherwise "".
func (n *negotiation) endedOffer(peer types.JID, floor int64) string {
	buyers, err := n.m.st.SellTaskBuyers(context.WithoutCancel(n.ctx), n.t.ID)
	if err != nil {
		return ""
	}
	for _, b := range buyers {
		if b.Peer == peer.String() && offerMeetsFloor(b, floor) {
			return store.FormatPaise(b.LastOffer)
		}
	}
	return ""
}

// offerMeetsFloor reports whether a buyer who left the haggle without
// buying made a last offer at or above floor. Buyers with a payment to
// refund are left out: they need you for that already.
func offerMeetsFloor(b store.SellBuyer, floor int64) bool {
	switch b.State {
	case store.BuyerTimedOut, store.BuyerPassed, store.BuyerParked:
		return b.LastOffer > 0 && b.LastOffer >= floor && b.Refund == ""
	}
	return false
}

// directSale reports a buyer who paid outside a turn and got the meal.
func (n *negotiation) directSale() (types.JID, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.directBuyer, !n.directBuyer.IsEmpty()
}

// event records one step of a buyer's negotiation for the dashboard.
func (n *negotiation) event(buyer types.JID, kind string, rupees int, text string) {
	n.m.event(n.ctx, store.SellEvent{TaskID: n.t.ID, Peer: buyer.String(), Kind: kind, N: int64(rupees) * 100, Text: text})
}

// park stops the bot talking to a buyer and records why they need you.
// On autopilot nobody is there to answer, so the buyer is skipped instead.
func (n *negotiation) park(ctx context.Context, ss *session, note string) {
	if n.t.Autopilot {
		n.skip(ctx, ss, note)
		return
	}
	n.mu.Lock()
	holder := n.holder == ss.buyer
	if !holder {
		ss.state = sessionEnded
		n.wakeLocked()
	}
	n.mu.Unlock()
	n.log.Info("buyer needs you", "buyer", ss.buyer, "why", note)
	if err := n.m.st.ParkSellTaskBuyer(ctx, n.t.ID, ss.buyer.String(), note); err != nil {
		n.log.Error("could not park buyer", "buyer", ss.buyer, "err", err)
	}
	n.event(ss.buyer, store.EventParked, 0, note)
}

// skip drops a buyer an autopilot task would otherwise park for you, even
// one offering more than the rest: the bot stops talking to them and sells
// to the others. A buyer holding the payment turn loses it now, through
// dropC, and droppedTurn tells them not to pay. A WhatsApp Pay payment
// that matches what they were quoted still gets the meal, as for anyone
// who left the haggle.
func (n *negotiation) skip(ctx context.Context, ss *session, note string) {
	n.mu.Lock()
	holder := n.holder == ss.buyer
	if holder {
		n.dropLocked(ss.buyer, droppedTurn{note: note})
	} else {
		ss.state = sessionEnded
	}
	n.mu.Unlock()
	n.log.Info("autopilot skipped a buyer who would need you", "buyer", ss.buyer, "why", note, "had_turn", holder)
	if !holder {
		if err := n.m.st.SkipSellTaskBuyer(ctx, n.t.ID, ss.buyer.String(), note); err != nil {
			n.log.Error("could not skip buyer", "buyer", ss.buyer, "err", err)
		}
	}
	n.event(ss.buyer, store.EventAutoSkipped, 0, note)
}

// backOut ends the deal of an agreed buyer who no longer takes the agreed
// price, such as "naa I said 50" after the pay prompt. Autopilot skips
// them. Otherwise they need you, and a buyer holding the payment turn
// loses it so the meal doesn't wait on a deal that fell through. park
// can't do that: a buyer who says they paid keeps their turn.
func (n *negotiation) backOut(ctx context.Context, ss *session, note string) {
	if n.t.Autopilot {
		n.skip(ctx, ss, note)
		return
	}
	n.mu.Lock()
	holder := n.holder == ss.buyer
	if holder {
		n.dropLocked(ss.buyer, droppedTurn{note: note, park: true})
	} else {
		ss.state = sessionEnded
	}
	n.mu.Unlock()
	n.log.Info("buyer backed out of the agreed price; needs you", "buyer", ss.buyer, "why", note, "had_turn", holder)
	if !holder {
		if err := n.m.st.ParkSellTaskBuyer(ctx, n.t.ID, ss.buyer.String(), note); err != nil {
			n.log.Error("could not park buyer", "buyer", ss.buyer, "err", err)
		}
	}
	n.event(ss.buyer, store.EventParked, 0, note)
}

// droppedTurn is why a turn holder's turn ends for good. park puts them
// with the buyers who need you instead of skipping them.
type droppedTurn struct {
	note string
	park bool
}

// dropLocked ends the turn of holder buyer through dropC; droppedTurn then
// drops them. n.mu must be held.
func (n *negotiation) dropLocked(buyer types.JID, d droppedTurn) {
	if n.dropped == nil {
		n.dropped = map[types.JID]droppedTurn{}
	}
	n.dropped[buyer] = d
	select {
	case n.dropC <- struct{}{}:
	default:
	}
}

// dropTurn ends a dropped holder's turn for good. A buyer autopilot
// skipped after the pay prompt hears not to pay. One who backed out is
// parked for you, and the bot says nothing more to them.
func (n *negotiation) dropTurn(ctx context.Context, buyer types.JID, d droppedTurn) error {
	to := store.BuyerSkipped
	if d.park {
		to = store.BuyerParked
	}
	prompted, err := n.m.st.MoveSellTaskBuyer(ctx, n.t.ID, buyer.String(), store.BuyerOffered, to, "", time.Time{})
	if err != nil {
		return err
	}
	if !prompted {
		if _, err := n.m.st.MoveSellTaskBuyerFrom(ctx, n.t.ID, buyer.String(), []string{store.BuyerAgreed, store.BuyerHandedOff}, to); err != nil {
			return err
		}
	}
	if d.park {
		if err := n.m.st.ParkSellTaskBuyer(ctx, n.t.ID, buyer.String(), d.note); err != nil {
			n.log.Error("could not note why the buyer was parked", "buyer", buyer, "err", err)
		}
		n.log.Info("ended the turn of a buyer who backed out", "buyer", buyer, "prompted", prompted)
		return nil
	}
	if err := n.m.st.SkipSellTaskBuyer(ctx, n.t.ID, buyer.String(), d.note); err != nil {
		n.log.Error("could not note why the buyer was skipped", "buyer", buyer, "err", err)
	}
	n.log.Info("autopilot ended a skipped buyer's turn", "buyer", buyer, "prompted", prompted)
	if prompted {
		// It goes out whatever the message limit: they must not pay now.
		r := n.lines(buyer).dropped()
		if err := n.send(ctx, buyer, r); err != nil {
			n.log.Warn("could not tell a skipped buyer not to pay", "buyer", buyer, "err", err)
		} else {
			n.event(buyer, store.EventBotSaid, 0, strings.Join(r, "\n"))
		}
	}
	return nil
}

func (n *negotiation) send(ctx context.Context, to types.JID, r reply) error {
	return sendReply(ctx, n.gate.send, to, r)
}

// pausableTimer wraps a time.Timer whose remaining duration can be frozen
// while a session is paused and resumed once the meal is unreserved.
type pausableTimer struct {
	t        *time.Timer
	deadline time.Time
	left     time.Duration
	running  bool
	paused   bool
}

func (p *pausableTimer) start(d time.Duration) {
	p.stop()
	if d < 0 {
		d = 0
	}
	p.deadline = time.Now().Add(d)
	p.left = d
	p.t = time.NewTimer(d)
	p.running = true
	p.paused = false
}

func (p *pausableTimer) stop() {
	if p.t != nil {
		p.t.Stop()
		p.t = nil
	}
	p.running = false
	p.paused = false
	p.left = 0
}

func (p *pausableTimer) pause() {
	if !p.running || p.paused {
		return
	}
	p.left = max(0, time.Until(p.deadline))
	if p.t != nil {
		p.t.Stop()
		p.t = nil
	}
	p.paused = true
}

func (p *pausableTimer) resume() {
	if !p.running || !p.paused {
		return
	}
	p.paused = false
	p.deadline = time.Now().Add(p.left)
	p.t = time.NewTimer(p.left)
}

func (p *pausableTimer) ch() <-chan time.Time {
	if !p.running || p.paused || p.t == nil {
		return nil
	}
	return p.t.C
}

func (p *pausableTimer) fired() {
	p.t = nil
	p.running = false
	p.paused = false
	p.left = 0
}

// run is one buyer's negotiation. first is the reply that queued them;
// slot says the session holds one of the maxSessions slots, freed once the
// buyer agrees or leaves.
func (ss *session) run(first string, slot bool) {
	n := ss.n
	defer n.wg.Done()
	release := func() {
		if slot {
			slot = false
			<-n.slots
		}
	}
	defer release()

	n.mu.Lock()
	started := ss.started
	if started.IsZero() {
		started = time.Now()
	}
	if ss.revived.After(started) {
		// A revived buyer gets a fresh window from their late reply.
		started = ss.revived
	}
	state := ss.state
	fresh := ss.hs.botMsgs == 0
	if !ss.revived.IsZero() {
		ss.heard = true
	}
	n.mu.Unlock()

	var limitT, silenceT, nudgeT, debounceT pausableTimer
	defer limitT.stop()
	defer silenceT.stop()
	defer nudgeT.stop()
	defer debounceT.stop()
	limitT.start(max(sessionLimit-time.Since(started), n.silence))
	silenceT.start(n.silence)
	var paidWait <-chan time.Time
	var batch []string
	var debounceAt time.Time
	repricePending := false
	// nudge fires while the buyer is quiet after the bot's first message.
	if ss.nudgeDue() {
		nudgeT.start(nudgeAfter)
	}
	if state == sessionNegotiating && (fresh || strings.TrimSpace(first) != "") {
		// Restored sessions have no in-memory first message. Open with
		// the price instead of counting the missing text as unreadable.
		// A revived session answers the late reply that revived it.
		if strings.TrimSpace(first) == "" {
			first = "price?"
		}
		batch = []string{first}
		debounceAt = time.Now().Add(replyDebounce)
		debounceT.start(replyDebounce)
	}
	if state == sessionAgreed {
		release()
	}
	for {
		state, paused, pauseCh := ss.loopState()
		if state == sessionEnded {
			return
		}
		if state != sessionNegotiating {
			limitT.stop()
			silenceT.stop()
			nudgeT.stop()
		} else if paused {
			limitT.pause()
			silenceT.pause()
			nudgeT.pause()
			debounceT.stop()
		} else {
			limitT.resume()
			silenceT.resume()
			nudgeT.resume()
			if len(batch) > 0 && !debounceT.running {
				debounceT.start(max(0, time.Until(debounceAt)))
			}
		}
		if state == sessionNegotiating && !paused && repricePending && len(batch) == 0 {
			repricePending = false
			switch ss.reprice() {
			case handledEnded:
				return
			case handledPaused:
				repricePending = true
				continue
			case handledReplied:
				silenceT.start(n.silence)
				if !ss.nudgeDue() {
					nudgeT.stop()
				}
			}
			continue
		}
		var limitC, silenceC, nudgeC, debounceC <-chan time.Time
		if state == sessionNegotiating && !paused {
			limitC, silenceC, nudgeC = limitT.ch(), silenceT.ch(), nudgeT.ch()
			if nudgeC != nil {
				// The nudges decide when a quiet buyer times out.
				silenceC = nil
			}
		}
		if !paused {
			debounceC = debounceT.ch()
		}
		select {
		case <-n.talk.Done():
			return
		case <-pauseCh:
		case m := <-ss.inbox:
			if m.id != "" && m.id == ss.firstID {
				continue
			}
			batch = append(batch, m.text)
			debounceAt = time.Now().Add(replyDebounce)
			if !paused {
				debounceT.start(replyDebounce)
			}
			n.mu.Lock()
			ss.heard = true
			n.mu.Unlock()
			nudgeT.stop()
		case <-debounceC:
			debounceT.fired()
			texts := batch
			batch = nil
			switch ss.handle(texts) {
			case handledEnded:
				return
			case handledPaused:
				batch = append(texts, batch...)
				debounceAt = time.Now()
			case handledAgreed:
				release()
			case handledReplied:
				silenceT.start(n.silence)
				if ss.nudgeDue() {
					nudgeT.start(nudgeAfter)
				}
			case handledPaidClaim:
				if paidWait == nil {
					grace := paidGrace
					if n.s.acceptImages {
						grace = screenshotGrace
					}
					paidWait = time.After(grace)
				}
			}
		case <-ss.repriced:
			if len(batch) > 0 {
				// The reply to the buyer's pending messages uses the new price.
				continue
			}
			if paused {
				repricePending = true
				continue
			}
			switch ss.reprice() {
			case handledEnded:
				return
			case handledPaused:
				repricePending = true
			case handledReplied:
				silenceT.start(n.silence)
				if !ss.nudgeDue() {
					nudgeT.stop()
				}
			}
		case <-nudgeC:
			nudgeT.fired()
			switch ss.quietNudge() {
			case handledEnded:
				return
			case handledPaused:
				nudgeT.start(0)
			case handledReplied:
				silenceT.start(n.silence)
				if ss.nudgeDue() {
					nudgeT.start(nudgeAfter)
				}
			}
		case <-silenceC:
			silenceT.fired()
			if ss.timeOut("went quiet after the bot's last message") {
				return
			}
			silenceT.start(0)
		case <-limitC:
			limitT.fired()
			if ss.timeOut("negotiation took too long") {
				return
			}
			limitT.start(0)
		case <-paidWait:
			paidWait = nil
			n.mu.Lock()
			sold := n.sold
			n.mu.Unlock()
			if !sold {
				note := "says they paid, but no WhatsApp Pay payment reached the bot; check the chat"
				if n.s.acceptImages {
					note = "says they paid, but sent no WhatsApp Pay payment or screenshot; check the chat"
				}
				n.park(context.WithoutCancel(n.talk), ss, note)
				if ss.current() == sessionEnded {
					return
				}
			}
		}
	}
}

// nudgeDue reports whether the buyer has said nothing since the bot's
// first message and has a follow-up left: the bot has sent only that
// message and its nudges.
func (ss *session) nudgeDue() bool {
	ss.n.mu.Lock()
	defer ss.n.mu.Unlock()
	hs := ss.hs
	return ss.state == sessionNegotiating && !ss.heard && hs.nudges < 2 && hs.botMsgs == 1+hs.nudges
}

// quietNudge follows up on a buyer who stayed quiet after the bot's first
// message. The first time it asks "yes or no?" at the same price. The
// second time it offers one lower price and times them out, so a late
// reply revives them at that price.
func (ss *session) quietNudge() handled {
	n := ss.n
	n.agreeMu.Lock()
	if !ss.nudgeDue() || n.gate.muted(n.talk, ss.buyer) {
		n.agreeMu.Unlock()
		return handledNothing
	}
	if n.isPausedFor(ss.buyer) {
		n.agreeMu.Unlock()
		return handledPaused
	}
	n.mu.Lock()
	hs := ss.hs
	n.mu.Unlock()
	next := hs
	next.quoted = slices.Clone(hs.quoted)
	next.nudges++
	if hs.nudges == 0 {
		text := n.lines(ss.buyer).nudge(min(hs.ask, n.prices().max))
		ok := ss.claimSayLocked(next)
		n.agreeMu.Unlock()
		if !ok || !ss.sendClaimed(next.ask, next.rounds, text) {
			return handledEnded
		}
		n.event(ss.buyer, store.EventNudged, next.ask, "quiet after the first message; asked if they're buying")
		return handledReplied
	}
	k, ok := n.prices().quietDrop(hs)
	if ok {
		was := min(hs.ask, n.prices().max)
		next.ask = k
		next.rounds++
		if !slices.Contains(next.quoted, k) {
			next.quoted = append(next.quoted, k)
		}
		text := n.lines(ss.buyer).quietDrop(k, was)
		claimed := ss.claimSayLocked(next)
		n.agreeMu.Unlock()
		if !claimed || !ss.sendClaimed(next.ask, next.rounds, text) {
			return handledEnded
		}
		n.event(ss.buyer, store.EventNudged, k, fmt.Sprintf("still quiet; dropped from ₹%d to ₹%d", was, k))
		ss.finishTimeOut("went quiet after the first message and a follow-up")
		return handledEnded
	}
	ss.finishTimeOut("went quiet after the first message and a follow-up")
	n.agreeMu.Unlock()
	return handledEnded
}

// reprice offers a haggling buyer the new opening price after a range edit
// lowered it below what the bot last asked them: "how about 100". It sends
// nothing when the buyer was never priced, is no longer haggling, has no
// replies left, or was already asked that much or less.
func (ss *session) reprice() handled {
	n := ss.n
	n.agreeMu.Lock()
	if n.gate.muted(n.talk, ss.buyer) {
		n.agreeMu.Unlock()
		return handledNothing
	}
	if n.isPausedFor(ss.buyer) {
		n.agreeMu.Unlock()
		return handledPaused
	}
	n.mu.Lock()
	state, hs := ss.state, ss.hs
	n.mu.Unlock()
	k := n.prices().max
	if state != sessionNegotiating || hs.botMsgs == 0 || hs.botMsgs >= maxBotMsgs || k >= hs.ask {
		n.agreeMu.Unlock()
		return handledNothing
	}
	next := hs
	next.quoted = slices.Clone(hs.quoted)
	next.ask = k
	if !slices.Contains(next.quoted, k) {
		next.quoted = append(next.quoted, k)
	}
	text := n.lines(ss.buyer).counter(k, hs.ask, 1)
	claimed := ss.claimSayLocked(next)
	n.agreeMu.Unlock()
	if !claimed || !ss.sendClaimed(next.ask, next.rounds, text) {
		return handledEnded
	}
	n.log.Info("price lowered; offered the buyer the new price", "buyer", ss.buyer, "from", hs.ask, "ask", k)
	return handledReplied
}

func (ss *session) current() string {
	ss.n.mu.Lock()
	defer ss.n.mu.Unlock()
	return ss.state
}

type handled int

const (
	handledNothing handled = iota
	handledReplied
	handledAgreed
	handledPaidClaim
	handledPaused
	handledEnded
)

// handle answers one burst of messages.
func (ss *session) handle(texts []string) handled {
	n := ss.n
	ctx := n.talk
	if n.gate.muted(ctx, ss.buyer) {
		return handledNothing
	}
	in := combineIntents(texts)
	n.mu.Lock()
	state, agreed := ss.state, ss.agreed
	n.mu.Unlock()

	if state == sessionAgreed {
		// A picture or sticker has no text to read; the payment turn records
		// pictures on its own.
		if strings.TrimSpace(strings.Join(texts, "")) != "" {
			n.event(ss.buyer, store.EventRead, in.offer, in.name())
		}
		// The price is settled. Only a payment claim, another way to pay or
		// dropping out matters now.
		switch {
		case in.kind == intentPaid || in.kind == intentOK && in.done:
			ss.askScreenshot()
			return handledPaidClaim
		case in.upi && (in.kind == intentPayOther || in.kind == intentAccept) && n.s.payUPI():
			// They have the UPI QR, or will once their turn comes. Asking
			// for a UPI ID, scanner or QR gets it once more.
			if ss.holding() {
				ss.resendPayQR()
			}
			return handledNothing
		case in.upi && (in.kind == intentPayOther || in.kind == intentAccept) && n.s.acceptImages:
			// Screenshots count, so a UPI payment to the number works. A
			// buyer waiting for their turn hears it once the prompt is out.
			if !ss.holding() {
				return handledNothing
			}
			if !ss.sayOnce(linePayNumber, n.lines(ss.buyer).payNumber()) && in.kind == intentPayOther {
				n.park(context.WithoutCancel(ctx), ss, "has no WhatsApp Pay and was told to pay the number; check the chat")
			}
			return handledNothing
		case in.kind == intentPayOther:
			if !ss.holding() {
				return handledNothing
			}
			if !ss.sayPayOther() {
				way := "WhatsApp Pay"
				if n.s.payUPI() {
					way = "UPI"
				}
				n.park(context.WithoutCancel(ctx), ss, "wants to pay some way other than "+way)
			}
			return handledNothing
		case in.kind == intentOffer && int64(in.offer)*100 < paise(agreed), in.kind == intentLower:
			// "naa I said 50" after agreeing ₹60: the deal they're waiting
			// to pay isn't one they took.
			note := fmt.Sprintf("agreed ₹%d, then wants it cheaper; check the chat", paise(agreed)/100)
			if in.kind == intentOffer {
				note = fmt.Sprintf("agreed ₹%d, then offered ₹%d; check the chat", paise(agreed)/100, in.offer)
			}
			n.backOut(context.WithoutCancel(ctx), ss, note)
			if ss.current() == sessionEnded {
				return handledEnded
			}
			return handledNothing
		case in.kind == intentPass:
			if ok, _ := n.m.st.MoveSellTaskBuyerFrom(ctx, n.t.ID, ss.buyer.String(), []string{store.BuyerAgreed}, store.BuyerPassed); ok {
				n.mu.Lock()
				ss.state = sessionEnded
				n.mu.Unlock()
				n.event(ss.buyer, store.EventPassed, 0, "")
				return handledEnded
			}
		}
		return handledNothing
	}
	if state != sessionNegotiating {
		return handledEnded
	}

	n.agreeMu.Lock()
	n.mu.Lock()
	state, hs := ss.state, ss.hs
	paused := n.paused && n.holder != ss.buyer
	n.mu.Unlock()
	if state != sessionNegotiating {
		n.agreeMu.Unlock()
		return handledEnded
	}
	if paused {
		n.agreeMu.Unlock()
		return handledPaused
	}
	// A picture or sticker has no text to read; the payment turn records
	// pictures on its own.
	if strings.TrimSpace(strings.Join(texts, "")) != "" {
		n.event(ss.buyer, store.EventRead, in.offer, in.name())
	}

	if in.kind == intentPayOther && in.upi && n.s.acceptImages {
		// Screenshots count, so paying by UPI is a yes: the pay prompt
		// follows, and "send to my number" if they still have no
		// WhatsApp Pay.
		in.kind = intentAccept
	}
	if in.kind == intentPayOther {
		if ok, err := n.m.st.ClaimBuyerLine(ctx, n.t.ID, ss.buyer.String(), linePayOther); err == nil && ok {
			r := ss.payOtherReply()
			n.agreeMu.Unlock()
			if err := n.send(ctx, ss.buyer, r); err != nil {
				n.log.Warn("negotiation message failed or is uncertain", "buyer", ss.buyer, "err", err)
			}
			return handledReplied
		}
	}
	h := n.prices()
	h.l = n.lines(ss.buyer)
	st := h.decide(hs, in, time.Now(), n.competing())
	switch {
	case st.pass:
		n.m.st.MoveSellTaskBuyerFrom(ctx, n.t.ID, ss.buyer.String(), []string{store.BuyerQueued, store.BuyerNegotiating}, store.BuyerPassed)
		n.mu.Lock()
		ss.state = sessionEnded
		n.mu.Unlock()
		n.agreeMu.Unlock()
		n.log.Info("buyer passed", "buyer", ss.buyer, "why", st.why)
		n.event(ss.buyer, store.EventPassed, 0, st.why)
		return handledEnded
	case st.park != "":
		ss.keepOffer(hs, st.next)
		n.park(context.WithoutCancel(ctx), ss, st.park)
		n.agreeMu.Unlock()
		return handledEnded
	case st.paidClaim:
		n.agreeMu.Unlock()
		return handledPaidClaim
	case st.agree > 0:
		res := ss.agreeLocked(st)
		n.agreeMu.Unlock()
		return res
	case len(st.reply) > 0:
		claimed := ss.claimSayLocked(st.next)
		n.agreeMu.Unlock()
		if !claimed || !ss.sendClaimed(st.next.ask, st.next.rounds, st.reply) {
			return handledEnded
		}
		return handledReplied
	}
	n.agreeMu.Unlock()
	return handledNothing
}

// keepOffer records the buyer's latest offer when the bot answers it
// without a message, as when it parks them. say and agree record it with
// the rest of the state; without this, the status kept showing an earlier
// offer.
func (ss *session) keepOffer(prev, next haggleState) {
	if next.lastOffer == prev.lastOffer {
		return
	}
	n := ss.n
	if err := n.m.st.RecordBuyerOffer(context.WithoutCancel(n.talk), n.t.ID, ss.buyer.String(), int64(next.lastOffer)*100); err != nil {
		n.log.Warn("could not record the buyer's offer", "buyer", ss.buyer, "err", err)
		return
	}
	n.mu.Lock()
	ss.hs.lastOffer = next.lastOffer
	n.mu.Unlock()
}

// claimSayLocked claims one negotiation reply in the store and records next
// as the buyer's state. n.agreeMu must be held; the send runs after
// unlocking so n.agreeMu is never held across WhatsApp I/O.
func (ss *session) claimSayLocked(next haggleState) bool {
	n := ss.n
	ctx := n.talk
	n.mu.Lock()
	sent := ss.hs.botMsgs
	if ss.started.IsZero() {
		ss.started = time.Now()
	}
	started := ss.started
	n.mu.Unlock()
	if sent >= maxBotMsgs {
		n.park(context.WithoutCancel(ctx), ss, fmt.Sprintf("reached the limit of %d replies the bot sends one buyer", maxBotMsgs))
		return false
	}
	next.botMsgs = sent + 1
	ok, err := n.m.st.ClaimNegotiationMessage(ctx, n.t.ID, ss.buyer.String(), sent, next.store(started))
	if err != nil || !ok {
		n.log.Warn("not sending a negotiation message", "buyer", ss.buyer, "err", err)
		return false
	}
	n.mu.Lock()
	ss.hs = next
	n.mu.Unlock()
	return true
}

// sendClaimed sends a reply claimed by claimSayLocked and logs it.
func (ss *session) sendClaimed(ask, rounds int, text reply) bool {
	n := ss.n
	if err := n.send(n.talk, ss.buyer, text); err != nil {
		// It may have reached WhatsApp; it is never sent again.
		n.log.Warn("negotiation message failed or is uncertain", "buyer", ss.buyer, "err", err)
		return false
	}
	n.log.Info("negotiation message sent", "buyer", ss.buyer, "ask", ask, "round", rounds)
	n.event(ss.buyer, store.EventBotSaid, ask, strings.Join(text, "\n"))
	return true
}

// agreeLocked records the agreed price and puts the buyer in the pay queue,
// reserving the task/pool so other negotiations pause until this turn ends.
// n.agreeMu must be held.
func (ss *session) agreeLocked(st step) handled {
	n := ss.n
	ctx := n.talk
	next := st.next
	if !slices.Contains(next.quoted, st.agree) {
		next.quoted = append(next.quoted, st.agree)
	}
	amount := store.FormatPaise(int64(st.agree) * 100)
	n.mu.Lock()
	started := ss.started
	n.mu.Unlock()
	if started.IsZero() {
		started = time.Now()
	}
	// In a pool the buyer goes to the task whose floor takes the price.
	if to, ok := n.m.sellerFor(ctx, n.t, int64(st.agree)*100, 0); ok && to != n.t.ID {
		if n.passAgreed(ctx, ss, to, amount, next, started) {
			return handledEnded
		}
	}
	n.mu.Lock()
	own := n.ownMin
	n.mu.Unlock()
	honoured := st.agree >= own
	if !honoured {
		var err error
		honoured, err = n.m.st.BuyerPriceHonoured(ctx, n.t.ID, ss.buyer.String(), int64(st.agree)*100)
		if err != nil {
			n.log.Warn("could not check the buyer's earlier quotes", "buyer", ss.buyer, "err", err)
		}
	}
	if !honoured {
		// No remaining task takes this pool quote. Previously valid
		// quotes from this task still count after its floor rises.
		n.mu.Lock()
		prev := ss.hs
		n.mu.Unlock()
		ss.keepOffer(prev, next)
		n.park(context.WithoutCancel(ctx), ss, fmt.Sprintf("agreed ₹%d, which another meal in the pool took when the bot quoted it; no meal left takes it now, so it's your call", st.agree))
		return handledEnded
	}
	ok, err := n.m.st.AgreeSellTaskBuyer(ctx, n.t.ID, ss.buyer.String(), amount, next.store(started), time.Now())
	if err != nil || !ok {
		n.log.Warn("could not record the agreement", "buyer", ss.buyer, "amount", amount, "err", err)
		return handledEnded
	}
	paise := int64(st.agree) * 100
	n.mu.Lock()
	ss.state, ss.agreed, ss.hs, ss.started = sessionAgreed, amount, next, started
	// Highest price pays first if multiple buyers are already in n.pay.
	// Enqueue while holding n.mu so n.busy() is true before syncPoolPause.
	n.pay.setPriority(ss.buyer, paise)
	n.pay.requeue(ss.buyer)
	n.mu.Unlock()
	n.m.syncPoolPause(n.t)
	n.log.Info("buyer agreed", "buyer", ss.buyer, "amount", amount)
	n.event(ss.buyer, store.EventAgreed, st.agree, "")
	return handledAgreed
}

func (ss *session) payOtherReply() reply {
	l := ss.n.lines(ss.buyer)
	if ss.n.s.payUPI() {
		return l.payOtherUPI()
	}
	return l.payOther()
}

// sayPayOther tells a buyer who wants to pay another way that it's
// WhatsApp Pay only. It reports false if they already heard it, so asking
// again hands them to you.
func (ss *session) sayPayOther() bool {
	return ss.sayOnce(linePayOther, ss.payOtherReply())
}

// resendPayQR sends the paying buyer the UPI QR again, once per buyer.
func (ss *session) resendPayQR() {
	n := ss.n
	if ok, err := n.m.st.ClaimBuyerLine(n.talk, n.t.ID, ss.buyer.String(), linePayQRAgain); err != nil || !ok {
		return
	}
	if err := n.s.resendPayQR(n.talk, ss.buyer); err != nil {
		n.log.Warn("UPI QR resend failed or is uncertain", "buyer", ss.buyer, "err", err)
	}
}

// holding reports whether the buyer holds the payment turn.
func (ss *session) holding() bool {
	ss.n.mu.Lock()
	defer ss.n.mu.Unlock()
	return ss.n.holder == ss.buyer
}

// askScreenshot asks the paying buyer for a screenshot after they say they
// paid, when screenshots count as payment, the meal is unsold and they
// sent no image since agreeing. Their next image gets the QR. It asks once
// per buyer.
func (ss *session) askScreenshot() {
	n := ss.n
	if !n.s.acceptImages {
		return
	}
	n.mu.Lock()
	skip := n.sold || n.holder != ss.buyer || ss.imaged
	n.mu.Unlock()
	if skip {
		return
	}
	ss.sayOnce(lineAskScreenshot, n.lines(ss.buyer).askScreenshot())
}

// sayOnce sends r unless the buyer already heard line. It reports false
// if they had.
func (ss *session) sayOnce(line string, r reply) bool {
	n := ss.n
	if ok, err := n.m.st.ClaimBuyerLine(n.talk, n.t.ID, ss.buyer.String(), line); err != nil || !ok {
		return false
	}
	if err := n.send(n.talk, ss.buyer, r); err != nil {
		n.log.Warn("negotiation message failed or is uncertain", "buyer", ss.buyer, "err", err)
	}
	return true
}

// timeOut ends ss unless the negotiation became paused right as the timer
// fired, in which case it reports false so ss.run waits for unpause.
func (ss *session) timeOut(why string) bool {
	n := ss.n
	n.agreeMu.Lock()
	defer n.agreeMu.Unlock()
	if n.isPausedFor(ss.buyer) {
		return false
	}
	ss.finishTimeOut(why)
	return true
}

func (ss *session) finishTimeOut(why string) {
	n := ss.n
	n.mu.Lock()
	ss.state = sessionEnded
	n.mu.Unlock()
	n.log.Info("negotiation ended", "buyer", ss.buyer, "why", why)
	if ok, _ := n.m.st.MoveSellTaskBuyerFrom(n.talk, n.t.ID, ss.buyer.String(), []string{store.BuyerQueued, store.BuyerNegotiating}, store.BuyerTimedOut); ok {
		n.event(ss.buyer, store.EventTimedOut, 0, why)
	}
}

// negotiatedJournal is the pay queue's journal for a negotiating task: a
// buyer's price is the one they agreed, and their turn starts from agreed.
type negotiatedJournal struct {
	*taskJournal
	n *negotiation
}

var errNotAgreed = errors.New("buyer is no longer waiting to pay")

func (j negotiatedJournal) price(ctx context.Context, buyer types.JID) (string, error) {
	if j.muted(ctx, buyer) {
		return "", errBuyerMuted
	}
	amount, ok := j.n.agreedPrice(buyer)
	if !ok {
		return "", errNotAgreed
	}
	// The store arms the reservation only at or above the floor, or at the
	// price agreed with this buyer: a range edit that raised the floor
	// since doesn't undo the deal.
	if _, err := store.ParsePaise(amount); err != nil {
		return "", err
	}
	return amount, nil
}

func (j negotiatedJournal) prompting(ctx context.Context, buyer types.JID, amount string, payUntil time.Time) (bool, error) {
	ok, err := j.st.MoveSellTaskBuyer(ctx, j.t.ID, buyer.String(), store.BuyerAgreed, store.BuyerOffered, amount, payUntil)
	if err == nil && !ok {
		err = errors.New("buyer was already prompted, or the task ended")
	}
	return ok, err
}

func (j negotiatedJournal) turnEnded(ctx context.Context, buyer types.JID, state string) error {
	if state == store.BuyerSkipped {
		// They passed or were parked while waiting; their session already
		// recorded it.
		return nil
	}
	_, err := j.st.MoveSellTaskBuyer(ctx, j.t.ID, buyer.String(), store.BuyerOffered, state, "", time.Time{})
	j.n.turnOver(buyer)
	return err
}

func (j negotiatedJournal) watchDrop(buyer types.JID) <-chan struct{} {
	return j.n.watchDrop(buyer)
}

func (j negotiatedJournal) dropped(ctx context.Context, buyer types.JID, handedOff bool) error {
	return j.n.droppedTurn(ctx, buyer, handedOff)
}

func (j negotiatedJournal) accepts(buyer types.JID, agreed string) []string {
	return j.n.accepts(buyer, agreed)
}

func (j negotiatedJournal) reprice(ctx context.Context, d store.Delivery, amount string) (store.Delivery, error) {
	return j.st.RepriceTaskDelivery(ctx, j.t.ID, d, amount)
}

// sellNegotiated runs the sessions and the pay queue until the meal sells,
// is handed off, or the task stops.
func (m *sellManager) sellNegotiated(ctx context.Context, n *negotiation, s *sale, j *taskJournal, resume *resumedTurn) error {
	n.start()
	defer n.stop()
	sold := s.sold
	s.sold = func() {
		n.halt()
		if sold != nil {
			sold()
		}
	}
	s.others = n.others
	if pooled(n.t) {
		// The meal sold: buyers still in play move to the rest of the
		// pool, once their sessions have stopped.
		s.keep = func(ctx context.Context, buyers []types.JID) []types.JID {
			n.hush()
			n.wg.Wait()
			moved := m.passOn(ctx, n.t)
			return slices.DeleteFunc(buyers, func(b types.JID) bool { return moved[b.String()] })
		}
	}
	nj := negotiatedJournal{taskJournal: j, n: n}
	notifyCtx := context.WithoutCancel(ctx)
	err := s.sellToQueue(n.ctx, notifyCtx, n.pay, nj, 0, resume)
	if buyer, ok := n.directSale(); ok {
		if derr := <-n.direct; derr != nil {
			return derr
		}
		s.sold()
		if _, err := m.st.MoveSellTaskBuyerFrom(notifyCtx, n.t.ID, buyer.String(),
			[]string{store.BuyerQueued, store.BuyerNegotiating, store.BuyerAgreed}, store.BuyerBought); err != nil {
			m.log.Warn("could not record the sale", "err", err)
		}
		waiting := append(n.pay.closeAndDrain(), n.others()...)
		if s.keep != nil {
			waiting = s.keep(notifyCtx, waiting)
		}
		s.tellSold(notifyCtx, nj, waiting)
		return nil
	}
	return err
}

// describeAgreement says what the bot agreed with a buyer it handed over.
func describeAgreement(amount string) string {
	return "The bot agreed INR " + strings.TrimSuffix(amount, ".00") + " with them and has not asked them to pay."
}

// busy reports whether a buyer is paying for the task's meal or waiting
// to.
func (n *negotiation) busy() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sold || !n.holder.IsEmpty() || n.pay.waiting() > 0
}

func (n *negotiation) isMoved(peer types.JID) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.moved[peer]
}

// passAgreed hands a buyer who just agreed amount to task to in the pool,
// which asks them to pay. It reports false, leaving them here, when the
// store refuses.
func (n *negotiation) passAgreed(ctx context.Context, ss *session, to int64, amount string, next haggleState, started time.Time) bool {
	ok, err := n.m.st.PassSellTaskBuyer(ctx, n.t.ID, to, ss.buyer.String(), amount, next.store(started), time.Now())
	if err != nil || !ok {
		n.log.Warn("could not pass an agreed buyer to another task in the pool", "to", to, "buyer", ss.buyer, "amount", amount, "err", err)
		return false
	}
	n.drop(ss.buyer)
	notify := context.WithoutCancel(ctx)
	n.m.notePassed(notify, n.t.ID, to, ss.buyer.String(), paise(amount))
	n.m.event(notify, store.SellEvent{TaskID: to, Peer: ss.buyer.String(), Kind: store.EventAgreed, N: paise(amount)})
	n.m.adoptPassed(notify, to, ss.buyer.String())
	return true
}

// drop forgets a buyer passed to another task in the pool.
func (n *negotiation) drop(peer types.JID) {
	n.mu.Lock()
	if ss := n.sessions[peer]; ss != nil {
		ss.state = sessionEnded
		n.wakeLocked()
	}
	n.moved[peer] = true
	n.mu.Unlock()
	n.join.remove(peer)
	n.pay.remove(peer)
}

// adopt takes a buyer another task in the pool passed here, as the store
// has them now: an agreed buyer queues to pay, one who was haggling
// carries on from the bot's last ask, and one who timed out can be revived
// by a late reply. A task that is selling to someone else already leaves
// them in the store, for its own end to pass on again.
func (n *negotiation) adopt(b store.SellBuyer) {
	peer, err := types.ParseJID(b.Peer)
	if err != nil {
		return
	}
	n.join.restore(peer, false)
	n.mu.Lock()
	if n.sold || n.talk.Err() != nil {
		n.mu.Unlock()
		return
	}
	delete(n.moved, peer)
	ss := n.sessionLocked(peer)
	ss.joined, ss.started = b.JoinedAt, b.StartedAt
	ss.hs = haggleFromStore(b.Negotiation, n.h.max)
	switch b.State {
	case store.BuyerAgreed:
		ss.state, ss.agreed = sessionAgreed, b.Agreed
		if a, err := store.ParsePaise(b.Agreed); err == nil {
			n.pay.setPriority(peer, a)
		}
		n.pay.requeue(peer)
	case store.BuyerQueued, store.BuyerNegotiating:
		ss.state = sessionNegotiating
	default:
		ss.state = sessionEnded
	}
	state := ss.state
	n.mu.Unlock()
	n.log.Info("took a buyer from another task in the pool", "buyer", peer, "state", b.State)
	switch state {
	case sessionAgreed:
		n.m.syncPoolPause(n.t)
	case sessionNegotiating:
		go func() {
			select {
			case n.revived <- ss:
			case <-n.talk.Done():
			}
		}()
	}
}
