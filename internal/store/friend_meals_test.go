package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFriendMeals(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := FriendMeal{Date: "2026-11-13", Slot: "dinner", Mess: "kadamba-nonveg", Friend: "Ravi", Extras: []string{"2 omelette", "lassi"}, QR: []byte("png"), CreatedAt: time.Unix(100, 0)}
	a, err := st.AddFriendMeal(ctx, m)
	if err != nil || a != "dinner#f1" {
		t.Fatalf("first slot %q %v", a, err)
	}
	b, err := st.AddFriendMeal(ctx, m)
	if err != nil || b != "dinner#f2" {
		t.Fatalf("second slot %q %v", b, err)
	}
	if !IsFriendSlot(b) || IsFriendSlot("dinner") || IsFriendSlot("dinner#2") {
		t.Fatal("IsFriendSlot")
	}
	got, err := st.FriendMeal(ctx, "2026-11-13", a)
	if err != nil || got == nil || string(got.QR) != "png" || got.Friend != "Ravi" || len(got.Extras) != 2 || got.Extras[1] != "lassi" {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := st.DeleteFriendMeal(ctx, "2026-11-13", a); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.FriendMeal(ctx, "2026-11-13", a); got != nil {
		t.Fatal("deleted meal still there")
	}
	list, err := st.FriendMeals(ctx, "2026-11-01")
	if err != nil || len(list) != 1 || list[0].Slot != b || list[0].QR != nil {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, err := st.AddFriendMeal(ctx, FriendMeal{Date: "2026-11-13", Slot: "dinner", Mess: "palash-veg"}); err == nil {
		t.Fatal("a meal without a QR was saved")
	}
}
