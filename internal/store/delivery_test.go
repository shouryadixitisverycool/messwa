package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGroupDeliveryReservesUnusedSlot(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := Delivery{Date: "2026-11-03", Meal: "lunch", Peer: "buyer", Amount: "1.00", ImageHash: "hash", ArmedAt: time.Now()}
	if exists, err := s.DeliveryExists(ctx, d.Date, d.Meal); err != nil || exists {
		t.Fatalf("unused slot exists=%t, err=%v", exists, err)
	}
	armed, err := s.ArmNewDelivery(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := s.DeliveryExists(ctx, d.Date, d.Meal); err != nil || !exists {
		t.Fatalf("reserved slot exists=%t, err=%v", exists, err)
	}
	if _, err := s.ArmNewDelivery(ctx, d); err == nil {
		t.Fatal("same buyer reserved the same meal again")
	}
	other := d
	other.Peer = "second buyer"
	if _, err := s.ArmNewDelivery(ctx, other); err == nil {
		t.Fatal("second buyer reserved the same meal")
	}
	if resumed, err := s.ArmDelivery(ctx, d); err != nil || !resumed.Equal(armed) {
		t.Fatalf("first reservation changed, resumed=%v, err=%v", resumed, err)
	}
}

func TestReleaseUnpaidDeliveryAllowsNextBuyer(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := Delivery{Date: "2026-11-08", Meal: "lunch", Peer: "a", Amount: "1.00", ImageHash: "hash", ArmedAt: time.Now()}
	second := first
	second.Peer = "b"
	if _, err := s.ArmNewDelivery(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseUnpaidDelivery(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArmNewDelivery(ctx, second); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimDelivery(ctx, first, Payment{ID: "late-a"}, "image-a"); err != nil || ok {
		t.Fatalf("old buyer claimed the new reservation: %t, %v", ok, err)
	}
	if ok, err := s.ClaimDelivery(ctx, second, Payment{ID: "payment-b"}, "image-b"); err != nil || !ok {
		t.Fatalf("next buyer could not claim the reservation: %t, %v", ok, err)
	}
	if err := s.ReleaseUnpaidDelivery(ctx, second); err == nil {
		t.Fatal("a claimed delivery was released")
	}
}

func TestDeliveryClaimSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := Delivery{Date: "2026-11-03", Meal: "lunch", Peer: "buyer", Amount: "60.00", ImageHash: "hash", ArmedAt: time.Now()}
	armed, err := s.ArmDelivery(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	other := d
	other.Peer = "another buyer"
	if _, err := s.ArmDelivery(ctx, other); err == nil {
		t.Fatal("changed buyer accepted")
	}
	other = d
	other.Amount = "30.00"
	if _, err := s.ArmDelivery(ctx, other); err == nil {
		t.Fatal("changed price accepted")
	}
	other = d
	other.ImageHash = "other image"
	if _, err := s.ArmDelivery(ctx, other); err == nil {
		t.Fatal("changed image accepted")
	}
	d.ArmedAt = d.ArmedAt.Add(time.Hour)
	resumed, err := s.ArmDelivery(ctx, d)
	if err != nil || !resumed.Equal(armed) {
		t.Fatal("resuming changed the arming time", err)
	}
	var claims atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.ClaimDelivery(ctx, d, Payment{ID: "payment"}, "outgoing")
			if err != nil {
				t.Error(err)
			}
			if ok {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("claimed %d times", claims.Load())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ArmDelivery(ctx, d); err == nil {
		t.Fatal("uncertain send retried after restart")
	}
	if err := s.FinishDelivery(ctx, d, "wrong id"); err == nil {
		t.Fatal("wrong outgoing id accepted")
	}
	if err := s.FinishDelivery(ctx, d, "outgoing"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArmDelivery(ctx, d); err == nil {
		t.Fatal("sent meal rearmed")
	}
	ok, err := s.ClaimDelivery(ctx, d, Payment{ID: "another payment"}, "another outgoing")
	if err != nil || ok {
		t.Fatal("sent meal reclaimed", err)
	}
	other = d
	other.Meal = "dinner"
	if _, err := s.ArmDelivery(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDelivery(ctx, other, Payment{ID: "payment"}, "second outgoing"); err == nil {
		t.Fatal("payment reused for another meal")
	}
}

func TestHandedOffSlot(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	offer := Delivery{Date: "2026-11-12", Meal: "lunch", Peer: "a", Amount: "60.00", ImageHash: "portal:x", ArmedAt: time.Unix(1794000000, 0), Mess: "palash", Diet: "veg"}
	if err := s.HandOffDelivery(ctx, offer, "Asha"); err != nil {
		t.Fatal(err)
	}
	if err := s.HandOffDelivery(ctx, offer, "Asha"); err == nil {
		t.Fatal("handed the same meal off twice")
	}
	if _, err := s.ArmNewDelivery(ctx, offer); err == nil {
		t.Fatal("a new group offer took a handed-off meal")
	}
	ds, err := s.Deliveries(ctx)
	if err != nil || len(ds) != 1 || ds[0].State != "handed_off" || ds[0].BuyerName != "Asha" || ds[0].Mess != "palash" {
		t.Fatalf("ledger %+v, %v", ds, err)
	}

	// You agreed 50 with someone else; deliver-qr -peer takes the slot over.
	agreed := offer
	agreed.Peer, agreed.Amount, agreed.ArmedAt, agreed.Mess, agreed.Diet = "b", "50.00", offer.ArmedAt.Add(time.Hour), "", ""
	armed, err := s.ArmDelivery(ctx, agreed)
	if err != nil || !armed.Equal(agreed.ArmedAt) {
		t.Fatalf("resume armed at %v, %v", armed, err)
	}
	ds, _ = s.Deliveries(ctx)
	if d := ds[0]; d.State != "armed" || d.Peer != "b" || d.Amount != "50.00" || d.BuyerName != "" || d.Mess != "palash" {
		t.Fatalf("resumed %+v", d)
	}
	if freed, err := s.ReleaseHandoff(ctx, offer.Date, offer.Meal); err != nil || freed {
		t.Fatalf("released an armed meal: %t, %v", freed, err)
	}

	// A handoff that falls through frees the meal for a new offer.
	other := offer
	other.Meal = "dinner"
	if err := s.HandOffDelivery(ctx, other, ""); err != nil {
		t.Fatal(err)
	}
	if freed, err := s.ReleaseHandoff(ctx, other.Date, other.Meal); err != nil || !freed {
		t.Fatalf("release: %t, %v", freed, err)
	}
	if _, err := s.ArmNewDelivery(ctx, other); err != nil {
		t.Fatal(err)
	}
}

// TestReleaseUnpaid frees what a stopped deliver-qr -peer leaves armed, and
// nothing a buyer paid for or a running sell task holds.
func TestReleaseUnpaid(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ReleaseUnpaid(ctx, "2026-11-12", "lunch"); err == nil {
		t.Fatal("released a meal with no reservation")
	}
	d := Delivery{Date: "2026-11-12", Meal: "lunch", Peer: "a", Amount: "15.00", ImageHash: "portal:x", ArmedAt: time.Unix(1794000000, 0)}
	if _, err := s.ArmDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
	if freed, err := s.ReleaseHandoff(ctx, d.Date, d.Meal); err != nil || freed {
		t.Fatalf("release-handoff without -unpaid freed an armed meal: %t, %v", freed, err)
	}
	if state, err := s.ReleaseUnpaid(ctx, d.Date, d.Meal); err != nil || state != "armed" {
		t.Fatalf("release armed: %q, %v", state, err)
	}
	if _, err := s.ArmNewDelivery(ctx, d); err != nil {
		t.Fatalf("meal not free after release: %v", err)
	}

	// Paid, sending and sent meals stay.
	if ok, err := s.ClaimDelivery(ctx, d, Payment{ID: "pay"}, "out"); err != nil || !ok {
		t.Fatalf("claim: %t, %v", ok, err)
	}
	if _, err := s.ReleaseUnpaid(ctx, d.Date, d.Meal); err == nil {
		t.Fatal("released a meal being sent")
	}
	if err := s.FinishDelivery(ctx, d, "out"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseUnpaid(ctx, d.Date, d.Meal); err == nil {
		t.Fatal("released a sent meal")
	}

	// A handed-off meal is freed too.
	handed := d
	handed.Meal = "dinner"
	if err := s.HandOffDelivery(ctx, handed, ""); err != nil {
		t.Fatal(err)
	}
	if state, err := s.ReleaseUnpaid(ctx, handed.Date, handed.Meal); err != nil || state != "handed_off" {
		t.Fatalf("release handoff: %q, %v", state, err)
	}

	// An active sell task's reservation is the task's to free.
	task := SellTask{Date: "2026-11-13", Meal: "lunch", MinPaise: 2000, MaxPaise: 3000, CreatedAt: time.Now(), EndsAt: time.Now().Add(time.Hour)}
	id, err := s.CreateSellTask(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	td := Delivery{Date: task.Date, Meal: task.Meal, Peer: "c", Amount: "20.00", ImageHash: "portal:x", ArmedAt: time.Now()}
	if _, err := s.ArmTaskDelivery(ctx, id, td); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseUnpaid(ctx, td.Date, td.Meal); err == nil {
		t.Fatal("released a reservation an active sell task holds")
	}
	if ok, _ := s.EndSellTask(ctx, id, TaskFailed, "stopped", time.Now()); !ok {
		t.Fatal("could not end the task")
	}
	if state, err := s.ReleaseUnpaid(ctx, td.Date, td.Meal); err != nil || state != "armed" {
		t.Fatalf("release after the task ended: %q, %v", state, err)
	}
}
