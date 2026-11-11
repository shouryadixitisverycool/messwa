package market

import (
	"testing"

	"messwa/internal/store"
)

func TestMedianAsk(t *testing.T) {
	// 2026-11-11 is a Wednesday.
	ps := []store.ServingPrices{
		{Date: "2026-11-04", Meal: "lunch", Mess: "kadamba", Prices: []int{100, 110}},
		{Date: "2026-10-28", Meal: "lunch", Mess: "kadamba", Prices: []int{90}},
		{Date: "2026-11-10", Meal: "lunch", Mess: "kadamba", Prices: []int{40, 45, 50}},
		{Date: "2026-11-11", Meal: "lunch", Mess: "bakul", Prices: []int{30, 35, 40}},
		{Date: "2026-11-11", Meal: "dinner", Mess: "kadamba", Prices: []int{60, 60, 60}},
		{Date: "2026-09-01", Meal: "lunch", Mess: "kadamba", Prices: []int{500, 500, 500}},
	}
	cases := []struct {
		name, date, meal, mess string
		want                   float64
		ok                     bool
	}{
		{"same weekday at this mess when the serving has too few", "2026-11-11", "lunch", "kadamba", 100, true},
		{"serving at this mess", "2026-11-10", "lunch", "kadamba", 45, true},
		{"every mess for the serving", "2026-11-11", "lunch", "palash", 35, true},
		{"any ask for the meal", "2026-11-12", "lunch", "palash", 45, true},
		{"nothing for the meal", "2026-11-11", "breakfast", "kadamba", 0, false},
	}
	for _, c := range cases {
		got, ok := MedianAsk(ps, c.date, c.meal, c.mess)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
