package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
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

func TestConversationPaymentPlainNote(t *testing.T) {
	text := conversationPaymentText(&waE2E.Message{SendPaymentMessage: &waE2E.SendPaymentMessage{
		NoteMessage: &waE2E.Message{Conversation: proto.String(" dinner ")},
	}})
	if text != "Amount unavailable.\nNote: dinner" {
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

// withInteropNote appends the Android UPI interopNote entry, laid out as in
// TestPaymentInteropNote, to a payload.
func withInteropNote(t *testing.T, payload []byte, note string) string {
	t.Helper()
	metadata, err := hex.DecodeString("0b00000069006e007400650072006f0070004e006f007400650000000100000015000000500000000f0000006a006100760061002e006c0061006e0067002e0043006c00610073007300000025000000aced0005767200106a6176612e6c616e672e537472696e67a0f0a4387a3bb342020000787000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(appendParcelString(append(payload, metadata...), note))
}

func TestConversationPaymentInteropNote(t *testing.T) {
	data := withInteropNote(t, mockPaymentPayload("65.00", time.Now()), " table 3 ")
	text := conversationPaymentText(&waE2E.Message{SendPaymentMessage: &waE2E.SendPaymentMessage{TransactionData: proto.String(data)}})
	if text != "Paid INR 65.00\nNote: table 3" {
		t.Fatalf("interopNote only: %q", text)
	}
	text = conversationPaymentText(&waE2E.Message{SendPaymentMessage: &waE2E.SendPaymentMessage{
		TransactionData: proto.String(data),
		NoteMessage:     &waE2E.Message{Conversation: proto.String("lunch")},
	}})
	if text != "Paid INR 65.00\nNote: lunch" {
		t.Fatalf("noteMessage first: %q", text)
	}
}

// oldDump is a payment row as the code before the payment card stored it.
func oldDump(t *testing.T, label string, pay *waE2E.SendPaymentMessage) string {
	t.Helper()
	b, err := protojson.Marshal(pay)
	if err != nil {
		t.Fatal(err)
	}
	var pretty any
	if err := json.Unmarshal(b, &pretty); err != nil {
		t.Fatal(err)
	}
	b, _ = json.MarshalIndent(pretty, "", "  ")
	return label + "\n\nRetained payment details:\n" + string(b)
}

func TestOldPaymentText(t *testing.T) {
	at := time.Now()
	plain := base64.StdEncoding.EncodeToString(mockPaymentPayload("65.00", at))
	for _, c := range []struct{ old, want string }{
		{oldDump(t, "Reported amount: INR 65.00 (unverified).", &waE2E.SendPaymentMessage{
			TransactionData: proto.String(plain),
			NoteMessage:     &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("")}}}),
			"Paid INR 65.00"},
		{oldDump(t, "Reported amount: INR 65.00 (unverified).", &waE2E.SendPaymentMessage{
			TransactionData: proto.String(plain),
			NoteMessage:     &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("lunch\n")}}}),
			"Paid INR 65.00\nNote: lunch"},
		{oldDump(t, "Reported amount: INR 65.00 (unverified).", &waE2E.SendPaymentMessage{
			TransactionData: proto.String(withInteropNote(t, mockPaymentPayload("65.00", at), "table 3"))}),
			"Paid INR 65.00\nNote: table 3"},
		{oldDump(t, "Amount unavailable.", &waE2E.SendPaymentMessage{
			TransactionData: proto.String("unreadable"),
			NoteMessage:     &waE2E.Message{Conversation: proto.String("dinner")}}),
			"Amount unavailable.\nNote: dinner"},
		{"Reported amount: INR 65.00 (unverified).\n\nRetained payment details:\n{not json", "Paid INR 65.00"},
		{"Amount unavailable.\n\nRetained payment details:\n{not json", "Amount unavailable."},
	} {
		if got := oldPaymentText(c.old); got != c.want {
			t.Errorf("oldPaymentText(%q) = %q, want %q", c.old, got, c.want)
		}
	}
}

// The real hook is wired up: opening a database rewrites an old row.
func TestOpenRewritesOldPaymentText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data := withInteropNote(t, mockPaymentPayload("65.00", time.Now()), "table 3")
	old := oldDump(t, "Reported amount: INR 65.00 (unverified).", &waE2E.SendPaymentMessage{TransactionData: proto.String(data)})
	if err := st.AddSaleMessage(t.Context(), 1, "buyer", store.SaleMessage{ID: "p", Kind: "payment", Text: old}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if st, err = store.Open(path); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.SaleMessages(t.Context(), 1, "buyer")
	if err != nil || len(got) != 1 || got[0].Text != "Paid INR 65.00\nNote: table 3" {
		t.Fatalf("messages: %+v %v", got, err)
	}
}
