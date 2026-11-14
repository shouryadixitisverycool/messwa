package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/ingest"
	"messwa/internal/mcpserver"
	"messwa/internal/portal"
	"messwa/internal/store"
)

func TestSales(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Groups = []string{"g1@g.us"}
	cfg.Sales.MonthlyFee = 5600
	loc := cfg.Location()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// The market asked 50, 60 and 70 for Kadamba lunch on the 2nd, and
	// 120, 130 and 140 for its non-veg.
	in := ingest.New(cfg, st)
	for i, txt := range []string{"Selling kadamba lunch @50", "Selling kadamba lunch @60", "Selling kadamba lunch @70",
		"Selling kadamba nonveg lunch @120", "Selling kadamba nonveg lunch @130", "Selling kadamba nonveg lunch @140"} {
		ts := time.Date(2026, 11, 2, 10, i, 0, 0, loc)
		if err := in.Post(ctx, ingest.GroupPost{Chat: "g1@g.us", ID: string(rune('a' + i)), Sender: string(rune('1'+i)) + "1@s.whatsapp.net", TS: ts, Text: txt}); err != nil {
			t.Fatal(err)
		}
	}

	// Sold dinner on the 2nd; lunch today is armed.
	sold := store.Delivery{Date: "2026-11-02", Meal: "dinner", Peer: "919876543210@s.whatsapp.net", Amount: "55.00", ImageHash: "h", ArmedAt: time.Date(2026, 11, 2, 18, 0, 0, 0, loc)}
	if _, err := st.ArmDelivery(ctx, sold); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOfferSent(ctx, sold, sold.ArmedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimDelivery(ctx, sold, store.Payment{ID: "p1", At: sold.ArmedAt.Add(90 * time.Second), BuyerName: "Meera"}, "m1"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishDelivery(ctx, sold, "m1"); err != nil {
		t.Fatal(err)
	}
	armed := store.Delivery{Date: "2026-11-03", Meal: "lunch", Peer: "77@lid", Amount: "60.00", ImageHash: "h", ArmedAt: time.Date(2026, 11, 3, 12, 0, 0, 0, loc)}
	if _, err := st.ArmDelivery(ctx, armed); err != nil {
		t.Fatal(err)
	}

	// Ravi's lunch, sold for him through your WhatsApp for 45.
	if _, err := st.AddFriendMeal(ctx, store.FriendMeal{Date: "2026-11-03", Slot: "lunch", Mess: "kadamba-nonveg", Friend: "Ravi", QR: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	friend := store.Delivery{Date: "2026-11-03", Meal: "lunch#f1", Peer: "919811112222@s.whatsapp.net", Amount: "45.00", ImageHash: "h", ArmedAt: time.Date(2026, 11, 3, 12, 40, 0, 0, loc)}
	if _, err := st.ArmDelivery(ctx, friend); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimDelivery(ctx, friend, store.Payment{ID: "p9", At: friend.ArmedAt.Add(time.Minute), BuyerName: "Asha"}, "m9"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishDelivery(ctx, friend, "m9"); err != nil {
		t.Fatal(err)
	}

	str := func(s string) *string { return &s }
	orig := readRegistrations
	defer func() { readRegistrations = orig }()
	var admin *testClient
	readRegistrations = func(_ context.Context, _ *accounts.Accounts, userID int64, from, to string) ([]portal.Registration, error) {
		if userID != admin.user.ID {
			return nil, errors.New("portal read with someone else's key")
		}
		if from != "2026-10-07" || to != "2026-11-03" {
			t.Errorf("read %s..%s, want the four weeks to today", from, to)
		}
		return []portal.Registration{
			{Date: "2026-11-02", Meal: "breakfast", Mess: "palash", Category: "registered", AvailedAt: str("2026-11-02T08:00:00Z")},
			{Date: "2026-11-02", Meal: "lunch", Mess: "kadamba-nonveg", Category: "registered"},
			{Date: "2026-11-02", Meal: "dinner", Mess: "palash", Category: "registered"},
			{Date: "2026-11-03", Meal: "lunch", Mess: "palash", Category: "registered"},
			{Date: "2026-11-03", Meal: "snacks", Mess: "palash", Category: "registered"},
			{Date: "2026-11-03", Meal: "dinner", Mess: "bakul-veg", Category: "registered"},
			{Date: "2026-11-01", Meal: "snacks", Mess: "palash", Category: "registered", CancelledAt: str("2026-11-01T08:00:00Z")},
		}, nil
	}

	origBill := readBill
	defer func() { readBill = origBill }()
	readBill = func(_ context.Context, _ *accounts.Accounts, userID int64, year, month int) (portal.Bill, error) {
		if userID != admin.user.ID {
			return portal.Bill{}, errors.New("bill read with someone else's key")
		}
		if year != 2026 || month != 11 {
			t.Errorf("read the bill for %d-%d, want this month", year, month)
		}
		return portal.Bill{Availed: 77000, Projected: 66450}, nil
	}

	ms := mcpserver.New(cfg, st, nil)
	ms.SetClock(func() time.Time { return time.Date(2026, 11, 3, 13, 0, 0, 0, loc) })
	srv := startDashboard(t, cfg, st, ms, NewHub())
	admin = srv.as(t, accounts.Admin)

	get := func() salesState {
		res, err := admin.Get(srv.URL + "/api/sales")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var s salesState
		if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := get()
	cellAt := func(s salesState, date, meal string) cell {
		for _, d := range s.Days {
			for _, c := range d.Cells {
				if d.Date == date && c.Meal == meal {
					return c
				}
			}
		}
		t.Fatalf("no cell %s %s", date, meal)
		return cell{}
	}
	for _, tc := range []struct{ date, meal, want, mess, diet string }{
		{"2026-11-02", "breakfast", "ate", "palash", "veg"},
		{"2026-11-02", "lunch", "wasted", "kadamba", "nonveg"},
		{"2026-11-02", "dinner", "sold", "palash", "veg"},
		{"2026-11-03", "lunch", "armed", "palash", "veg"},
		{"2026-11-03", "dinner", "open", "bakul", "veg"},
		{"2026-11-01", "snacks", "none", "", ""},
	} {
		c := cellAt(s, tc.date, tc.meal)
		if c.State != tc.want || c.Mess != tc.mess || c.Diet != tc.diet {
			t.Errorf("%s %s: got %s %s/%s, want %s %s/%s", tc.date, tc.meal, c.State, c.Mess, c.Diet, tc.want, tc.mess, tc.diet)
		}
	}
	// The wasted lunch was non-veg, so only the non-veg asks price it.
	if c := cellAt(s, "2026-11-02", "lunch"); c.Ask == nil || *c.Ask != 130 {
		t.Errorf("wasted non-veg lunch should be worth the 130 non-veg median, got %v", c.Ask)
	}
	if s.From != "2026-10-07" || s.Today != "2026-11-03" || len(s.Days) != 28 {
		t.Errorf("window %s..%s, %d days", s.From, s.Today, len(s.Days))
	}
	m := s.Month
	if m.Start != "2026-11-01" || m.Fee != 1434.5 || m.Bill == nil || m.Bill.Availed != 770 || m.Bill.Projected != 664.5 || m.Earned != 55 || m.Sold != 1 || m.Wasted != 1 || m.Potential != 130 || m.Unpriced != 0 {
		t.Errorf("month %+v", m)
	}
	if len(s.Sales) != 2 {
		t.Fatalf("sales %+v", s.Sales)
	}
	x := s.Sales[0]
	if x.State != "sold" || x.Phone != "+919876543210" || x.Buyer != "+919876543210" || x.Name != "Meera" || x.Amount != 55 || x.Mess != "palash" || x.PaidAt-x.OfferAt != 90 {
		t.Errorf("sold %+v", x)
	}
	if y := s.Sales[1]; y.State != "armed" || y.Phone != "" || y.Buyer != "77@lid" {
		t.Errorf("armed %+v", y)
	}
	// Lunch's spread for every diet, then veg and non-veg on their own.
	if mk := s.Market; len(mk) != 3 || mk[0].Diet != "" || mk[0].Median != 95 ||
		mk[1].Diet != "veg" || mk[1].Median != 60 || mk[1].Q1 != 55 || mk[1].Q3 != 65 || mk[2].Diet != "nonveg" || mk[2].Median != 130 {
		t.Errorf("market %+v", s.Market)
	}

	if !s.Owner {
		t.Error("admin is not the owner")
	}
	// The friend's sale is listed on its own and stays out of every total
	// above (two sales, 55 earned).
	if len(s.Friends) != 1 {
		t.Fatalf("friends %+v", s.Friends)
	}
	if f := s.Friends[0]; f.Slot != "lunch#f1" || f.Meal != "lunch" || f.Mess != "kadamba" || f.Diet != "nonveg" || f.Friend != "Ravi" || f.State != "sold" || f.Amount != 45 || f.Buyer != "Asha" || f.End == 0 {
		t.Errorf("friend %+v", f)
	}
	// Lunch is being served: snacks and dinner are left today, and the
	// next meal is snacks, already listed.
	if len(s.FriendSlots) != 3 || s.FriendSlots[0].Meal != "lunch" || s.FriendSlots[2].Meal != "dinner" {
		t.Errorf("friend slots %+v", s.FriendSlots)
	}

	// Lunch is being served, so snacks is the next meal and the only open
	// one the ledger offers to sell. Sell tasks are off here.
	if c := cellAt(s, "2026-11-03", "snacks"); c.State != "open" || !c.Sellable {
		t.Errorf("next meal snacks %+v, want open and sellable", c)
	}
	if c := cellAt(s, "2026-11-03", "dinner"); c.Sellable {
		t.Errorf("dinner is two meals away but sellable")
	}
	if s.SellOff == "" {
		t.Error("sell tasks are off but the ledger isn't told")
	}

	// A running sell task turns its open meal into selling, with its range.
	tid, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-03", Meal: "dinner", MinPaise: 5000, MaxPaise: 8000,
		CreatedAt: time.Date(2026, 11, 3, 12, 50, 0, 0, loc), EndsAt: time.Date(2026, 11, 3, 21, 30, 0, 0, loc)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSellTaskBuyer(ctx, tid, "919811111111@s.whatsapp.net", "", time.Date(2026, 11, 3, 12, 52, 0, 0, loc)); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordBuyerOffer(ctx, tid, "919811111111@s.whatsapp.net", 6500); err != nil {
		t.Fatal(err)
	}
	s = get()
	c := cellAt(s, "2026-11-03", "dinner")
	if c.State != "selling" || c.Task == nil || c.Task.ID != tid || c.Task.Min != 50 || c.Task.Max != 80 {
		t.Fatalf("dinner with a running task %+v", c)
	}
	// The buyer's key is what mark-sold takes; their offer prefills the price.
	if bs := c.Task.Buyers; len(bs) != 1 || bs[0].Key != "+919811111111" || bs[0].Price != 65 {
		t.Errorf("task buyers %+v", bs)
	}
	if _, err := st.EndSellTask(ctx, tid, store.TaskCancelled, "test", time.Date(2026, 11, 3, 12, 55, 0, 0, loc)); err != nil {
		t.Fatal(err)
	}

	// The board's day track gets today's armed lunch.
	res, err := admin.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var b state
	json.NewDecoder(res.Body).Decode(&b)
	res.Body.Close()
	if len(b.Mine) != 1 || b.Mine[0].Meal != "lunch" || b.Mine[0].State != "armed" || b.Mine[0].Amount != 60 {
		t.Errorf("mine %+v", b.Mine)
	}

	// Handed-off meals show as handoff and earn nothing, unless you ate it.
	for _, h := range []store.Delivery{
		{Date: "2026-11-03", Meal: "dinner", Peer: "919800000000@s.whatsapp.net", Amount: "70.00", ImageHash: "h", ArmedAt: time.Date(2026, 11, 3, 12, 30, 0, 0, loc)},
		{Date: "2026-11-02", Meal: "breakfast", Peer: "919800000000@s.whatsapp.net", Amount: "40.00", ImageHash: "h", ArmedAt: time.Date(2026, 11, 2, 7, 0, 0, 0, loc)},
	} {
		if err := st.HandOffDelivery(ctx, h, "Ravi"); err != nil {
			t.Fatal(err)
		}
	}
	s = get()
	if c := cellAt(s, "2026-11-03", "dinner"); c.State != "handoff" || c.Sale == nil || s.Sales[*c.Sale].Name != "Ravi" || s.Sales[*c.Sale].Amount != 70 {
		t.Errorf("handed-off dinner %+v", c)
	}
	if c := cellAt(s, "2026-11-02", "breakfast"); c.State != "ate" {
		t.Errorf("handed-off breakfast you ate shows %s", c.State)
	}
	if s.Month.Earned != 55 || s.Month.Sold != 1 {
		t.Errorf("handoffs counted as sales: %+v", s.Month)
	}

	// A member sees their own registrations and none of the owner's sales.
	member := srv.as(t, accounts.Member)
	readRegistrations = func(_ context.Context, _ *accounts.Accounts, userID int64, _, _ string) ([]portal.Registration, error) {
		if userID != member.user.ID {
			return nil, errors.New("portal read with someone else's key")
		}
		return []portal.Registration{{Date: "2026-11-02", Meal: "lunch", Mess: "kadamba-nonveg", Category: "registered"}}, nil
	}
	var ms2 salesState
	member.get(t, "/api/sales", &ms2)
	if ms2.Owner || len(ms2.Sales) != 0 || ms2.Portal != "" || cellAt(ms2, "2026-11-02", "lunch").State != "wasted" || cellAt(ms2, "2026-11-02", "dinner").State != "none" || ms2.Month.Earned != 0 || ms2.Month.Wasted != 1 {
		t.Errorf("member sales: owner %v, %d sales, portal %q, lunch %s, dinner %s, month %+v", ms2.Owner, len(ms2.Sales), ms2.Portal,
			cellAt(ms2, "2026-11-02", "lunch").State, cellAt(ms2, "2026-11-02", "dinner").State, ms2.Month)
	}
	var mb state
	member.get(t, "/api/state", &mb)
	if len(mb.Mine) != 0 {
		t.Errorf("member sees the owner's armed meals: %+v", mb.Mine)
	}

	// Without the portal, sales still show and everything else is unknown.
	// The bill falls back to sales.monthly_fee.
	readRegistrations = func(context.Context, *accounts.Accounts, int64, string, string) ([]portal.Registration, error) {
		return nil, portal.ErrNoKey
	}
	readBill = func(context.Context, *accounts.Accounts, int64, int, int) (portal.Bill, error) {
		return portal.Bill{}, portal.ErrNoKey
	}
	srv2 := startDashboard(t, cfg, st, ms, NewHub())
	admin = srv2.as(t, accounts.Admin)
	srv = srv2
	s = get()
	if s.Portal == "" || cellAt(s, "2026-11-02", "dinner").State != "sold" || cellAt(s, "2026-11-02", "lunch").State != "unknown" || s.Month.Wasted != 0 {
		t.Errorf("portal down: %q dinner %s lunch %s", s.Portal, cellAt(s, "2026-11-02", "dinner").State, cellAt(s, "2026-11-02", "lunch").State)
	}
	if s.Month.Fee != 5600 || s.Month.Bill != nil || s.Month.BillErr == "" {
		t.Errorf("bill fallback: %+v", s.Month)
	}
}

func TestPhone(t *testing.T) {
	for in, want := range map[string]string{
		"919876543210@s.whatsapp.net": "+919876543210", "919876543210:4@s.whatsapp.net": "+919876543210",
		"123@lid": "", "abc@s.whatsapp.net": "", "": "",
	} {
		if got := phone(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// After today's last meal, tomorrow's breakfast comes back as Ahead, so it
// can be sold the night before.
func TestSalesAhead(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Groups = []string{"g1@g.us"}
	loc := cfg.Location()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	orig := readRegistrations
	defer func() { readRegistrations = orig }()
	var to string
	readRegistrations = func(_ context.Context, _ *accounts.Accounts, _ int64, _, t string) ([]portal.Registration, error) {
		to = t
		return []portal.Registration{
			{Date: "2026-11-03", Meal: "dinner", Mess: "palash", Category: "registered"},
			{Date: "2026-11-04", Meal: "breakfast", Mess: "kadamba-nonveg", Category: "registered"},
		}, nil
	}
	origBill := readBill
	defer func() { readBill = origBill }()
	readBill = func(context.Context, *accounts.Accounts, int64, int, int) (portal.Bill, error) {
		return portal.Bill{}, nil
	}

	clock := time.Date(2026, 11, 3, 23, 0, 0, 0, loc)
	ms := mcpserver.New(cfg, st, nil)
	ms.SetClock(func() time.Time { return clock })
	srv := startDashboard(t, cfg, st, ms, NewHub())
	admin := srv.as(t, accounts.Admin)
	get := func() salesState {
		res, err := admin.Get(srv.URL + "/api/sales")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out salesState
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	out := get()
	if to != "2026-11-04" {
		t.Errorf("read registrations to %s, want tomorrow", to)
	}
	if out.Ahead == nil || out.Ahead.Date != "2026-11-04" {
		t.Fatalf("ahead %+v, want tomorrow's breakfast", out.Ahead)
	}
	c := out.Ahead.Cell
	if c.Meal != "breakfast" || c.State != "open" || !c.Sellable || c.Mess != "kadamba" || c.Diet != "nonveg" {
		t.Fatalf("ahead cell %+v", c)
	}
	if last := out.Days[len(out.Days)-1]; last.Date != "2026-11-03" {
		t.Errorf("days end %s, want today", last.Date)
	}

	// Scheduled, it says when it goes up.
	at := time.Unix(c.Start, 0).Add(-15 * time.Minute)
	if _, err := st.CreateSellSchedule(ctx, store.SellSchedule{Date: "2026-11-04", Meal: "breakfast", MinPaise: 4000, MaxPaise: 6000,
		StartsAt: at, EndsAt: time.Unix(c.End, 0), CreatedAt: clock}); err != nil {
		t.Fatal(err)
	}
	if c := get().Ahead.Cell; c.State != "scheduled" || c.Schedule == nil || c.Schedule.At != at.Unix() || c.Schedule.Min != 40 {
		t.Fatalf("scheduled ahead cell %+v", c)
	}

	// During the day there's nothing ahead.
	clock = time.Date(2026, 11, 3, 13, 0, 0, 0, loc)
	if out := get(); out.Ahead != nil || to != "2026-11-03" {
		t.Fatalf("ahead %+v and read to %s at lunchtime", out.Ahead, to)
	}
}
