package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/ingest"
	"messwa/internal/mcpserver"
	"messwa/internal/portal"
	"messwa/internal/store"
)

func makeWeekDays(mon string, byDate map[string][]cell) []salesDay {
	t, err := time.Parse(time.DateOnly, mon)
	if err != nil {
		panic(err)
	}
	out := make([]salesDay, 7)
	for i := range 7 {
		d := t.AddDate(0, 0, i).Format(time.DateOnly)
		out[i] = salesDay{Date: d, Cells: byDate[d]}
	}
	return out
}

func f64p(v float64) *float64 { return &v }

func TestWeeklyRecaps(t *testing.T) {
	wins := []slot{{Meal: "breakfast"}, {Meal: "lunch"}, {Meal: "snacks"}, {Meal: "dinner"}}

	tests := []struct {
		name string
		st   salesState
		want []WeekRecap
	}{
		{
			name: "non-owner returns empty non-nil slice",
			st: salesState{
				Owner:   false,
				Today:   "2026-11-03",
				Windows: wins,
				Days: makeWeekDays("2026-10-26", map[string][]cell{
					"2026-10-26": {{Meal: "lunch", State: "sold"}},
				}),
				Sales: []sale{{Date: "2026-10-26", Meal: "lunch", State: "sold", Amount: 60, Buyer: "+911"}},
			},
			want: []WeekRecap{},
		},
		{
			name: "cell states, extras, vs_market rounding, and by_meal",
			st: salesState{
				Owner:   true,
				Today:   "2026-11-02", // Monday after Sun 2026-11-01
				Windows: []slot{{Meal: "breakfast"}, {Meal: "lunch"}, {Meal: "breakfast"}, {Meal: "dinner"}},
				Days: makeWeekDays("2026-10-26", map[string][]cell{
					"2026-10-26": {
						{Meal: "breakfast", State: "ate"},
						{Meal: "lunch", State: "sold"},
						{Meal: "dinner", State: "wasted"},
						{Meal: "brunch", State: "handoff"},
					},
					"2026-10-27": {
						{Meal: "breakfast", State: "none"},
						{Meal: "lunch", State: "sending"},
						{Meal: "supper", State: "wasted"},
						{Meal: "dinner", State: "unknown"},
					},
					"2026-10-28": {
						{Meal: "breakfast", State: "open"},
						{Meal: "lunch", State: "selling"},
						{Meal: "snacks", State: "scheduled"},
						{Meal: "dinner", State: "armed"},
					},
				}),
				Sales: []sale{
					{Date: "2026-10-26", Meal: "lunch", State: "sold", Amount: 65, Ask: f64p(60), Buyer: "+919000000001", Phone: "+919000000001", Name: "Asha"},
					{Date: "2026-10-27", Meal: "lunch", State: "sending", Amount: 50, Ask: f64p(52), Buyer: "+919000000001", Phone: "+919000000001", Name: "Asha K"},
					// Extra counts in extras_sold, earned, and buyer counts, not spare/sold/vs_market.
					{Date: "2026-10-27", Meal: "lunch", Extra: "Egg", State: "sold", Amount: 15, Ask: f64p(100), Buyer: "+919000000001", Phone: "+919000000001"},
					// Non-sold sales ignored in earned, extras_sold, vs_market, and buyer counts.
					{Date: "2026-10-28", Meal: "dinner", State: "armed", Amount: 70, Ask: f64p(60), Buyer: "+919000000002", Phone: "+919000000002", Name: "Ravi"},
					{Date: "2026-10-26", Meal: "brunch", State: "handoff", Amount: 40, Ask: f64p(40), Buyer: "+919000000002", Phone: "+919000000002"},
					{Date: "2026-10-28", Meal: "lunch", Extra: "Curd", State: "lapsed", Amount: 20, Buyer: "+919000000002"},
					// Earlier sale outside the week still counts in all_time and latest name.
					{Date: "2026-10-10", Meal: "dinner", State: "sold", Amount: 55, Buyer: "+919000000001", Phone: "+919000000001", Name: "Old Asha"},
				},
			},
			want: []WeekRecap{{
				Start:      "2026-10-26",
				End:        "2026-11-01",
				Spare:      5, // lunch sold, lunch sending, dinner wasted, brunch handoff, supper wasted
				Sold:       2, // lunch sold, lunch sending
				Wasted:     2, // dinner wasted, supper wasted
				Unknown:    1, // dinner unknown
				ExtrasSold: 1,
				Earned:     130,       // 65 + 50 + 15
				VsMarket:   f64p(1.5), // ((65-60) + (50-52)) / 2 = 1.5
				VsMarketN:  2,
				TopBuyer: &RecapBuyer{
					Key:     "+919000000001",
					Name:    "Asha K",
					Phone:   "+919000000001",
					Week:    3,
					AllTime: 4,
				},
				ByMeal: []RecapMeal{
					{Meal: "lunch", Spare: 2, Sold: 2},
					{Meal: "dinner", Spare: 1, Sold: 0},
					{Meal: "brunch", Spare: 1, Sold: 0},
					{Meal: "supper", Spare: 1, Sold: 0},
				},
			}},
		},
		{
			name: "vs_market null when no asks and top_buyer null when no sales",
			st: salesState{
				Owner:   true,
				Today:   "2026-11-02",
				Windows: wins,
				Days: makeWeekDays("2026-10-26", map[string][]cell{
					"2026-10-26": {{Meal: "lunch", State: "wasted"}},
				}),
			},
			want: []WeekRecap{{
				Start:    "2026-10-26",
				End:      "2026-11-01",
				Spare:    1,
				Wasted:   1,
				VsMarket: nil,
				TopBuyer: nil,
				ByMeal:   []RecapMeal{{Meal: "lunch", Spare: 1, Sold: 0}},
			}},
		},
		{
			name: "vs_market null when only extra or unpriced sales",
			st: salesState{
				Owner:   true,
				Today:   "2026-11-02",
				Windows: wins,
				Days: makeWeekDays("2026-10-26", map[string][]cell{
					"2026-10-26": {{Meal: "lunch", State: "sold"}},
				}),
				Sales: []sale{
					{Date: "2026-10-26", Meal: "lunch", State: "sold", Amount: 60, Buyer: "b1"},
					{Date: "2026-10-26", Meal: "lunch", Extra: "Egg", State: "sold", Amount: 10, Ask: f64p(10), Buyer: "b1"},
				},
			},
			want: []WeekRecap{{
				Start:      "2026-10-26",
				End:        "2026-11-01",
				Spare:      1,
				Sold:       1,
				ExtrasSold: 1,
				Earned:     70,
				VsMarket:   nil,
				VsMarketN:  0,
				TopBuyer:   &RecapBuyer{Key: "b1", Week: 2, AllTime: 2},
				ByMeal:     []RecapMeal{{Meal: "lunch", Spare: 1, Sold: 1}},
			}},
		},
		{
			name: "top buyer tie-break by spent then key, and latest name across all sales",
			st: salesState{
				Owner:   true,
				Today:   "2026-11-09",
				Windows: wins,
				Days: append(
					makeWeekDays("2026-10-26", nil),
					makeWeekDays("2026-11-02", nil)...,
				),
				Sales: []sale{
					// Week 1 (Oct 26 - Nov 1): b1 and b2 both have 2 sales; b2 spent more (130 vs 110).
					{Date: "2026-10-26", Meal: "lunch", State: "sold", Amount: 55, Buyer: "b1", Phone: "+911", Name: "One"},
					{Date: "2026-10-27", Meal: "lunch", State: "sold", Amount: 55, Buyer: "b1", Phone: "+911"},
					{Date: "2026-10-26", Meal: "dinner", State: "sold", Amount: 65, Buyer: "b2", Phone: "+912", Name: "Two Old"},
					{Date: "2026-10-27", Meal: "dinner", State: "sold", Amount: 65, Buyer: "b2", Phone: "+912"},
					// Week 2 (Nov 2 - Nov 8): a-key and z-key tie on count (1) and spent (60); a-key wins deterministically.
					{Date: "2026-11-02", Meal: "lunch", State: "sold", Amount: 60, Buyer: "z-key", Name: "Zed"},
					{Date: "2026-11-03", Meal: "lunch", State: "sold", Amount: 60, Buyer: "a-key", Name: "Alpha"},
					// Later non-sold sale updates b2's latest name without incrementing all_time.
					{Date: "2026-11-09", Meal: "lunch", State: "armed", Amount: 60, Buyer: "b2", Name: "Two New"},
				},
			},
			want: []WeekRecap{
				{
					Start:    "2026-11-02",
					End:      "2026-11-08",
					Earned:   120,
					TopBuyer: &RecapBuyer{Key: "a-key", Name: "Alpha", Week: 1, AllTime: 1},
					ByMeal:   []RecapMeal{},
				},
				{
					Start:    "2026-10-26",
					End:      "2026-11-01",
					Earned:   240,
					TopBuyer: &RecapBuyer{Key: "b2", Name: "Two New", Phone: "+912", Week: 2, AllTime: 2},
					ByMeal:   []RecapMeal{},
				},
			},
		},
		{
			name: "excludes Sunday equal to Today, current partial week, and earlier partial week",
			st: salesState{
				Owner:   true,
				Today:   "2026-11-01", // Sunday of Oct 26..Nov 1
				Windows: wins,
				Days: func() []salesDay {
					var ds []salesDay
					// Partial earlier week: Thu Oct 15 .. Sun Oct 18 (missing Mon-Wed)
					for _, d := range []string{"2026-10-15", "2026-10-16", "2026-10-17", "2026-10-18"} {
						ds = append(ds, salesDay{Date: d})
					}
					// Complete week: Mon Oct 19 .. Sun Oct 25
					ds = append(ds, makeWeekDays("2026-10-19", map[string][]cell{
						"2026-10-20": {{Meal: "lunch", State: "sold"}},
					})...)
					// Current week ending Today (Sunday Nov 1): must be excluded.
					ds = append(ds, makeWeekDays("2026-10-26", map[string][]cell{
						"2026-10-27": {{Meal: "lunch", State: "sold"}},
					})...)
					return ds
				}(),
			},
			want: []WeekRecap{{
				Start:  "2026-10-19",
				End:    "2026-10-25",
				Spare:  1,
				Sold:   1,
				ByMeal: []RecapMeal{{Meal: "lunch", Spare: 1, Sold: 1}},
			}},
		},
		{
			name: "week missing a mid-week day is excluded",
			st: salesState{
				Owner:   true,
				Today:   "2026-11-03",
				Windows: wins,
				Days: func() []salesDay {
					ds := makeWeekDays("2026-10-26", nil)
					return append(ds[:3], ds[4:]...) // drop Thursday
				}(),
			},
			want: []WeekRecap{},
		},
		{
			name: "vs_market rounds negative average to 1 decimal and avoids negative zero",
			st: salesState{
				Owner:   true,
				Today:   "2026-11-09",
				Windows: wins,
				Days: append(
					makeWeekDays("2026-10-26", nil),
					makeWeekDays("2026-11-02", nil)...,
				),
				Sales: []sale{
					// Oct 26 week: (-10)/3 = -3.333... -> -3.3
					{Date: "2026-10-26", Meal: "lunch", State: "sold", Amount: 50, Ask: f64p(54), Buyer: "b1"},
					{Date: "2026-10-27", Meal: "lunch", State: "sold", Amount: 50, Ask: f64p(53), Buyer: "b1"},
					{Date: "2026-10-28", Meal: "lunch", State: "sold", Amount: 50, Ask: f64p(53), Buyer: "b1"},
					// Nov 2 week: -0.01 -> 0 (not -0)
					{Date: "2026-11-02", Meal: "lunch", State: "sold", Amount: 59.99, Ask: f64p(60), Buyer: "b1"},
				},
			},
			want: []WeekRecap{
				{
					Start:     "2026-11-02",
					End:       "2026-11-08",
					Earned:    59.99,
					VsMarket:  f64p(0),
					VsMarketN: 1,
					TopBuyer:  &RecapBuyer{Key: "b1", Week: 1, AllTime: 4},
					ByMeal:    []RecapMeal{},
				},
				{
					Start:     "2026-10-26",
					End:       "2026-11-01",
					Earned:    150,
					VsMarket:  f64p(-3.3),
					VsMarketN: 3,
					TopBuyer:  &RecapBuyer{Key: "b1", Week: 3, AllTime: 4},
					ByMeal:    []RecapMeal{},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := weeklyRecaps(tc.st)
			if got == nil {
				t.Fatalf("got nil slice, want non-nil")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("weeklyRecaps:\ngot  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestRecapHandler(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Groups = []string{"g1@g.us"}
	loc := cfg.Location()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Market asks for lunch on Mon 2026-10-26: median 60.
	in := ingest.New(cfg, st)
	for i, txt := range []string{"Selling palash lunch @50", "Selling palash lunch @60", "Selling palash lunch @70"} {
		ts := time.Date(2026, 10, 26, 10, i, 0, 0, loc)
		if err := in.Post(ctx, ingest.GroupPost{Chat: "g1@g.us", ID: string(rune('a' + i)), Sender: string(rune('1'+i)) + "1@s.whatsapp.net", TS: ts, Text: txt}); err != nil {
			t.Fatal(err)
		}
	}

	// Sold lunch on 2026-10-26 for 65 (vs market +5) and an extra Egg on 2026-10-27 for 15.
	sell := func(date, meal, extra, peer, amt, buyerName, pid, mid string, at time.Time) {
		d := store.Delivery{Date: date, Meal: meal, Extra: extra, Peer: peer, Amount: amt, ImageHash: "h", ArmedAt: at}
		if _, err := st.ArmDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ClaimDelivery(ctx, d, store.Payment{ID: pid, At: at.Add(time.Minute), BuyerName: buyerName}, mid); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishDelivery(ctx, d, mid); err != nil {
			t.Fatal(err)
		}
	}
	sell("2026-10-26", "lunch", "", "919876543210@s.whatsapp.net", "65.00", "Meera", "p1", "m1", time.Date(2026, 10, 26, 12, 0, 0, 0, loc))
	sell("2026-10-27", "lunch#e1", "Egg", "919876543210@s.whatsapp.net", "15.00", "Meera", "p2", "m2", time.Date(2026, 10, 27, 12, 0, 0, 0, loc))

	str := func(s string) *string { return &s }
	origRegs, origBill := readRegistrations, readBill
	defer func() { readRegistrations = origRegs; readBill = origBill }()
	readRegistrations = func(_ context.Context, _ *accounts.Accounts, _ int64, _, _ string) ([]portal.Registration, error) {
		return []portal.Registration{
			{Date: "2026-10-26", Meal: "breakfast", Mess: "palash", Category: "registered", AvailedAt: str("2026-10-26T08:00:00Z")},
			{Date: "2026-10-26", Meal: "lunch", Mess: "palash", Category: "registered"},
			{Date: "2026-10-26", Meal: "dinner", Mess: "palash", Category: "registered"},
		}, nil
	}
	readBill = func(_ context.Context, _ *accounts.Accounts, _ int64, _, _ int) (portal.Bill, error) {
		return portal.Bill{}, errors.New("no bill")
	}

	// Today is Tue 2026-11-03, so 28-day window is 2026-10-07..2026-11-03,
	// giving three completed weeks: Oct 26-Nov 1, Oct 19-25, Oct 12-18.
	ms := mcpserver.New(cfg, st, nil)
	ms.SetClock(func() time.Time { return time.Date(2026, 11, 3, 13, 0, 0, 0, loc) })
	srv := startDashboard(t, cfg, st, ms, NewHub())

	admin := srv.as(t, accounts.Admin)
	res, err := admin.Get(srv.URL + "/api/recap")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if raw["owner"] != true {
		t.Fatalf("owner = %v, want true", raw["owner"])
	}
	weeksRaw, ok := raw["weeks"].([]any)
	if !ok || len(weeksRaw) != 3 {
		t.Fatalf("weeks = %#v, want 3 completed weeks", raw["weeks"])
	}

	var got recapResp
	admin.get(t, "/api/recap", &got)
	if !got.Owner || len(got.Weeks) != 3 {
		t.Fatalf("recap %+v", got)
	}
	if got.Weeks[0].Start != "2026-10-26" || got.Weeks[0].End != "2026-11-01" ||
		got.Weeks[1].Start != "2026-10-19" || got.Weeks[2].Start != "2026-10-12" {
		t.Fatalf("week order: %+v", got.Weeks)
	}
	w0 := got.Weeks[0]
	if w0.Spare != 2 || w0.Sold != 1 || w0.Wasted != 1 || w0.Unknown != 0 ||
		w0.ExtrasSold != 1 || w0.Earned != 80 || w0.VsMarket == nil || *w0.VsMarket != 5 || w0.VsMarketN != 1 {
		t.Errorf("week 0 metrics: %+v", w0)
	}
	if w0.TopBuyer == nil || w0.TopBuyer.Name != "Meera" || w0.TopBuyer.Phone != "+919876543210" || w0.TopBuyer.Week != 2 || w0.TopBuyer.AllTime != 2 {
		t.Errorf("week 0 top_buyer: %+v", w0.TopBuyer)
	}
	wantByMeal := []RecapMeal{{Meal: "lunch", Spare: 1, Sold: 1}, {Meal: "dinner", Spare: 1, Sold: 0}}
	if !reflect.DeepEqual(w0.ByMeal, wantByMeal) {
		t.Errorf("week 0 by_meal = %+v, want %+v", w0.ByMeal, wantByMeal)
	}
	// Earlier empty week serializes null vs_market/top_buyer and empty by_meal array.
	w1Raw := weeksRaw[1].(map[string]any)
	if v, exists := w1Raw["vs_market"]; !exists || v != nil {
		t.Errorf("week 1 vs_market = %v (exists=%v), want null", v, exists)
	}
	if v, exists := w1Raw["top_buyer"]; !exists || v != nil {
		t.Errorf("week 1 top_buyer = %v (exists=%v), want null", v, exists)
	}
	if bm, ok := w1Raw["by_meal"].([]any); !ok || len(bm) != 0 {
		t.Errorf("week 1 by_meal = %#v, want []", w1Raw["by_meal"])
	}

	// WeeklyRecaps helper matches the handler output.
	direct, err := WeeklyRecaps(ctx, cfg, ms, nil, admin.user)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(direct, got.Weeks) {
		t.Errorf("WeeklyRecaps = %+v, want %+v", direct, got.Weeks)
	}

	// Non-admin member gets owner:false and empty weeks array.
	member := srv.as(t, accounts.Member)
	var mGot recapResp
	member.get(t, "/api/recap", &mGot)
	if mGot.Owner || mGot.Weeks == nil || len(mGot.Weeks) != 0 {
		t.Errorf("member recap = %+v, want owner:false and empty weeks", mGot)
	}
}
