package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"messwa/internal/notify"
	"messwa/internal/store"
)

type mockSelfAlertSender struct {
	conn      *mockConn
	connected bool
	selfJID   types.JID
}

func (s *mockSelfAlertSender) Connected() bool                         { return s.connected }
func (s *mockSelfAlertSender) SelfJID(context.Context) (types.JID, error) { return s.selfJID, nil }
func (s *mockSelfAlertSender) SendText(ctx context.Context, to types.JID, text string) error {
	_, err := s.conn.send(ctx, to, &waE2E.Message{Conversation: proto.String(text)})
	return err
}

// End-to-end check: real store + mcpserver.Attention + notify.Dispatcher +
// notify.WhatsAppChannel delivering through mockConn (from mock_chat.go).
func TestWhatsAppSelfAlertsEndToEndWithMockChat(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := config.Default()
	cfg.Dash.PublicURL = "https://mess.example.com"
	cfg.Alerts.WhatsApp = true

	loc, _ := time.LoadLocation("Asia/Kolkata")
	now := time.Date(2026, 11, 20, 12, 30, 0, 0, loc)

	// Create an active sell task with 2 failed group posts (like a lunch offer
	// failing in 2 groups).
	taskID, err := st.CreateSellTask(ctx, store.SellTask{
		Date: "2026-11-20", Meal: "lunch", MinPaise: 3000, MaxPaise: 5000,
		CreatedAt: now.Add(-5 * time.Minute), EndsAt: now.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, group := range []string{"g1@g.us", "g2@g.us"} {
		oid := "OFFER" + string(rune('1'+i))
		if err := st.ClaimOfferPost(ctx, taskID, group, oid, 0, now); err != nil {
			t.Fatal(err)
		}
		if err := st.SetOfferState(ctx, taskID, oid, store.OfferFailed); err != nil {
			t.Fatal(err)
		}
	}

	srv := mcpserver.New(cfg, st, nil)
	conn := newMockConn(types.NewJID("120363000000000001", types.GroupServer))
	selfJID := types.NewJID("919876543210", types.DefaultUserServer)

	d := notify.NewDispatcher(notify.DispatcherConfig{
		UserID: 1,
		Store:  st,
		Config: func() *config.Config { return cfg.Live() },
		Source: func(ctx context.Context) ([]notify.Item, error) {
			return attentionToNotifyItems(srv.Attention(ctx, 1))
		},
		BaseURL:     func() string { return cfg.Dash.BaseURL() },
		Now:         func() time.Time { return now },
		BatchWindow: 15 * time.Second,
	})
	d.Register(notify.NewWhatsAppChannel(&mockSelfAlertSender{
		conn:      conn,
		connected: true,
		selfJID:   selfJID,
	}))

	// First step buffers both failed posts.
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, msgs := conn.snapshot(); len(msgs) != 0 {
		t.Fatalf("expected 0 messages during batch window, got %d", len(msgs))
	}

	// After batch window, one self-chat message lands in mockConn.
	now = now.Add(15 * time.Second)
	if err := d.Step(ctx); err != nil {
		t.Fatal(err)
	}
	_, msgs := conn.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 self-alert message, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Chat != selfJID.String() {
		t.Fatalf("chat = %q, want self JID %q", msgs[0].Chat, selfJID.String())
	}
	if !strings.HasPrefix(msgs[0].Text, "⚠️ Lunch offer didn't post in 2 groups. Fix: https://mess.example.com/#attn-") {
		t.Fatalf("unexpected self-alert text: %q", msgs[0].Text)
	}
}
