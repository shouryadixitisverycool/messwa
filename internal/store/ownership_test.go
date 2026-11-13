package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPrivateAccountIsolation(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	board, err := Open(filepath.Join(dir, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer board.Close()
	a, err := OpenPrivate(filepath.Join(dir, "a.db"), board)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenPrivate(filepath.Join(dir, "b.db"), board)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	now := time.Now()
	for i, s := range []*Store{a, b} {
		text := []string{"A secret", "B secret"}[i]
		if err := s.TouchPeer(ctx, "buyer", "", "name "+text, false, now); err != nil {
			t.Fatal(err)
		}
		if err := s.AddDM(ctx, "buyer", "same-message", false, now, text); err != nil {
			t.Fatal(err)
		}
		task := SellTask{UserID: int64(i + 1), Date: "2026-11-13", Meal: "lunch", MinPaise: 100, MaxPaise: 100, CreatedAt: now, EndsAt: now.Add(time.Hour)}
		id, err := s.CreateSellTask(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		if id != 1 {
			t.Fatal(id)
		}
		if _, err := s.AddSellTaskBuyer(ctx, id, text, "", now); err != nil {
			t.Fatal(err)
		}
		d := Delivery{Date: task.Date, Meal: task.Meal, Peer: text, Amount: "1.00", ImageHash: text, ArmedAt: now}
		if _, err := s.ArmNewDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
		if ok, err := s.ClaimDelivery(ctx, d, Payment{ID: "same-payment"}, "same-QR"); err != nil || !ok {
			t.Fatalf("payment: %v %v", ok, err)
		}
	}
	for i, s := range []*Store{a, b} {
		own := []string{"A secret", "B secret"}[i]
		other := []string{"B secret", "A secret"}[i]
		dms, err := s.DMs(ctx, "buyer", now.Add(-time.Hour), 50)
		if err != nil || len(dms) != 1 || dms[0].Text != own {
			t.Fatalf("DM leak: %+v %v", dms, err)
		}
		ds, err := s.Deliveries(ctx)
		if err != nil || len(ds) != 1 || ds[0].Peer != own {
			t.Fatalf("sales leak: %+v %v", ds, err)
		}
		buyers, err := s.SellTaskBuyers(ctx, 1)
		if err != nil || len(buyers) != 1 || buyers[0].Peer != own {
			t.Fatalf("buyer leak: %+v %v", buyers, err)
		}
		if _, changed, err := s.MuteSellTaskBuyer(ctx, 1, true, now, other); err == nil || changed {
			t.Fatalf("acted on other account buyer: %v %v", changed, err)
		}
	}
	// Content deletion and sold state changes cannot cross the database boundary.
	if err := a.DropDMContent(ctx, "buyer"); err != nil {
		t.Fatal(err)
	}
	if msgs, err := b.DMs(ctx, "buyer", now.Add(-time.Hour), 50); err != nil || len(msgs) != 1 {
		t.Fatalf("cross-account deletion: %v %v", msgs, err)
	}
}

func TestLegacyOwnershipSurvivesRoleChangesAndDeletion(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "messwa.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := st.AddUser(ctx, "original", "admin", "hash", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArmNewDelivery(ctx, Delivery{Date: "2026-11-13", Meal: "lunch", Peer: "legacy", Amount: "1.00", ImageHash: "hash", ArmedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	legacyTask, err := st.CreateSellTask(ctx, SellTask{UserID: 42, Date: "2026-11-13", Meal: "dinner", MinPaise: 100, MaxPaise: 100, CreatedAt: time.Now(), EndsAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.AccountDir(ctx, dir, admin); err != nil || got != dir {
		t.Fatalf("migration: %s %v", got, err)
	}
	if task, err := st.GetSellTask(ctx, legacyTask); err != nil || task.UserID != admin {
		t.Fatalf("legacy task owner: %+v %v", task, err)
	}
	other, err := st.AddUser(ctx, "other", "admin", "hash", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.AccountDir(ctx, dir, other)
	if err != nil || got == dir {
		t.Fatalf("ownership transferred: %s %v", got, err)
	}
	private, err := OpenPrivate(filepath.Join(got+".db"), st)
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	if ds, err := private.Deliveries(ctx); err != nil || len(ds) != 0 {
		t.Fatalf("legacy sales leaked: %v %v", ds, err)
	}
	if err := st.RemoveUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	newID, err := st.AddUser(ctx, "new", "admin", "hash", time.Now())
	if err != nil || newID <= other {
		t.Fatalf("reused account ID: %d %v", newID, err)
	}
}
