package dashboard

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"messwa/internal/portal"
	"messwa/internal/store"
)

type extraTestOperator struct {
	dashboardOperator
	extraCalls []string
	extraRes   mcpserver.ExtraSaleResult
	extraErr   error
}

func (o *extraTestOperator) StartExtras(_ context.Context, userID int64, date, meal, extraName string, count, min, max int, at time.Time, startNow, autopilot bool) (mcpserver.ExtraSaleResult, error) {
	o.extraCalls = append(o.extraCalls, date+"|"+meal+"|"+extraName)
	if o.extraErr != nil {
		return mcpserver.ExtraSaleResult{}, o.extraErr
	}
	return o.extraRes, nil
}

func TestDashboardExtrasStateEndpointAndSellAction(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Groups = []string{"g1@g.us"}
	cfg.Sell.GroupCount = 1
	cfg.MCP.SellTasks = true
	loc := cfg.Location()

	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Record one sold extra egg for today's dinner while the dinner itself is still open!
	soldEgg := store.Delivery{
		Date: "2026-11-25", Meal: "dinner#e1", Extra: "Egg", Mess: "kadamba",
		Peer: "919000000001@s.whatsapp.net", Amount: "15.00", ImageHash: "h",
		ArmedAt: time.Date(2026, 11, 25, 18, 0, 0, 0, loc),
	}
	if _, err := st.ArmDelivery(ctx, soldEgg); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimDelivery(ctx, soldEgg, store.Payment{ID: "p-egg", At: soldEgg.ArmedAt.Add(time.Minute), BuyerName: "Buyer One"}, "m-egg"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishDelivery(ctx, soldEgg, "m-egg"); err != nil {
		t.Fatal(err)
	}

	// Also record a historical extra sale 3 days ago (must NOT trigger a live
	// portal extras fetch) and an unpaid armed extra on today's dinner (must
	// count under Selling, not Sold).
	oldEgg := store.Delivery{
		Date: "2026-11-22", Meal: "dinner#e1", Extra: "Egg", Mess: "kadamba",
		Peer: "919000000002@s.whatsapp.net", Amount: "15.00", ImageHash: "h",
		ArmedAt: time.Date(2026, 11, 22, 18, 0, 0, 0, loc),
	}
	if _, err := st.ArmDelivery(ctx, oldEgg); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimDelivery(ctx, oldEgg, store.Payment{ID: "p-old", At: oldEgg.ArmedAt.Add(time.Minute), BuyerName: "Buyer Old"}, "m-old"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishDelivery(ctx, oldEgg, "m-old"); err != nil {
		t.Fatal(err)
	}
	armedEgg := store.Delivery{
		Date: "2026-11-25", Meal: "dinner#e2", Extra: "Egg", Mess: "kadamba",
		Peer: "919000000003@s.whatsapp.net", Amount: "15.00", ImageHash: "h",
		ArmedAt: time.Date(2026, 11, 25, 18, 20, 0, 0, loc),
	}
	if _, err := st.ArmDelivery(ctx, armedEgg); err != nil {
		t.Fatal(err)
	}

	origReg, origBill, origEx := readRegistrations, readBill, readExtras
	defer func() { readRegistrations, readBill, readExtras = origReg, origBill, origEx }()

	readRegistrations = func(_ context.Context, _ *accounts.Accounts, _ int64, _, _ string) ([]portal.Registration, error) {
		return []portal.Registration{
			{Date: "2026-11-25", Meal: "dinner", Mess: "kadamba-nonveg", Category: "registered"},
		}, nil
	}
	readBill = func(_ context.Context, _ *accounts.Accounts, _ int64, _, _ int) (portal.Bill, error) {
		return portal.Bill{Availed: 300000, Projected: 200000}, nil
	}
	var fetchedDates []string
	readExtras = func(_ context.Context, _ *accounts.Accounts, _ int64, date, meal string) ([]portal.ExtraRegistration, error) {
		fetchedDates = append(fetchedDates, date+"|"+meal)
		if date == "2026-11-25" && meal == "dinner" {
			return []portal.ExtraRegistration{
				{ID: "ex1", Date: date, Meal: meal, Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
				{ID: "ex2", Date: date, Meal: meal, Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
				{ID: "ex3", Date: date, Meal: meal, Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
			}, nil
		}
		return nil, nil
	}

	ms := mcpserver.New(cfg, st, nil)
	ms.SetClock(func() time.Time { return time.Date(2026, 11, 25, 18, 30, 0, 0, loc) })
	op := &extraTestOperator{
		extraRes: mcpserver.ExtraSaleResult{
			IDs: []int64{10}, Slots: []string{"dinner#e3"},
			Extra: "Egg", Count: 1, VerifiedTotal: 3, VerifiedUnavailed: 3, VerifiedAvailable: 0,
		},
	}
	ms.SetSellTasks(op)
	srv := startDashboard(t, cfg, st, ms, NewHub())
	admin := srv.as(t, accounts.Admin)

	// 1. /api/sales shows dinner STILL open, 3 registered eggs (1 sold, 1 armed/selling, 1 available),
	// and counts ₹30 in Month.Earned and 2 in Month.ExtrasSold (not Month.Sold), without hitting
	// the portal for the historical 2026-11-22 extra.
	res, err := admin.Get(srv.URL + "/api/sales")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var s salesState
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.Month.Earned != 30 || s.Month.Sold != 0 || s.Month.ExtrasSold != 2 || s.Month.ExtrasEarned != 30 {
		t.Fatalf("month = %+v", s.Month)
	}
	for _, fd := range fetchedDates {
		if fd == "2026-11-22|dinner" {
			t.Fatalf("historical extra delivery triggered live portal fetch: %v", fetchedDates)
		}
	}
	var dinner *cell
	for i := range s.Days {
		if s.Days[i].Date == "2026-11-25" {
			for j := range s.Days[i].Cells {
				if s.Days[i].Cells[j].Meal == "dinner" {
					dinner = &s.Days[i].Cells[j]
				}
			}
		}
	}
	if dinner == nil || dinner.State != "open" {
		t.Fatalf("dinner cell = %+v, want open (not overwritten by egg sale)", dinner)
	}
	if len(dinner.Extras) != 1 || dinner.Extras[0].Name != "Egg" || dinner.Extras[0].Total != 3 || dinner.Extras[0].Sold != 1 || dinner.Extras[0].Selling != 1 || dinner.Extras[0].Available != 1 || dinner.Extras[0].Rate != 15 {
		t.Fatalf("dinner extras = %+v", dinner.Extras)
	}

	// 2. GET /api/meal-extras verifies live for a specific meal.
	exRes, err := admin.Get(srv.URL + "/api/meal-extras?date=2026-11-25&meal=dinner&refresh=1")
	if err != nil {
		t.Fatal(err)
	}
	defer exRes.Body.Close()
	var mx mealExtrasResp
	if err := json.NewDecoder(exRes.Body).Decode(&mx); err != nil {
		t.Fatal(err)
	}
	if len(mx.Extras) != 1 || mx.Extras[0].Available != 1 || mx.Extras[0].Sold != 1 || mx.Extras[0].Selling != 1 {
		t.Fatalf("meal-extras = %+v", mx)
	}

	// 3. POST /api/sell-action with action=sell-extras invokes ExtraSeller.
	actRes := admin.send(t, "POST", "/api/sell-action", map[string]any{
		"action": "sell-extras", "date": "2026-11-25", "meal": "dinner",
		"extra": "Egg", "count": 1, "min": 10, "max": 15, "now": true,
	})
	if actRes.StatusCode != 200 {
		t.Fatalf("sell-extras status = %d", actRes.StatusCode)
	}
	var out extraStartedResp
	if err := json.NewDecoder(actRes.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	actRes.Body.Close()
	if out.Count != 1 || out.VerifiedTotal != 3 || out.VerifiedAvailable != 0 || len(op.extraCalls) != 1 {
		t.Fatalf("sell-extras resp = %+v, calls = %+v", out, op.extraCalls)
	}
}
