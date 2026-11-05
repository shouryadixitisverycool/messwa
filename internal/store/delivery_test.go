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
