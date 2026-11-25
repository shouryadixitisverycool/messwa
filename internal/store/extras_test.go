package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestExtraSlotsUsageAndPoolIsolation(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if !IsExtraSlot("dinner#e1") || !IsExtraSlot("dinner#e12") || IsExtraSlot("dinner") || IsExtraSlot("dinner#f1") || IsExtraSlot("dinner#e") || IsExtraSlot("dinner#e1a") {
		t.Fatal("IsExtraSlot classification failed")
	}
	if got := BaseMeal("dinner#e2"); got != "dinner" {
		t.Fatalf("BaseMeal = %q", got)
	}

	now := time.Unix(1700000000, 0)
	ends := now.Add(2 * time.Hour)

	// Main dinner task and two egg extra tasks must NOT pool with each other,
	// while the two egg tasks MUST pool together.
	dinnerID, err := st.CreateSellTask(ctx, SellTask{
		Date: "2026-11-25", Meal: "dinner", MinPaise: 4000, MaxPaise: 6000,
		Mess: "kadamba", Diet: "nonveg", CreatedAt: now, EndsAt: ends,
	})
	if err != nil {
		t.Fatal(err)
	}
	egg1, err := st.CreateSellTask(ctx, SellTask{
		Date: "2026-11-25", Meal: "dinner#e1", Extra: "Egg", MinPaise: 1000, MaxPaise: 1500,
		Mess: "kadamba", CreatedAt: now, EndsAt: ends,
	})
	if err != nil {
		t.Fatal(err)
	}
	egg2, err := st.CreateSellTask(ctx, SellTask{
		Date: "2026-11-25", Meal: "dinner#e2", Extra: "egg", MinPaise: 1000, MaxPaise: 1500,
		Mess: "kadamba", CreatedAt: now, EndsAt: ends,
	})
	if err != nil {
		t.Fatal(err)
	}

	dinnerPool, err := st.SellTaskPool(ctx, dinnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dinnerPool) != 1 || dinnerPool[0].ID != dinnerID {
		t.Fatalf("dinner pooled with extras: %+v", dinnerPool)
	}
	eggPool, err := st.SellTaskPool(ctx, egg1)
	if err != nil {
		t.Fatal(err)
	}
	if len(eggPool) != 2 || eggPool[0].ID != egg1 || eggPool[1].ID != egg2 {
		t.Fatalf("egg pool = %+v", eggPool)
	}

	// Fixed-price extras for the same extra on the same meal pool together
	// (never with negotiating extras), so repricing keeps shared posts intact.
	fix1, err := st.CreateSellTask(ctx, SellTask{
		Date: "2026-11-26", Meal: "dinner#e1", Extra: "Egg", MinPaise: 1500, MaxPaise: 1500,
		Mess: "kadamba", CreatedAt: now, EndsAt: ends,
	})
	if err != nil {
		t.Fatal(err)
	}
	fix2, err := st.CreateSellTask(ctx, SellTask{
		Date: "2026-11-26", Meal: "dinner#e2", Extra: "Egg", MinPaise: 1500, MaxPaise: 1500,
		Mess: "kadamba", CreatedAt: now, EndsAt: ends,
	})
	if err != nil {
		t.Fatal(err)
	}
	negEgg, err := st.CreateSellTask(ctx, SellTask{
		Date: "2026-11-26", Meal: "dinner#e3", Extra: "Egg", MinPaise: 1000, MaxPaise: 1500,
		Mess: "kadamba", CreatedAt: now, EndsAt: ends,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixPool, err := st.SellTaskPool(ctx, fix1)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixPool) != 2 || fixPool[0].ID != fix1 || fixPool[1].ID != fix2 {
		t.Fatalf("fixed egg pool = %+v (neg=%d)", fixPool, negEgg)
	}

	// Schedule one more egg on 2026-11-25 and sell egg1.
	if _, err := st.CreateSellSchedule(ctx, SellSchedule{
		Date: "2026-11-25", Meal: "dinner#e3", Extra: "Egg", MinPaise: 1000, MaxPaise: 1500,
		StartsAt: now.Add(time.Minute), EndsAt: ends, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArmTaskDelivery(ctx, egg1, Delivery{
		Date: "2026-11-25", Meal: "dinner#e1", Extra: "Egg", Peer: "1@s.whatsapp.net",
		Amount: "15.00", ImageHash: "h", ArmedAt: now, Mess: "kadamba",
	}); err != nil {
		t.Fatal(err)
	}
	usage, err := st.ExtraSlotUsage(ctx, "2026-11-25", "dinner", "egg", "")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Sold != 0 || usage.Armed != 1 || usage.Active != 1 || usage.Scheduled != 1 {
		t.Fatalf("usage = %+v, want sold=0 armed=1 active=1 scheduled=1", usage)
	}
	excl, err := st.ExtraSlotUsage(ctx, "2026-11-25", "dinner", "egg", "dinner#e2")
	if err != nil {
		t.Fatal(err)
	}
	if excl.Armed != 1 || excl.Active != 0 || excl.Scheduled != 1 {
		t.Fatalf("usage excluding dinner#e2 = %+v, want armed=1 active=0 scheduled=1", excl)
	}
	alloc, err := st.AllocatedExtras(ctx, "2026-11-25", "dinner")
	if err != nil {
		t.Fatal(err)
	}
	u := alloc["egg"]
	if u.Sold+u.Armed+u.Active+u.Scheduled != 3 {
		t.Fatalf("allocated = %+v", alloc)
	}
	free, err := st.FreeExtraSlots(ctx, "2026-11-25", "dinner", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(free) != 2 || free[0] != "dinner#e4" || free[1] != "dinner#e5" {
		t.Fatalf("free slots = %+v", free)
	}
}
