package store

import (
	"path/filepath"
	"testing"
	"time"
)

// A buy-post ask the pool's floor allows doesn't let the leader sell below
// its own floor: saving it records no quote.
func TestBuyPostAskIsNotAQuote(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	create := func(meal string, lo, hi int64) int64 {
		t.Helper()
		id, err := s.CreateSellTask(ctx, SellTask{Date: "2026-11-11", Meal: meal, Mess: "bakul", Diet: "nonveg",
			MinPaise: lo * 100, MaxPaise: hi * 100, CreatedAt: now, EndsAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	leader := create("lunch", 70, 80)
	create("lunch#2", 50, 60)
	if ok, err := s.AddSellTaskBuyer(ctx, leader, "poster", "", now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.SetBuyPostAsk(ctx, leader, "poster", 5500); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.BuyerPriceHonoured(ctx, leader, "poster", 5500); err != nil || ok {
		t.Fatalf("leader with a 70 floor honours 55 = %v, %v", ok, err)
	}
	// The pool may still quote it.
	if ok, err := s.ClaimNegotiationMessage(ctx, leader, "poster", 0, Negotiation{Ask: 5500, Quoted: []int64{5500}, BotMsgs: 1, StartedAt: now}); err != nil || !ok {
		t.Fatalf("pool opening at 55 = %v, %v", ok, err)
	}
	if ok, err := s.BuyerPriceHonoured(ctx, leader, "poster", 5500); err != nil || ok {
		t.Fatalf("after the opening, leader honours 55 = %v, %v", ok, err)
	}
}
