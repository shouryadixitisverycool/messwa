package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSellTaskClaims(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "data.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	task := SellTask{Date: "2026-11-11", Meal: "lunch", MinPaise: 3000, MaxPaise: 6000, CreatedAt: now, EndsAt: now.Add(time.Hour)}
	if _, err := s.CreateSellTask(ctx, SellTask{Date: "2026-11-11", Meal: "lunch", MinPaise: 6000, MaxPaise: 3000}); err == nil {
		t.Fatal("task with max below min was created")
	}
	id, err := s.CreateSellTask(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSellTask(ctx, task); err == nil {
		t.Fatal("second active task for one meal")
	}

	if err := s.ClaimOfferPost(ctx, id, "g@g.us", "offer", now); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimOfferPost(ctx, id, "g@g.us", "offer2", now); err == nil {
		t.Fatal("group posted twice")
	}
	if ok, _ := s.OfferClaimed(ctx, id, "g@g.us", "offer"); !ok {
		t.Fatal("claimed post not sendable")
	}
	s.SetOfferState(ctx, id, "offer", OfferPosted)
	if ok, _ := s.OfferClaimed(ctx, id, "g@g.us", "offer"); ok {
		t.Fatal("posted offer could be sent again")
	}

	if ok, _ := s.AddSellTaskBuyer(ctx, id, "1@s.whatsapp.net", "9@lid", now); !ok {
		t.Fatal("buyer not added")
	}
	if ok, _ := s.AddSellTaskBuyer(ctx, id, "1@s.whatsapp.net", "", now); ok {
		t.Fatal("buyer added twice")
	}
	if ok, _ := s.IsSellTaskBuyer(ctx, id, "9@lid"); !ok {
		t.Fatal("buyer's LID chat not recognised")
	}
	if ok, _ := s.IsSellTaskBuyer(ctx, id, "2@s.whatsapp.net"); ok {
		t.Fatal("stranger counted as a buyer")
	}
	if ok, _ := s.MoveSellTaskBuyer(ctx, id, "1@s.whatsapp.net", BuyerQueued, BuyerOffered, "60.00", now); !ok {
		t.Fatal("prompt not claimed")
	}
	if ok, _ := s.MoveSellTaskBuyer(ctx, id, "1@s.whatsapp.net", BuyerQueued, BuyerOffered, "60.00", now); ok {
		t.Fatal("prompt claimed twice")
	}

	d := Delivery{Date: "2026-11-11", Meal: "lunch", Peer: "1@s.whatsapp.net", Amount: "60.00", ImageHash: "h", ArmedAt: now}
	if _, err := s.ArmTaskDelivery(ctx, id, d); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// After a restart the claims hold.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, state, err := s.TaskDelivery(ctx, id); err != nil || got == nil || state != "armed" || got.Amount != "60.00" {
		t.Fatalf("delivery after restart = %+v %q %v", got, state, err)
	}
	if buyers, _ := s.SellTaskBuyers(ctx, id); len(buyers) != 1 || buyers[0].State != BuyerOffered || buyers[0].Alt != "9@lid" {
		t.Fatalf("buyers after restart = %+v", buyers)
	}
	if ok, _ := s.EndSellTask(ctx, id, TaskCancelled, "cancelled", now); !ok {
		t.Fatal("task not ended")
	}
	if ok, _ := s.AddSellTaskBuyer(ctx, id, "3@s.whatsapp.net", "", now); ok {
		t.Fatal("ended task took a new buyer")
	}
	if err := s.ClaimOfferPost(ctx, id, "h@g.us", "offer3", now); err == nil {
		t.Fatal("ended task claimed a post")
	}
	if ok, _ := s.EndSellTask(ctx, id, TaskSold, "", now); ok {
		t.Fatal("ended task ended again")
	}
	// The meal still has its delivery record, so it can't be put up again.
	if _, err := s.CreateSellTask(ctx, task); err == nil {
		t.Fatal("new task for a meal with a delivery record")
	}
}

func TestPaise(t *testing.T) {
	for in, want := range map[string]int64{"60.00": 6000, "0.50": 50, "1.05": 105} {
		if got, err := ParsePaise(in); err != nil || got != want {
			t.Errorf("ParsePaise(%q) = %d, %v", in, got, err)
		}
		if FormatPaise(want) != in {
			t.Errorf("FormatPaise(%d) = %q", want, FormatPaise(want))
		}
	}
	for _, bad := range []string{"60", "60.0", "-1.00", "a.00", ".00"} {
		if _, err := ParsePaise(bad); err == nil {
			t.Errorf("ParsePaise(%q) accepted", bad)
		}
	}
}

func TestClaimBuyerLine(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "data.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	id, err := s.CreateSellTask(ctx, SellTask{Date: "2026-11-12", Meal: "lunch", MinPaise: 2000, MaxPaise: 3000, CreatedAt: now, EndsAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	const buyer = "1@s.whatsapp.net"
	if ok, _ := s.ClaimBuyerLine(ctx, id, buyer, "pay_other"); ok {
		t.Fatal("line claimed for someone who isn't a buyer")
	}
	s.AddSellTaskBuyer(ctx, id, buyer, "", now)
	// A buyer mid-turn can still hear a line.
	s.MoveSellTaskBuyer(ctx, id, buyer, BuyerQueued, BuyerOffered, "30.00", now)
	for _, kind := range []string{"pay_other", "screenshot"} {
		if ok, err := s.ClaimBuyerLine(ctx, id, buyer, kind); !ok || err != nil {
			t.Fatalf("%s not claimed: %v", kind, err)
		}
	}
	// Each is said once, also after a restart.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, kind := range []string{"pay_other", "screenshot"} {
		if ok, _ := s.ClaimBuyerLine(ctx, id, buyer, kind); ok {
			t.Fatalf("%s claimed twice", kind)
		}
	}
	if ok, _ := s.ClaimBuyerLine(ctx, id, buyer, "pay"); !ok {
		t.Fatal("a kind that prefixes a claimed one counted as claimed")
	}
	if _, err := s.ClaimBuyerLine(ctx, id, buyer, "a,b"); err == nil {
		t.Fatal("kind with a comma accepted")
	}
}
