package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"messwa/internal/store"
)

// quietA starts a negotiating task (30 to 60, usual price 40) and has
// buyer A reply to the post, so the bot opens at 60.
func quietA(t *testing.T, f *sellFixture, ctx context.Context) (*sellManager, int64) {
	t.Helper()
	m := f.negotiatingManager(ctx, 40)
	id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
	if err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
	f.conn.emit(f.reply(buyerA, "price?"))
	settle()
	if got := texts(sentTo(f.conn.messages(), buyerA)); strings.Join(got, "|") != "60" {
		t.Fatalf("A got %q", got)
	}
	return m, id
}

func TestNudgeQuietBuyerThenDropAndTimeOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		_, id := quietA(t, f, ctx)

		time.Sleep(nudgeAfter)
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); strings.Join(got, "|") != "60|yes or no?" {
			t.Fatalf("A got %q", got)
		}
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerNegotiating || b.Nudges != 1 || b.Ask != 6000 {
			t.Fatalf("A = %+v after the nudge", b)
		}

		// Still quiet: one lower price, the usual 40, and the bot stops
		// waiting on them.
		time.Sleep(nudgeAfter)
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); strings.Join(got, "|") != "60|yes or no?|can do 40 if you want it" {
			t.Fatalf("A got %q", got)
		}
		b := buyerState(t, f, id, buyerA)
		if b.State != store.BuyerTimedOut || b.Nudges != 2 || b.Ask != 4000 || !slices.Contains(b.Quoted, 4000) {
			t.Fatalf("A = %+v after the drop", b)
		}
		evs := eventsFor(t, f, id, buyerA)
		if n := len(slices.DeleteFunc(slices.Clone(evs), func(k string) bool { return k != store.EventNudged })); n != 2 {
			t.Fatalf("events = %q", evs)
		}

		// Nothing more while they stay quiet.
		time.Sleep(f.silence())
		synctest.Wait()
		if got := sentTo(f.conn.messages(), buyerA); len(got) != 3 {
			t.Fatalf("A got %q", texts(got))
		}

		// A late reply brings them back at the lower price.
		f.conn.emit(dm(buyerA, "ok"))
		settle()
		toA := texts(sentTo(f.conn.messages(), buyerA))
		if len(toA) != 6 || toA[3] != "ok 40" {
			t.Fatalf("A got %q", toA)
		}
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerOffered || b.Amount != "40.00" {
			t.Fatalf("A = %+v", b)
		}
	})
}

func TestNudgeNoNegotiates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		_, id := quietA(t, f, ctx)
		time.Sleep(nudgeAfter)
		synctest.Wait()

		// "no" to "yes or no?" gets the pass counter, not a goodbye.
		f.conn.emit(dm(buyerA, "no"))
		settle()
		if got := texts(sentTo(f.conn.messages(), buyerA)); strings.Join(got, "|") != "60|yes or no?|how about 40" {
			t.Fatalf("A got %q", got)
		}
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerNegotiating || !b.PassCountered {
			t.Fatalf("A = %+v", b)
		}
		// They spoke, so no more nudges: the usual silence applies.
		time.Sleep(2 * nudgeAfter)
		synctest.Wait()
		if got := sentTo(f.conn.messages(), buyerA); len(got) != 3 {
			t.Fatalf("A got %q", texts(got))
		}
	})
}

func TestNudgeYesAgrees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		_, id := quietA(t, f, ctx)
		time.Sleep(nudgeAfter)
		synctest.Wait()
		f.conn.emit(dm(buyerA, "yes"))
		settle()
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerOffered || b.Amount != "60.00" {
			t.Fatalf("A = %+v", b)
		}
	})
}

func TestNoNudgeOnceBuyerWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		_, id := quietA(t, f, ctx)
		// "only whatsapp pay sorry" doesn't count as a negotiation message,
		// but the buyer wrote, so they get no nudge.
		f.conn.emit(dm(buyerA, "upi id?"))
		settle()
		time.Sleep(3 * nudgeAfter)
		synctest.Wait()
		for _, s := range texts(sentTo(f.conn.messages(), buyerA)) {
			if s == "yes or no?" {
				t.Fatalf("A was nudged after writing")
			}
		}
		if b := buyerState(t, f, id, buyerA); b.Nudges != 0 || b.State != store.BuyerNegotiating {
			t.Fatalf("A = %+v", b)
		}
	})
}

func TestNudgeResumesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, stop := context.WithCancel(t.Context())
		_, id := quietA(t, f, ctx)
		time.Sleep(nudgeAfter)
		synctest.Wait()
		stop()
		synctest.Wait()

		ctx2, cancel := context.WithCancel(t.Context())
		defer cancel()
		m2 := f.negotiatingManager(ctx2, 40)
		if err := m2.Resume(ctx2, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		// The restart doesn't nudge again; a minute later comes the drop.
		time.Sleep(nudgeAfter)
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); strings.Join(got, "|") != "60|yes or no?|can do 40 if you want it" {
			t.Fatalf("A got %q", got)
		}
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerTimedOut {
			t.Fatalf("A = %+v", b)
		}
	})
}
