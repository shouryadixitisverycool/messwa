package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSellTaskPool(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	create := func(meal, mess, diet, handoff string, lo, hi int64) int64 {
		t.Helper()
		id, err := s.CreateSellTask(ctx, SellTask{Date: "2026-11-11", Meal: meal, Mess: mess, Diet: diet, Handoff: handoff,
			MinPaise: lo * 100, MaxPaise: hi * 100, CreatedAt: now, EndsAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	own := create("dinner", "kadamba", "nonveg", "never", 50, 60)
	friend := create("dinner#f1", "kadamba", "nonveg", "", 40, 50)
	create("dinner#f2", "kadamba", "veg", "", 40, 50)                // other diet
	create("dinner#f3", "kadamba", "nonveg", "", 45, 45)             // fixed price
	create("dinner#f4", "kadamba", "nonveg", "on_agreement", 40, 50) // hands off
	create("lunch", "kadamba", "nonveg", "", 40, 50)                 // other meal

	pool, err := s.SellTaskPool(ctx, own)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 2 || pool[0].ID != own || pool[1].ID != friend {
		t.Fatalf("pool = %+v", pool)
	}

	// The leader may quote down to the pool's floor of ₹40.
	if ok, err := s.AddSellTaskBuyer(ctx, own, "buyer", "", now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.ClaimNegotiationMessage(ctx, own, "buyer", 0, Negotiation{Ask: 3500, BotMsgs: 1, StartedAt: now}); err != nil || ok {
		t.Fatalf("quoted under the pool's floor: %v %v", ok, err)
	}
	// A quote allowed by the pool must not authorize this task to sell
	// under its own floor.
	if ok, err := s.ClaimNegotiationMessage(ctx, own, "buyer", 0, Negotiation{Ask: 4500, Quoted: []int64{6000, 4500}, BotMsgs: 1, StartedAt: now}); err != nil || !ok {
		t.Fatalf("could not quote within the pool's floor: %v %v", ok, err)
	}
	// So it can't agree under its own floor.
	if ok, _ := s.AgreeSellTaskBuyer(ctx, own, "buyer", "45.00", Negotiation{Ask: 4500, BotMsgs: 1}, now); ok {
		t.Fatal("agreed under the task's own floor")
	}
	// Agreeing at a valid price must not reintroduce lower pool quotes
	// from the in-memory haggle as prices this seller may charge.
	if ok, err := s.AddSellTaskBuyer(ctx, own, "other", "", now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.AgreeSellTaskBuyer(ctx, own, "other", "55.00", Negotiation{Ask: 5500, Quoted: []int64{4500, 5500}}, now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.BuyerPriceHonoured(ctx, own, "other", 4500); err != nil || ok {
		t.Fatalf("agreement made a lower pool quote chargeable: %v %v", ok, err)
	}

	// Passing to the friend's task at a price under its floor fails; at
	// its floor it works, and quotes under that floor stay behind.
	if ok, err := s.PassSellTaskBuyer(ctx, own, friend, "buyer", "35.00", Negotiation{}, now); err != nil || ok {
		t.Fatalf("passed under the floor: %v %v", ok, err)
	}
	if ok, err := s.PassSellTaskBuyer(ctx, own, friend, "buyer", "45.00", Negotiation{Ask: 4500, Quoted: []int64{6000, 4500, 3000}, BotMsgs: 2, Nudges: 1}, now); err != nil || !ok {
		t.Fatalf("pass failed: %v %v", ok, err)
	}
	buyers, _ := s.SellTaskBuyers(ctx, friend)
	if len(buyers) != 1 || buyers[0].State != BuyerAgreed || buyers[0].Agreed != "45.00" || buyers[0].BotMsgs != 2 || buyers[0].Nudges != 1 || len(buyers[0].Quoted) != 2 {
		t.Fatalf("friend's buyers = %+v", buyers)
	}
	left, _ := s.SellTaskBuyers(ctx, own)
	if len(left) != 2 || left[0].State != BuyerMoved {
		t.Fatalf("own buyers = %+v", left)
	}
	if ok, _ := s.IsSellTaskBuyer(ctx, own, "buyer"); ok {
		t.Fatal("the task that passed a buyer may still message them")
	}
	if ok, _ := s.PassSellTaskBuyer(ctx, own, friend, "buyer", "", Negotiation{}, now); ok {
		t.Fatal("passed a buyer twice")
	}
}
