package store

import (
	"path/filepath"
	"testing"
	"time"
)

// The price named in a reply to a buy post counts as a quote, so the
// opening still claims after the floor rises above it.
func TestBuyPostAskIsAQuote(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	id, err := s.CreateSellTask(ctx, SellTask{Date: "2026-11-11", Meal: "lunch", Mess: "bakul", Diet: "nonveg",
		MinPaise: 8000, MaxPaise: 11000, CreatedAt: now, EndsAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AddSellTaskBuyer(ctx, id, "poster", "", now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.SetBuyPostAsk(ctx, id, "poster", 9000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSellTaskPrices(ctx, id, 9500, 11000); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimNegotiationMessage(ctx, id, "poster", 0, Negotiation{Ask: 9000, Quoted: []int64{9000}, BotMsgs: 1, StartedAt: now}); err != nil || !ok {
		t.Fatalf("claim at the quoted 90 = %v, %v", ok, err)
	}
}
