package dashboard

import (
	"context"
	"net/http"
	"sync"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
)

// The usage page shows admins how every account uses messwa. It runs the
// sales page's numbers once per approved account and keeps only totals:
// no buyer names, numbers or chats leave a user's own sales page.

// usageTTL is how long one build of the page is reused. Each build reads
// every user's mess portal, so it must not run on every page load.
const usageTTL = 2 * time.Minute

// usageWorkers caps how many users' portals are read at once.
const usageWorkers = 4

type usageState struct {
	Now   int64  `json:"now"`
	TZ    string `json:"tz"`
	Today string `json:"today"`
	// From is the first day in each user's Days, Recent the start of the
	// four-week window and Month the first of this month.
	From   string      `json:"from"`
	Recent string      `json:"recent"`
	Month  string      `json:"month"`
	Users  []usageUser `json:"users"`
	// Listings counts sell listings in the groups per meal date, for the
	// share messwa's sell tasks make up.
	Listings map[string]int `json:"listings"`
	// OldestWait is when the longest-waiting sign-up asked for approval.
	Waiting    int   `json:"waiting"`
	OldestWait int64 `json:"oldest_wait,omitempty"`
}

type usageUser struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Status   string `json:"status"`
	Disabled bool   `json:"disabled,omitempty"`
	MessKey  bool   `json:"mess_key"`
	// Linked is a WhatsApp device paired to the account; Connected is
	// that client being online now.
	Linked    bool  `json:"linked"`
	Connected bool  `json:"connected"`
	Created   int64 `json:"created"`
	StatusAt  int64 `json:"status_at,omitempty"`
	// EverTask and EverSold cover all time, for the onboarding funnel.
	EverTask   bool  `json:"ever_task"`
	EverSold   bool  `json:"ever_sold"`
	LastActive int64 `json:"last_active,omitempty"`
	// Error says why the numbers below are missing; Portal why the mess
	// portal couldn't be read, which leaves wasted meals uncounted.
	Error  string      `json:"error,omitempty"`
	Portal string      `json:"portal,omitempty"`
	Bill   bool        `json:"bill"` // Fee came from the portal's bill
	Fee    float64     `json:"fee"`
	Days   []usageDay  `json:"days"`
	Sales  []usageSale `json:"sales"`
	Tasks  []usageTask `json:"tasks"`
}

// usageDay counts one user's meals on one day by how they ended.
type usageDay struct {
	Date    string `json:"date"`
	Sold    int    `json:"sold"`
	Handoff int    `json:"handoff"`
	Lapsed  int    `json:"lapsed"`
	Wasted  int    `json:"wasted"`
	// Missed is what the wasted meals would have fetched at the median ask.
	Missed float64 `json:"missed,omitempty"`
}

// usageSale is a sale without its buyer.
type usageSale struct {
	Date   string   `json:"date"`
	Amount float64  `json:"amount"`
	Ask    *float64 `json:"ask,omitempty"`
	State  string   `json:"state"`
	PaidBy string   `json:"paid_by,omitempty"`
	End    int64    `json:"end"`
	// Wait is seconds from the offer to the payment, 0 when unknown.
	Wait int64 `json:"wait,omitempty"`
}

type usageTask struct {
	Date  string `json:"date"`
	State string `json:"state"`
}

type usageSource struct {
	cfg    *config.Config
	root   *mcpserver.Server
	acc    *accounts.Accounts
	lookup func(context.Context, int64) (*mcpserver.Server, error)
	regs   *regCache

	mu    sync.Mutex
	at    time.Time
	state *usageState
}

func newUsage(cfg *config.Config, root *mcpserver.Server, acc *accounts.Accounts, lookup func(context.Context, int64) (*mcpserver.Server, error)) *usageSource {
	return &usageSource{cfg: cfg, root: root, acc: acc, lookup: lookup, regs: newRegCache(acc)}
}

func (u *usageSource) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.state == nil || time.Since(u.at) >= usageTTL {
		st, err := u.build(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		u.state, u.at = st, time.Now()
	}
	writeJSON(w, r, u.state)
}

func (u *usageSource) build(ctx context.Context) (*usageState, error) {
	loc := u.cfg.Location()
	now := u.root.Now()
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	recent := today.AddDate(0, 0, 1-salesDays)
	monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
	from := recent
	if monthStart.Before(from) {
		from = monthStart
	}
	ymd := func(t time.Time) string { return t.Format("2006-01-02") }
	out := &usageState{Now: now.Unix(), TZ: u.cfg.Timezone, Today: ymd(today), From: ymd(from), Recent: ymd(recent), Month: ymd(monthStart),
		Users: []usageUser{}, Listings: map[string]int{}}

	facts, err := u.root.Facts(ctx, ymd(from), ymd(today))
	if err != nil {
		return nil, err
	}
	for _, f := range facts {
		if f.Kind != "buy" && !f.Deleted {
			out.Listings[f.Date]++
		}
	}

	users, err := u.acc.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, us := range users {
		if us.Status == accounts.Requested {
			out.Waiting++
			if t := us.StatusAt.Unix(); !us.StatusAt.IsZero() && (out.OldestWait == 0 || t < out.OldestWait) {
				out.OldestWait = t
			}
		}
	}

	out.Users = make([]usageUser, len(users))
	sem := make(chan struct{}, usageWorkers)
	var wg sync.WaitGroup
	for i := range users {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			out.Users[i] = u.user(ctx, &users[i], now, from)
		}(i)
	}
	wg.Wait()
	return out, nil
}

// user builds one account's row. Errors stay on the row, so one broken
// account doesn't hide everyone else.
func (u *usageSource) user(ctx context.Context, us *accounts.User, now, from time.Time) usageUser {
	row := usageUser{Name: us.Name, Role: string(us.Role), Status: string(us.Status), Disabled: us.Disabled, MessKey: us.HasMessKey,
		Created: unix(us.CreatedAt), StatusAt: unix(us.StatusAt), Days: []usageDay{}, Sales: []usageSale{}, Tasks: []usageTask{}}
	if us.Disabled {
		return row
	}
	srv, err := u.lookup(ctx, us.ID)
	if err != nil {
		row.Error = err.Error()
		return row
	}
	if srv.Device != nil {
		row.Linked = srv.Device.Linked()
		if st, ok := srv.Device.Status().(map[string]any); ok {
			row.Connected, _ = st["connected"].(bool)
		}
	}

	tasks, err := srv.SellTasksSince(ctx, time.Unix(0, 0))
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.EverTask = len(tasks) > 0
	fromDay := from.Format("2006-01-02")
	for _, t := range tasks {
		row.LastActive = max(row.LastActive, unix(t.CreatedAt))
		if t.Date >= fromDay {
			row.Tasks = append(row.Tasks, usageTask{Date: t.Date, State: t.State})
		}
	}
	ds, err := srv.Deliveries(ctx)
	if err != nil {
		row.Error = err.Error()
		return row
	}
	for _, d := range ds {
		if d.State == "sent" {
			row.EverSold = true
		}
		row.LastActive = max(row.LastActive, unix(d.ArmedAt), unix(d.PaidAt))
	}

	if !us.IsActive() {
		return row
	}
	sales, err := buildSales(ctx, srv.Config(), srv, u.regs, us)
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Portal = sales.Portal
	row.Fee, row.Bill = sales.Month.Fee, sales.Month.Bill != nil
	for _, s := range sales.Sales {
		us := usageSale{Date: s.Date, Amount: s.Amount, Ask: s.Ask, State: s.State, PaidBy: s.PaidBy, End: s.End}
		if s.PaidAt > 0 && s.OfferAt > 0 && s.PaidAt >= s.OfferAt {
			us.Wait = s.PaidAt - s.OfferAt
		}
		row.Sales = append(row.Sales, us)
	}
	for _, d := range sales.Days {
		day := usageDay{Date: d.Date}
		for _, c := range d.Cells {
			lapsed := c.Sale != nil && sales.Sales[*c.Sale].State == "lapsed"
			switch {
			case c.State == "sold" || c.State == "sending":
				day.Sold++
			case c.State == "handoff":
				day.Handoff++
			case lapsed && c.State != "ate" && c.State != "open":
				day.Lapsed++
			case c.State == "wasted":
				day.Wasted++
				if c.Ask != nil {
					day.Missed += *c.Ask
				}
			}
		}
		row.Days = append(row.Days, day)
	}
	return row
}
