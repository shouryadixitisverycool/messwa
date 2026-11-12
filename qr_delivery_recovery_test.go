package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"messwa/internal/portal"
	"messwa/internal/store"
)

type failingQRPortal struct {
	fakeMealPortal
	calls    int
	failures int
}

func (p *failingQRPortal) QRForMeal(ctx context.Context, date, meal, hash string) (portal.CurrentQR, error) {
	p.calls++
	if p.calls > 1 && p.failures > 0 {
		p.failures--
		return portal.CurrentQR{}, errors.New("portal temporarily unavailable")
	}
	return p.fakeMealPortal.QRForMeal(ctx, date, meal, hash)
}

type failingQRUpload struct {
	*fakeConn
	calls    int
	failures int
}

func (c *failingQRUpload) Upload(ctx context.Context, data []byte, kind whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	c.calls++
	if c.failures > 0 {
		c.failures--
		return whatsmeow.UploadResponse{}, errors.New("media upload temporarily unavailable")
	}
	return c.fakeConn.Upload(ctx, data, kind)
}

func TestPaidQRRecoversFromPreparationFailure(t *testing.T) {
	for _, stage := range []string{"portal", "upload"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.db")
			synctest.Test(t, func(t *testing.T) {
				f := newSellFixture(t, path)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				m := f.negotiatingManager(ctx, 20)
				p := &failingQRPortal{fakeMealPortal: fakeMealPortal{mess: "bakul-veg"}}
				u := &failingQRUpload{fakeConn: f.conn}
				if stage == "portal" {
					p.failures = 1
				} else {
					u.failures = 1
				}
				m.portalFor = func(context.Context, int64) (mealPortal, error) { return p, nil }
				m.conn = u
				id, err := m.Start(ctx, 0, f.date, "lunch#2", 15, 30, "")
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				f.conn.emit(f.reply(buyerA, "how much"))
				settle()
				f.conn.emit(dm(buyerA, "yeah fine"))
				settle()
				paid := paymentOf(buyerA, "paid-once", "30.00", time.Now())
				f.conn.emit(paid)
				synctest.Wait()
				time.Sleep(5 * time.Second)
				synctest.Wait()
				if task := f.task(id); task.State != store.TaskSold {
					t.Fatalf("task=%s: %s", task.State, task.Detail)
				}
				if _, state, err := f.st.TaskDelivery(ctx, id); err != nil || state != "sent" {
					t.Fatalf("delivery=%s, %v", state, err)
				}
				qrs := 0
				for _, msg := range sentTo(f.conn.messages(), buyerA) {
					if msg.qr {
						qrs++
					}
				}
				if qrs != 1 {
					t.Fatalf("sent %d QRs", qrs)
				}
				f.conn.emit(paid)
				synctest.Wait()
				if stage == "portal" && p.calls != 3 {
					t.Fatalf("portal calls=%d", p.calls)
				}
				if stage == "upload" && u.calls != 2 {
					t.Fatalf("upload calls=%d", u.calls)
				}
			})
		})
	}
}

// A missing acknowledgement may mean the recipient got the image. Only
// preparation is retryable; this path must still attempt one message.
func TestPaidQRFailureKeepsCauseAndDoesNotResend(t *testing.T) {
	for _, stage := range []string{"portal", "upload", "send"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.db")
			synctest.Test(t, func(t *testing.T) {
				f := newSellFixture(t, path)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				m := f.negotiatingManager(ctx, 20)
				p := &failingQRPortal{fakeMealPortal: fakeMealPortal{mess: "bakul-veg"}}
				u := &failingQRUpload{fakeConn: f.conn}
				cause := ""
				switch stage {
				case "portal":
					p.failures = 10
					cause = "fetch current meal QR: portal temporarily unavailable"
				case "upload":
					u.failures = 10
					cause = "upload QR media: media upload temporarily unavailable"
				case "send":
					cause = "send QR message: acknowledgement lost"
				}
				m.portalFor = func(context.Context, int64) (mealPortal, error) { return p, nil }
				m.conn = u
				attempts := 0
				m.send = func(ctx context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
					response, err := f.conn.send(ctx, to, msg, extra...)
					if msg.GetImageMessage() != nil {
						attempts++
						if stage == "send" {
							return response, errors.New("acknowledgement lost")
						}
					}
					return response, err
				}
				id, err := m.Start(ctx, 0, f.date, "lunch#2", 15, 30, "")
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				f.conn.emit(f.reply(buyerA, "how much"))
				settle()
				f.conn.emit(dm(buyerA, "yeah fine"))
				settle()
				paid := paymentOf(buyerA, "paid-once", "30.00", time.Now())
				f.conn.emit(paid)
				synctest.Wait()
				time.Sleep(5 * time.Second)
				synctest.Wait()
				task := f.task(id)
				if task.State != store.TaskFailed || !strings.Contains(task.Detail, cause) {
					t.Fatalf("task=%s: %s; want cause %q", task.State, task.Detail, cause)
				}
				if _, state, err := f.st.TaskDelivery(ctx, id); err != nil || state != "sending" {
					t.Fatalf("delivery=%s, %v", state, err)
				}
				if stage == "portal" && p.calls != 4 {
					t.Fatalf("portal calls=%d", p.calls)
				}
				if stage == "upload" && u.calls != 3 {
					t.Fatalf("upload calls=%d", u.calls)
				}
				want := 0
				if stage == "send" {
					want = 1
				}
				if attempts != want {
					t.Fatalf("message attempts=%d, want %d", attempts, want)
				}
				f.conn.emit(paid)
				if err := f.manager(ctx).Resume(ctx, true); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if attempts != want {
					t.Fatalf("retried an uncertain message after restart: %d", attempts)
				}
			})
		})
	}
}
