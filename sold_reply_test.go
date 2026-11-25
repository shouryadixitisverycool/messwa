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

func TestNewBuyerAfterMealSold(t *testing.T) {
	for _, mode := range []string{"fixed", "negotiated", "manual", "plain"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.db")
			synctest.Test(t, func(t *testing.T) {
				f := newSellFixture(t, path)
				f.cfg.Sell.AcceptPlainDMs = mode == "plain"
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				m := f.negotiatingManager(ctx, 40)
				maximum := 60
				if mode == "fixed" {
					maximum = 30
				}
				id, err := m.Start(ctx, 0, f.date, "lunch", 30, maximum, "")
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				offer := f.offerID()
				if mode == "manual" {
					if err := m.MarkSold(ctx, id, buyerA.String(), "Test buyer", maximum, "", false); err != nil {
						t.Fatal(err)
					}
				} else {
					f.conn.emit(f.reply(buyerA, "buy"))
					settle()
					amount := "60.00"
					if maximum == 30 {
						amount = "30.00"
					}
					f.conn.emit(paymentOf(buyerA, "pay-a", amount, time.Now()))
				}
				synctest.Wait()
				if task := f.task(id); task.State != store.TaskSold {
					t.Fatalf("task = %+v", task)
				}
				time.Sleep(time.Second)
				// A first-time buyer writes only after the sale has ended.
				for range 3 {
					if mode == "plain" {
						f.conn.emit(plainDM(buyerB, "buy lunch?", time.Now()))
					} else {
						f.conn.emit(replyTo(buyerB, f.group, offer, "buy"))
					}
				}
				settle()
				if got := texts(sentTo(f.conn.messages(), buyerB)); strings.Join(got, "|") != "sorry, sold" {
					t.Fatalf("new buyer got %q", got)
				}
				if b := buyerState(t, f, id, buyerB); b.State != store.BuyerToldSold {
					t.Fatalf("buyer = %+v", b)
				}
				// The buyer who paid must never get a sold reply, even if they write again.
				before := len(sentTo(f.conn.messages(), buyerA))
				f.conn.emit(replyTo(buyerA, f.group, offer, "thanks"))
				settle()
				if got := sentTo(f.conn.messages(), buyerA); len(got) != before {
					t.Fatalf("winner got extra messages: %+v", got[before:])
				}
				cancel()
				synctest.Wait()
				ctx2, cancel2 := context.WithCancel(t.Context())
				defer cancel2()
				f.negotiatingManager(ctx2, 40)
				f.conn.emit(replyTo(buyerB, f.group, offer, "still available?"))
				f.conn.emit(replyTo(stranger, f.group, offer, "buy"))
				settle()
				if got := texts(sentTo(f.conn.messages(), buyerB)); strings.Join(got, "|") != "sorry, sold" {
					t.Fatalf("repeat buyer got %q", got)
				}
				if got := texts(sentTo(f.conn.messages(), stranger)); strings.Join(got, "|") != "sorry, sold" {
					t.Fatalf("new buyer after restart got %q", got)
				}
			})
		})
	}
}

func TestSoldReplyIgnoresUnrelatedOrIneligibleMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 30, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		offer := f.offerID()
		if err := m.MarkSold(ctx, id, buyerA.String(), "", 30, "", false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Second)
		f.conn.emit(plainDM(buyerB, "buy", time.Now())) // Plain DMs disabled.
		f.conn.emit(replyTo(buyerB, f.group, "unrelated-post", "buy"))
		f.conn.emit(replyTo(buyerB, f.group, offer, "no thanks"))
		settle()
		if got := sentTo(f.conn.messages(), buyerB); len(got) > 0 {
			t.Fatalf("unexpected messages: %+v", got)
		}
		time.Sleep(time.Until(f.task(id).EndsAt) + time.Second)
		f.conn.emit(replyTo(buyerB, f.group, offer, "buy"))
		settle()
		if got := sentTo(f.conn.messages(), buyerB); len(got) > 0 {
			t.Fatalf("messages after meal ended: %+v", got)
		}
	})
}

func TestSoldReplyLetsActivePoolAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.AcceptPlainDMs = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 40)
		first, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		_, err = m.Start(ctx, 0, f.date, "lunch#2", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		offer := f.offerID()
		if err := m.MarkSold(ctx, first, buyerA.String(), "", 60, "", false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Second)
		f.conn.emit(replyTo(buyerB, f.group, offer, "buy"))
		f.conn.emit(plainDM(stranger, "buy lunch?", time.Now()))
		settle()
		for _, msgs := range [][]string{texts(sentTo(f.conn.messages(), buyerB)), texts(sentTo(f.conn.messages(), stranger))} {
			if len(msgs) == 0 || strings.Contains(strings.Join(msgs, "|"), "sold") {
				t.Fatalf("active pool buyer got %q", msgs)
			}
		}
	})
}

func TestSoldReplyStopsFifteenMinutesAfterSale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 30, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		offer := f.offerID()
		// Selling near the meal's end still gets the full 15 minutes after sale.
		time.Sleep(time.Until(f.task(id).EndsAt.Add(-time.Minute)))
		if err := m.MarkSold(ctx, id, buyerA.String(), "", 30, "", false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		soldAt := f.task(id).EndedAt
		time.Sleep(time.Until(soldAt.Add(14*time.Minute + 55*time.Second)))
		f.conn.emit(replyTo(buyerB, f.group, offer, "buy"))
		settle()
		if got := texts(sentTo(f.conn.messages(), buyerB)); strings.Join(got, "|") != "sorry, sold" {
			t.Fatalf("inside window got %q", got)
		}
		cancel()
		synctest.Wait()
		ctx2, cancel2 := context.WithCancel(t.Context())
		defer cancel2()
		f.manager(ctx2)
		time.Sleep(time.Until(soldAt.Add(store.SoldReplyWindow)))
		f.conn.emit(replyTo(stranger, f.group, offer, "buy"))
		settle()
		if got := sentTo(f.conn.messages(), stranger); len(got) != 0 {
			t.Fatalf("after cutoff got %+v", got)
		}
	})
}
