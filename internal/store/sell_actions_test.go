package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func actionTask(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now()
	id, err := s.CreateSellTask(context.Background(), SellTask{Date: "2026-11-12", Meal: "dinner", MinPaise: 4000, MaxPaise: 6000,
		Mess: "kadamba", Diet: "nonveg", CreatedAt: now, EndsAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

func TestMuteSellTaskBuyer(t *testing.T) {
	ctx := context.Background()
	s, id := actionTask(t)
	if _, _, err := s.MuteSellTaskBuyer(ctx, id, true, time.Now(), "a@s.whatsapp.net"); !errors.Is(err, ErrNoSuchBuyer) {
		t.Fatalf("muting a stranger: %v", err)
	}
	if _, err := s.AddSellTaskBuyer(ctx, id, "a@s.whatsapp.net", "1@lid", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveSellTaskBuyer(ctx, id, "a@s.whatsapp.net", BuyerQueued, BuyerNegotiating, "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	// By alt, the chat they replied from.
	peer, changed, err := s.MuteSellTaskBuyer(ctx, id, true, time.Now(), "1@lid")
	if err != nil || !changed || peer != "a@s.whatsapp.net" {
		t.Fatalf("mute = %q, %v, %v", peer, changed, err)
	}
	if _, changed, _ := s.MuteSellTaskBuyer(ctx, id, true, time.Now(), peer); changed {
		t.Fatal("muted twice")
	}
	for _, p := range []string{peer, "1@lid"} {
		if m, err := s.SellTaskBuyerMuted(ctx, id, p); err != nil || !m {
			t.Fatalf("muted(%s) = %v, %v", p, m, err)
		}
	}
	// A muted buyer is never told it sold, and keeps their state.
	if ok, err := s.ClaimSoldNotice(ctx, id, peer); err != nil || ok {
		t.Fatalf("claimed a sold notice for a muted buyer: %v, %v", ok, err)
	}
	bs, _ := s.SellTaskBuyers(ctx, id)
	if len(bs) != 1 || !bs[0].Muted || bs[0].State != BuyerNegotiating {
		t.Fatalf("buyer = %+v", bs)
	}
	if _, changed, err := s.MuteSellTaskBuyer(ctx, id, false, time.Now(), peer); err != nil || !changed {
		t.Fatalf("unmute = %v, %v", changed, err)
	}
	if m, _ := s.SellTaskBuyerMuted(ctx, id, peer); m {
		t.Fatal("still muted")
	}
	if ok, err := s.ClaimSoldNotice(ctx, id, peer); err != nil || !ok {
		t.Fatalf("sold notice after unmute: %v, %v", ok, err)
	}
	if ok, _ := s.ClaimSoldNotice(ctx, id, peer); ok {
		t.Fatal("sold notice claimed twice")
	}
}

func TestNameSellTaskBuyer(t *testing.T) {
	ctx := context.Background()
	s, id := actionTask(t)
	s.AddSellTaskBuyer(ctx, id, "a@s.whatsapp.net", "", time.Now())
	if err := s.NameSellTaskBuyer(ctx, id, "a@s.whatsapp.net", "Dheeraj"); err != nil {
		t.Fatal(err)
	}
	if bs, _ := s.SellTaskBuyers(ctx, id); len(bs) != 1 || bs[0].Name != "Dheeraj" {
		t.Fatalf("buyers = %+v", bs)
	}
}

func TestRecordTaskManualSale(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 12, 14, 49, 0, 0, time.UTC)

	t.Run("new row", func(t *testing.T) {
		s, id := actionTask(t)
		s.AddSellTaskBuyer(ctx, id, "a@s.whatsapp.net", "1@lid", time.Now())
		if err := s.RecordTaskManualSale(ctx, id, TaskManualSale{Peer: "1@lid", BuyerName: "Utkarsh", Amount: "55.00", At: at}); err != nil {
			t.Fatal(err)
		}
		d, state, err := s.TaskDelivery(ctx, id)
		if err != nil || d == nil || state != "sent" || d.Amount != "55.00" || d.Peer != "1@lid" || d.Mess != "kadamba" || d.Diet != "nonveg" {
			t.Fatalf("delivery = %+v %q %v", d, state, err)
		}
		if kind, _ := s.PaymentKind(ctx, "2026-11-12", "dinner"); kind != PaidManual {
			t.Fatalf("kind = %q", kind)
		}
		paid, sent, _ := s.TaskDeliveryTimes(ctx, id)
		if !paid.Equal(at) || !sent.Equal(at) {
			t.Fatalf("paid %v sent %v", paid, sent)
		}
		if name, _ := s.TaskBuyerName(ctx, id); name != "Utkarsh" {
			t.Fatalf("name = %q", name)
		}
		if bs, _ := s.SellTaskBuyers(ctx, id); bs[0].State != BuyerBought {
			t.Fatalf("buyer = %+v", bs[0])
		}
		if err := s.RecordTaskManualSale(ctx, id, TaskManualSale{Peer: "b", Amount: "50.00", At: at}); !errors.Is(err, ErrAlreadySold) {
			t.Fatalf("second sale: %v", err)
		}
	})

	t.Run("name only", func(t *testing.T) {
		s, id := actionTask(t)
		if err := s.RecordTaskManualSale(ctx, id, TaskManualSale{BuyerName: "a friend", Amount: "50.00", At: at}); err != nil {
			t.Fatal(err)
		}
		if name, _ := s.TaskBuyerName(ctx, id); name != "a friend" {
			t.Fatalf("name = %q", name)
		}
	})

	t.Run("armed reservation", func(t *testing.T) {
		s, id := actionTask(t)
		d := Delivery{Date: "2026-11-12", Meal: "dinner", Peer: "x@s.whatsapp.net", Amount: "60.00", ImageHash: "h", ArmedAt: time.Now()}
		if _, err := s.ArmTaskDelivery(ctx, id, d); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordTaskManualSale(ctx, id, TaskManualSale{Peer: "a@s.whatsapp.net", Amount: "45.00", At: at}); err != nil {
			t.Fatal(err)
		}
		got, state, _ := s.TaskDelivery(ctx, id)
		if state != "sent" || got.Peer != "a@s.whatsapp.net" || got.Amount != "45.00" {
			t.Fatalf("delivery = %+v %q", got, state)
		}
	})

	t.Run("refused", func(t *testing.T) {
		s, id := actionTask(t)
		if err := s.RecordTaskManualSale(ctx, id, TaskManualSale{Amount: "45.00", At: at}); err == nil {
			t.Fatal("sale with no buyer")
		}
		if err := s.RecordTaskManualSale(ctx, id, TaskManualSale{Peer: "a", Amount: "45", At: at}); err == nil {
			t.Fatal("amount without paise")
		}
		// A payment the bot claimed wins.
		d := Delivery{Date: "2026-11-12", Meal: "dinner", Peer: "x", Amount: "60.00", ImageHash: "h", ArmedAt: time.Now()}
		if ok, err := s.ClaimTaskSale(ctx, id, d, Payment{ID: "p1", At: time.Now()}, "m1"); err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
		if err := s.RecordTaskManualSale(ctx, id, TaskManualSale{Peer: "a", Amount: "45.00", At: at}); !errors.Is(err, ErrAlreadySold) {
			t.Fatalf("over a claimed payment: %v", err)
		}
		if _, err := s.EndSellTask(ctx, id, TaskCancelled, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordTaskManualSale(ctx, id, TaskManualSale{Peer: "a", Amount: "45.00", At: at}); err == nil {
			t.Fatal("sale on an ended task")
		}
	})
}
