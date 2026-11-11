package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/portal"
	"messwa/internal/store"
)

type sentMessage struct {
	to   types.JID
	text string
	qr   bool
	id   string
}

// fakeConn stands in for the WhatsApp connection. Events are delivered by
// emit, and every message sent through the raw send is recorded.
type fakeConn struct {
	mu       sync.Mutex
	handlers map[uint32]whatsmeow.EventHandler
	nextID   uint32
	ids      int
	sent     []sentMessage
	groups   []*types.GroupInfo
	lids     map[types.JID]types.JID
}

func newFakeConn(group types.JID) *fakeConn {
	return &fakeConn{handlers: map[uint32]whatsmeow.EventHandler{}, groups: []*types.GroupInfo{{JID: group, GroupName: types.GroupName{Name: "Mess sell"}}}, lids: map[types.JID]types.JID{}}
}

func (f *fakeConn) AddEventHandler(h whatsmeow.EventHandler) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.handlers[f.nextID] = h
	return f.nextID
}

func (f *fakeConn) RemoveEventHandler(id uint32) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.handlers, id)
	return true
}

func (f *fakeConn) GenerateMessageID() types.MessageID {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids++
	return types.MessageID("MSG" + string(rune('A'+f.ids)))
}

func (f *fakeConn) Upload(context.Context, []byte, whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return whatsmeow.UploadResponse{URL: "u", DirectPath: "d"}, nil
}

func (f *fakeConn) SendPeerMessage(context.Context, *waE2E.Message) (whatsmeow.SendResponse, error) {
	return whatsmeow.SendResponse{}, nil
}

func (f *fakeConn) BuildUnavailableMessageRequest(types.JID, types.JID, string) *waE2E.Message {
	return &waE2E.Message{}
}

func (f *fakeConn) GetJoinedGroups(context.Context) ([]*types.GroupInfo, error) { return f.groups, nil }

func (f *fakeConn) PNForLID(_ context.Context, lid types.JID) types.JID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lids[lid]
}

func (f *fakeConn) send(_ context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := sentMessage{to: to, text: msg.GetConversation(), qr: msg.GetImageMessage() != nil}
	if len(extra) > 0 {
		m.id = extra[0].ID
	}
	f.sent = append(f.sent, m)
	return whatsmeow.SendResponse{}, nil
}

func (f *fakeConn) emit(evt any) {
	f.mu.Lock()
	var hs []whatsmeow.EventHandler
	for _, h := range f.handlers {
		hs = append(hs, h)
	}
	f.mu.Unlock()
	for _, h := range hs {
		h(evt)
	}
}

func (f *fakeConn) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

type fakeMealPortal struct{ mess string }

func (p fakeMealPortal) RequireRegistration(_ context.Context, date, meal string) (portal.Registration, error) {
	return portal.Registration{Date: date, Meal: meal, Mess: p.mess, Category: "registered"}, nil
}

func (p fakeMealPortal) QRForMeal(ctx context.Context, date, meal, _ string) (portal.CurrentQR, error) {
	reg, _ := p.RequireRegistration(ctx, date, meal)
	return portal.CurrentQR{PNG: placeholderQR, AccountHash: "account", Registration: reg}, nil
}

type sellFixture struct {
	t     *testing.T
	cfg   *config.Config
	st    *store.Store
	conn  *fakeConn
	group types.JID
	date  string
}

// newSellFixture runs inside a synctest bubble. It moves the fake clock to
// midnight UTC on 11 Nov 2026, 05:30 in Kolkata, so today's lunch is ahead
// and payment timestamps look current.
func newSellFixture(t *testing.T, path string) *sellFixture {
	time.Sleep(time.Until(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)))
	cfg := config.Default()
	group := types.NewJID("120363000000000001", types.GroupServer)
	cfg.Groups = []string{group.String()}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &sellFixture{t: t, cfg: cfg, st: st, conn: newFakeConn(group), group: group, date: time.Now().In(cfg.Location()).Format("2006-01-02")}
}

func (f *sellFixture) manager(ctx context.Context) *sellManager {
	return newSellManager(ctx, f.cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), f.st, f.conn, f.conn.send,
		func(time.Duration) bool { return true },
		func(context.Context, int64) (mealPortal, error) { return fakeMealPortal{mess: "bakul-nonveg"}, nil })
}

func (f *sellFixture) task(id int64) store.SellTask {
	f.t.Helper()
	task, err := f.st.GetSellTask(context.Background(), id)
	if err != nil || task == nil {
		f.t.Fatalf("task %d: %v", id, err)
	}
	return *task
}

func (f *sellFixture) offerID() string {
	f.t.Helper()
	for _, m := range f.conn.messages() {
		if m.to == f.group {
			return m.id
		}
	}
	f.t.Fatal("no offer posted")
	return ""
}

// reply is a private reply from buyer that quotes the offer.
func (f *sellFixture) reply(buyer types.JID, text string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: buyer}, ID: "reply-" + buyer.User + text, Timestamp: time.Now()},
		Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String(f.offerID()), RemoteJID: proto.String(f.group.String())},
		}},
	}
}

// payment is a WhatsApp Pay message for INR 1.00 from buyer, sent now.
func payment(buyer types.JID, id string) *events.Message { return paymentAt(buyer, id, time.Now()) }

func paymentAt(buyer types.JID, id string, at time.Time) *events.Message {
	payload, _ := base64.StdEncoding.DecodeString(observedPaymentPrefix)
	binary.LittleEndian.PutUint64(payload[8:16], uint64(at.UnixMilli()))
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: buyer}, ID: id, Timestamp: at},
		Message: &waE2E.Message{SendPaymentMessage: &waE2E.SendPaymentMessage{TransactionData: proto.String(base64.StdEncoding.EncodeToString(payload))}},
	}
}

func sentTo(msgs []sentMessage, to types.JID) []sentMessage {
	var out []sentMessage
	for _, m := range msgs {
		if m.to == to {
			out = append(out, m)
		}
	}
	return out
}

var (
	buyerA   = types.NewJID("911111111111", types.DefaultUserServer)
	buyerB   = types.NewJID("912222222222", types.DefaultUserServer)
	stranger = types.NewJID("913333333333", types.DefaultUserServer)
)

func TestSellTaskSellsToFirstPayingBuyer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		posts := sentTo(f.conn.messages(), f.group)
		if len(posts) != 1 || posts[0].text != "sell bakul non-veg lunch" {
			t.Fatalf("group posts = %+v", posts)
		}
		if _, err := m.Start(ctx, 0, f.date, "lunch", 1, 1); err == nil {
			t.Fatal("second task for the same meal started")
		}

		// Nobody who didn't reply to the offer can be messaged, even by
		// a caller holding the gate.
		m.mu.Lock()
		gate := m.running[id].gate
		m.mu.Unlock()
		if _, err := gate.send(ctx, stranger, &waE2E.Message{Conversation: proto.String("hi")}); err == nil {
			t.Fatal("gate let a message through to someone who never replied")
		}
		if _, err := gate.send(ctx, f.group, &waE2E.Message{Conversation: proto.String("again")}, whatsmeow.SendRequestExtra{ID: "new"}); err == nil {
			t.Fatal("gate let a second group post through")
		}
		// A plain DM without the quote doesn't join the queue.
		f.conn.emit(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: stranger}, ID: "plain", Timestamp: time.Now()},
			Message: &waE2E.Message{Conversation: proto.String("still available?")}})

		f.conn.emit(f.reply(buyerA, "interested"))
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "me too"))
		synctest.Wait()
		prompts := sentTo(f.conn.messages(), buyerA)
		if len(prompts) != 1 || !strings.HasPrefix(prompts[0].text, "pay ₹1 here via WhatsApp Pay, I'll send the QR") {
			t.Fatalf("buyer A got %+v", prompts)
		}
		if got := sentTo(f.conn.messages(), buyerB); len(got) != 0 {
			t.Fatalf("queued buyer was messaged before their turn: %+v", got)
		}

		time.Sleep(time.Second)
		f.conn.emit(payment(buyerB, "pay-b")) // not B's turn
		f.conn.emit(payment(buyerA, "pay-a"))
		synctest.Wait()
		toA, toB := sentTo(f.conn.messages(), buyerA), sentTo(f.conn.messages(), buyerB)
		if len(toA) != 2 || !toA[1].qr {
			t.Fatalf("buyer A got %+v, want the prompt then the QR", toA)
		}
		if len(toB) != 1 || toB[0].text != "sorry sold" {
			t.Fatalf("buyer B got %+v, want sorry sold", toB)
		}
		if len(sentTo(f.conn.messages(), stranger)) != 0 {
			t.Fatal("stranger was messaged")
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
		if _, state, _ := f.st.TaskDelivery(ctx, id); state != "sent" {
			t.Fatalf("delivery is %q", state)
		}
		if _, err := gate.send(ctx, buyerB, &waE2E.Message{Conversation: proto.String("hi")}); err != errTaskInactive {
			t.Fatalf("ended task could still send: %v", err)
		}
	})
}

func TestSellTaskResumesWithoutResending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		first, stopRun := context.WithCancel(t.Context())
		m := f.manager(first)
		id, err := m.Start(first, 0, f.date, "lunch", 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "interested"))
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "me too"))
		synctest.Wait()

		// run stops: the task stays active and keeps its reservation.
		stopRun()
		synctest.Wait()
		if task := f.task(id); task.State != store.TaskActive {
			t.Fatalf("stopping run ended the task: %s", task.State)
		}
		before := len(f.conn.messages())

		second, cancel := context.WithCancel(t.Context())
		defer cancel()
		m2 := f.manager(second)
		if err := m2.Resume(second, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if after := len(f.conn.messages()); after != before {
			t.Fatalf("resuming sent %d messages again: %+v", after-before, f.conn.messages()[before:])
		}
		// The resumed turn still takes A's payment, and B still hears.
		time.Sleep(time.Second)
		f.conn.emit(payment(buyerA, "pay-a"))
		synctest.Wait()
		if toA := sentTo(f.conn.messages(), buyerA); len(toA) != 2 || !toA[1].qr {
			t.Fatalf("buyer A got %+v", toA)
		}
		if toB := sentTo(f.conn.messages(), buyerB); len(toB) != 1 || toB[0].text != "sorry sold" {
			t.Fatalf("buyer B got %+v", toB)
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
		// A third start finds the sold task and does nothing.
		third, stop := context.WithCancel(t.Context())
		defer stop()
		n := len(f.conn.messages())
		if err := f.manager(third).Resume(third, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(f.conn.messages()) != n {
			t.Fatal("a sold task sent again after restart")
		}
	})
}

func TestResumedTurnTakesPaymentSentWhileDown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		first, stopRun := context.WithCancel(t.Context())
		id, err := f.manager(first).Start(first, 0, f.date, "lunch", 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "interested"))
		synctest.Wait()
		stopRun()
		synctest.Wait()
		// A pays a minute later, while run is down for five.
		time.Sleep(time.Minute)
		paidAt := time.Now()
		time.Sleep(4 * time.Minute)
		second, cancel := context.WithCancel(t.Context())
		defer cancel()
		if err := f.manager(second).Resume(second, true); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second) // reconnecting
		f.conn.emit(paymentAt(buyerA, "pay-a", paidAt))
		synctest.Wait()
		if toA := sentTo(f.conn.messages(), buyerA); len(toA) != 2 || !toA[1].qr {
			t.Fatalf("buyer A got %+v", toA)
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
	})
}

func TestSellTaskNeverAsksBelowFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		m.negotiate = func(_ context.Context, t store.SellTask, _ types.JID) (int64, error) { return t.MinPaise - 1, nil }
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "interested"))
		synctest.Wait()
		if got := sentTo(f.conn.messages(), buyerA); len(got) != 0 {
			t.Fatalf("buyer was asked below the floor: %+v", got)
		}
		buyers, _ := f.st.SellTaskBuyers(ctx, id)
		if len(buyers) != 1 || buyers[0].State != store.BuyerSkipped {
			t.Fatalf("buyers = %+v", buyers)
		}
		// The store refuses a reservation below the floor on its own too.
		d := store.Delivery{Date: f.date, Meal: "lunch", Peer: buyerA.String(), Amount: "29.99", ImageHash: "portal:account", ArmedAt: time.Now()}
		if _, err := f.st.ArmTaskDelivery(ctx, id, d); err == nil {
			t.Fatal("store armed a delivery below the floor")
		}
		d.Amount = "30.00"
		if _, err := f.st.ArmTaskDelivery(ctx, id, d); err != nil {
			t.Fatalf("store refused the floor price: %v", err)
		}
	})
}

func TestSellTaskTurnsAndExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "interested"))
		f.conn.emit(f.reply(buyerB, "me"))
		synctest.Wait()
		// A passes, B lets the two minutes run out.
		time.Sleep(time.Second)
		f.conn.emit(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: buyerA}, ID: "pass", Timestamp: time.Now()},
			Message: &waE2E.Message{Conversation: proto.String("not interested")}})
		synctest.Wait()
		if got := sentTo(f.conn.messages(), buyerB); len(got) != 1 || !strings.HasPrefix(got[0].text, "pay ₹1") {
			t.Fatalf("B's turn didn't start: %+v", got)
		}
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		buyers, _ := f.st.SellTaskBuyers(ctx, id)
		if len(buyers) != 2 || buyers[0].State != store.BuyerPassed || buyers[1].State != store.BuyerTimedOut {
			t.Fatalf("buyers = %+v", buyers)
		}
		if d, _, _ := f.st.TaskDelivery(ctx, id); d != nil {
			t.Fatal("an unpaid reservation was kept after the turns ended")
		}
		// The task ends with the meal.
		time.Sleep(time.Until(f.task(id).EndsAt) + time.Second)
		synctest.Wait()
		if task := f.task(id); task.State != store.TaskExpired {
			t.Fatalf("task is %s after the meal ended", task.State)
		}
	})
}

func TestCancelSellTaskReleasesReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "interested"))
		synctest.Wait()
		if err := m.Cancel(ctx, id); err != nil {
			t.Fatal(err)
		}
		if task := f.task(id); task.State != store.TaskCancelled {
			t.Fatalf("task is %s", task.State)
		}
		if d, _, _ := f.st.TaskDelivery(ctx, id); d != nil {
			t.Fatal("cancelling kept the reservation")
		}
		n := len(f.conn.messages())
		f.conn.emit(payment(buyerA, "late"))
		f.conn.emit(f.reply(buyerB, "interested"))
		synctest.Wait()
		if len(f.conn.messages()) != n {
			t.Fatal("a cancelled task sent a message")
		}
		// The meal can be put up again.
		if _, err := m.Start(ctx, 0, f.date, "lunch", 1, 1); err != nil {
			t.Fatalf("restart after cancel: %v", err)
		}
	})
}

func TestSendLimits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newSendLimits()
		start := time.Now()
		for range taskSendsPerMinute {
			if err := l.wait(t.Context(), 1); err != nil {
				t.Fatal(err)
			}
		}
		if time.Since(start) != 0 {
			t.Fatal("sends under the limit waited")
		}
		if err := l.wait(t.Context(), 1); err != nil || time.Since(start) != time.Minute {
			t.Fatalf("send over the task limit waited %s, err %v", time.Since(start), err)
		}
		// Other tasks share the global limit.
		for task := int64(2); task < 4; task++ {
			for range taskSendsPerMinute {
				if err := l.wait(t.Context(), task); err != nil {
					t.Fatal(err)
				}
			}
		}
		// 1 + 12 sends in the second minute: the last waits a minute more.
		if time.Since(start) != 2*time.Minute {
			t.Fatalf("global limit didn't hold: %s", time.Since(start))
		}
		for l.totals[5] < taskSendCap {
			l.totals[5]++
		}
		if err := l.wait(t.Context(), 5); err == nil {
			t.Fatal("task sent past its cap")
		}
	})
}
