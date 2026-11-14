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

// On autopilot a buyer who would be parked for you is skipped, and the
// task sells to the others.
func TestAutopilotSkipsUnreadableBuyer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 50)
		id, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, true)
		if err != nil {
			t.Fatal(err)
		}
		if !f.task(id).Autopilot {
			t.Fatal("task not on autopilot")
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "hi"))
		settle()
		f.conn.emit(dm(buyerA, "what do you think about the weather"))
		settle()
		f.conn.emit(dm(buyerA, "the weather is nice"))
		settle()
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerSkipped || b.Note != "sent messages the bot couldn't read" {
			t.Fatalf("A = %+v, want skipped", b)
		}
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		if got := sentTo(f.conn.messages(), buyerB); len(got) != 1 {
			t.Fatalf("B got %+v", got)
		}
	})
}

// A buyer holding the payment turn who wants to pay another way loses the
// turn on autopilot, even though they agreed more, and hears not to pay.
// The next buyer is asked to pay.
func TestAutopilotDropsHolderWhoNeedsYou(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		id, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, true)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		// B agrees the opening 60 and holds the turn.
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		f.conn.emit(dm(buyerB, "ok"))
		settle()
		// C haggles down and agrees less; they wait behind B.
		f.conn.emit(f.reply(buyerC, "price?"))
		settle()
		f.conn.emit(dm(buyerC, "40?"))
		settle()
		f.conn.emit(dm(buyerC, "ok"))
		settle()
		if b := buyerState(t, f, id, buyerB); b.State != store.BuyerOffered {
			t.Fatalf("B = %+v, want asked to pay", b)
		}
		before := len(sentTo(f.conn.messages(), buyerC))

		// B asks twice to pay some other way.
		f.conn.emit(dm(buyerB, "upi id?"))
		settle()
		f.conn.emit(dm(buyerB, "can i pay in other ways"))
		settle()
		// The task's per-minute send limit may hold the second line.
		time.Sleep(time.Minute)
		synctest.Wait()

		b := buyerState(t, f, id, buyerB)
		if b.State != store.BuyerSkipped || !strings.Contains(b.Note, "other than WhatsApp Pay") {
			t.Fatalf("B = %+v, want skipped", b)
		}
		got := texts(sentTo(f.conn.messages(), buyerB))
		if tail := strings.Join(got[len(got)-2:], "|"); tail != "wait dont pay|cant do this one sorry" {
			t.Fatalf("B got %q", got)
		}
		c := buyerState(t, f, id, buyerC)
		if c.State != store.BuyerOffered {
			t.Fatalf("C = %+v, want asked to pay after B was dropped", c)
		}
		if len(sentTo(f.conn.messages(), buyerC)) <= before {
			t.Fatal("C was never asked to pay")
		}
		// C pays and gets the meal.
		f.conn.emit(paymentOf(buyerC, "pay-c", c.Amount, time.Now()))
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerC)); got[len(got)-1] != "[QR]" {
			t.Fatalf("C got %q", got)
		}
	})
}

// Autopilot tasks never pool, so another task's rules never reach them.
func TestAutopilotNeverPools(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	mk := func(meal string, auto bool) int64 {
		id, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-11", Meal: meal, MinPaise: 3000, MaxPaise: 6000, Mess: "kadamba", Diet: "veg",
			CreatedAt: now, EndsAt: now.Add(time.Hour), Autopilot: auto})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	plain := mk("lunch", false)
	friend := mk("lunch#f1", false)
	auto := mk("lunch#f2", true)
	if p, _ := st.SellTaskPool(ctx, plain); len(p) != 2 {
		t.Fatalf("plain pool %d tasks, want it and the friend's", len(p))
	}
	if p, _ := st.SellTaskPool(ctx, auto); len(p) != 1 || p[0].ID != auto || !p[0].Autopilot {
		t.Fatalf("autopilot pool %+v, want itself alone", p)
	}
	_ = friend
}
