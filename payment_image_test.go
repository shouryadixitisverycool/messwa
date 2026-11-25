package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"messwa/internal/store"
)

// imageFrom is an image DM from buyer, sent at.
func imageFrom(buyer types.JID, id string, at time.Time) *events.Message {
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: buyer}, ID: id, Timestamp: at},
		Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg")}},
	}
}

func TestImagePaymentRejection(t *testing.T) {
	now := time.Unix(1793700000, 0)
	armed, ends := now.Add(-time.Minute), now.Add(time.Hour)
	if err := imagePaymentRejection(imageFrom(buyerA, "img", now), armed, ends, now); err != nil {
		t.Fatalf("image in the window rejected: %v", err)
	}
	group := imageFrom(types.NewJID("120363000000000001", types.GroupServer), "img", now)
	group.Info.IsGroup = true
	mine := imageFrom(buyerA, "img", now)
	mine.Info.IsFromMe = true
	text := imageFrom(buyerA, "img", now)
	text.Message = &waE2E.Message{Conversation: proto.String("paid")}
	for name, e := range map[string]*events.Message{
		"group":         group,
		"own message":   mine,
		"text":          text,
		"before arming": imageFrom(buyerA, "img", armed.Add(-time.Second)),
		"after the end": imageFrom(buyerA, "img", ends),
		"no ID":         imageFrom(buyerA, "", now),
	} {
		if imagePaymentRejection(e, armed, ends, now) == nil {
			t.Errorf("%s counted as a payment image", name)
		}
	}
}

// TestSellTaskTakesPaymentImage is the 12 Nov case: the buyer pays outside
// WhatsApp Pay and sends a screenshot. With accept_payment_images on, the
// image from the buyer whose turn it is sells the meal, and the ledger
// says it wasn't verified.
func TestSellTaskTakesPaymentImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.AcceptPaymentImages = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 20, 20, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "interested"))
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "me too"))
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 3 || got[0] != "ok 20" || got[1] != "pay using whatsapp pay pls" || got[2] != "ill send the qr right after" {
			t.Fatalf("A got %q", got)
		}
		time.Sleep(time.Second)
		// Images that don't count: from the buyer who is still waiting,
		// and from before A's turn began.
		f.conn.emit(imageFrom(buyerB, "img-b", time.Now()))
		f.conn.emit(imageFrom(buyerA, "img-old", time.Now().Add(-time.Hour)))
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 3 {
			t.Fatalf("A got %q before a qualifying image", got)
		}
		if got := sentTo(f.conn.messages(), buyerB); len(got) != 0 {
			t.Fatalf("B got %+v", got)
		}

		f.conn.emit(dm(buyerA, "20"))
		f.conn.emit(imageFrom(buyerA, "img-a", time.Now()))
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 4 || got[3] != "[QR]" {
			t.Fatalf("A got %q, want the prompt then the QR", got)
		}
		if got := texts(sentTo(f.conn.messages(), buyerB)); len(got) != 1 || got[0] != "sold sorry" {
			t.Fatalf("B got %q, want sold sorry", got)
		}
		task := f.task(id)
		if task.State != store.TaskSold || !strings.Contains(task.Detail, "NOT verified") {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
		ds, err := f.st.Deliveries(ctx)
		if err != nil || len(ds) != 1 || ds[0].State != "sent" || ds[0].PaymentKind != store.PaidByImage || ds[0].Amount != "20.00" {
			t.Fatalf("ledger = %+v, %v", ds, err)
		}
		group := sentTo(f.conn.messages(), f.group)
		if len(group) != 2 || group[1].reaction != soldReaction {
			t.Fatalf("group messages = %+v, want the post then a ✅", group)
		}
	})
}

// TestPaymentImageOffByDefault keeps today's behaviour: an image is not a
// payment. The buyer hears once that screenshots don't count.
func TestPaymentImageOffByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 20, 20, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "interested"))
		synctest.Wait()
		time.Sleep(time.Second)
		f.conn.emit(imageFrom(buyerA, "img-a", time.Now()))
		synctest.Wait()
		f.conn.emit(imageFrom(buyerA, "img-a2", time.Now()))
		synctest.Wait()
		want := []string{"ok 20", "pay using whatsapp pay pls, not gpay/upi", "ill send the qr right after",
			"cant check screenshots, pay using whatsapp pay pls"}
		if got := texts(sentTo(f.conn.messages(), buyerA)); !slices.Equal(got, want) {
			t.Fatalf("A got %q, want %q", got, want)
		}
		if task := f.task(id); task.State != store.TaskActive {
			t.Fatalf("task is %s", task.State)
		}
		cancel()
		synctest.Wait()
	})
}

// TestPaymentImageNeedsThePrompt checks that an image sent before the
// payment prompt went out doesn't count, even inside the turn.
func TestPaymentImageNeedsThePrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		end := time.Now().Add(time.Hour)
		s := &sale{log: slog.New(slog.NewTextHandler(io.Discard, nil)), loc: f.cfg.Location(), conn: f.conn, st: f.st,
			send: f.conn.send, sendPaid: f.conn.send, date: f.date, meal: "lunch", end: end,
			image: placeholderQR, imageHash: fmt.Sprintf("%x", sha256.Sum256(placeholderQR)), acceptImages: true}
		d := s.reservation(buyerA, "20.00")
		d.ArmedAt = time.Now().Add(-time.Minute)
		done := make(chan error, 1)
		go func() {
			done <- s.takeTurn(ctx, buyerA, d, turn{
				arm:    f.st.ArmNewDelivery,
				prompt: reply{"pay ₹20"},
				prompting: func(context.Context) (bool, error) {
					// The buyer's image lands while the prompt is being claimed.
					f.conn.emit(imageFrom(buyerA, "img-early", time.Now()))
					return true, nil
				},
			})
		}()
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 1 || got[0] != "pay ₹20" {
			t.Fatalf("A got %q; an image from before the prompt counted", got)
		}
		f.conn.emit(imageFrom(buyerA, "img-late", time.Now()))
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 2 || got[1] != "[QR]" {
			t.Fatalf("A got %q", got)
		}
		if kind, err := f.st.PaymentKind(ctx, f.date, "lunch"); err != nil || kind != store.PaidByImage {
			t.Fatalf("payment kind = %q, %v", kind, err)
		}
	})
}

// TestNegotiationScreenshotFlow is the 17 Nov case with screenshots
// counting: asking to pay by UPI is a yes, the pay prompt doesn't rule out
// UPI, a buyer with no WhatsApp Pay hears to pay the number, a "done" with
// no image gets one ask for the screenshot, and the screenshot gets the
// QR.
func TestNegotiationScreenshotFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.AcceptPaymentImages = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "price?"))
		settle()
		f.conn.emit(dm(buyerA, "can i pay by upi id"))
		settle()
		f.conn.emit(dm(buyerA, "I don't have WhatsApp pay"))
		f.conn.emit(dm(buyerA, "I can do upi"))
		settle()
		f.conn.emit(dm(buyerA, "upi?"))
		settle()
		f.conn.emit(dm(buyerA, "done"))
		settle()
		f.conn.emit(dm(buyerA, "sent"))
		settle()
		want := []string{"60", "ok 60", "pay using whatsapp pay pls", "ill send the qr right after",
			"send to my number then", "can u send payment screenshot"}
		if got := texts(sentTo(f.conn.messages(), buyerA)); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("A got %q, want %q", got, want)
		}
		if b := buyerState(t, f, id, buyerA); b.State == store.BuyerParked {
			t.Fatalf("A = %+v, parked before the screenshot grace ran out", b)
		}
		f.conn.emit(imageFrom(buyerA, "img-a", time.Now()))
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); got[len(got)-1] != "[QR]" {
			t.Fatalf("A got %q, want the QR last", got)
		}
	})
}

// An image before "sent" is the screenshot already, so the bot doesn't
// ask for one.
func TestNegotiationNoScreenshotAskAfterImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.AcceptPaymentImages = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		if _, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "price?"))
		settle()
		f.conn.emit(dm(buyerA, "ok"))
		settle()
		f.conn.emit(imageFrom(buyerA, "img-a", time.Now()))
		f.conn.emit(dm(buyerA, "sent"))
		settle()
		for _, got := range texts(sentTo(f.conn.messages(), buyerA)) {
			if got == "can u send payment screenshot" {
				t.Fatal("asked for a screenshot after one came in")
			}
		}
	})
}

// TestNegotiationUPIQR: with screenshots counting and a UPI ID saved, the
// pay message comes with a UPI QR for the agreed price, a buyer asking for
// the UPI ID gets it once more, and their screenshot gets the meal QR.
func TestNegotiationUPIQR(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.AcceptPaymentImages = true
		f.cfg.Sell.UPI = "seller@okaxis"
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "price?"))
		settle()
		f.conn.emit(dm(buyerA, "ok"))
		settle()
		f.conn.emit(dm(buyerA, "upi id?"))
		settle()
		f.conn.emit(dm(buyerA, "scanner?"))
		settle()
		f.conn.emit(dm(buyerA, "Can you send the qr"))
		settle()
		f.conn.emit(dm(buyerA, "cash?"))
		settle()
		var got []string
		for _, m := range sentTo(f.conn.messages(), buyerA) {
			if m.qr {
				got = append(got, "[image]")
			} else {
				got = append(got, m.text)
			}
		}
		want := []string{"60", "ok 60", "pay on this qr and send the screenshot, ill send the meal qr after",
			"[image]", "[image]", "only upi sorry"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("A got %q, want %q", got, want)
		}
		evs, err := f.st.SellTaskEvents(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var amounts []int64
		for _, e := range evs {
			if e.Kind == store.EventPayQR {
				amounts = append(amounts, e.N)
			}
		}
		if !slices.Equal(amounts, []int64{6000, 6000}) {
			t.Fatalf("UPI QR amounts %v, want 60 twice", amounts)
		}
		f.conn.emit(imageFrom(buyerA, "img-a", time.Now()))
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); got[len(got)-1] != "[QR]" {
			t.Fatalf("A got %q, want the meal QR last", got)
		}
	})
}

// TestNegotiationNoPayAppPromptReply checks that "Cannot do" or "ion have it"
// right after a WhatsApp Pay prompt tells the buyer to pay the number when
// screenshots count.
func TestNegotiationNoPayAppPromptReply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.AcceptPaymentImages = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		if _, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "price?"))
		settle()
		f.conn.emit(dm(buyerA, "ok"))
		settle()
		f.conn.emit(dm(buyerA, "Cannot do"))
		settle()
		want := []string{"60", "ok 60", "pay using whatsapp pay pls", "ill send the qr right after",
			"send to my number then"}
		if got := texts(sentTo(f.conn.messages(), buyerA)); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("A got %q, want %q", got, want)
		}
	})
}

// TestFirstMessageSendQRQuotesFirst checks that QR questions as a buyer's
// very first reply open with the price instead of agreeing unseen.
func TestFirstMessageSendQRQuotesFirst(t *testing.T) {
	for _, msg := range []string{"Can you send the QR?", "Can I pay on QR?"} {
		t.Run(msg, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.db")
			synctest.Test(t, func(t *testing.T) {
				f := newSellFixture(t, path)
				f.cfg.Sell.AcceptPaymentImages = true
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				m := f.negotiatingManager(ctx, 60)
				if _, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, ""); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				f.conn.emit(f.reply(buyerA, msg))
				settle()
				want := []string{"60"}
				if got := texts(sentTo(f.conn.messages(), buyerA)); !slices.Equal(got, want) {
					t.Fatalf("A got %q, want %q", got, want)
				}
			})
		})
	}
}

// TestTooMuchWithCantDoWhatsAppPayCountersLower checks that objecting to the
// price while mentioning no WhatsApp Pay counters lower instead of agreeing.
func TestTooMuchWithCantDoWhatsAppPayCountersLower(t *testing.T) {
	for _, tc := range []struct {
		name string
		msgs []string
	}{
		{"single", []string{"Too much, can't do WhatsApp Pay"}},
		{"burst", []string{"Too much", "can't do WhatsApp Pay"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.db")
			synctest.Test(t, func(t *testing.T) {
				f := newSellFixture(t, path)
				f.cfg.Sell.AcceptPaymentImages = true
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				m := f.negotiatingManager(ctx, 60)
				id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				f.conn.emit(f.reply(buyerA, "price?"))
				settle()
				for _, msg := range tc.msgs {
					f.conn.emit(dm(buyerA, msg))
				}
				settle()
				if b := buyerState(t, f, id, buyerA); b.State != store.BuyerNegotiating {
					t.Fatalf("A = %+v, want still negotiating after price objection", b)
				}
				got := texts(sentTo(f.conn.messages(), buyerA))
				if len(got) < 2 || strings.Contains(strings.Join(got, " "), "whatsapp pay") {
					t.Fatalf("A got %q, want lower counter without payment prompt", got)
				}
			})
		})
	}
}

// TestMultiMealAcceptanceParks checks that multi-meal openings, acceptances,
// and QR requests ("for both?", "I'll take both", ["for both?", "send qr"])
// park instead of quoting or starting a single-meal payment turn.
func TestMultiMealAcceptanceParks(t *testing.T) {
	t.Run("opening for both parks before quoting", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "data.db")
		synctest.Test(t, func(t *testing.T) {
			f := newSellFixture(t, path)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m := f.negotiatingManager(ctx, 60)
			id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			f.conn.emit(f.reply(buyerA, "for both?"))
			settle()
			if b := buyerState(t, f, id, buyerA); b.State != store.BuyerParked {
				t.Fatalf("A = %+v, want parked on multi-meal opening", b)
			}
			if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 0 {
				t.Fatalf("A got %q, want no single-meal quote before parking", got)
			}
		})
	})
	for _, tc := range []struct {
		name string
		msgs []string
	}{
		{"take both", []string{"I'll take both"}},
		{"for both then send qr", []string{"for both?", "send qr"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.db")
			synctest.Test(t, func(t *testing.T) {
				f := newSellFixture(t, path)
				f.cfg.Sell.AcceptPaymentImages = true
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				m := f.negotiatingManager(ctx, 60)
				id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				f.conn.emit(f.reply(buyerA, "price?"))
				settle()
				for _, msg := range tc.msgs {
					f.conn.emit(dm(buyerA, msg))
				}
				settle()
				if b := buyerState(t, f, id, buyerA); b.State != store.BuyerParked {
					t.Fatalf("A = %+v, want parked for multi-meal request", b)
				}
			})
		})
	}
}

// TestAutopilotTakesScreenshot: autopilot counts a screenshot as payment
// when the setting is on.
func TestAutopilotTakesScreenshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.AcceptPaymentImages = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		id, _, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, true)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "price?"))
		settle()
		f.conn.emit(dm(buyerA, "ok"))
		settle()
		f.conn.emit(imageFrom(buyerA, "img-a", time.Now()))
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); got[len(got)-1] != "[QR]" {
			t.Fatalf("A got %q, want the meal QR last", got)
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
	})
}

// TestPayQRPNG: the UPI QR is a square image big enough to scan, and its
// caption names the price and the UPI ID.
func TestPayQRPNG(t *testing.T) {
	s := &sale{upi: "seller@okaxis", mess: "kadamba", meal: "lunch"}
	png, err := s.payQRPNG("30.00")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, w, h, err := deliveryImageBytes(png); err != nil || w < 200 || w != h {
		t.Fatalf("QR is %dx%d, %v", w, h, err)
	}
	if got := payQRCaption("30.00", "seller@okaxis"); got != "₹30 to seller@okaxis" {
		t.Errorf("caption %q", got)
	}
	if got := payQRCaption("", "seller@okaxis"); got != "pay to seller@okaxis" {
		t.Errorf("caption %q", got)
	}
}
