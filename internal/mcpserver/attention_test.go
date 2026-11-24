package mcpserver

import (
	"context"
	"messwa/internal/config"
	"messwa/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestAttentionOldIssuesAndResolutionIsolation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(config.Default(), st, nil)
	old := time.Now().Add(-72 * time.Hour)
	id, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-10", Meal: "lunch", MinPaise: 1000, MaxPaise: 4000, CreatedAt: old, EndsAt: old.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.EndSellTask(ctx, id, store.TaskFailed, "Connection failed", old); err != nil {
		t.Fatal(err)
	}
	a, err := s.Attention(ctx, 1)
	if err != nil || len(a) != 1 {
		t.Fatalf("old failure missing: %v %v", a, err)
	}
	again, _ := s.Attention(ctx, 1)
	if again[0].ID != a[0].ID {
		t.Fatal("unstable issue ID")
	}
	if err = s.ResolveAttention(ctx, 1, a[0].ID); err != nil {
		t.Fatal(err)
	}
	a, err = s.Attention(ctx, 1)
	if err != nil || len(a) != 0 {
		t.Fatalf("resolution not saved: %v %v", a, err)
	}
	a, err = s.Attention(ctx, 2)
	if err != nil || len(a) != 1 {
		t.Fatalf("resolution leaked to another user: %v %v", a, err)
	}
	v, err := s.AttentionSale(ctx, id)
	if err != nil || v == nil || v.ID != id {
		t.Fatalf("old sale inaccessible: %v %v", v, err)
	}
}

func TestAttentionRefundSurvivesBoardLinger(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(config.Default(), st, nil)
	old := time.Now().Add(-72 * time.Hour)
	id, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-10", Meal: "dinner", MinPaise: 1000, MaxPaise: 4000, CreatedAt: old, EndsAt: old.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	peer := "919999999999@s.whatsapp.net"
	if _, err = st.AddSellTaskBuyer(ctx, id, peer, "", old); err != nil {
		t.Fatal(err)
	}
	if err = st.RefundSellTaskBuyer(ctx, id, peer, "40.00", "late payment"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.EndSellTask(ctx, id, store.TaskCancelled, "cancelled", old); err != nil {
		t.Fatal(err)
	}
	items, err := s.Attention(ctx, 1)
	if err != nil || len(items) != 1 || items[0].Title != "Refund due" {
		t.Fatalf("refund missing: %v %v", items, err)
	}
	// The dashboard opens the buyer the refund is for, by the key the sale
	// view gives that buyer.
	v, err := s.AttentionSale(ctx, id)
	if err != nil || v == nil || len(v.Buyers) != 1 || items[0].Buyer != v.Buyers[0].Key {
		t.Fatalf("refund buyer %q does not match the sale view: %+v %v", items[0].Buyer, v, err)
	}
	if err = s.ResolveAttention(ctx, 1, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = st.RefundSellTaskBuyer(ctx, id, peer, "60.00", "another payment"); err != nil {
		t.Fatal(err)
	}
	items, err = s.Attention(ctx, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("changed refund was hidden: %v %v", items, err)
	}
}
