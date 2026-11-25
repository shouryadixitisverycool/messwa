package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"messwa/internal/payshot"
	"messwa/internal/store"
)

// fakeReader answers each screenshot by its bytes, which the tests set to
// the image's ID.
type fakeReader struct {
	mu    sync.Mutex
	shots map[string]payshot.Receipt
	errs  map[string]error
	// gates hold a read until closed, like a slow model.
	gates map[string]chan struct{}
}

func (r *fakeReader) Read(ctx context.Context, img []byte, _ string) (payshot.Receipt, string, error) {
	r.mu.Lock()
	gate := r.gates[string(img)]
	r.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return payshot.Receipt{}, "", ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errs[string(img)]; err != nil {
		return payshot.Receipt{}, "", err
	}
	rc, ok := r.shots[string(img)]
	if !ok {
		return payshot.Receipt{Status: "unknown"}, "fake", nil
	}
	return rc, "fake", nil
}

// paidShot is a PhonePe-style receipt for rupees to name, with utr.
func paidShot(rupees int64, name, utr string) payshot.Receipt {
	return payshot.Receipt{IsReceipt: true, Status: "success", AmountPaise: rupees * 100, PayeeName: name, UTR: utr}
}

// screenshotFixture is a sell task at ₹35 with screenshots counting and a
// reader that knows the receipts in shots.
func screenshotFixture(t *testing.T, shots map[string]payshot.Receipt, errs map[string]error) (*sellFixture, *fakeReader) {
	f := newSellFixture(t, filepath.Join(t.TempDir(), "data.db"))
	f.cfg.Sell.AcceptPaymentImages = true
	f.cfg.Sell.PayeeName = "Shourya Dixit"
	f.conn.media = map[string][]byte{}
	r := &fakeReader{shots: shots, errs: errs}
	old := screenshotReader
	screenshotReader = func() receiptReader { return r }
	t.Cleanup(func() { screenshotReader = old })
	return f, r
}

// shot sends buyer's image id, whose bytes the fake reader looks up.
func (f *sellFixture) shot(buyer types.JID, id string) {
	f.conn.mu.Lock()
	f.conn.media[id] = []byte(id)
	f.conn.mu.Unlock()
	f.conn.emit(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: buyer}, ID: id, Timestamp: time.Now()},
		Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg"),
			DirectPath: proto.String(id), FileSHA256: []byte(id)}},
	})
	synctest.Wait()
}

// startPaying starts the task and gives buyer the payment turn.
func (f *sellFixture) startPaying(ctx context.Context, buyer types.JID) int64 {
	f.t.Helper()
	m := f.manager(ctx)
	id, err := m.Start(ctx, 0, f.date, "lunch", 35, 35, "")
	if err != nil {
		f.t.Fatal(err)
	}
	synctest.Wait()
	f.conn.emit(f.reply(buyer, "interested"))
	synctest.Wait()
	time.Sleep(time.Second)
	return id
}

var screenshotPrompt = []string{"ok 35", "pay using whatsapp pay pls", "ill send the qr right after"}

// TestScreenshotShortThenTopUp is the 24 Nov case: the buyer paid ₹3 of
// ₹35, then ₹32. The ₹3 screenshot gets "32 more", not the meal QR; the
// screenshots add up, so the ₹32 one sells the meal at ₹35.
func TestScreenshotShortThenTopUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, _ := screenshotFixture(t, map[string]payshot.Receipt{
			"rs3":  paidShot(3, "Shourya Dixit", "277298581603"),
			"rs32": paidShot(32, "SHOURYA DIXIT", "755527205655"),
		}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		id := f.startPaying(ctx, buyerA)
		f.shot(buyerA, "rs3")
		want := append(slices.Clone(screenshotPrompt), "got 3, 32 more pls")
		if got := texts(sentTo(f.conn.messages(), buyerA)); !slices.Equal(got, want) {
			t.Fatalf("after ₹3, A got %q, want %q", got, want)
		}
		// The ₹3 screenshot sent again doesn't count twice.
		f.shot(buyerA, "rs3")
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != len(want) {
			t.Fatalf("a resent screenshot got %q", got[len(want):])
		}
		f.shot(buyerA, "rs32")
		if got := texts(sentTo(f.conn.messages(), buyerA)); got[len(got)-1] != "[QR]" {
			t.Fatalf("after ₹32 more, A got %q, want the meal QR", got)
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s", task.State)
		}
		ds, err := f.st.Deliveries(ctx)
		if err != nil || len(ds) != 1 || ds[0].PaymentKind != store.PaidByImage || ds[0].Amount != "35.00" {
			t.Fatalf("ledger = %+v, %v", ds, err)
		}
	})
}

// TestScreenshotPartialPayerKeepsTurn keeps a buyer who paid part of the
// price on their turn past the usual two minutes, so the meal doesn't go
// to the next buyer while their money is in.
func TestScreenshotPartialPayerKeepsTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, _ := screenshotFixture(t, map[string]payshot.Receipt{"rs3": paidShot(3, "Shourya Dixit", "1"), "rs32": paidShot(32, "Shourya Dixit", "2")}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.startPaying(ctx, buyerA)
		f.conn.emit(f.reply(buyerB, "me too"))
		synctest.Wait()
		f.shot(buyerA, "rs3")
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		for _, m := range texts(sentTo(f.conn.messages(), buyerB)) {
			if m == "ok 35" {
				t.Fatalf("B was asked to pay while A had paid part: %q", texts(sentTo(f.conn.messages(), buyerB)))
			}
		}
		// The rest, well past the two minutes, still sells the meal.
		f.shot(buyerA, "rs32")
		if got := texts(sentTo(f.conn.messages(), buyerA)); got[len(got)-1] != "[QR]" {
			t.Fatalf("A's late top-up got %q, want the meal QR", got)
		}
	})
}

// TestScreenshotNotAPayment answers a photo, a failed payment and a
// payment to someone else with "cant see a payment", at most twice, and
// sends no meal QR.
func TestScreenshotNotAPayment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failed := paidShot(35, "Shourya Dixit", "2")
		failed.Status = "failed"
		f, _ := screenshotFixture(t, map[string]payshot.Receipt{
			"selfie":    {Status: "unknown"},
			"failed":    failed,
			"elsewhere": paidShot(35, "Someone Else", "3"),
		}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		id := f.startPaying(ctx, buyerA)
		f.shot(buyerA, "selfie")
		f.shot(buyerA, "failed")
		f.shot(buyerA, "elsewhere")
		unclear := "cant see a payment to me in that, can u send the payment screenshot"
		want := append(slices.Clone(screenshotPrompt), unclear, unclear)
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

// TestScreenshotNameOrOtherUPI takes a payment to the seller's banking
// name at another of their UPI IDs: the name decides.
func TestScreenshotNameOrOtherUPI(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		other := paidShot(35, "SHOURYA DIXIT", "4")
		other.PayeeUPI = "••••cool@okhdfcbank"
		f, _ := screenshotFixture(t, map[string]payshot.Receipt{"other": other}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.startPaying(ctx, buyerA)
		f.shot(buyerA, "other")
		if got := texts(sentTo(f.conn.messages(), buyerA)); got[len(got)-1] != "[QR]" {
			t.Fatalf("A got %q, want the meal QR", got)
		}
	})
}

// TestScreenshotUnreadable follows accept_unreadable_payments when the
// reader fails: off keeps the QR, on sends it for the agreed price.
func TestScreenshotUnreadable(t *testing.T) {
	for _, assume := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			f, _ := screenshotFixture(t, nil, map[string]error{"blur": errors.New("503 overloaded")})
			f.cfg.Sell.AcceptUnreadablePayments = assume
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.startPaying(ctx, buyerA)
			f.shot(buyerA, "blur")
			got := texts(sentTo(f.conn.messages(), buyerA))
			if sent := got[len(got)-1] == "[QR]"; sent != assume {
				t.Fatalf("accept_unreadable_payments=%v: A got %q", assume, got)
			}
			cancel()
			synctest.Wait()
		})
	}
}

func TestCoveredPrice(t *testing.T) {
	for _, c := range []struct {
		paid   int64
		others []string
		want   string
		ok     bool
	}{
		{3500, nil, "35.00", true},
		{3600, nil, "35.00", true},
		{300, nil, "", false},
		{3000, []string{"30.00", "40.00"}, "30.00", true},
		{4000, []string{"30.00", "40.00"}, "40.00", true},
	} {
		if got, ok := coveredPrice(c.paid, "35.00", c.others); got != c.want || ok != c.ok {
			t.Errorf("coveredPrice(%d, %v) = %q, %v; want %q, %v", c.paid, c.others, got, ok, c.want, c.ok)
		}
	}
}

// TestScreenshotSamePaymentTwoWays counts one payment once when a second
// image of it shows only some of its references, such as a crop without
// the UTR.
func TestScreenshotSamePaymentTwoWays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		full := paidShot(20, "Shourya Dixit", "111111111111")
		full.TxnID = "T26111"
		crop := paidShot(20, "Shourya Dixit", "")
		crop.TxnID = "T26111"
		f, _ := screenshotFixture(t, map[string]payshot.Receipt{"full": full, "crop": crop, "rest": paidShot(15, "Shourya Dixit", "222222222222")}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.startPaying(ctx, buyerA)
		f.shot(buyerA, "full")
		f.shot(buyerA, "crop")
		want := append(slices.Clone(screenshotPrompt), "got 20, 15 more pls")
		if got := texts(sentTo(f.conn.messages(), buyerA)); !slices.Equal(got, want) {
			t.Fatalf("A got %q, want %q", got, want)
		}
		f.shot(buyerA, "rest")
		if got := texts(sentTo(f.conn.messages(), buyerA)); got[len(got)-1] != "[QR]" {
			t.Fatalf("A got %q, want the meal QR", got)
		}
	})
}

// TestScreenshotNeedsSuccessAndToday turns away a receipt that doesn't say
// the payment finished, and one dated before the buyer's turn.
func TestScreenshotNeedsSuccessAndToday(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		unknown := paidShot(35, "Shourya Dixit", "3")
		unknown.Status = "unknown"
		old := paidShot(35, "Shourya Dixit", "4")
		old.PaidDate = "2026-11-10"
		f, _ := screenshotFixture(t, map[string]payshot.Receipt{"unknown": unknown, "old": old}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		id := f.startPaying(ctx, buyerA)
		f.shot(buyerA, "unknown")
		f.shot(buyerA, "old")
		for _, m := range texts(sentTo(f.conn.messages(), buyerA)) {
			if m == "[QR]" {
				t.Fatal("meal QR sent for an unfinished or old receipt")
			}
		}
		if task := f.task(id); task.State != store.TaskActive {
			t.Fatalf("task is %s", task.State)
		}
		cancel()
		synctest.Wait()
	})
}

// TestScreenshotDropDuringRead ends the turn of a buyer who backs out
// while their screenshot is still being read, once it turns out not to be
// a payment, so the next buyer is asked to pay.
func TestScreenshotDropDuringRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, r := screenshotFixture(t, map[string]payshot.Receipt{"photo": {Status: "unknown"}}, nil)
		r.gates = map[string]chan struct{}{"photo": make(chan struct{})}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 60)
		if _, _, err := m.StartNow(ctx, 0, f.date, "lunch", 30, 60, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		f.conn.emit(dm(buyerB, "ok"))
		settle()
		f.conn.emit(f.reply(buyerC, "60"))
		settle()
		f.shot(buyerB, "photo")
		f.conn.emit(dm(buyerB, "Naa I said for 50 bro"))
		settle()
		if got := sentTo(f.conn.messages(), buyerC); len(got) != 0 {
			t.Fatalf("C was asked to pay while B's screenshot was being read: %q", texts(got))
		}
		close(r.gates["photo"])
		settle()
		if got := texts(sentTo(f.conn.messages(), buyerC)); len(got) == 0 {
			t.Fatal("B backed out and the photo wasn't a payment, but C was never asked to pay")
		}
		cancel()
		synctest.Wait()
	})
}

// TestScreenshotDuplicateLearnsKeys catches a third view of one payment
// that shares a key only with the second, which was itself a duplicate.
func TestScreenshotDuplicateLearnsKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		utrOnly := paidShot(20, "Shourya Dixit", "111111111111")
		both := paidShot(20, "Shourya Dixit", "111111111111")
		both.TxnID = "T26111"
		txnOnly := paidShot(20, "Shourya Dixit", "")
		txnOnly.TxnID = "T26111"
		f, _ := screenshotFixture(t, map[string]payshot.Receipt{"utr": utrOnly, "both": both, "txn": txnOnly}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.startPaying(ctx, buyerA)
		f.shot(buyerA, "utr")
		f.shot(buyerA, "both")
		f.shot(buyerA, "txn")
		want := append(slices.Clone(screenshotPrompt), "got 20, 15 more pls")
		if got := texts(sentTo(f.conn.messages(), buyerA)); !slices.Equal(got, want) {
			t.Fatalf("A got %q, want %q", got, want)
		}
		cancel()
		synctest.Wait()
	})
}

// TestScreenshotTopUpWhileReading takes a top-up sent after the two
// minutes while the first screenshot is still being read.
func TestScreenshotTopUpWhileReading(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, r := screenshotFixture(t, map[string]payshot.Receipt{
			"rs3":  paidShot(3, "Shourya Dixit", "1"),
			"rs32": paidShot(32, "Shourya Dixit", "2"),
		}, nil)
		r.gates = map[string]chan struct{}{"rs3": make(chan struct{})}
		// A read that times out mustn't stand in for the payment here.
		f.cfg.Sell.AcceptUnreadablePayments = false
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.startPaying(ctx, buyerA)
		f.conn.emit(f.reply(buyerB, "me too"))
		synctest.Wait()
		time.Sleep(90 * time.Second)
		f.shot(buyerA, "rs3")
		// The two minutes end while ₹3 is still being read.
		time.Sleep(40 * time.Second)
		f.shot(buyerA, "rs32")
		close(r.gates["rs3"])
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); got[len(got)-1] != "[QR]" {
			t.Fatalf("A got %q, want the meal QR", got)
		}
	})
}

// TestScreenshotThenWhatsAppPay completes the price with a WhatsApp Pay
// payment of the rest after a short screenshot, at the full price.
func TestScreenshotThenWhatsAppPay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, _ := screenshotFixture(t, map[string]payshot.Receipt{"rs3": paidShot(3, "Shourya Dixit", "1")}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.startPaying(ctx, buyerA)
		f.shot(buyerA, "rs3")
		f.conn.emit(paymentOf(buyerA, "pay-32", "32.00", time.Now()))
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); got[len(got)-1] != "[QR]" {
			t.Fatalf("A got %q, want the meal QR", got)
		}
		ds, err := f.st.Deliveries(ctx)
		if err != nil || len(ds) != 1 || ds[0].Amount != "35.00" {
			t.Fatalf("ledger = %+v, %v", ds, err)
		}
	})
}
