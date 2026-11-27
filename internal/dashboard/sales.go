package dashboard

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	marketask "messwa/internal/market"
	"messwa/internal/mcpserver"
	"messwa/internal/meals"
	"messwa/internal/portal"
	"messwa/internal/store"
)

// The sales page covers the last four weeks, and the whole month so far
// when that started earlier. Buyers are counted over all time.
const salesDays = 28

// A median from fewer asks than this falls back to a wider pool.
const minAsks = marketask.MinAsks

type salesState struct {
	Now      int64        `json:"now"`
	TZ       string       `json:"tz"`
	Today    string       `json:"today"`
	From     string       `json:"from"`   // first day in Days
	Recent   string       `json:"recent"` // first day of the four-week window
	Messes   []mess       `json:"messes"`
	Windows  []slot       `json:"windows"` // today's meal windows
	Days     []salesDay   `json:"days"`
	Upcoming []salesDay   `json:"upcoming"` // next four weeks, separate from historical totals
	Sales    []sale       `json:"sales"`
	Market   []mealMarket `json:"market"`
	Month    month        `json:"month"`
	// Portal says why registrations are missing. Empty when they loaded.
	Portal string `json:"portal,omitempty"`
	// Owner is true for admins, who see the linked WhatsApp account's
	// sales. Everyone else sees only their own registrations.
	Owner bool `json:"owner"`
	// SellOff says why the ledger can't start a sale right now (sell
	// tasks off, WhatsApp not linked). Empty when it can.
	SellOff string `json:"sell_off,omitempty"`
	// Friends are meals you sold, or are selling, for someone else. They
	// stay out of Sales, the ledger and every total.
	Friends []friendMeal `json:"friends"`
	// Ahead is tomorrow's first meal once today's last one has ended, so
	// it can be sold the night before. Days stop at today; Ahead is
	// outside them and every total. Owners only.
	Ahead *aheadCell `json:"ahead,omitempty"`
	// ListAt is [sell] list_at in seconds: when a sale started before its
	// meal goes up, from the meal's start.
	ListAt int64 `json:"list_at"`
	// FriendSlots are the meals a friend's meal can be sold for: today's
	// that haven't ended, and the next one.
	FriendSlots []slot `json:"friend_slots"`
}

// friendMeal is one friend's meal and what became of it. State is
// scheduled, selling, armed, sending, sold, handoff, or ended (stopped, expired or
// failed unsold).
type friendMeal struct {
	Date   string    `json:"date"`
	Meal   string    `json:"meal"` // breakfast, lunch, ...
	Slot   string    `json:"slot"` // dinner#f1
	Mess   string    `json:"mess"`
	Diet   string    `json:"diet,omitempty"`
	Friend string    `json:"friend,omitempty"`
	Extras []string  `json:"extras,omitempty"`
	Start  int64     `json:"start"`
	End    int64     `json:"end"`
	State  string    `json:"state"`
	Detail string    `json:"detail,omitempty"`
	Amount float64   `json:"amount,omitempty"`
	Buyer  string    `json:"buyer,omitempty"` // untrusted push name, else phone
	Task   *cellTask `json:"task,omitempty"`
	Ask    *float64  `json:"ask,omitempty"`
	// Schedule is set while the sale waits to start; State is scheduled.
	Schedule *cellSchedule `json:"schedule,omitempty"`
	// Opens and OpensAfter are as in cell.
	Opens      int64  `json:"opens,omitempty"`
	OpensAfter string `json:"opens_after,omitempty"`
}

type salesDay struct {
	Date  string `json:"date"`
	Cells []cell `json:"cells"`
}

// cell is one registered (or sold) meal. State is one of:
//
//	sold     the buyer paid and the QR went out
//	sending  the buyer paid; the send was interrupted and may not have arrived
//	armed    waiting for the buyer's payment
//	handoff  a buyer replied and the bot handed the chat to you; how it ended is unknown
//	ate      you used the registration yourself
//	wasted   the meal ended unsold and unused
//	open     registered, not sold yet, and the meal hasn't ended
//	selling  open, with a sell task running for it
//	scheduled open, with a sale waiting to start before or during the meal
//	none     not registered
//	unknown  the portal couldn't be read, so eaten, wasted or unregistered is unknown
type cell struct {
	Meal  string   `json:"meal"`
	State string   `json:"state"`
	Mess  string   `json:"mess,omitempty"`
	Diet  string   `json:"diet,omitempty"`
	Start int64    `json:"start"`
	End   int64    `json:"end"`
	Sale  *int     `json:"sale,omitempty"` // index into Sales
	Ask   *float64 `json:"ask,omitempty"`  // median asking price, what it could have fetched
	// Task is the running sell task for this meal, if any.
	Task *cellTask `json:"task,omitempty"`
	// Schedule is the sale waiting to start for this meal, if any; the
	// state is then scheduled.
	Schedule *cellSchedule `json:"schedule,omitempty"`
	// Sellable is set on the open meal being served now and the next one,
	// the meals the ledger offers to sell.
	Sellable      bool   `json:"sellable,omitempty"`
	Kept          bool   `json:"kept,omitempty"`
	ScheduleIssue string `json:"schedule_issue,omitempty"`
	ScheduleState string `json:"schedule_state,omitempty"`
	// Opens is the earliest its offer may go up, when the meal before it,
	// OpensAfter, ends. Start now waits for it.
	Opens      int64  `json:"opens,omitempty"`
	OpensAfter string `json:"opens_after,omitempty"`
	// Extras lists registered extras (such as Egg) verified from the mess
	// portal for this meal, along with any separate extra sales/schedules.
	Extras           []cellExtra `json:"extras,omitempty"`
	ExtrasErr        string      `json:"extras_err,omitempty"`
	ExtrasVerifiedAt int64       `json:"extras_verified_at,omitempty"`
}

// cellExtra is one registered extra item on a meal (e.g. "Egg"), combining
// live mess-portal counts with separately sold/selling/scheduled extra slots.
type cellExtra struct {
	Name      string      `json:"name"`
	Mess      string      `json:"mess,omitempty"`
	Rate      float64     `json:"rate,omitempty"`
	Total     int         `json:"total"`
	Availed   int         `json:"availed"`
	Unavailed int         `json:"unavailed"`
	Available int         `json:"available"`
	Sold      int         `json:"sold"`
	Selling   int         `json:"selling"`
	Scheduled int         `json:"scheduled"`
	Earned    float64     `json:"earned,omitempty"`
	Slots     []extraSlot `json:"slots,omitempty"`
}

// extraSlot is one separately sold, selling, or scheduled extra slot
// (e.g. "dinner#e1").
type extraSlot struct {
	Slot     string        `json:"slot"`
	State    string        `json:"state"` // selling, scheduled, armed, sending, sold, handoff
	Amount   float64       `json:"amount,omitempty"`
	Buyer    string        `json:"buyer,omitempty"`
	Task     *cellTask     `json:"task,omitempty"`
	Schedule *cellSchedule `json:"schedule,omitempty"`
}

// cellTask is a running sell task: its price range in rupees and who has
// replied so far, for the ledger's sell menu.
type cellTask struct {
	ID     int64       `json:"id"`
	Min    float64     `json:"min"`
	Max    float64     `json:"max"`
	Buyers []taskBuyer `json:"buyers"`
	// Autopilot skips buyers who would need you.
	Autopilot bool `json:"autopilot,omitempty"`
}

// aheadCell is a meal on a day after Days.
type aheadCell struct {
	Date string `json:"date"`
	Cell cell   `json:"cell"`
}

// cellSchedule is a sale waiting for its meal: it starts a sell task at
// At (unix seconds) with this price range in rupees.
type cellSchedule struct {
	RuleID     int64   `json:"rule_id,omitempty"`
	Overridden bool    `json:"overridden,omitempty"`
	Revision   int64   `json:"revision"`
	State      string  `json:"state"`
	Detail     string  `json:"detail,omitempty"`
	RetryAt    int64   `json:"retry_at,omitempty"`
	ID         int64   `json:"id"`
	At         int64   `json:"at"`
	Min        float64 `json:"min"`
	Max        float64 `json:"max"`
	Autopilot  bool    `json:"autopilot,omitempty"`
}

// taskBuyer is one of a running task's buyers. Key is what the sell
// actions take to name them. Price is what they agreed, were asked, or
// last offered, in that order; 0 when there's none.
type taskBuyer struct {
	Key   string  `json:"key"`
	Name  string  `json:"name,omitempty"` // untrusted push name
	Phone string  `json:"phone,omitempty"`
	State string  `json:"state"`
	Price float64 `json:"price,omitempty"`
	Muted bool    `json:"muted,omitempty"`
}

type sale struct {
	Date   string  `json:"date"`
	Meal   string  `json:"meal"`
	Slot   string  `json:"slot,omitempty"`
	Extra  string  `json:"extra,omitempty"`
	Mess   string  `json:"mess,omitempty"`
	Diet   string  `json:"diet,omitempty"`
	Amount float64 `json:"amount"`
	// State is sold, sending, armed, lapsed (armed but the meal ended), or
	// handoff (you took over the chat; Amount is what the offer asked).
	State string `json:"state"`
	// Buyer groups a buyer's sales: their phone number when known, else the JID.
	Buyer   string `json:"buyer"`
	Name    string `json:"name,omitempty"` // untrusted push name
	Phone   string `json:"phone,omitempty"`
	End     int64  `json:"end"`
	ArmedAt int64  `json:"armed_at"`
	OfferAt int64  `json:"offer_at,omitempty"`
	PaidAt  int64  `json:"paid_at,omitempty"`
	SentAt  int64  `json:"sent_at,omitempty"`
	// PaidBy is "image" when an image the buyer sent counted as the
	// payment, unverified ([sell] accept_payment_images or mark-sold
	// -paid-by image), or "other" for a sale mark-sold recorded as paid
	// another way, or "manual" for a sell task marked sold by the user.
	// Empty means WhatsApp Pay.
	PaidBy string   `json:"paid_by,omitempty"`
	Ask    *float64 `json:"ask,omitempty"` // median asking price for that serving
}

// mealMarket is the spread of asks for one meal in one diet: veg, egg,
// nonveg, or "" for every diet together.
type mealMarket struct {
	Meal   string  `json:"meal"`
	Diet   string  `json:"diet"`
	Q1     float64 `json:"q1"`
	Median float64 `json:"median"`
	Q3     float64 `json:"q3"`
	N      int     `json:"n"`
}

type month struct {
	Start string `json:"start"`
	// Fee is the month's mess bill in rupees: the portal's bill when it
	// loads, else sales.monthly_fee. 0 when neither is there.
	Fee float64 `json:"fee"`
	// Bill splits Fee when it came from the portal.
	Bill         *bill   `json:"bill,omitempty"`
	BillErr      string  `json:"bill_err,omitempty"`
	Earned       float64 `json:"earned"`
	Sold         int     `json:"sold"`
	ExtrasSold   int     `json:"extras_sold,omitempty"`
	ExtrasEarned float64 `json:"extras_earned,omitempty"`
	Wasted       int     `json:"wasted"`
	// Potential is what the wasted meals would have fetched at the median
	// ask. Unpriced counts wasted meals with no market price to go on.
	Potential float64 `json:"potential"`
	Unpriced  int     `json:"unpriced"`
}

// bill is the portal's bill for the month, in rupees. Availed is for meals
// already served, Projected for registered meals still to come.
type bill struct {
	Availed   float64 `json:"availed"`
	Projected float64 `json:"projected"`
}

// readRegistrations reads the portal with that user's own mess API key.
// Tests replace it.
var readRegistrations = func(ctx context.Context, acc *accounts.Accounts, userID int64, from, to string) ([]portal.Registration, error) {
	c, err := acc.Portal(ctx, userID)
	if err != nil {
		return nil, err
	}
	return c.Registrations(ctx, from, to)
}

// readBill reads that user's mess bill for a month. Tests replace it.
var readBill = func(ctx context.Context, acc *accounts.Accounts, userID int64, year, month int) (portal.Bill, error) {
	c, err := acc.Portal(ctx, userID)
	if err != nil {
		return portal.Bill{}, err
	}
	return c.Bill(ctx, year, month)
}

// readExtras reads that user's registered extras for one meal. Tests replace it.
var readExtras = func(ctx context.Context, acc *accounts.Accounts, userID int64, date, meal string) ([]portal.ExtraRegistration, error) {
	if acc == nil {
		return nil, portal.ErrNoKey
	}
	c, err := acc.Portal(ctx, userID)
	if err != nil {
		return nil, err
	}
	return c.ExtraRegistrations(ctx, date, meal)
}

// regCache keeps each user's last portal reads in memory for a minute, so
// live updates on the page don't hit the portal for every market post.
type regCache struct {
	mu     sync.Mutex
	acc    *accounts.Accounts
	users  map[int64]*regEntry
	bills  map[int64]*billEntry
	extras map[int64]map[string]*extrasEntry
}

type regEntry struct {
	key  string
	at   time.Time
	rows []portal.Registration
	err  error
}

type billEntry struct {
	year, month int
	at          time.Time
	bill        portal.Bill
	err         error
}

type extrasEntry struct {
	at   time.Time
	rows []portal.ExtraRegistration
	err  error
}

func newRegCache(acc *accounts.Accounts) *regCache {
	return &regCache{acc: acc, users: map[int64]*regEntry{}, bills: map[int64]*billEntry{}, extras: map[int64]map[string]*extrasEntry{}}
}

func (c *regCache) getExtras(ctx context.Context, userID int64, date, meal string, refresh bool) ([]portal.ExtraRegistration, time.Time, error) {
	key := date + "|" + meal
	c.mu.Lock()
	if !refresh {
		if m := c.extras[userID]; m != nil {
			if e := m[key]; e != nil && time.Since(e.at) < time.Minute {
				c.mu.Unlock()
				return e.rows, e.at, e.err
			}
		}
	}
	c.mu.Unlock()
	rows, err := readExtras(ctx, c.acc, userID, date, meal)
	now := time.Now()
	c.mu.Lock()
	if c.extras[userID] == nil {
		c.extras[userID] = map[string]*extrasEntry{}
	}
	c.extras[userID][key] = &extrasEntry{at: now, rows: rows, err: err}
	c.mu.Unlock()
	return rows, now, err
}

func (c *regCache) bill(ctx context.Context, userID int64, year, month int) (portal.Bill, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.bills[userID]
	if e != nil && e.year == year && e.month == month && time.Since(e.at) < time.Minute {
		return e.bill, e.err
	}
	e = &billEntry{year: year, month: month, at: time.Now()}
	e.bill, e.err = readBill(ctx, c.acc, userID, year, month)
	c.bills[userID] = e
	return e.bill, e.err
}

func (c *regCache) get(ctx context.Context, userID int64, from, to string) ([]portal.Registration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := from + ".." + to
	e := c.users[userID]
	if e != nil && e.key == key && time.Since(e.at) < time.Minute {
		return e.rows, e.err
	}
	e = &regEntry{key: key, at: time.Now()}
	e.rows, e.err = readRegistrations(ctx, c.acc, userID, from, to)
	c.users[userID] = e
	return e.rows, e.err
}

// forget drops a user's cached read, after their key changes.
func (c *regCache) forget(userID int64) {
	c.mu.Lock()
	delete(c.users, userID)
	delete(c.bills, userID)
	delete(c.extras, userID)
	c.mu.Unlock()
}

func salesHandler(cfg *config.Config, srv *mcpserver.Server, cache *regCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := buildSales(r.Context(), cfg, srv, cache, accounts.UserFrom(r.Context()))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, r, st)
	}
}

// buildSales is the sales page for user u. Registrations come from u's own
// mess API key. Sales belong to the linked WhatsApp account, so only admins
// get them.
func buildSales(ctx context.Context, cfg *config.Config, srv *mcpserver.Server, cache *regCache, u *accounts.User) (salesState, error) {
	loc := cfg.Location()
	now := srv.Now()
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	recent := today.AddDate(0, 0, 1-salesDays)
	monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
	from := recent
	if monthStart.Before(from) {
		from = monthStart
	}
	ymd := func(t time.Time) string { return t.Format("2006-01-02") }
	out := salesState{Now: now.Unix(), TZ: cfg.Timezone, Today: ymd(today), From: ymd(from), Recent: ymd(recent),
		Days: []salesDay{}, Sales: []sale{}, Friends: []friendMeal{}, FriendSlots: []slot{}, Market: []mealMarket{}, Month: month{Start: ymd(monthStart), Fee: float64(cfg.Live().Sales.MonthlyFee)}, Owner: srv.Scoped() || u.IsAdmin(),
		ListAt: int64(cfg.Live().Sell.ListAt.Seconds())}
	for _, m := range cfg.Messes {
		out.Messes = append(out.Messes, mess{m.Name, m.VegOnly})
	}
	sched := srv.MealSchedule()
	for _, s := range sched.Windows(now) {
		out.Windows = append(out.Windows, toSlot(&s))
	}

	ps, err := srv.Prices(ctx, ymd(from), ymd(today))
	if err != nil {
		return out, err
	}
	asks := newAsks(ps, ymd(recent))
	for _, m := range cfg.Meals {
		if cfg.Live().HiddenMeal(m.Name) {
			continue
		}
		for _, d := range []string{"", "veg", "egg", "nonveg"} {
			if mm, ok := asks.market(m.Name, d); ok {
				out.Market = append(out.Market, mm)
			}
		}
	}

	var ds []store.DeliveryRecord
	running := map[string]*cellTask{}
	waiting := map[string]*cellSchedule{}
	issues := map[string]store.SellSchedule{}
	// Extra slots grouped by "date|baseMeal" so they attach to their meal cell.
	extraTasks := map[string][]extraTaskRec{}
	extraSchedules := map[string][]extraSchedRec{}
	extraDeliveries := map[string][]extraDelivRec{}
	// ahead is the next meal when it is tomorrow's, after today's last one.
	var ahead *meals.Slot
	sellable := map[string]bool{}
	if out.Owner {
		if ds, err = srv.Deliveries(ctx); err != nil {
			return out, err
		}
		views, err := srv.SaleViews(ctx)
		if err != nil {
			return out, err
		}
		for _, v := range views {
			if v.State != store.TaskActive {
				continue
			}
			t := &cellTask{ID: v.ID, Min: v.Min, Max: v.Max, Buyers: []taskBuyer{}, Autopilot: v.Autopilot}
			for _, b := range v.Buyers {
				price := b.Agreed
				if price == 0 {
					price = b.Asked
				}
				if price == 0 {
					price = b.Offer
				}
				t.Buyers = append(t.Buyers, taskBuyer{Key: b.Key, Name: b.Name, Phone: b.Phone, State: b.State, Price: price, Muted: b.Muted})
			}
			running[v.Date+"|"+v.Meal] = t
			if v.Extra != "" || store.IsExtraSlot(v.Meal) {
				bk := v.Date + "|" + store.BaseMeal(v.Meal)
				extraTasks[bk] = append(extraTasks[bk], extraTaskRec{slot: v.Meal, extra: v.Extra, mess: v.Mess, task: t})
			}
		}
		scs, err := srv.SellSchedules(ctx)
		if err != nil {
			return out, err
		}
		for _, sc := range scs {
			if _, ok := issues[sc.Date+"|"+sc.Meal]; !ok {
				issues[sc.Date+"|"+sc.Meal] = sc
			}
			if sc.State == store.ScheduleWaiting || sc.State == store.ScheduleStarting {
				cs := &cellSchedule{ID: sc.ID, RuleID: sc.RuleID, Overridden: sc.Overridden, Revision: sc.Revision, State: sc.State, Detail: sc.Detail, RetryAt: unix(sc.RetryAt), At: srv.ScheduledAt(sc).Unix(), Min: float64(sc.MinPaise) / 100, Max: float64(sc.MaxPaise) / 100, Autopilot: sc.Autopilot}
				waiting[sc.Date+"|"+sc.Meal] = cs
				if sc.Extra != "" || store.IsExtraSlot(sc.Meal) {
					bk := sc.Date + "|" + store.BaseMeal(sc.Meal)
					extraSchedules[bk] = append(extraSchedules[bk], extraSchedRec{slot: sc.Meal, extra: sc.Extra, sched: cs})
				}
			}
		}
		if _, err := srv.SellControl(); err != nil {
			out.SellOff = err.Error()
		}
		sc := srv.Schedule()
		for _, w := range []*meals.Slot{sc.Current, sc.Next} {
			if w != nil {
				sellable[w.Date+"|"+w.Meal] = true
			}
		}
		if sc.Next != nil && sc.Next.Date > ymd(today) {
			ahead = sc.Next
		}
	}
	byMeal := map[string]int{}
	slotSales := map[string]store.DeliveryRecord{} // date|slot, for friends' meals
	for _, d := range ds {
		if d.State != "armed" && d.State != "sending" && d.State != "sent" && d.State != "handed_off" {
			continue
		}
		isExtra := isExtraDelivery(d)
		// Friends' meals ("dinner#f1") and legacy proxy slots ("dinner#2") stay out of your totals.
		if isFriendDelivery(d) {
			slotSales[d.Date+"|"+d.Meal] = d
			continue
		}
		baseMeal := store.BaseMeal(d.Meal)
		day, err := time.ParseInLocation("2006-01-02", d.Date, loc)
		if err != nil {
			continue
		}
		var end time.Time
		for _, w := range sched.Windows(day) {
			if w.Meal == baseMeal {
				end = w.End
			}
		}
		amount, _ := strconv.ParseFloat(d.Amount, 64)
		s := sale{Date: d.Date, Meal: baseMeal, Mess: d.Mess, Diet: d.Diet, Amount: amount, Name: d.BuyerName,
			End: end.Unix(), ArmedAt: unix(d.ArmedAt), OfferAt: unix(d.OfferAt), PaidAt: unix(d.PaidAt), SentAt: unix(d.SentAt), PaidBy: d.PaymentKind}
		if isExtra {
			s.Slot = d.Meal
			s.Extra = d.Extra
			if s.Extra == "" {
				s.Extra = "Extra"
			}
		}
		switch {
		case d.State == "sent":
			s.State = "sold"
		case d.State == "sending":
			s.State = "sending"
		case d.State == "handed_off":
			s.State = "handoff"
		case !end.IsZero() && !now.Before(end):
			s.State = "lapsed"
		default:
			s.State = "armed"
		}
		s.Phone = phone(d.Peer)
		if s.Phone == "" {
			s.Phone = phone(d.PeerAlt)
		}
		s.Buyer = s.Phone
		if s.Buyer == "" {
			s.Buyer = d.Peer
		}
		if isExtra {
			bk := d.Date + "|" + baseMeal
			var sentAt time.Time
			if d.State == "sent" {
				sentAt = d.SentAt
			}
			extraDeliveries[bk] = append(extraDeliveries[bk], extraDelivRec{slot: d.Meal, extra: s.Extra, mess: d.Mess, state: s.State, amount: amount, buyer: firstStr(s.Name, s.Phone, s.Buyer), task: running[d.Date+"|"+d.Meal], sentAt: sentAt})
		} else {
			byMeal[d.Date+"|"+d.Meal] = len(out.Sales)
		}
		out.Sales = append(out.Sales, s)
	}

	if b, err := cache.bill(ctx, u.ID, local.Year(), int(local.Month())); err != nil {
		out.Month.BillErr = err.Error()
	} else {
		out.Month.Bill = &bill{Availed: float64(b.Availed) / 100, Projected: float64(b.Projected) / 100}
		out.Month.Fee = float64(b.Total()) / 100
	}

	kept, err := srv.KeptMeals(ctx, u.ID)
	if err != nil {
		return out, err
	}
	regs := map[string][]portal.Registration{}
	// Owners can plan registered meals over the next four weeks.
	to := ymd(today)
	if out.Owner {
		to = ymd(today.AddDate(0, 0, 27))
	}
	rows, err := cache.get(ctx, u.ID, ymd(from), to)
	if err != nil {
		out.Portal = err.Error()
	}
	for _, r := range rows {
		if !r.Cancelled() {
			regs[r.Date+"|"+r.Meal] = append(regs[r.Date+"|"+r.Meal], r)
		}
	}

	// Fetch portal extras for today and tomorrow's meals (including cancelled
	// main meals, since registered extras can still be sold separately), plus
	// any active/upcoming meal (date >= today) that already has extra slots.
	todayStr := ymd(today)
	tomorrowStr := ymd(today.AddDate(0, 0, 1))
	wantExtras := map[string][2]string{}
	for _, r := range rows {
		if r.Date == todayStr || r.Date == tomorrowStr {
			wantExtras[r.Date+"|"+r.Meal] = [2]string{r.Date, r.Meal}
		}
	}
	for _, mp := range []map[string]struct{}{
		keysOf(extraTasks), keysOf(extraSchedules), keysOf(extraDeliveries),
	} {
		for k := range mp {
			if d, m, ok := strings.Cut(k, "|"); ok && d >= todayStr {
				wantExtras[k] = [2]string{d, m}
			}
		}
	}
	portalExtras := map[string][]portal.ExtraRegistration{}
	portalExtrasErr := map[string]string{}
	portalExtrasAt := map[string]int64{}
	if len(wantExtras) > 0 && out.Portal == "" {
		var wg sync.WaitGroup
		var emu sync.Mutex
		sem := make(chan struct{}, 4)
		for k, dm := range wantExtras {
			wg.Add(1)
			go func(key, date, meal string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				erows, fetchedAt, eerr := cache.getExtras(ctx, u.ID, date, meal, false)
				emu.Lock()
				defer emu.Unlock()
				if !fetchedAt.IsZero() {
					portalExtrasAt[key] = fetchedAt.Unix()
				}
				if eerr != nil {
					if !errors.Is(eerr, portal.ErrNoKey) {
						portalExtrasErr[key] = eerr.Error()
					}
					return
				}
				portalExtras[key] = erows
			}(k, dm[0], dm[1])
		}
		wg.Wait()
	}

	// cellFor is what became of the meal in window w on date.
	cellFor := func(date string, w meals.Slot) cell {
		key := date + "|" + w.Meal
		c := cell{Meal: w.Meal, Start: w.Start.Unix(), End: w.End.Unix(), State: "none", Kept: kept[key]}
		c.Opens, c.OpensAfter = opensAt(srv, date, w.Meal)
		if issue, ok := issues[key]; ok && (issue.State == store.ScheduleFailed || issue.State == store.ScheduleCancelled) {
			c.ScheduleIssue = issue.Detail
			c.ScheduleState = issue.State
		}
		rs := regs[key]
		if len(rs) > 0 {
			c.Mess, c.Diet = rs[0].MessDiet()
			if c.Diet == "" && cfg.VegOnly(c.Mess) {
				c.Diet = "veg"
			}
		}
		i, sold := byMeal[key]
		if sold {
			s := &out.Sales[i]
			if s.Mess == "" {
				s.Mess, s.Diet = c.Mess, c.Diet
			}
			c.Mess, c.Diet, c.Sale = s.Mess, s.Diet, &i
		}
		switch {
		case sold && out.Sales[i].State == "handoff" && availed(rs):
			// You kept the meal after taking the chat over.
			c.State = "ate"
		case sold && out.Sales[i].State != "lapsed":
			c.State = out.Sales[i].State
		case out.Portal != "":
			c.State = "unknown"
		case len(rs) == 0:
			c.State = "none"
		case availed(rs):
			c.State = "ate"
		case !now.Before(w.End):
			c.State = "wasted"
		default:
			c.State = "open"
			c.Sellable = sellable[key]
		}
		// A task stays active while a buyer is asked to pay, so an
		// armed meal can still have one.
		if c.Task = running[key]; c.Task != nil && c.State == "open" {
			c.State = "selling"
		}
		if c.Schedule = waiting[key]; c.Schedule != nil && c.State == "open" {
			c.State = "scheduled"
		}
		if c.State != "none" && c.State != "ate" {
			c.Ask = asks.estimate(date, w.Meal, c.Mess, c.Diet)
		}
		if exs := buildCellExtras(portalExtras[key], extraDeliveries[key], extraTasks[key], extraSchedules[key], c.Mess, loc); len(exs) > 0 {
			c.Extras = exs
			if c.Mess == "" && exs[0].Mess != "" {
				c.Mess = exs[0].Mess
			}
		}
		if vat := portalExtrasAt[key]; vat > 0 {
			c.ExtrasVerifiedAt = vat
		}
		if eerr := portalExtrasErr[key]; eerr != "" {
			c.ExtrasErr = eerr
		}
		return c
	}
	for day := from; !day.After(today); day = day.AddDate(0, 0, 1) {
		sd := salesDay{Date: ymd(day)}
		for _, w := range sched.Windows(day) {
			c := cellFor(sd.Date, w)
			if c.State == "wasted" && !day.Before(monthStart) {
				out.Month.Wasted++
				if c.Ask != nil {
					out.Month.Potential += *c.Ask
				} else {
					out.Month.Unpriced++
				}
			}
			sd.Cells = append(sd.Cells, c)
		}
		out.Days = append(out.Days, sd)
	}
	if ahead != nil {
		if c := cellFor(ahead.Date, *ahead); c.State != "none" {
			out.Ahead = &aheadCell{Date: ahead.Date, Cell: c}
		}
	}
	if out.Owner {
		for day := today; !day.After(today.AddDate(0, 0, 27)); day = day.AddDate(0, 0, 1) {
			sd := salesDay{Date: ymd(day)}
			for _, w := range sched.Windows(day) {
				c := cellFor(sd.Date, w)
				c.Sellable = c.State == "open"
				sd.Cells = append(sd.Cells, c)
			}
			out.Upcoming = append(out.Upcoming, sd)
		}
	}

	if out.Owner {
		if err := friendMeals(ctx, srv, sched, asks, &out, ymd(recent), now, loc, running, waiting, slotSales); err != nil {
			return out, err
		}
	}

	for i := range out.Sales {
		s := &out.Sales[i]
		if s.Extra == "" {
			s.Ask = asks.estimate(s.Date, s.Meal, s.Mess, s.Diet)
		}
		if (s.State == "sold" || s.State == "sending") && s.Date >= out.Month.Start && s.Date <= out.Today {
			out.Month.Earned += s.Amount
			if s.Extra != "" {
				out.Month.ExtrasSold++
				out.Month.ExtrasEarned += s.Amount
			} else {
				out.Month.Sold++
			}
		}
	}
	return out, nil
}

type extraTaskRec struct {
	slot, extra, mess string
	task              *cellTask
}

type extraSchedRec struct {
	slot, extra string
	sched       *cellSchedule
}

type extraDelivRec struct {
	slot, extra, mess, state, buyer string
	amount                          float64
	task                            *cellTask
	sentAt                          time.Time
}

func firstStr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func keysOf[V any](m map[string]V) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

func extraSlotNum(slot string) int {
	_, suffix, ok := strings.Cut(slot, store.ExtraSlotPrefix)
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(suffix)
	return n
}

// buildCellExtras merges live mess-portal extra registrations with any
// separately sold, actively selling, or scheduled extra slots for one meal.
func buildCellExtras(rows []portal.ExtraRegistration, delivs []extraDelivRec, tasks []extraTaskRec, scheds []extraSchedRec, defaultMess string, loc *time.Location) []cellExtra {
	byKey := map[string]*cellExtra{}
	var order []string
	ensure := func(name, mess string) *cellExtra {
		name = portal.NormalizeExtraName(name)
		if name == "" {
			name = "Extra"
		}
		k := strings.ToLower(name)
		ce, ok := byKey[k]
		if !ok {
			ce = &cellExtra{Name: name, Mess: firstStr(mess, defaultMess)}
			byKey[k] = ce
			order = append(order, k)
		} else if ce.Mess == "" {
			ce.Mess = firstStr(mess, defaultMess)
		}
		return ce
	}
	for _, sum := range portal.SummarizeExtras(rows) {
		ce := ensure(sum.Name, sum.Mess)
		ce.Total = sum.Total
		ce.Availed = sum.Availed
		ce.Unavailed = sum.Unavailed
		if sum.RatePaise > 0 {
			ce.Rate = float64(sum.RatePaise) / 100
		}
	}
	delivSlots := map[string]bool{}
	soldSentAt := map[string][]time.Time{}
	for _, d := range delivs {
		if d.state == "lapsed" {
			continue
		}
		ce := ensure(d.extra, d.mess)
		k := strings.ToLower(ce.Name)
		delivSlots[d.slot] = true
		if d.state == "armed" {
			// Unpaid reservation: still in play (selling), not sold yet.
			ce.Selling++
		} else {
			ce.Sold++
			soldSentAt[k] = append(soldSentAt[k], d.sentAt)
			if d.state == "sold" || d.state == "sending" {
				ce.Earned += d.amount
			}
		}
		ce.Slots = append(ce.Slots, extraSlot{
			Slot: d.slot, State: d.state, Amount: d.amount, Buyer: d.buyer, Task: d.task,
		})
	}
	taskSlots := map[string]bool{}
	for _, t := range tasks {
		if delivSlots[t.slot] {
			continue
		}
		taskSlots[t.slot] = true
		ce := ensure(t.extra, t.mess)
		ce.Selling++
		ce.Slots = append(ce.Slots, extraSlot{Slot: t.slot, State: "selling", Task: t.task})
	}
	for _, sc := range scheds {
		if delivSlots[sc.slot] || taskSlots[sc.slot] {
			continue
		}
		ce := ensure(sc.extra, "")
		ce.Scheduled++
		ce.Slots = append(ce.Slots, extraSlot{Slot: sc.slot, State: "scheduled", Schedule: sc.sched})
	}
	out := make([]cellExtra, 0, len(order))
	for _, k := range order {
		ce := byKey[k]
		_, _, _, avail := portal.AvailableExtras(rows, ce.Name, soldSentAt[k], ce.Selling+ce.Scheduled, loc)
		ce.Available = avail
		sort.SliceStable(ce.Slots, func(i, j int) bool {
			ni, nj := extraSlotNum(ce.Slots[i].Slot), extraSlotNum(ce.Slots[j].Slot)
			if ni != nj {
				return ni < nj
			}
			return ce.Slots[i].Slot < ce.Slots[j].Slot
		})
		out = append(out, *ce)
	}
	return out
}

type mealExtrasResp struct {
	Date       string      `json:"date"`
	Meal       string      `json:"meal"`
	Extras     []cellExtra `json:"extras"`
	VerifiedAt int64       `json:"verified_at"`
	Error      string      `json:"error,omitempty"`
}

// mealExtrasHandler verifies and returns registered extras for one (date, meal)
// on demand, e.g. when inspecting an upcoming meal in the planner or clicking
// refresh.
func mealExtrasHandler(cfg *config.Config, srv *mcpserver.Server, cache *regCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		date := strings.TrimSpace(r.URL.Query().Get("date"))
		meal := strings.TrimSpace(r.URL.Query().Get("meal"))
		refresh := r.URL.Query().Get("refresh") == "1"
		liveCfg := cfg.Live()
		loc := liveCfg.Location()
		day, err := time.ParseInLocation("2006-01-02", date, loc)
		if err != nil {
			jsonError(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		validMeal := false
		for _, m := range liveCfg.Meals {
			if m.Name == meal {
				validMeal = true
				break
			}
		}
		if !validMeal {
			jsonError(w, "meal must name a configured meal", http.StatusBadRequest)
			return
		}
		var mealEnd time.Time
		for _, win := range meals.New(liveCfg).Windows(day) {
			if win.Meal == meal {
				mealEnd = win.End
				break
			}
		}
		now := srv.Now()
		u := accounts.UserFrom(ctx)
		owner := srv.Scoped() || u.IsAdmin()
		rows, fetchedAt, err := cache.getExtras(ctx, u.ID, date, meal, refresh)
		verifiedAt := now.Unix()
		if !fetchedAt.IsZero() {
			verifiedAt = fetchedAt.Unix()
		}
		resp := mealExtrasResp{Date: date, Meal: meal, Extras: []cellExtra{}, VerifiedAt: verifiedAt}
		if err != nil {
			resp.Error = err.Error()
		}
		var delivs []extraDelivRec
		var tasks []extraTaskRec
		var scheds []extraSchedRec
		if owner {
			running := map[string]*cellTask{}
			views, verr := srv.SaleViews(ctx)
			if verr != nil {
				jsonError(w, verr.Error(), http.StatusInternalServerError)
				return
			}
			for _, v := range views {
				if v.State != store.TaskActive || v.Date != date || store.BaseMeal(v.Meal) != meal {
					continue
				}
				if v.Extra == "" && !store.IsExtraSlot(v.Meal) {
					continue
				}
				t := &cellTask{ID: v.ID, Min: v.Min, Max: v.Max, Buyers: []taskBuyer{}, Autopilot: v.Autopilot}
				for _, b := range v.Buyers {
					price := b.Agreed
					if price == 0 {
						price = b.Asked
					}
					if price == 0 {
						price = b.Offer
					}
					t.Buyers = append(t.Buyers, taskBuyer{Key: b.Key, Name: b.Name, Phone: b.Phone, State: b.State, Price: price, Muted: b.Muted})
				}
				running[v.Meal] = t
				tasks = append(tasks, extraTaskRec{slot: v.Meal, extra: v.Extra, mess: v.Mess, task: t})
			}
			scs, serr := srv.SellSchedules(ctx)
			if serr != nil {
				jsonError(w, serr.Error(), http.StatusInternalServerError)
				return
			}
			for _, sc := range scs {
				if sc.Date != date || store.BaseMeal(sc.Meal) != meal || (sc.State != store.ScheduleWaiting && sc.State != store.ScheduleStarting) {
					continue
				}
				if sc.Extra == "" && !store.IsExtraSlot(sc.Meal) {
					continue
				}
				cs := &cellSchedule{ID: sc.ID, RuleID: sc.RuleID, Overridden: sc.Overridden, Revision: sc.Revision, State: sc.State, Detail: sc.Detail, RetryAt: unix(sc.RetryAt), At: srv.ScheduledAt(sc).Unix(), Min: float64(sc.MinPaise) / 100, Max: float64(sc.MaxPaise) / 100, Autopilot: sc.Autopilot}
				scheds = append(scheds, extraSchedRec{slot: sc.Meal, extra: sc.Extra, sched: cs})
			}
			ds, derr := srv.Deliveries(ctx)
			if derr != nil {
				jsonError(w, derr.Error(), http.StatusInternalServerError)
				return
			}
			for _, d := range ds {
				if d.Date != date || store.BaseMeal(d.Meal) != meal || (!store.IsExtraSlot(d.Meal) && d.Extra == "") {
					continue
				}
				var state string
				switch {
				case d.State == "sent":
					state = "sold"
				case d.State == "sending":
					state = "sending"
				case d.State == "handed_off":
					state = "handoff"
				case d.State == "armed" && !mealEnd.IsZero() && !now.Before(mealEnd):
					state = "lapsed"
				case d.State == "armed":
					state = "armed"
				default:
					continue
				}
				amount, _ := strconv.ParseFloat(d.Amount, 64)
				buyer := firstStr(d.BuyerName, phone(d.Peer), phone(d.PeerAlt), d.Peer)
				var sentAt time.Time
				if d.State == "sent" {
					sentAt = d.SentAt
				}
				delivs = append(delivs, extraDelivRec{slot: d.Meal, extra: firstStr(d.Extra, "Extra"), mess: d.Mess, state: state, amount: amount, buyer: buyer, task: running[d.Meal], sentAt: sentAt})
			}
		}
		resp.Extras = buildCellExtras(rows, delivs, tasks, scheds, "", loc)
		writeJSON(w, r, resp)
	}
}

// friendMeals fills out.Friends from the friends' meals saved since from,
// and out.FriendSlots from the schedule.
func friendMeals(ctx context.Context, srv *mcpserver.Server, sched *meals.Schedule, asks *asks, out *salesState, from string, now time.Time, loc *time.Location,
	running map[string]*cellTask, waiting map[string]*cellSchedule, sales map[string]store.DeliveryRecord) error {
	for _, w := range sched.Windows(now) {
		if now.Before(w.End) {
			out.FriendSlots = append(out.FriendSlots, toSlot(&w))
		}
	}
	if sc := srv.Schedule(); sc.Next != nil && sc.Next.Date != out.Today {
		out.FriendSlots = append(out.FriendSlots, toSlot(sc.Next))
	}
	fms, err := srv.FriendMeals(ctx, from)
	if err != nil {
		return err
	}
	tasks, err := srv.SellTasksSince(ctx, now.AddDate(0, 0, -salesDays-1))
	if err != nil {
		return err
	}
	last := map[string]store.SellTask{} // newest task per date|slot
	for _, t := range tasks {
		k := t.Date + "|" + t.Meal
		if cur, ok := last[k]; !ok || t.CreatedAt.After(cur.CreatedAt) {
			last[k] = t
		}
	}
	for _, m := range fms {
		if srv.Config().Live().HiddenMeal(m.Slot) {
			continue
		}
		k := m.Date + "|" + m.Slot
		base, _, _ := strings.Cut(m.Slot, "#")
		f := friendMeal{Date: m.Date, Meal: base, Slot: m.Slot, Friend: m.Friend, Extras: m.Extras, State: "ended", Task: running[k]}
		f.Opens, f.OpensAfter = opensAt(srv, m.Date, base)
		f.Mess, f.Diet = (portal.Registration{Mess: m.Mess}).MessDiet()
		f.Ask = asks.estimate(m.Date, base, f.Mess, f.Diet)
		if day, err := time.ParseInLocation("2006-01-02", m.Date, loc); err == nil {
			for _, w := range sched.Windows(day) {
				if w.Meal == base {
					f.Start, f.End = w.Start.Unix(), w.End.Unix()
				}
			}
		}
		if t, ok := last[k]; ok {
			f.Detail = t.Detail
			if t.State == store.TaskActive {
				f.State = "selling"
			}
		}
		if f.Schedule = waiting[k]; f.Schedule != nil && f.State == "ended" {
			f.State = "scheduled"
		}
		if d, ok := sales[k]; ok {
			f.State = map[string]string{"sent": "sold", "sending": "sending", "armed": "armed", "handed_off": "handoff"}[d.State]
			if f.State == "armed" && f.End > 0 && now.Unix() >= f.End {
				f.State = "ended"
			}
			f.Amount, _ = strconv.ParseFloat(d.Amount, 64)
			f.Buyer = d.BuyerName
			if f.Buyer == "" {
				f.Buyer = phone(d.Peer)
			}
		}
		out.Friends = append(out.Friends, f)
	}
	return nil
}

func availed(rs []portal.Registration) bool {
	for _, r := range rs {
		if r.Availed() {
			return true
		}
	}
	return false
}

// opensAt is when an offer for date's meal may go up, in unix seconds, and
// the meal it waits for. Zero and empty when there's none.
func opensAt(srv *mcpserver.Server, date, meal string) (int64, string) {
	prev, ok := srv.PrevMeal(date, meal)
	if !ok {
		return 0, ""
	}
	return prev.End.Unix(), prev.Meal
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// phone returns "+<digits>" for a phone-number JID and "" for anything else,
// such as a LID.
func phone(jid string) string {
	user, server, ok := strings.Cut(jid, "@")
	if !ok || server != "s.whatsapp.net" {
		return ""
	}
	user, _, _ = strings.Cut(user, ":")
	if user == "" || strings.Trim(user, "0123456789") != "" {
		return ""
	}
	return "+" + user
}

// asks answers "what does this meal usually go for" from asking prices.
// Each map is keyed by diet too: veg, egg, nonveg, or "" for every diet.
type asks struct {
	serving map[string][]int // date|meal|mess|diet
	pooled  map[string][]int // date|meal|diet, every mess
	recent  map[string][]int // meal|diet, every serving in the four-week window
}

func newAsks(ps []store.ServingPrices, recentFrom string) *asks {
	a := &asks{serving: map[string][]int{}, pooled: map[string][]int{}, recent: map[string][]int{}}
	for _, p := range ps {
		for _, d := range []string{p.Diet, ""} {
			a.serving[p.Date+"|"+p.Meal+"|"+p.Mess+"|"+d] = append(a.serving[p.Date+"|"+p.Meal+"|"+p.Mess+"|"+d], p.Prices...)
			a.pooled[p.Date+"|"+p.Meal+"|"+d] = append(a.pooled[p.Date+"|"+p.Meal+"|"+d], p.Prices...)
			if p.Date >= recentFrom && !p.Gala {
				a.recent[p.Meal+"|"+d] = append(a.recent[p.Meal+"|"+d], p.Prices...)
			}
		}
	}
	return a
}

// estimate is the median ask for that serving at that mess, else at every
// mess, else for that meal over the four weeks. Only asks in the meal's
// diet count; with none of those, or no diet known, every diet does. Nil
// when there's nothing.
func (a *asks) estimate(date, meal, mess, diet string) *float64 {
	if diet != "" {
		if v := a.estimateIn(date, meal, mess, store.PriceDiet(diet)); v != nil {
			return v
		}
	}
	return a.estimateIn(date, meal, mess, "")
}

func (a *asks) estimateIn(date, meal, mess, diet string) *float64 {
	for _, xs := range [][]int{a.serving[date+"|"+meal+"|"+mess+"|"+diet], a.pooled[date+"|"+meal+"|"+diet]} {
		if len(xs) >= minAsks {
			v := quantile(xs, .5)
			return &v
		}
	}
	if xs := a.recent[meal+"|"+diet]; len(xs) > 0 {
		v := quantile(xs, .5)
		return &v
	}
	return nil
}

// market is the spread of asks for meal in diet ("" for every diet) over
// the four weeks.
func (a *asks) market(meal, diet string) (mealMarket, bool) {
	xs := a.recent[meal+"|"+diet]
	if len(xs) == 0 {
		return mealMarket{}, false
	}
	return mealMarket{Meal: meal, Diet: diet, Q1: quantile(xs, .25), Median: quantile(xs, .5), Q3: quantile(xs, .75), N: len(xs)}, true
}

// quantile interpolates between the closest ranks.
func quantile(xs []int, q float64) float64 { return marketask.Quantile(xs, q) }
