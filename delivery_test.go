package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"messwa/internal/store"
)

func TestPortalMealLabel(t *testing.T) {
	if got := portalMealLabel("bakul", "nonveg", "lunch"); got != "bakul non-veg lunch" {
		t.Fatalf("unexpected label %q", got)
	}
	if got := portalMealLabel("kadamba", "", "dinner"); got != "kadamba dinner" {
		t.Fatalf("unexpected label %q", got)
	}
}

func TestDeliveryPaymentGate(t *testing.T) {
	now := time.Unix(1793700000, 0)
	armed := now.Add(-time.Minute)
	ends := now.Add(time.Hour)
	payload, _ := base64.StdEncoding.DecodeString(observedPaymentPrefix)
	binary.LittleEndian.PutUint64(payload[8:16], uint64(now.UnixMilli()))
	good := func() *events.Message {
		return &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("123", types.DefaultUserServer)}, ID: "payment", Timestamp: now}, Message: &waE2E.Message{SendPaymentMessage: &waE2E.SendPaymentMessage{TransactionData: proto.String(base64.StdEncoding.EncodeToString(payload))}}}
	}
	if !qualifiesForDelivery(good(), "1.00", armed, ends, now) {
		t.Fatal("valid payment rejected")
	}
	short := good()
	short.Message.SendPaymentMessage.TransactionData = proto.String("")
	if err := deliveryRejection(short, "1.00", armed, ends, now); !errors.Is(err, errTruncatedPayment) {
		t.Fatalf("expected recovery for the incomplete payment, got %v", err)
	}
	short.Info.Timestamp = armed.Add(-time.Second)
	if err := deliveryRejection(short, "1.00", armed, ends, now); errors.Is(err, errTruncatedPayment) {
		t.Fatal("old payment must be rejected before recovery")
	}
	cases := map[string]func(*events.Message){
		"outgoing":       func(e *events.Message) { e.Info.IsFromMe = true },
		"group":          func(e *events.Message) { e.Info.IsGroup = true },
		"broadcast":      func(e *events.Message) { e.Info.Chat = types.NewJID("status", types.BroadcastServer) },
		"old message":    func(e *events.Message) { e.Info.Timestamp = armed.Add(-time.Second) },
		"future message": func(e *events.Message) { e.Info.Timestamp = now.Add(time.Minute) },
		"missing id":     func(e *events.Message) { e.Info.ID = "" },
		"ordinary text": func(e *events.Message) {
			e.Message = &waE2E.Message{Conversation: proto.String("Unknown message type, please view it on the WhatsApp app")}
		},
		"bad payload": func(e *events.Message) { e.Message.SendPaymentMessage.TransactionData = proto.String("garbage") },
		"replayed old transaction": func(e *events.Message) {
			e.Message.SendPaymentMessage.TransactionData = proto.String(observedPaymentPrefix)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e := good()
			change(e)
			if qualifiesForDelivery(e, "1.00", armed, ends, now) {
				t.Fatal("invalid event accepted")
			}
		})
	}
	if qualifiesForDelivery(good(), "2.00", armed, ends, now) {
		t.Fatal("wrong amount accepted")
	}
	if qualifiesForDelivery(good(), "1.00", armed, now, now) {
		t.Fatal("expired meal accepted")
	}
}

func TestDeliverOnce(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "uncertain network error"}[fail], func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			d := store.Delivery{Date: "2026-11-03", Meal: "lunch", Peer: "buyer", Amount: "1.00", ImageHash: "placeholder", ArmedAt: time.Now()}
			if _, err := st.ArmDelivery(ctx, d); err != nil {
				t.Fatal(err)
			}
			calls := 0
			send := func() error {
				calls++
				if fail {
					return errors.New("network timeout")
				}
				return nil
			}
			err = deliverOnce(ctx, st, d, store.Payment{ID: "payment"}, "outgoing", send)
			if (err != nil) != fail {
				t.Fatalf("unexpected result: %v", err)
			}
			if err := deliverOnce(ctx, st, d, store.Payment{ID: "payment"}, "outgoing", send); err == nil {
				t.Fatal("duplicate accepted")
			}
			if err := deliverOnce(ctx, st, d, store.Payment{ID: "new payment"}, "new outgoing", send); err == nil {
				t.Fatal("second sale accepted")
			}
			if calls != 1 {
				t.Fatalf("network send attempted %d times", calls)
			}
		})
	}
}

func TestDeliveryPlaceholder(t *testing.T) {
	_, mime, w, h, err := readDeliveryImage("")
	if err != nil || mime != "image/png" || w < 1 || h < 1 {
		t.Fatal("bad placeholder", err)
	}
	path := filepath.Join(t.TempDir(), "fake.png")
	if err := os.WriteFile(path, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := readDeliveryImage(path); err == nil {
		t.Fatal("invalid image accepted")
	}
}
