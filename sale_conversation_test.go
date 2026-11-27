package main

import (
	"context"
	"encoding/base64"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"messwa/internal/store"
)

func TestConversationUnreadablePayment(t *testing.T) {
	for _, payload := range []string{"", "unreadable payload"} {
		text := conversationPaymentText(&waE2E.Message{SendPaymentMessage: &waE2E.SendPaymentMessage{TransactionData: proto.String(payload)}})
		if !strings.HasPrefix(text, "Amount unavailable.") || strings.Contains(text, "Paid") {
			t.Fatalf("unreadable payment shows an amount: %s", text)
		}
	}
}

func TestConversationPaymentHidesPayload(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(mockPaymentPayload("65.00", time.Now()))
	text := conversationPaymentText(&waE2E.Message{SendPaymentMessage: &waE2E.SendPaymentMessage{
		TransactionData: proto.String(payload),
		NoteMessage:     &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("lunch")}},
	}})
	if text != "Paid INR 65.00\nNote: lunch" {
		t.Fatalf("payment text = %q", text)
	}
}

// A new buyer's first DM is recorded even when the offer handler admits
// them only after the recorder's first lookup.
func TestConversationWaitsForAdmission(t *testing.T) {
	old := conversationRetry
	conversationRetry = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond}
	defer func() { conversationRetry = old }()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	now := time.Now().Truncate(time.Second)
	task, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-24", Meal: "lunch", MinPaise: 4000, MaxPaise: 4000, CreatedAt: now, EndsAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	m := &sellManager{st: st, base: context.Background(), log: slog.Default()}
	m.recordConversation("buyer", "", "first", now, false, &waE2E.Message{Conversation: proto.String("still available?")})
	time.Sleep(20 * time.Millisecond) // the first lookup misses
	if _, err := st.AddSellTaskBuyer(ctx, task, "buyer", "", now); err != nil {
		t.Fatal(err)
	}
	for range 50 {
		if got, _ := st.SaleMessages(ctx, task, "buyer"); len(got) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("first message was dropped")
}
