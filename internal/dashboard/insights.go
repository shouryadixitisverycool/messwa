package dashboard

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"messwa/internal/config"
	"messwa/internal/mcpserver"
)

// insightsCols names the fields of each row in /api/insights, in order.
// Rows are arrays because a month of the market is ~25,000 of them.
var insightsCols = []string{
	"date", "meal", "mess", "diet", "kind", "price", "qty",
	"first", "last", "end", "sold", "posts", "groups", "seller", "closed", "others", "deleted",
}

// totalCols names the fields of each row in Totals. season indexes Seasons.
var totalCols = []string{"season", "mess", "diet", "offers", "sold", "wanted", "ask_sum", "asks"}

type insightsState struct {
	Now   int64  `json:"now"`
	TZ    string `json:"tz"`
	Today string `json:"today"`
	// From..To are the meal dates Rows cover, First the earliest one stored.
	From  string `json:"from"`
	To    string `json:"to"`
	First string `json:"first"`
	// Season is the key of the season Rows come from, or "all". Current is
	// today's season and Next the one after it.
	Season  string            `json:"season"`
	Current string            `json:"current"`
	Next    config.SeasonSpan `json:"next"`
	// Seasons run from the one holding First to today's, oldest first.
	Seasons []seasonStat `json:"seasons"`
	Messes  []mess       `json:"messes"`
	Windows []window     `json:"windows"`
	// Sellers are indexed by each row's seller field. Names are untrusted
	// push names.
	Sellers []seller `json:"sellers"`
	Cols    []string `json:"cols"`
	Rows    [][]any  `json:"rows"`
	// Totals sum the stored facts before today per season, mess and diet,
	// whatever the range, so the page can compare seasons under its own
	// filters. Seasons[i].Days counts the same dates.
	TotalCols []string `json:"total_cols"`
	Totals    [][]any  `json:"totals"`
}

// seasonStat is a season span and how many of its meal dates have facts.
type seasonStat struct {
	config.SeasonSpan
	Days int `json:"days"`
}

type window struct {
	Meal   string `json:"meal"`
	Starts string `json:"starts"`
	Ends   string `json:"ends"`
}

type seller struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// insightsHandler serves the listing facts for one season: ?season= names
// it by key, a missing or unknown key means today's, and "all" means every
// season at once. ?weeks= keeps only its last weeks, counted back from the
// season's end or today, whichever is earlier.
func insightsHandler(cfg *config.Config, srv *mcpserver.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fail := func(err error) { http.Error(w, err.Error(), http.StatusInternalServerError) }
		now := srv.Now()
		today := now.In(cfg.Location()).Format("2006-01-02")
		first, err := srv.FirstFactDate(r.Context())
		if err != nil {
			fail(err)
			return
		}
		cur, err := cfg.SeasonOf(today)
		if err != nil {
			fail(err)
			return
		}
		next, _ := cfg.SeasonOf(addDays(cur.To, 1))
		spans := []config.SeasonSpan{cur}
		if first != "" && first <= today {
			if spans, err = cfg.SeasonSpans(first, today); err != nil {
				fail(err)
				return
			}
		}
		st := insightsState{Now: now.Unix(), TZ: cfg.Timezone, Today: today, First: first,
			Current: cur.Key, Next: next, Seasons: []seasonStat{},
			Messes: []mess{}, Windows: []window{}, Sellers: []seller{}, Cols: insightsCols, Rows: [][]any{},
			TotalCols: totalCols, Totals: [][]any{}}
		for _, m := range cfg.Messes {
			st.Messes = append(st.Messes, mess{m.Name, m.VegOnly})
		}
		for _, m := range cfg.Meals {
			st.Windows = append(st.Windows, window{m.Name, m.Starts, m.Ends})
		}
		for _, sp := range spans {
			st.Seasons = append(st.Seasons, seasonStat{SeasonSpan: sp})
		}

		if q := r.URL.Query().Get("season"); q == "all" {
			st.Season, st.From, st.To = "all", first, today
		} else {
			pick := cur
			for _, sp := range spans {
				if sp.Key == q {
					pick = sp
				}
			}
			st.Season, st.From, st.To = pick.Key, max(pick.From, first), min(pick.To, today)
		}
		if weeks, _ := strconv.Atoi(r.URL.Query().Get("weeks")); weeks > 0 {
			st.From = max(st.From, addDays(st.To, 1-7*weeks))
		}
		if first == "" {
			st.From = st.To
			writeJSON(w, r, st)
			return
		}

		// Today is only partly over, so it would drag per-day numbers down.
		totals, err := srv.FactTotals(r.Context(), addDays(today, -1))
		if err != nil {
			fail(err)
			return
		}
		// Both lists are oldest first, so one pass assigns each date its season.
		i, last := 0, ""
		for _, t := range totals {
			for i < len(spans)-1 && t.Date > spans[i].To {
				i++
			}
			if t.Date != last {
				st.Seasons[i].Days++
				last = t.Date
			}
			st.Totals = append(st.Totals, []any{i, t.Mess, t.Diet, t.Offers, t.Sold, t.Wanted, t.AskSum, t.Asks})
		}

		facts, err := srv.Facts(r.Context(), st.From, st.To)
		if err != nil {
			fail(err)
			return
		}
		idx := map[string]int{}
		for _, f := range facts {
			i, ok := idx[f.Seller]
			if !ok {
				i = len(st.Sellers)
				idx[f.Seller] = i
				st.Sellers = append(st.Sellers, seller{Phone: phoneOf(f.Seller)})
			}
			// Facts come oldest first, so the last name wins.
			if f.SellerName != "" {
				st.Sellers[i].Name = f.SellerName
			}
			st.Rows = append(st.Rows, []any{
				f.Date, f.Meal, f.Mess, f.Diet, f.Kind, f.Price, f.Qty,
				f.FirstSeen.Unix(), f.LastSeen.Unix(), f.End.Unix(), unixOrZero(f.SoldAt), f.Posts, f.Groups,
				i, b2i(f.Closed), f.OtherTicks, b2i(f.Deleted),
			})
		}
		writeJSON(w, r, st)
	}
}

// addDays moves a "YYYY-MM-DD" date by n days.
func addDays(date string, n int) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return d.AddDate(0, 0, n).Format("2006-01-02")
}

func phoneOf(jid string) string {
	if u, ok := strings.CutSuffix(jid, "@s.whatsapp.net"); ok {
		return "+" + u
	}
	return ""
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
