package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"messwa/internal/store"
)

func priceEvents(t *testing.T, f *sellFixture, id int64) []store.SellEvent {
	t.Helper()
	events, err := f.st.SellTaskEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.SellEvent
	for _, e := range events {
		if e.Kind == store.EventPrices {
			out = append(out, e)
		}
	}
	return out
}

// TestSetPricesLowersFloorMidNegotiation is the 12 Nov dinner case: the
// floor drops while a buyer is haggling, and their next offer is read
// against the new range without reposting or restarting the task.
func TestSetPricesLowersFloorMidNegotiation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		// The market says 50, so with a floor of 60 the bot holds at 60.
		m := f.negotiatingManager(ctx, 50)
		id, err := m.Start(ctx, 0, f.date, "lunch", 60, 70, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "price?"))
		settle()
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 1 || got[0] != "70" {
			t.Fatalf("A got %q", got)
		}
		posts := len(sentTo(f.conn.messages(), f.group))

		if err := m.SetPrices(ctx, id, 40, 70); err != nil {
			t.Fatal(err)
		}
		task := f.task(id)
		if task.State != store.TaskActive || task.MinPaise != 4000 || task.MaxPaise != 7000 || task.TargetPaise != 5000 {
			t.Fatalf("task = %+v", task)
		}
		if ev := priceEvents(t, f, id); len(ev) != 1 || ev[0].N != 4000 || ev[0].Text != "40.00-70.00 was 60.00-70.00" {
			t.Fatalf("price events = %+v", ev)
		}

		// ₹50 was below the old floor; now it meets the target and is agreed.
		f.conn.emit(dm(buyerA, "50"))
		settle()
		toA := texts(sentTo(f.conn.messages(), buyerA))
		if len(toA) != 3 || toA[1] != "ok 50" {
			t.Fatalf("A got %q", toA)
		}
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerOffered || b.Amount != "50.00" {
			t.Fatalf("A = %+v", b)
		}
		if got := len(sentTo(f.conn.messages(), f.group)); got != posts {
			t.Fatalf("the edit reposted: %d posts, was %d", got, posts)
		}
		f.conn.emit(paymentOf(buyerA, "pay-a", "50.00", time.Now()))
		synctest.Wait()
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
	})
}

// TestSetPricesRaisedFloorKeepsQuotesAndDeals: a raised floor never takes
// back a price the bot already quoted to a buyer, or one it agreed.
func TestSetPricesRaisedFloorKeepsQuotesAndDeals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 40)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		// A takes the opening price and holds the payment turn. B agrees
		// ₹40 and waits behind A.
		f.conn.emit(f.reply(buyerA, "ok same number?"))
		settle()
		f.conn.emit(f.reply(buyerB, "40"))
		settle()
		if b := buyerState(t, f, id, buyerB); b.State != store.BuyerAgreed || b.Agreed != "40.00" {
			t.Fatalf("B = %+v", b)
		}

		if err := m.SetPrices(ctx, id, 50, 60); err != nil {
			t.Fatal(err)
		}
		// A never pays. B's turn comes at the ₹40 they agreed, below the
		// new floor, and their payment gets the meal.
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if b := buyerState(t, f, id, buyerB); b.State != store.BuyerOffered || b.Amount != "40.00" {
			t.Fatalf("B = %+v", b)
		}
		f.conn.emit(paymentOf(buyerB, "pay-b", "40.00", time.Now()))
		synctest.Wait()
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
		if d, state, _ := f.st.TaskDelivery(ctx, id); state != "sent" || d.Amount != "40.00" || d.Peer != buyerB.String() {
			t.Fatalf("delivery = %+v %q", d, state)
		}
	})
}

func TestSetPricesRaisedFloorKeepsQuotedAsk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 40)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "30?"))
		settle()
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 1 || got[0] != "how about 45" {
			t.Fatalf("A got %q", got)
		}
		if err := m.SetPrices(ctx, id, 50, 60); err != nil {
			t.Fatal(err)
		}
		// The bot neither goes below its ₹45 nor raises it to the new floor.
		f.conn.emit(dm(buyerA, "40?"))
		settle()
		toA := texts(sentTo(f.conn.messages(), buyerA))
		if len(toA) < 2 || !strings.HasPrefix(toA[1], "45") {
			t.Fatalf("A got %q", toA)
		}
		f.conn.emit(dm(buyerA, "ok"))
		settle()
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerOffered || b.Agreed != "45.00" || b.Amount != "45.00" {
			t.Fatalf("A = %+v", b)
		}
		// Nobody else was quoted ₹45, so the store still holds the new
		// floor for them.
		f.conn.emit(f.reply(buyerB, "price?"))
		synctest.Wait()
		if ok, _ := f.st.AgreeSellTaskBuyer(ctx, id, buyerB.String(), "45.00", store.Negotiation{}, time.Now()); ok {
			t.Fatal("store agreed below the raised floor with a buyer never quoted that price")
		}
	})
}

// TestSetPricesListsOffersTheNewFloorMeets is the Shrish case from 12 Nov:
// a buyer offered ₹40 under a ₹50 floor and timed out, then the floor
// dropped to ₹40. They show in needs_you, the bot doesn't message them,
// and a payment of their offer gets the meal.
func TestSetPricesListsOffersTheNewFloorMeets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 50)
		id, err := m.Start(ctx, 0, f.date, "lunch", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "40"))
		f.conn.emit(f.reply(buyerB, "35"))
		settle()
		time.Sleep(f.silence() + time.Second)
		synctest.Wait()
		for _, buyer := range []string{buyerA.String(), buyerB.String()} {
			if b := buyerStateOf(t, f, id, buyer); b.State != store.BuyerTimedOut {
				t.Fatalf("%s = %+v", buyer, b)
			}
		}
		sentA := len(sentTo(f.conn.messages(), buyerA))

		if err := m.SetPrices(ctx, id, 40, 60); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		a := buyerState(t, f, id, buyerA)
		if a.State != store.BuyerTimedOut || !strings.Contains(a.Note, "offered 40") {
			t.Fatalf("A = %+v", a)
		}
		if b := buyerState(t, f, id, buyerB); b.Note != "" {
			t.Fatalf("B's ₹35 is still below the floor but B = %+v", b)
		}
		if got := len(sentTo(f.conn.messages(), buyerA)); got != sentA {
			t.Fatalf("the bot messaged A")
		}

		f.conn.emit(paymentOf(buyerA, "pay-a", "40.00", time.Now()))
		synctest.Wait()
		if toA := texts(sentTo(f.conn.messages(), buyerA)); toA[len(toA)-1] != "[QR]" {
			t.Fatalf("A got %q", toA)
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
	})
}

func buyerStateOf(t *testing.T, f *sellFixture, id int64, peer string) store.SellBuyer {
	t.Helper()
	buyers, err := f.st.SellTaskBuyers(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range buyers {
		if b.Peer == peer {
			return b
		}
	}
	t.Fatalf("no buyer %s", peer)
	return store.SellBuyer{}
}

func TestSetPricesValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 0)
		id, err := m.Start(ctx, 0, f.date, "lunch", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		fixed, err := m.Start(ctx, 0, f.date, "dinner", 50, 50, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for _, c := range []struct {
			name     string
			id       int64
			min, max int
			want     string
		}{
			{"min above max", id, 60, 50, "max_price between"},
			{"zero floor", id, 0, 60, "at least 1"},
			{"negative", id, -5, 60, "at least 1"},
			{"too high", id, 50, 20000, "10000"},
			{"negotiating to fixed", id, 55, 55, "above min_price"},
			{"fixed to negotiating", fixed, 40, 60, "must be equal"},
			{"no such task", 999, 40, 60, "no sell task"},
		} {
			if err := m.SetPrices(ctx, c.id, c.min, c.max); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
			}
		}
		if task := f.task(id); task.MinPaise != 5000 || task.MaxPaise != 6000 {
			t.Fatalf("a rejected edit changed the task: %+v", task)
		}
		if ev := priceEvents(t, f, id); len(ev) != 0 {
			t.Fatalf("a rejected edit was logged: %+v", ev)
		}
		// A fixed-price task can move its one price.
		if err := m.SetPrices(ctx, fixed, 45, 45); err != nil {
			t.Fatal(err)
		}
		if task := f.task(fixed); task.MinPaise != 4500 || task.MaxPaise != 4500 {
			t.Fatalf("fixed = %+v", task)
		}

		if err := m.Cancel(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := m.SetPrices(ctx, id, 40, 60); err == nil || !strings.Contains(err.Error(), "not active") {
			t.Fatalf("edit on a cancelled task: %v", err)
		}
		// The store refuses on its own too.
		if _, err := f.st.SetSellTaskPrices(ctx, id, 4000, 6000); err == nil {
			t.Fatal("store edited a cancelled task")
		}
		if _, err := f.st.SetSellTaskPrices(ctx, fixed, 5000, 4000); err == nil {
			t.Fatal("store took max below min")
		}
	})
}

// TestSetPricesSurvivesRestart: the new range is in the store, so a resumed
// task negotiates with it.
func TestSetPricesSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, stop := context.WithCancel(t.Context())
		m := f.negotiatingManager(ctx, 50)
		id, err := m.Start(ctx, 0, f.date, "lunch", 60, 70, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := m.SetPrices(ctx, id, 40, 70); err != nil {
			t.Fatal(err)
		}
		stop()
		synctest.Wait()

		ctx2, cancel := context.WithCancel(t.Context())
		defer cancel()
		m2 := f.negotiatingManager(ctx2, 50)
		if err := m2.Resume(ctx2, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if task := f.task(id); task.State != store.TaskActive || task.MinPaise != 4000 || task.TargetPaise != 5000 {
			t.Fatalf("task = %+v", task)
		}
		f.conn.emit(f.reply(buyerA, "50"))
		settle()
		if b := buyerState(t, f, id, buyerA); b.Agreed != "50.00" {
			t.Fatalf("A = %+v", b)
		}
	})
}

// TestSetPricesFlaggedBuyerWritesAgain: a buyer listed for an offer the
// new floor meets who then writes again is revived, leaves needs_you, and
// gets the new range.
func TestSetPricesFlaggedBuyerWritesAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 40)
		id, err := m.Start(ctx, 0, f.date, "lunch", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "40"))
		settle()
		time.Sleep(f.silence() + time.Second)
		synctest.Wait()
		if err := m.SetPrices(ctx, id, 40, 60); err != nil {
			t.Fatal(err)
		}
		if a := buyerState(t, f, id, buyerA); a.State != store.BuyerTimedOut || a.Note == "" {
			t.Fatalf("A = %+v", a)
		}
		f.conn.emit(dm(buyerA, "40"))
		settle()
		a := buyerState(t, f, id, buyerA)
		if a.Note != "" || a.Agreed != "40.00" || a.State != store.BuyerOffered {
			t.Fatalf("A = %+v", a)
		}
	})
}
