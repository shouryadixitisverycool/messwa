package dashboard

import (
	"context"
	"math"
	"net/http"
	"slices"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
)

// WeekRecap summarizes one completed Monday-to-Sunday week of meals and sales.
type WeekRecap struct {
	Start      string      `json:"start"`
	End        string      `json:"end"`
	Spare      int         `json:"spare"`
	Sold       int         `json:"sold"`
	Wasted     int         `json:"wasted"`
	Unknown    int         `json:"unknown"`
	ExtrasSold int         `json:"extras_sold"`
	Earned     float64     `json:"earned"`
	VsMarket   *float64    `json:"vs_market"`
	VsMarketN  int         `json:"vs_market_n"`
	TopBuyer   *RecapBuyer `json:"top_buyer"`
	ByMeal     []RecapMeal `json:"by_meal"`
}

// RecapBuyer is the week's top buyer, with their latest known name and
// all-time purchase count across Sales.
type RecapBuyer struct {
	Key     string `json:"key"`  // phone, else WhatsApp peer
	Name    string `json:"name"` // untrusted push name
	Phone   string `json:"phone"`
	Week    int    `json:"week"`
	AllTime int    `json:"all_time"`
}

// RecapMeal is one meal's spare and sold counts for a week.
type RecapMeal struct {
	Meal  string `json:"meal"`
	Spare int    `json:"spare"`
	Sold  int    `json:"sold"`
}

type recapResp struct {
	Owner bool        `json:"owner"`
	Weeks []WeekRecap `json:"weeks"`
}

func recapHandler(cfg *config.Config, srv *mcpserver.Server, cache *regCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := buildSales(r.Context(), cfg, srv, cache, accounts.UserFrom(r.Context()))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, r, recapResp{Owner: st.Owner, Weeks: weeklyRecaps(st)})
	}
}

// WeeklyRecaps is user u's completed Monday-to-Sunday weeks, newest first,
// as the Money tab's recap shows them. It reads the portal with u's own key,
// so it suits a once-a-week caller such as a WhatsApp recap message.
// Non-owners get an empty slice.
func WeeklyRecaps(ctx context.Context, cfg *config.Config, srv *mcpserver.Server, acc *accounts.Accounts, u *accounts.User) ([]WeekRecap, error) {
	st, err := buildSales(ctx, cfg, srv, newRegCache(acc), u)
	if err != nil {
		return nil, err
	}
	return weeklyRecaps(st), nil
}

// weeklyRecaps derives completed Monday-to-Sunday week recaps from st, newest
// week first. Only weeks whose Sunday is strictly before st.Today and whose
// seven days all appear in st.Days are included.
func weeklyRecaps(st salesState) []WeekRecap {
	out := []WeekRecap{}
	if !st.Owner || len(st.Days) == 0 {
		return out
	}
	today, err := time.Parse(time.DateOnly, st.Today)
	if err != nil {
		return out
	}

	days := make(map[string]salesDay, len(st.Days))
	var mondays []time.Time
	seenMon := map[string]bool{}
	for _, d := range st.Days {
		days[d.Date] = d
		t, err := time.Parse(time.DateOnly, d.Date)
		if err != nil || t.Weekday() != time.Monday || seenMon[d.Date] {
			continue
		}
		seenMon[d.Date] = true
		mondays = append(mondays, t)
	}

	var complete []time.Time
	for _, mon := range mondays {
		sun := mon.AddDate(0, 0, 6)
		if !sun.Before(today) {
			continue
		}
		full := true
		for i := range 7 {
			if _, ok := days[mon.AddDate(0, 0, i).Format(time.DateOnly)]; !ok {
				full = false
				break
			}
		}
		if full {
			complete = append(complete, mon)
		}
	}
	if len(complete) == 0 {
		return out
	}
	slices.SortFunc(complete, func(a, b time.Time) int { return b.Compare(a) })

	type buyerInfo struct {
		name, nameDate   string
		phone, phoneDate string
		allTime          int
	}
	buyers := map[string]*buyerInfo{}
	for _, s := range st.Sales {
		if s.Buyer == "" {
			continue
		}
		b := buyers[s.Buyer]
		if b == nil {
			b = &buyerInfo{}
			buyers[s.Buyer] = b
		}
		if s.Name != "" && s.Date >= b.nameDate {
			b.name = s.Name
			b.nameDate = s.Date
		}
		if s.Phone != "" && s.Date >= b.phoneDate {
			b.phone = s.Phone
			b.phoneDate = s.Date
		}
		if s.State == "sold" || s.State == "sending" {
			b.allTime++
		}
	}

	var windowOrder []string
	windowSeen := map[string]bool{}
	for _, w := range st.Windows {
		if w.Meal != "" && !windowSeen[w.Meal] {
			windowSeen[w.Meal] = true
			windowOrder = append(windowOrder, w.Meal)
		}
	}

	for _, mon := range complete {
		start := mon.Format(time.DateOnly)
		end := mon.AddDate(0, 0, 6).Format(time.DateOnly)
		rec := WeekRecap{Start: start, End: end, ByMeal: []RecapMeal{}}

		mealSpare := map[string]int{}
		mealSold := map[string]int{}
		for i := range 7 {
			d := days[mon.AddDate(0, 0, i).Format(time.DateOnly)]
			for _, c := range d.Cells {
				switch c.State {
				case "sold", "sending":
					rec.Spare++
					rec.Sold++
					mealSpare[c.Meal]++
					mealSold[c.Meal]++
				case "handoff":
					rec.Spare++
					mealSpare[c.Meal]++
				case "wasted":
					rec.Spare++
					rec.Wasted++
					mealSpare[c.Meal]++
				case "unknown":
					rec.Unknown++
				}
			}
		}

		for _, m := range windowOrder {
			if mealSpare[m] > 0 {
				rec.ByMeal = append(rec.ByMeal, RecapMeal{Meal: m, Spare: mealSpare[m], Sold: mealSold[m]})
			}
		}
		var otherMeals []string
		for m, sp := range mealSpare {
			if sp > 0 && !windowSeen[m] {
				otherMeals = append(otherMeals, m)
			}
		}
		slices.Sort(otherMeals)
		for _, m := range otherMeals {
			rec.ByMeal = append(rec.ByMeal, RecapMeal{Meal: m, Spare: mealSpare[m], Sold: mealSold[m]})
		}

		type weekBuyer struct {
			count int
			spent float64
		}
		weekBuyers := map[string]*weekBuyer{}
		var diffSum float64
		var diffN int
		for _, s := range st.Sales {
			if s.Date < start || s.Date > end || (s.State != "sold" && s.State != "sending") {
				continue
			}
			rec.Earned += s.Amount
			if s.Extra != "" {
				rec.ExtrasSold++
			} else if s.Ask != nil {
				diffSum += s.Amount - *s.Ask
				diffN++
			}
			if s.Buyer != "" {
				wb := weekBuyers[s.Buyer]
				if wb == nil {
					wb = &weekBuyer{}
					weekBuyers[s.Buyer] = wb
				}
				wb.count++
				wb.spent += s.Amount
			}
		}
		if diffN > 0 {
			avg := math.Round((diffSum/float64(diffN))*10) / 10
			if avg == 0 {
				avg = 0 // no "-0" in the JSON
			}
			rec.VsMarket = &avg
			rec.VsMarketN = diffN
		}

		var bestKey string
		var best weekBuyer
		for k, wb := range weekBuyers {
			if bestKey == "" || wb.count > best.count ||
				(wb.count == best.count && wb.spent > best.spent) ||
				(wb.count == best.count && wb.spent == best.spent && k < bestKey) {
				bestKey = k
				best = *wb
			}
		}
		if bestKey != "" {
			info := buyers[bestKey]
			rec.TopBuyer = &RecapBuyer{
				Key:     bestKey,
				Name:    info.name,
				Phone:   info.phone,
				Week:    best.count,
				AllTime: info.allTime,
			}
		}
		out = append(out, rec)
	}
	return out
}
