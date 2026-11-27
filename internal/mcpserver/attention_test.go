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
	if err = s.ResolveAttention(ctx, 1, a[0].ID, a[0].Kind); err != nil {
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
	if items[0].Amount != 40 || items[0].Phone != "+919999999999" {
		t.Fatalf("refund amount/phone: %+v", items[0])
	}
	if err = s.ResolveAttention(ctx, 1, items[0].ID, items[0].Kind); err != nil {
		t.Fatal(err)
	}
	if err = st.RefundSellTaskBuyer(ctx, id, peer, "60.00", "another payment"); err != nil {
		t.Fatal(err)
	}
	items, err = s.Attention(ctx, 1)
	if err != nil || len(items) != 1 || items[0].Amount != 60 {
		t.Fatalf("changed refund was hidden: %v %v", items, err)
	}
}

func TestAttentionPostGroupAndRefundFields(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "fields.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetGroup(ctx, "g1@g.us", "Kadamba Exchange", nil); err != nil {
		t.Fatal(err)
	}
	s := New(config.Default(), st, nil)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })

	tid, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-04", Meal: "lunch", MinPaise: 4000, MaxPaise: 6000, CreatedAt: now, EndsAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ClaimOfferPost(ctx, tid, "g1@g.us", "offer-1", 0, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOfferFailed(ctx, tid, "offer-1", "group send failed"); err != nil {
		t.Fatal(err)
	}
	if err := st.ClaimOfferPost(ctx, tid, "g2@g.us", "offer-2", 0, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOfferFailed(ctx, tid, "offer-2", "timeout"); err != nil {
		t.Fatal(err)
	}
	// Refund buyer with LID only (no phone known).
	if _, err := st.AddSellTaskBuyer(ctx, tid, "77@lid", "", now); err != nil {
		t.Fatal(err)
	}
	if err := st.RefundSellTaskBuyer(ctx, tid, "77@lid", "52.50", "overpaid"); err != nil {
		t.Fatal(err)
	}

	items, err := s.Attention(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	byGroup := map[string]AttentionItem{}
	var refund *AttentionItem
	for i := range items {
		switch items[i].Kind {
		case "post":
			byGroup[items[i].Group] = items[i]
		case "refund":
			refund = &items[i]
		}
	}
	if got := byGroup["g1@g.us"]; got.Group != "g1@g.us" || got.GroupName != "Kadamba Exchange" || got.Task != tid {
		t.Fatalf("named group post item: %+v", got)
	}
	if got := byGroup["g2@g.us"]; got.Group != "g2@g.us" || got.GroupName != "" || got.Task != tid {
		t.Fatalf("unnamed group post item: %+v", got)
	}
	if refund == nil || refund.Amount != 52.5 || refund.Phone != "" || refund.Buyer != "77@lid" {
		t.Fatalf("lid refund item: %+v", refund)
	}
}
