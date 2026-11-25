package main

import (
	"context"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"messwa/internal/store"
)

// A buyer asked to pay who then offers less than they agreed didn't take
// the deal. They lose the turn and wait for you, with no word from the
// bot, and the next buyer is asked to pay. On autopilot they're skipped
// and hear not to pay.
func TestHolderWhoOffersLessLosesTurn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		autopilot bool
		state     string
		note      string
		told      []string
	}{
		{"manual", false, store.BuyerParked, "agreed ₹60, then offered ₹50; check the chat", nil},
		{"autopilot", true, store.BuyerSkipped, "agreed ₹60, then offered ₹50; check the chat", []string{"wait dont pay", "cant do this one sorry"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.db")
			synctest.Test(t, func(t *testing.T) {
				f := newSellFixture(t, path)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				m := f.negotiatingManager(ctx, 60)
				id, _, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, tc.autopilot)
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()

				// B agrees the opening 60 and holds the turn.
				f.conn.emit(f.reply(buyerB, "price?"))
				settle()
				f.conn.emit(dm(buyerB, "ok"))
				settle()
				// C replies while B holds the turn; C stays paused silently
				// until B's turn ends.
				f.conn.emit(f.reply(buyerC, "60"))
				settle()
				if b := buyerState(t, f, id, buyerB); b.State != store.BuyerOffered {
					t.Fatalf("B = %+v, want asked to pay", b)
				}
				if got := sentTo(f.conn.messages(), buyerC); len(got) != 0 {
					t.Fatalf("C was messaged while B held the turn: %+v", got)
				}
				before := len(sentTo(f.conn.messages(), buyerB))
				beforeC := len(sentTo(f.conn.messages(), buyerC))

				f.conn.emit(dm(buyerB, "Naa I said for 50 bro"))
				settle()
				time.Sleep(time.Minute)
				synctest.Wait()

				b := buyerState(t, f, id, buyerB)
				if b.State != tc.state || b.Note != tc.note {
					t.Fatalf("B = %+v, want %s with note %q", b, tc.state, tc.note)
				}
				got := texts(sentTo(f.conn.messages(), buyerB))[before:]
				if len(got) != len(tc.told) {
					t.Fatalf("B was told %q, want %q", got, tc.told)
				}
				for i := range got {
					if got[i] != tc.told[i] {
						t.Fatalf("B was told %q, want %q", got, tc.told)
					}
				}
				if c := buyerState(t, f, id, buyerC); c.State != store.BuyerOffered {
					t.Fatalf("C = %+v, want asked to pay after B backed out", c)
				}
				if len(sentTo(f.conn.messages(), buyerC)) <= beforeC {
					t.Fatal("C was never asked to pay")
				}
			})
		})
	}
}

// Naming the agreed price, or more, after the pay prompt keeps the turn.
func TestHolderWhoRepeatsPriceKeepsTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		id, _, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		f.conn.emit(dm(buyerB, "ok"))
		settle()
		f.conn.emit(dm(buyerB, "60 right?"))
		settle()
		if b := buyerState(t, f, id, buyerB); b.State != store.BuyerOffered {
			t.Fatalf("B = %+v, want still asked to pay", b)
		}
	})
}

// Rejecting the agreed price by number ("Won't buy at 60") drops the turn
// instead of treating 60 as repeating the agreed price.
func TestHolderWhoRejectsAgreedPriceLosesTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		id, _, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		f.conn.emit(dm(buyerB, "ok"))
		settle()
		f.conn.emit(dm(buyerB, "Won't buy at 60"))
		settle()
		if b := buyerState(t, f, id, buyerB); b.State != store.BuyerParked {
			t.Fatalf("B = %+v, want parked after rejecting agreed price", b)
		}
	})
}

// "Can't do, too much" during a payment turn is a price objection (backOut),
// not a missing-payment-app prompt.
func TestHolderCantDoTooMuchBacksOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.AcceptPaymentImages = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		id, _, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		f.conn.emit(dm(buyerB, "ok"))
		settle()
		f.conn.emit(dm(buyerB, "Can't do, too much"))
		settle()
		if b := buyerState(t, f, id, buyerB); b.State != store.BuyerParked {
			t.Fatalf("B = %+v, want parked after price objection", b)
		}
		for _, msg := range texts(sentTo(f.conn.messages(), buyerB)) {
			if msg == "send to my number then" {
				t.Fatalf("B was told to pay number instead of backing out")
			}
		}
	})
}

// A burst with a price objection followed by an apology ("too much",
// "i do not have sorry") releases the turn instead of sending payment info.
func TestHolderTooMuchThenApologyReleasesTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.AcceptPaymentImages = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		id, _, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		f.conn.emit(dm(buyerB, "ok"))
		settle()
		f.conn.emit(dm(buyerB, "too much"))
		f.conn.emit(dm(buyerB, "i do not have sorry"))
		settle()
		if b := buyerState(t, f, id, buyerB); b.State != store.BuyerParked {
			t.Fatalf("B = %+v, want parked after too much + apology", b)
		}
		for _, msg := range texts(sentTo(f.conn.messages(), buyerB)) {
			if msg == "send to my number then" {
				t.Fatalf("B was told to pay number instead of releasing turn")
			}
		}
	})
}

// "less balance, can i pay later" after agreeing is a pay-other request, not
// a price objection that backs out or drops the buyer on autopilot.
func TestHolderLessBalancePayLaterDoesNotBackOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		id, _, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, true)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		f.conn.emit(dm(buyerB, "ok"))
		settle()
		f.conn.emit(dm(buyerB, "less balance, can i pay later"))
		settle()
		if b := buyerState(t, f, id, buyerB); b.State != store.BuyerOffered {
			t.Fatalf("B = %+v, want still holding turn (not backed out)", b)
		}
	})
}

// A multi-meal request during an active payment turn releases the turn for the
// next buyer instead of leaving it open.
func TestHolderMultiMealReleasesTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		id, _, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, false)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		f.conn.emit(dm(buyerB, "ok"))
		settle()
		f.conn.emit(f.reply(buyerC, "60"))
		settle()
		f.conn.emit(dm(buyerB, "120 for both?"))
		settle()
		time.Sleep(time.Minute)
		synctest.Wait()
		if b := buyerState(t, f, id, buyerB); b.State != store.BuyerParked {
			t.Fatalf("B = %+v, want parked after multi-meal request", b)
		}
		if c := buyerState(t, f, id, buyerC); c.State != store.BuyerOffered {
			t.Fatalf("C = %+v, want asked to pay after B released turn", c)
		}
	})
}
