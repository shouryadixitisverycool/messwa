package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
	"unicode/utf16"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"rsc.io/qr"

	"messwa/internal/config"
	"messwa/internal/portal"
	"messwa/internal/store"
)

//go:embed assets/mockchat.html
var mockChatPage []byte

// mockChatCmd runs real sell tasks against a pretend WhatsApp and serves a
// chat page to play the buyers on. Nothing reaches WhatsApp or the mess
// portal, and the sales go to a throwaway database, so it's safe to try
// the bot's replies, the UPI QR and screenshots end to end.
func mockChatCmd(ctx context.Context, cfg *config.Config, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("mockchat", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8769", "address to serve on")
	upi := fs.String("upi", "", "UPI ID the bot asks buyers to pay")
	fs.Parse(args)
	if err := privateAddr(*listen); err != nil {
		return fmt.Errorf("-listen: %w", err)
	}
	dir, err := os.MkdirTemp("", "messwa-mockchat-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(filepath.Join(dir, "data.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	// The mock sells from defaults plus your meal times, not your saved
	// settings, so the page's UPI and screenshot switches are the ones
	// that count.
	mcfg := config.Default()
	mcfg.Timezone, mcfg.Meals = cfg.Timezone, cfg.Meals
	group := types.NewJID("120363999999999999", types.GroupServer)
	mcfg.Groups = []string{group.String()}
	m := &mockChat{cfg: mcfg, conn: newMockConn(group), buyers: []mockBuyer{
		{JID: "911111111111@s.whatsapp.net", Name: "Asha"},
		{JID: "912222222222@s.whatsapp.net", Name: "Ravi"},
	}}
	mcfg.ApplySaved(nil)
	if err := m.setSettings(*upi, true); err != nil {
		return err
	}
	m.sells = newSellManager(ctx, mcfg, log, st, m.conn, m.conn.send,
		func(time.Duration) bool { return true },
		func(context.Context, int64) (mealPortal, error) { return mockPortal{}, nil }, ownGroupLog{})
	m.st = st
	go m.conn.deliver(ctx)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	fmt.Printf("mock chat on http://%s/\n", *listen)
	return serve(ctx, log, ln, m.routes())
}

type mockBuyer struct {
	JID  string `json:"jid"`
	Name string `json:"name"`
}

type mockChat struct {
	cfg   *config.Config
	conn  *mockConn
	st    *store.Store
	sells *sellManager

	mu       sync.Mutex
	buyers   []mockBuyer
	tasks    []int64
	upi      string
	images   bool
	nextUser int
}

func (m *mockChat) setSettings(upi string, images bool) error {
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	live, err := m.cfg.Update(config.Settings{
		"sell.upi":                   raw(upi),
		"sell.accept_payment_images": raw(images),
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.upi, m.images = live.Sell.UPI, images
	m.mu.Unlock()
	return nil
}

// setClock pretends it's hhmm today ("19:00"), or the real time for "".
// The clock keeps running from there. It swaps the time zone the sales
// tell time in, so meal windows, "too early" and dates all follow it.
func (m *mockChat) setClock(ctx context.Context, hhmm string) error {
	for _, id := range m.taskIDs() {
		if t, err := m.st.GetSellTask(ctx, id); err == nil && t != nil && t.State == store.TaskActive {
			return errors.New("cancel the running sale before changing the time")
		}
	}
	base, err := time.LoadLocation(m.cfg.Timezone)
	if err != nil {
		return err
	}
	loc := base
	if hhmm != "" {
		at, err := time.Parse("15:04", hhmm)
		if err != nil {
			return errors.New("pick a time like 19:00")
		}
		now := time.Now().In(base)
		want := time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), now.Second(), 0, base)
		_, off := now.Zone()
		loc = time.FixedZone("mock "+hhmm, off+int(want.Sub(now).Round(time.Minute).Seconds()))
	}
	m.cfg.SetLocation(loc)
	m.mu.Lock()
	upi, images := m.upi, m.images
	m.mu.Unlock()
	// Update rebuilds Live from the file config, which now has loc.
	return m.setSettings(upi, images)
}

func (m *mockChat) taskIDs() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int64(nil), m.tasks...)
}

func (m *mockChat) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(mockChatPage)
	})
	mux.HandleFunc("GET /media/{id}", func(w http.ResponseWriter, r *http.Request) {
		data := m.conn.mediaFor(r.PathValue("id"))
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(data))
		w.Write(data)
	})
	mux.HandleFunc("GET /api/state", m.state)
	mux.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			UPI    string `json:"upi"`
			Images bool   `json:"images"`
		}
		if !mockRead(w, r, &in) {
			return
		}
		if err := m.setSettings(in.UPI, in.Images); err != nil {
			mockError(w, err)
			return
		}
		m.conn.bump()
		m.state(w, r)
	})
	mux.HandleFunc("POST /api/clock", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Time string }
		if !mockRead(w, r, &in) {
			return
		}
		if err := m.setClock(r.Context(), in.Time); err != nil {
			mockError(w, err)
			return
		}
		m.conn.bump()
		m.state(w, r)
	})
	mux.HandleFunc("POST /api/start", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Meal     string `json:"meal"`
			Min, Max int
		}
		if !mockRead(w, r, &in) {
			return
		}
		date := time.Now().In(m.cfg.Location()).Format("2006-01-02")
		id, err := m.sells.Start(r.Context(), 0, date, in.Meal, in.Min, in.Max, "")
		if err != nil {
			mockError(w, err)
			return
		}
		m.mu.Lock()
		m.tasks = append(m.tasks, id)
		m.mu.Unlock()
		m.conn.bump()
		m.state(w, r)
	})
	mux.HandleFunc("POST /api/cancel", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ ID int64 }
		if !mockRead(w, r, &in) {
			return
		}
		if err := m.sells.Cancel(r.Context(), in.ID, false); err != nil {
			mockError(w, err)
			return
		}
		m.conn.bump()
		m.state(w, r)
	})
	mux.HandleFunc("POST /api/buyer", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		if !mockRead(w, r, &in) {
			return
		}
		m.mu.Lock()
		m.nextUser++
		b := mockBuyer{JID: fmt.Sprintf("9133%08d@s.whatsapp.net", m.nextUser), Name: in.Name}
		if b.Name == "" {
			b.Name = fmt.Sprintf("Buyer %d", len(m.buyers)+1)
		}
		m.buyers = append(m.buyers, b)
		m.mu.Unlock()
		m.conn.bump()
		m.state(w, r)
	})
	// A buyer's text, optionally quoting the offer like a private reply.
	mux.HandleFunc("POST /api/send", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Buyer, Text string
			Quote       bool
		}
		if !mockRead(w, r, &in) {
			return
		}
		b, ok := m.buyer(in.Buyer)
		if !ok || in.Text == "" {
			mockError(w, errors.New("pick a buyer and type something"))
			return
		}
		m.conn.fromBuyer(b, in.Text, in.Quote, nil, "")
		w.WriteHeader(http.StatusNoContent)
	})
	// A buyer's image, such as a payment screenshot.
	mux.HandleFunc("POST /api/image", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		b, ok := m.buyer(r.FormValue("buyer"))
		f, _, err := r.FormFile("image")
		if !ok || err != nil {
			mockError(w, errors.New("pick a buyer and an image"))
			return
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			mockError(w, err)
			return
		}
		m.conn.fromBuyer(b, "", false, data, "")
		w.WriteHeader(http.StatusNoContent)
	})
	// A WhatsApp Pay payment from a buyer.
	mux.HandleFunc("POST /api/pay", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Buyer, Amount string }
		if !mockRead(w, r, &in) {
			return
		}
		b, ok := m.buyer(in.Buyer)
		rupees, err := strconv.ParseFloat(in.Amount, 64)
		if !ok || err != nil || rupees <= 0 {
			mockError(w, errors.New("pick a buyer and an amount"))
			return
		}
		m.conn.fromBuyer(b, "", false, nil, fmt.Sprintf("%.2f", rupees))
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (m *mockChat) buyer(jid string) (mockBuyer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.buyers {
		if b.JID == jid {
			return b, true
		}
	}
	return mockBuyer{}, false
}

type mockTask struct {
	ID     int64  `json:"id"`
	Meal   string `json:"meal"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

func (m *mockChat) state(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	buyers := append([]mockBuyer(nil), m.buyers...)
	ids := append([]int64(nil), m.tasks...)
	upi, images := m.upi, m.images
	m.mu.Unlock()
	tasks := []mockTask{}
	for _, id := range ids {
		t, err := m.st.GetSellTask(r.Context(), id)
		if err != nil || t == nil {
			continue
		}
		tasks = append(tasks, mockTask{t.ID, t.Meal, string(t.State), t.Detail})
	}
	// next is the first meal today that can go up now.
	type meal struct {
		Name   string `json:"name"`
		Starts string `json:"starts"`
		Ends   string `json:"ends"`
	}
	meals, next := []meal{}, ""
	now := time.Now()
	date := now.In(m.cfg.Location()).Format("2006-01-02")
	for _, ml := range m.cfg.Meals {
		meals = append(meals, meal{ml.Name, ml.Starts, ml.Ends})
		if slot, err := findSlot(m.cfg, date, ml.Name); next == "" && err == nil && now.Before(slot.End) && tooEarly(m.cfg, slot, now) == nil {
			next = ml.Name
		}
	}
	version, msgs := m.conn.snapshot()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"version": version, "group": m.conn.group.String(), "buyers": buyers, "messages": msgs,
		"tasks": tasks, "upi": upi, "images": images, "meals": meals, "next_meal": next,
		"clock": now.In(m.cfg.Location()).Format("15:04"), "pretend": m.cfg.Location().String() != m.cfg.Timezone,
	})
}

func mockRead(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		mockError(w, err)
		return false
	}
	return true
}

func mockError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// mockMessage is one chat bubble on the page.
type mockMessage struct {
	ID       string    `json:"id"`
	Chat     string    `json:"chat"`
	FromBot  bool      `json:"from_bot"`
	Text     string    `json:"text,omitempty"`
	Image    string    `json:"image,omitempty"`
	Quoted   string    `json:"quoted,omitempty"`
	Payment  string    `json:"payment,omitempty"`
	Reaction string    `json:"reaction,omitempty"`
	Deleted  bool      `json:"deleted,omitempty"`
	Edited   bool      `json:"edited,omitempty"`
	At       time.Time `json:"at"`
}

// mockConn is a pretend WhatsApp: what the bot sends lands in msgs, and
// the buyers' messages go to its event handlers one at a time, in order.
type mockConn struct {
	group types.JID

	mu       sync.Mutex
	handlers map[uint32]whatsmeow.EventHandler
	nextH    uint32
	ids      int
	msgs     []mockMessage
	media    map[string][]byte
	version  int
	queue    chan any
}

func newMockConn(group types.JID) *mockConn {
	return &mockConn{group: group, handlers: map[uint32]whatsmeow.EventHandler{}, media: map[string][]byte{}, queue: make(chan any, 64)}
}

func (c *mockConn) deliver(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-c.queue:
			c.mu.Lock()
			hs := make([]whatsmeow.EventHandler, 0, len(c.handlers))
			for _, h := range c.handlers {
				hs = append(hs, h)
			}
			c.mu.Unlock()
			for _, h := range hs {
				h(evt)
			}
		}
	}
}

func (c *mockConn) bump() {
	c.mu.Lock()
	c.version++
	c.mu.Unlock()
}

func (c *mockConn) snapshot() (int, []mockMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version, append([]mockMessage{}, c.msgs...)
}

func (c *mockConn) mediaFor(id string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.media[id]
}

// newIDLocked is a message ID; c.mu must be held.
func (c *mockConn) newIDLocked() string {
	c.ids++
	return "MOCK" + strconv.Itoa(c.ids)
}

// fromBuyer records a buyer's message and hands it to the bot: text
// (quoting the latest offer if quote), an image, or a WhatsApp Pay
// payment of amount rupees.
func (c *mockConn) fromBuyer(b mockBuyer, text string, quote bool, image []byte, amount string) {
	jid, _ := types.ParseJID(b.JID)
	now := time.Now()
	c.mu.Lock()
	id := c.newIDLocked()
	bubble := mockMessage{ID: id, Chat: b.JID, Text: text, At: now}
	msg := &waE2E.Message{Conversation: proto.String(text)}
	switch {
	case image != nil:
		c.media[id] = image
		bubble.Image = "media/" + id
		sum := sha256.Sum256(image)
		msg = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String(http.DetectContentType(image)), DirectPath: proto.String(id), FileSHA256: sum[:]}}
	case amount != "":
		bubble.Payment = amount
		msg = &waE2E.Message{SendPaymentMessage: &waE2E.SendPaymentMessage{
			TransactionData: proto.String(base64.StdEncoding.EncodeToString(mockPaymentPayload(amount, now)))}}
	case quote:
		if offer := c.lastOfferLocked(); offer != "" {
			bubble.Quoted = offer
			msg = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text),
				ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String(offer), RemoteJID: proto.String(c.group.String())}}}
		}
	}
	c.msgs = append(c.msgs, bubble)
	c.version++
	c.mu.Unlock()
	c.queue <- &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, Sender: jid}, ID: id, Timestamp: now, PushName: b.Name},
		Message: msg,
	}
}

// lastOfferLocked is the newest offer the bot posted in the group.
func (c *mockConn) lastOfferLocked() string {
	for i := len(c.msgs) - 1; i >= 0; i-- {
		if m := c.msgs[i]; m.FromBot && m.Chat == c.group.String() && !m.Deleted && m.Text != "" {
			return m.ID
		}
	}
	return ""
}

func (c *mockConn) send(_ context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() { c.version++ }()
	if r := msg.GetReactionMessage(); r != nil {
		c.updateLocked(r.GetKey().GetID(), func(m *mockMessage) { m.Reaction = r.GetText() })
		return whatsmeow.SendResponse{}, nil
	}
	if pm := msg.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			c.updateLocked(pm.GetKey().GetID(), func(m *mockMessage) { m.Deleted = true })
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			c.updateLocked(pm.GetKey().GetID(), func(m *mockMessage) { m.Text, m.Edited = messageText(pm.GetEditedMessage()), true })
		}
		return whatsmeow.SendResponse{}, nil
	}
	id := ""
	if len(extra) > 0 {
		id = string(extra[0].ID)
	}
	if id == "" {
		id = c.newIDLocked()
	}
	bubble := mockMessage{ID: id, Chat: to.String(), FromBot: true, Text: messageText(msg), At: time.Now(),
		Quoted: msg.GetExtendedTextMessage().GetContextInfo().GetStanzaID()}
	if img := msg.GetImageMessage(); img != nil {
		bubble.Text, bubble.Image = img.GetCaption(), img.GetURL()
	}
	c.msgs = append(c.msgs, bubble)
	return whatsmeow.SendResponse{ID: types.MessageID(id), Timestamp: bubble.At}, nil
}

func (c *mockConn) updateLocked(id string, f func(*mockMessage)) {
	for i := range c.msgs {
		if c.msgs[i].ID == id {
			f(&c.msgs[i])
		}
	}
}

func (c *mockConn) AddEventHandler(h whatsmeow.EventHandler) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextH++
	c.handlers[c.nextH] = h
	return c.nextH
}

func (c *mockConn) RemoveEventHandler(id uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.handlers, id)
	return true
}

func (c *mockConn) GenerateMessageID() types.MessageID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return types.MessageID(c.newIDLocked())
}

func (c *mockConn) Upload(_ context.Context, data []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.newIDLocked()
	c.media[id] = data
	return whatsmeow.UploadResponse{URL: "media/" + id, DirectPath: "/" + id, FileLength: uint64(len(data))}, nil
}

func (c *mockConn) SendPeerMessage(context.Context, *waE2E.Message) (whatsmeow.SendResponse, error) {
	return whatsmeow.SendResponse{}, nil
}

func (c *mockConn) BuildUnavailableMessageRequest(types.JID, types.JID, string) *waE2E.Message {
	return &waE2E.Message{}
}

func (c *mockConn) BuildReaction(chat, _ types.JID, id types.MessageID, reaction string) *waE2E.Message {
	return &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String(reaction), Key: &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), ID: proto.String(id)}}}
}

func (c *mockConn) BuildRevoke(chat, _ types.JID, id types.MessageID) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key: &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(true), ID: proto.String(id)}}}
}

func (c *mockConn) BuildEdit(chat types.JID, id types.MessageID, newContent *waE2E.Message) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key: &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(true), ID: proto.String(id)}, EditedMessage: newContent}}
}

func (c *mockConn) GetJoinedGroups(context.Context) ([]*types.GroupInfo, error) {
	return []*types.GroupInfo{{JID: c.group, GroupName: types.GroupName{Name: "Mess Buy Sell (mock)"}}}, nil
}

func (c *mockConn) GetGroupInfo(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
	if jid != c.group {
		return nil, errors.New("not in group")
	}
	return &types.GroupInfo{JID: c.group, GroupName: types.GroupName{Name: "Mess Buy Sell (mock)"}}, nil
}

func (c *mockConn) PNForLID(context.Context, types.JID) types.JID   { return types.EmptyJID }
func (c *mockConn) SavedContact(context.Context, ...types.JID) bool { return false }

// Download returns a buyer's image by the ID fromBuyer put in its path.
func (c *mockConn) Download(_ context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error) {
	if data := c.mediaFor(msg.GetDirectPath()); data != nil {
		return data, nil
	}
	return nil, errors.New("no such mock media")
}

// mockPortal says every meal is registered at kadamba and hands out a
// meal QR that says it's a mock.
type mockPortal struct{}

func (mockPortal) RequireRegistration(_ context.Context, date, meal string) (portal.Registration, error) {
	return portal.Registration{Date: date, Meal: meal, Mess: "kadamba", Category: "registered"}, nil
}

func (p mockPortal) QRForMeal(ctx context.Context, date, meal, _ string) (portal.CurrentQR, error) {
	reg, _ := p.RequireRegistration(ctx, date, meal)
	code, err := qr.Encode("MOCK MEAL QR "+date+" "+meal, qr.M)
	if err != nil {
		return portal.CurrentQR{}, err
	}
	return portal.CurrentQR{PNG: code.PNG(), AccountHash: "mock", Registration: reg}, nil
}

// mockPaymentPayload is WhatsApp Pay's transaction data for amount rupees
// ("45.00") at at, laid out like the one payment we decoded (see
// decodePaymentPayload).
func mockPaymentPayload(amount string, at time.Time) []byte {
	b := []byte{1, 0, 0, 0, 0x91, 1, 0, 0}
	b = binary.LittleEndian.AppendUint64(b, uint64(at.UnixMilli()))
	b = appendParcelString(b, "INR")
	b = binary.LittleEndian.AppendUint32(b, 2)
	b = appendParcelString(b, amount)
	return b
}

// appendParcelString writes s as an Android Parcel string: its length in
// UTF-16 units, the units, a zero unit, then padding to 4 bytes.
func appendParcelString(b []byte, s string) []byte {
	u := utf16.Encode([]rune(s))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(u)))
	for _, r := range u {
		b = binary.LittleEndian.AppendUint16(b, r)
	}
	b = append(b, 0, 0)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}
