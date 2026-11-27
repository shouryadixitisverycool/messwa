package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"messwa/internal/store"
)

func TestHomeVisitPermissions(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	srv := mcpserver.New(cfg, st, nil)
	mux := http.NewServeMux()
	homeRoutes(mux, cfg, srv)

	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "/api/home/visit", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("anonymous %s /api/home/visit: %d", method, w.Code)
		}
		r = r.WithContext(accounts.WithUser(r.Context(), &accounts.User{ID: 2, Role: accounts.Member, Status: accounts.Active}))
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("shared server member %s /api/home/visit: %d", method, w.Code)
		}
		r = httptest.NewRequest(method, "/api/home/visit", nil)
		r = r.WithContext(accounts.WithUser(r.Context(), &accounts.User{ID: 1, Role: accounts.Admin, Status: accounts.Active}))
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("admin %s /api/home/visit: %d %s", method, w.Code, w.Body.String())
		}
	}

	// On a scoped server, a member owns their own server and is allowed.
	srv.BindAccount(2, nil)
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "/api/home/visit", nil)
		r = r.WithContext(accounts.WithUser(r.Context(), &accounts.User{ID: 2, Role: accounts.Member, Status: accounts.Active}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("scoped member %s /api/home/visit: %d %s", method, w.Code, w.Body.String())
		}
	}
}

func TestHomeVisitEndpoint(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	loc := cfg.Location()
	st, err := store.Open(filepath.Join(t.TempDir(), "home.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Wednesday 2026-11-04 10:00 local: current Mon-Sun week is 2026-11-02..2026-11-08.
	clock := time.Date(2026, 11, 4, 10, 0, 0, 0, loc)
	ms := mcpserver.New(cfg, st, nil)
	ms.SetClock(func() time.Time { return clock })
	srv := startDashboard(t, cfg, st, ms, NewHub())
	admin := srv.as(t, accounts.Admin)

	addSold := func(date, meal, extra, amount string, armedAt, paidAt time.Time) {
		t.Helper()
		d := store.Delivery{Date: date, Meal: meal, Extra: extra, Peer: "919876543210@s.whatsapp.net", Amount: amount, ImageHash: "h", ArmedAt: armedAt}
		if _, err := st.ArmDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ClaimDelivery(ctx, d, store.Payment{ID: "pay-" + date + "-" + meal, At: paidAt, BuyerName: "Buyer"}, "msg-"+date+"-"+meal); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishDelivery(ctx, d, "msg-"+date+"-"+meal); err != nil {
			t.Fatal(err)
		}
	}

	// Prior week Sunday (2026-11-01) and this week Monday (2026-11-02), both before visit 1.
	addSold("2026-11-01", "dinner", "", "30.00", time.Date(2026, 11, 1, 19, 0, 0, 0, loc), time.Date(2026, 11, 1, 19, 5, 0, 0, loc))
	addSold("2026-11-02", "lunch", "", "50.00", time.Date(2026, 11, 2, 12, 0, 0, 0, loc), time.Date(2026, 11, 2, 12, 5, 0, 0, loc))

	// A failed task gives 1 attention item.
	failedTask, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-02", Meal: "dinner", MinPaise: 3000, MaxPaise: 5000, CreatedAt: clock.Add(-time.Hour), EndsAt: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EndSellTask(ctx, failedTask, store.TaskFailed, "WhatsApp down", clock.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}

	postVisit := func() homeVisitResp {
		t.Helper()
		res := admin.send(t, "POST", "/api/home/visit", nil)
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("POST /api/home/visit: %d", res.StatusCode)
		}
		var out homeVisitResp
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	getVisit := func() homeVisitResp {
		t.Helper()
		var out homeVisitResp
		res := admin.get(t, "/api/home/visit", &out)
		if res.StatusCode != 200 {
			t.Fatalf("GET /api/home/visit: %d", res.StatusCode)
		}
		return out
	}

	// First visit: since=0 so sold/earned/refunds are 0, while week totals are populated.
	v1 := postVisit()
	if v1.Since != 0 || v1.Sold != 0 || v1.Earned != 0 || v1.Refunds != 0 || v1.WeekSold != 1 || v1.WeekTotal != 50 {
		t.Fatalf("first visit: %+v", v1)
	}
	visit1End := clock.Unix()

	// Activity happens after visit 1.
	clock = clock.Add(15 * time.Minute)
	// Regular sold meal today (2026-11-04).
	addSold("2026-11-04", "lunch", "", "60.00", clock.Add(-5*time.Minute), clock)
	// Extra sold today (included in totals).
	addSold("2026-11-04", "lunch#e1", "Egg", "15.50", clock.Add(-3*time.Minute), clock)
	// Sending delivery with zero PaidAt/SentAt: falls back to ArmedAt (> visit1End). Also creates a delivery attention item.
	sending := store.Delivery{Date: "2026-11-04", Meal: "dinner", Peer: "919800001111@s.whatsapp.net", Amount: "40.00", ImageHash: "h", ArmedAt: clock}
	if _, err := st.ArmDelivery(ctx, sending); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimDelivery(ctx, sending, store.Payment{ID: "pay-sending", At: time.Time{}, BuyerName: "Nina"}, "msg-sending"); err != nil {
		t.Fatal(err)
	}
	// Friend meal today (excluded from all totals).
	addSold("2026-11-04", "dinner#f1", "", "70.00", clock.Add(-time.Minute), clock)
	// Armed (unpaid) and handed_off meals (excluded).
	if _, err := st.ArmDelivery(ctx, store.Delivery{Date: "2026-11-04", Meal: "snacks", Peer: "77@lid", Amount: "25.00", ImageHash: "h", ArmedAt: clock}); err != nil {
		t.Fatal(err)
	}
	if err := st.HandOffDelivery(ctx, store.Delivery{Date: "2026-11-04", Meal: "breakfast", Peer: "88@lid", Amount: "35.00", ImageHash: "h", ArmedAt: clock}, "Ravi"); err != nil {
		t.Fatal(err)
	}
	// Sale for next week Monday (2026-11-09): paid since visit 1, but outside this Mon-Sun week.
	addSold("2026-11-09", "breakfast", "", "45.00", clock.Add(-time.Minute), clock)

	// Add a refund attention item and resolve both it and the failed task via POST /api/attention.
	refundTask, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-03", Meal: "lunch", MinPaise: 4000, MaxPaise: 6000, CreatedAt: clock, EndsAt: clock.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSellTaskBuyer(ctx, refundTask, "919911223344@s.whatsapp.net", "", clock); err != nil {
		t.Fatal(err)
	}
	if err := st.RefundSellTaskBuyer(ctx, refundTask, "919911223344@s.whatsapp.net", "20.00", "duplicate"); err != nil {
		t.Fatal(err)
	}

	var attn []mcpserver.AttentionItem
	admin.get(t, "/api/attention", &attn)
	if len(attn) != 3 { // failed task, sending delivery, refund
		t.Fatalf("expected 3 attention items before resolve, got %+v", attn)
	}
	for _, it := range attn {
		if it.Kind == "refund" || it.Kind == "blocked" {
			res := admin.send(t, "POST", "/api/attention", map[string]string{"id": it.ID})
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatalf("resolve %s: %d", it.Kind, res.StatusCode)
			}
		}
	}

	// Move past the 30m visit gap so the next POST starts visit 2 (since = visit1End).
	clock = time.Unix(visit1End, 0).Add(45 * time.Minute)
	v2 := postVisit()
	// Since visit1End: 2026-11-04 lunch (60) + lunch#e1 (15.5) + dinner sending (40) + 2026-11-09 breakfast (45) = 4 sold, 160.5 earned.
	// This week (Mon 2026-11-02..Sun 2026-11-08): 2026-11-02 lunch (50) + 2026-11-04 lunch (60) + lunch#e1 (15.5) + dinner sending (40) = 4 sold, 165.5 total.
	// Refunds resolved since visit1End: 1 (blocked resolution does not count).
	if v2.Since != visit1End || v2.Sold != 4 || v2.Earned != 160.5 || v2.Refunds != 1 || v2.WeekSold != 4 || v2.WeekTotal != 165.5 {
		t.Fatalf("second visit: %+v (want since=%d sold=4 earned=160.5 refunds=1 week_sold=4 week_total=165.5)", v2, visit1End)
	}

	// Reload 10m later keeps the same since.
	clock = clock.Add(10 * time.Minute)
	v2Reload := postVisit()
	if v2Reload.Since != visit1End || v2Reload.Sold != v2.Sold || v2Reload.Earned != v2.Earned {
		t.Fatalf("same-session reload changed since: %+v", v2Reload)
	}
	visit2End := clock.Unix()

	// GET after 20m returns the same summary and does NOT bump last_seen.
	clock = clock.Add(20 * time.Minute)
	vGet := getVisit()
	if vGet != v2 {
		t.Fatalf("GET /api/home/visit mismatch: got %+v, want %+v", vGet, v2)
	}

	// 15m after GET (35m after visit2End): POST starts visit 3 with since = visit2End, proving GET didn't ping.
	clock = clock.Add(15 * time.Minute)
	v3 := postVisit()
	if v3.Since != visit2End || v3.Sold != 0 || v3.Earned != 0 || v3.Refunds != 0 || v3.WeekSold != 4 || v3.WeekTotal != 165.5 {
		t.Fatalf("third visit: %+v (want since=%d)", v3, visit2End)
	}

	// A meal sold ahead for Friday counts in the week only once Friday comes.
	addSold("2026-11-06", "lunch", "", "20.00", clock.Add(-time.Minute), clock)
	if v := getVisit(); v.WeekSold != 4 || v.WeekTotal != 165.5 {
		t.Fatalf("a meal later this week counted early: %+v", v)
	}

	// Sunday 2026-11-08 is still in the same Mon 2026-11-02..Sun 2026-11-08 week.
	clock = time.Date(2026, 11, 8, 22, 0, 0, 0, loc)
	vSun := getVisit()
	if vSun.WeekSold != 5 || vSun.WeekTotal != 185.5 {
		t.Fatalf("sunday week totals: %+v", vSun)
	}
	// Monday 2026-11-09 rolls into the new week (only the 2026-11-09 45.00 sale).
	clock = time.Date(2026, 11, 9, 8, 0, 0, 0, loc)
	vMon := getVisit()
	if vMon.WeekSold != 1 || vMon.WeekTotal != 45 {
		t.Fatalf("monday new week totals: %+v", vMon)
	}
}
