package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"messwa/internal/store"
)

// eventLog is a buyer's events as "kind n text", n in rupees.
func eventLog(t *testing.T, f *sellFixture, id int64, peer types.JID) []string {
	t.Helper()
	evs, err := f.st.SellTaskEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Peer != peer.String() {
			continue
		}
		s := e.Kind
		if e.N != 0 {
			s += fmt.Sprintf(" %d", e.N/100)
		}
		if e.Kind == store.EventRead {
			s += " " + e.Text
		}
		out = append(out, s)
	}
	return out
}

func imageDM(buyer types.JID) *events.Message {
	dmSeq++
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: buyer}, ID: fmt.Sprintf("img-%d", dmSeq), Timestamp: time.Now()},
		Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: new("paid")}},
	}
}

// The event log tells the dashboard the whole sale: what the bot read and
// said, the agreement, the prompt, a screenshot, the payment and the QR. It
// never holds what the buyer wrote.
func TestSellEventsRecordTheSale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 40)
		notified := 0
		m.notify = func() { notified++ }
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "30?"))
		settle()
		f.conn.emit(dm(buyerA, "40?"))
		settle()
		f.conn.emit(imageDM(buyerA))
		settle()
		f.conn.emit(paymentOf(buyerA, "pay-a", "40.00", time.Now()))
		synctest.Wait()

		got := strings.Join(eventLog(t, f, id, buyerA), " | ")
		want := "joined | read 30 offer | bot_said 45 | read 40 offer | agreed 40 | prompt 40 | image | payment 40 | qr_sent 40"
		if got != want {
			t.Fatalf("events\n got %s\nwant %s", got, want)
		}
		evs, _ := f.st.SellTaskEvents(ctx, id)
		kinds := map[string]bool{}
		for _, e := range evs {
			kinds[e.Kind] = true
			if strings.Contains(e.Text, "30?") || strings.Contains(e.Text, "40?") {
				t.Fatalf("buyer text reached the event log: %+v", e)
			}
		}
		for _, k := range []string{store.EventPosted, store.EventEnded} {
			if !kinds[k] {
				t.Fatalf("no %s event in %+v", k, evs)
			}
		}
		if task := f.task(id); task.TargetPaise != 4000 {
			t.Fatalf("target = %d", task.TargetPaise)
		}
		if notified == 0 {
			t.Fatal("the dashboard was never told")
		}
	})
}
