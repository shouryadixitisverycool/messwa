package dashboard

import (
	"context"
	"net/http"
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
	Now     int64        `json:"now"`
	TZ      string       `json:"tz"`
	Today   string       `json:"today"`
	From    string       `json:"from"`   // first day in Days
	Recent  string       `json:"recent"` // first day of the four-week window
	Messes  []mess       `json:"messes"`
	Windows []slot       `json:"windows"` // today's meal windows
	Days    []salesDay   `json:"days"`
	Sales   []sale       `json:"sales"`
	Market  []mealMarket `json:"market"`
	Month   month        `json:"month"`
	// Portal says why registrations are missing. Empty when they loaded.
	Portal string `json:"portal,omitempty"`
	// Owner is true for admins, who see the linked WhatsApp account's
	// sales. Everyone else sees only their own registrations.
	Owner bool `json:"owner"`
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
}

type sale struct {
	Date   string  `json:"date"`
	Meal   string  `json:"meal"`
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
	// payment, unverified ([sell] accept_payment_images). Empty means
	// WhatsApp Pay.
	PaidBy string   `json:"paid_by,omitempty"`
	Ask    *float64 `json:"ask,omitempty"` // median asking price for that serving
}

type mealMarket struct {
	Meal   string  `json:"meal"`
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
	Bill    *bill   `json:"bill,omitempty"`
	BillErr string  `json:"bill_err,omitempty"`
	Earned  float64 `json:"earned"`
	Sold    int     `json:"sold"`
	Wasted  int     `json:"wasted"`
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

// regCache keeps each user's last portal reads in memory for a minute, so
// live updates on the page don't hit the portal for every market post.
type regCache struct {
	mu    sync.Mutex
	acc   *accounts.Accounts
	users map[int64]*regEntry
	bills map[int64]*billEntry
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

func newRegCache(acc *accounts.Accounts) *regCache {
	return &regCache{acc: acc, users: map[int64]*regEntry{}, bills: map[int64]*billEntry{}}
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
		Days: []salesDay{}, Sales: []sale{}, Market: []mealMarket{}, Month: month{Start: ymd(monthStart), Fee: float64(cfg.Sales.MonthlyFee)}, Owner: u.IsAdmin()}
	for _, m := range cfg.Messes {
		out.Messes = append(out.Messes, mess{m.Name, m.VegOnly})
	}
	sched := meals.New(cfg)
	for _, s := range sched.Windows(now) {
		out.Windows = append(out.Windows, toSlot(&s))
	}

	ps, err := srv.Prices(ctx, ymd(from), ymd(today))
	if err != nil {
		return out, err
	}
	asks := newAsks(ps, ymd(recent))
	for _, m := range cfg.Meals {
		if mm, ok := asks.market(m.Name); ok {
			out.Market = append(out.Market, mm)
		}
	}

	var ds []store.DeliveryRecord
	if out.Owner {
		if ds, err = srv.Deliveries(ctx); err != nil {
			return out, err
		}
	}
	byMeal := map[string]int{}
	for _, d := range ds {
		if d.State != "armed" && d.State != "sending" && d.State != "sent" && d.State != "handed_off" {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", d.Date, loc)
		if err != nil {
			continue
		}
		var end time.Time
		for _, w := range sched.Windows(day) {
			if w.Meal == d.Meal {
				end = w.End
			}
		}
		amount, _ := strconv.ParseFloat(d.Amount, 64)
		s := sale{Date: d.Date, Meal: d.Meal, Mess: d.Mess, Diet: d.Diet, Amount: amount, Name: d.BuyerName,
			End: end.Unix(), ArmedAt: unix(d.ArmedAt), OfferAt: unix(d.OfferAt), PaidAt: unix(d.PaidAt), SentAt: unix(d.SentAt), PaidBy: d.PaymentKind}
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
		byMeal[d.Date+"|"+d.Meal] = len(out.Sales)
		out.Sales = append(out.Sales, s)
	}

	if b, err := cache.bill(ctx, u.ID, local.Year(), int(local.Month())); err != nil {
		out.Month.BillErr = err.Error()
	} else {
		out.Month.Bill = &bill{Availed: float64(b.Availed) / 100, Projected: float64(b.Projected) / 100}
		out.Month.Fee = float64(b.Total()) / 100
	}

	regs := map[string][]portal.Registration{}
	rows, err := cache.get(ctx, u.ID, ymd(from), ymd(today))
	if err != nil {
		out.Portal = err.Error()
	}
	for _, r := range rows {
		if !r.Cancelled() {
			regs[r.Date+"|"+r.Meal] = append(regs[r.Date+"|"+r.Meal], r)
		}
	}

	for day := from; !day.After(today); day = day.AddDate(0, 0, 1) {
		sd := salesDay{Date: ymd(day)}
		for _, w := range sched.Windows(day) {
			key := sd.Date + "|" + w.Meal
			c := cell{Meal: w.Meal, Start: w.Start.Unix(), End: w.End.Unix(), State: "none"}
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
			}
			if c.State != "none" && c.State != "ate" {
				c.Ask = asks.estimate(sd.Date, w.Meal, c.Mess)
			}
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

	for i := range out.Sales {
		s := &out.Sales[i]
		s.Ask = asks.estimate(s.Date, s.Meal, s.Mess)
		if (s.State == "sold" || s.State == "sending") && s.Date >= out.Month.Start && s.Date <= out.Today {
			out.Month.Earned += s.Amount
			out.Month.Sold++
		}
	}
	return out, nil
}

func availed(rs []portal.Registration) bool {
	for _, r := range rs {
		if r.Availed() {
			return true
		}
	}
	return false
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
type asks struct {
	serving map[string][]int // date|meal|mess
	pooled  map[string][]int // date|meal, every mess
	recent  map[string][]int // meal, every serving in the four-week window
}

func newAsks(ps []store.ServingPrices, recentFrom string) *asks {
	a := &asks{serving: map[string][]int{}, pooled: map[string][]int{}, recent: map[string][]int{}}
	for _, p := range ps {
		a.serving[p.Date+"|"+p.Meal+"|"+p.Mess] = append(a.serving[p.Date+"|"+p.Meal+"|"+p.Mess], p.Prices...)
		a.pooled[p.Date+"|"+p.Meal] = append(a.pooled[p.Date+"|"+p.Meal], p.Prices...)
		if p.Date >= recentFrom {
			a.recent[p.Meal] = append(a.recent[p.Meal], p.Prices...)
		}
	}
	return a
}

// estimate is the median ask for that serving at that mess, else at every
// mess, else for that meal over the four weeks. Nil when there's nothing.
func (a *asks) estimate(date, meal, mess string) *float64 {
	for _, xs := range [][]int{a.serving[date+"|"+meal+"|"+mess], a.pooled[date+"|"+meal]} {
		if len(xs) >= minAsks {
			v := quantile(xs, .5)
			return &v
		}
	}
	if xs := a.recent[meal]; len(xs) > 0 {
		v := quantile(xs, .5)
		return &v
	}
	return nil
}

func (a *asks) market(meal string) (mealMarket, bool) {
	xs := a.recent[meal]
	if len(xs) == 0 {
		return mealMarket{}, false
	}
	return mealMarket{Meal: meal, Q1: quantile(xs, .25), Median: quantile(xs, .5), Q3: quantile(xs, .75), N: len(xs)}, true
}

// quantile interpolates between the closest ranks.
func quantile(xs []int, q float64) float64 { return marketask.Quantile(xs, q) }
