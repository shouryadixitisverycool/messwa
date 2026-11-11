// Package market answers "what does this meal usually go for" from the
// asking prices that store.Prices returns. The sales page and sell task
// negotiation share it.
package market

import (
	"sort"
	"time"

	"messwa/internal/store"
)

// MinAsks is how many asks a median needs before it is trusted. With fewer,
// MedianAsk falls back to a wider pool.
const MinAsks = 3

// Quantile interpolates between the closest ranks. xs must not be empty.
func Quantile(xs []int, q float64) float64 {
	v := append([]int(nil), xs...)
	sort.Ints(v)
	pos := q * float64(len(v)-1)
	lo := int(pos)
	if lo+1 >= len(v) {
		return float64(v[lo])
	}
	return float64(v[lo]) + (pos-float64(lo))*float64(v[lo+1]-v[lo])
}

// Window is how far back MedianAsk looks for a meal's usual price.
const Window = 28 * 24 * time.Hour

// MedianAsk is the median asking price, in rupees, for meal at mess on
// date, from ps (servings within Window before date). It tries, needing
// MinAsks each: that serving at that mess; the same weekday's meal at that
// mess, which catches menus that always go for more; that serving at every
// mess; then any ask for that meal. False when there is none.
func MedianAsk(ps []store.ServingPrices, date, meal, mess string) (float64, bool) {
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, false
	}
	var serving, weekday, pooled, recent []int
	for _, p := range ps {
		if p.Meal != meal {
			continue
		}
		d, err := time.Parse("2006-01-02", p.Date)
		if err != nil || d.After(day) || day.Sub(d) > Window {
			continue
		}
		recent = append(recent, p.Prices...)
		if p.Date == date {
			pooled = append(pooled, p.Prices...)
		}
		if p.Mess != mess {
			continue
		}
		if p.Date == date {
			serving = append(serving, p.Prices...)
		}
		if d.Weekday() == day.Weekday() {
			weekday = append(weekday, p.Prices...)
		}
	}
	for _, xs := range [][]int{serving, weekday, pooled} {
		if len(xs) >= MinAsks {
			return Quantile(xs, .5), true
		}
	}
	if len(recent) > 0 {
		return Quantile(recent, .5), true
	}
	return 0, false
}
