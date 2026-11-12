package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/store"
)

func TestOfferTopicAsksAbout(t *testing.T) {
	kadambaLunch := newOfferTopic(config.Default(), "kadamba", "lunch", "")
	for text, want := range map[string]bool{
		"price?":                         true,
		"How much?":                      true,
		"kitne ka hai":                   true,
		"is the kadamba lunch available": true,
		"kadamb?":                        true,
		"still available?":               true,
		"hey is the lunch still available, I can pay right now if you want": true,
		"I'll take it":                     true,
		"bakul lunch available?":           false, // another mess
		"is the dinner available":          false, // another meal
		"kadamba dinner?":                  false,
		"not interested":                   false,
		"not interested in buying, thanks": false,
		"wanna get lunch?":                 false, // no buying question
		"hey":                              false,
		"are we still meeting at 5 near the library":                       false,
		"how much did you get in the quiz, I got way less than I expected": false, // long, names nothing
	} {
		if got := kadambaLunch.asksAbout(text); got != want {
			t.Errorf("%q: asksAbout = %v, want %v", text, got, want)
		}
	}
	// A hand-written offer that names no mess is judged on the meal and
	// the question alone; one that names a mess takes it from the text.
	if !newOfferTopic(config.Default(), "", "lunch", "Test lunch offer").asksAbout("palash lunch price?") {
		t.Error("offer without a mess turned down a DM naming one")
	}
	if newOfferTopic(config.Default(), "", "lunch", "sell kadamba lunch").asksAbout("palash lunch price?") {
		t.Error("offer text names kadamba, but a palash DM matched")
	}
}

// plainDMFixture is a group offer with one copy posted at postedAt and plain
// DMs on. lastSent stands in for the DM records run keeps.
type plainDMFixture struct {
	o        *groupOffer
	conn     *fakeConn
	postedAt time.Time
	lastSent map[types.JID]time.Time
	lookErr  error
}

func newPlainDMFixture(t *testing.T) *plainDMFixture {
	f := &plainDMFixture{postedAt: time.Unix(1793900000, 0), lastSent: map[types.JID]time.Time{}}
	group := types.NewJID("111", types.GroupServer)
	f.conn = newFakeConn(group)
	f.o = &groupOffer{copies: []postedOffer{{Chat: group, ID: "offer", At: f.postedAt}}}
	f.o.acceptPlainDMs(&plainDMs{
		topic: newOfferTopic(config.Default(), "kadamba", "lunch", ""),
		lastSent: func(_ context.Context, ids ...types.JID) (time.Time, error) {
			var last time.Time
			for _, id := range ids {
				if at := f.lastSent[id]; at.After(last) {
					last = at
				}
			}
			return last, f.lookErr
		},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return f
}

func plainDM(from types.JID, text string, at time.Time) *events.Message {
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: from}, ID: "dm-" + from.User, Timestamp: at},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
}

func TestPlainDMsJoinOnlyWhenAboutTheOffer(t *testing.T) {
	buyer := types.NewJID("911111111111", types.DefaultUserServer)
	f := newPlainDMFixture(t)
	after := f.postedAt.Add(time.Minute)
	if peer, ok := f.o.buyer(t.Context(), f.conn, plainDM(buyer, "price?", after)); !ok || peer != buyer {
		t.Fatalf("plain DM about the offer: %v, %v", peer, ok)
	}

	// A LID chat is queued by phone number, like a quoted reply.
	lid := types.NewJID("55555", types.HiddenUserServer)
	f.conn.lids[lid] = buyer
	if peer, ok := f.o.buyer(t.Context(), f.conn, plainDM(lid, "still available?", after)); !ok || peer != buyer {
		t.Fatalf("plain DM from a LID chat: %v, %v", peer, ok)
	}

	for name, alter := range map[string]func(*events.Message){
		"before the offer": func(e *events.Message) { e.Info.Timestamp = f.postedAt.Add(-time.Second) },
		"unrelated text": func(e *events.Message) {
			e.Message.Conversation = proto.String("are we still meeting at 5 near the library")
		},
		"other mess":        func(e *events.Message) { e.Message.Conversation = proto.String("bakul lunch price?") },
		"passes":            func(e *events.Message) { e.Message.Conversation = proto.String("not interested") },
		"edit":              func(e *events.Message) { e.IsEdit = true },
		"own message":       func(e *events.Message) { e.Info.IsFromMe = true },
		"group message":     func(e *events.Message) { e.Info.IsGroup = true; e.Info.Chat = types.NewJID("222", types.GroupServer) },
		"status broadcast":  func(e *events.Message) { e.Info.Chat = types.StatusBroadcastJID },
		"payment, not text": func(e *events.Message) { e.Message = &waE2E.Message{SendPaymentMessage: &waE2E.SendPaymentMessage{}} },
		"quotes another msg": func(e *events.Message) {
			e.Message = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("price?"),
				ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("earlier-message")}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := plainDM(buyer, "price?", after)
			alter(e)
			if _, ok := f.o.buyer(t.Context(), f.conn, e); ok {
				t.Fatal("joined the queue")
			}
		})
	}

	off := newPlainDMFixture(t)
	off.o.plain = nil
	if _, ok := off.o.buyer(t.Context(), off.conn, plainDM(buyer, "price?", after)); ok {
		t.Fatal("plain DM joined with the setting off")
	}
}

func TestPlainDMsSkipChatsYoureIn(t *testing.T) {
	friend := types.NewJID("914444444444", types.DefaultUserServer)
	friendLID := types.NewJID("44444", types.HiddenUserServer)
	at := time.Unix(1793900000, 0).Add(time.Hour)
	ask := func(f *plainDMFixture) bool {
		_, ok := f.o.buyer(context.Background(), f.conn, plainDM(friend, "how much?", at))
		return ok
	}

	// run's DM records say you messaged them 10 minutes ago.
	f := newPlainDMFixture(t)
	f.lastSent[friend] = at.Add(-10 * time.Minute)
	if ask(f) {
		t.Fatal("DM from someone you were chatting with joined")
	}
	// Or 31 minutes ago: that chat has gone quiet.
	f.lastSent[friend] = at.Add(-chatQuietFor - time.Minute)
	if !ask(f) {
		t.Fatal("DM after a quiet half hour was turned down")
	}

	// deliver-qr has no DM records, so it notes what you send from your
	// phone, here to their LID chat with their number as the alt.
	f = newPlainDMFixture(t)
	f.o.buyer(context.Background(), f.conn, &events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: friendLID, RecipientAlt: friend, IsFromMe: true},
			ID: "mine", Timestamp: at.Add(-time.Minute)},
		Message: &waE2E.Message{Conversation: proto.String("see you at 5")},
	})
	if ask(f) {
		t.Fatal("DM right after you messaged them joined")
	}

	// When the records can't be read, the DM is turned down.
	f = newPlainDMFixture(t)
	f.lookErr = errors.New("database is locked")
	if ask(f) {
		t.Fatal("DM joined although the chat check failed")
	}
}

func TestLastSentTo(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	at := time.Unix(1793900000, 0)
	st.TouchPeer(ctx, "911@s.whatsapp.net", "11@lid", "", true, at)
	st.TouchPeer(ctx, "911@s.whatsapp.net", "11@lid", "Asha", false, at.Add(time.Hour))
	st.TouchPeer(ctx, "922@s.whatsapp.net", "", "Ravi", false, at)
	for _, tc := range []struct {
		ids  []string
		want time.Time
	}{
		{[]string{"911@s.whatsapp.net"}, at},
		{[]string{"11@lid"}, at},
		{[]string{"nobody@lid", "11@lid"}, at},
		{[]string{"922@s.whatsapp.net"}, time.Time{}}, // only wrote to you
		{nil, time.Time{}},
	} {
		got, err := st.LastSentTo(ctx, tc.ids...)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("LastSentTo(%v) = %v, %v; want %v", tc.ids, got, err, tc.want)
		}
	}
}

// TestSellTaskAcceptsPlainDMs runs a whole task with accept_plain_dms on: a
// DM about the offer gets the usual prompt, unrelated DMs and a friend you
// were chatting with get nothing, and nothing but a matching payment from
// the buyer whose turn it is releases the QR.
func TestSellTaskAcceptsPlainDMs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.AcceptPlainDMs = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		friend := types.NewJID("914444444444", types.DefaultUserServer)
		// run's ingest recorded that you messaged the friend 5 minutes ago.
		if err := f.st.TouchPeer(ctx, friend.String(), "", "", true, time.Now().Add(-5*time.Minute)); err != nil {
			t.Fatal(err)
		}
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		// A DM sent before the offer, delivered late, doesn't count.
		f.conn.emit(plainDM(buyerB, "bakul lunch?", time.Now().Add(-time.Minute)))
		time.Sleep(time.Second)

		f.conn.emit(plainDM(stranger, "are we still meeting at 5 near the library", time.Now()))
		f.conn.emit(plainDM(friend, "how much?", time.Now()))
		f.conn.emit(plainDM(buyerA, "is the bakul lunch still available", time.Now()))
		synctest.Wait()
		toA := sentTo(f.conn.messages(), buyerA)
		if len(toA) != 2 || toA[0].text != "ok 1" || !strings.HasPrefix(toA[1].text, "pay on whatsapp pay in this chat") {
			t.Fatalf("plain-DM buyer got %+v, want the usual payment prompt", toA)
		}
		if got := len(sentTo(f.conn.messages(), stranger)) + len(sentTo(f.conn.messages(), friend)); got != 0 {
			t.Fatalf("%d messages went to people who didn't ask about the offer", got)
		}
		buyers, _ := f.st.SellTaskBuyers(ctx, id)
		if len(buyers) != 1 || buyers[0].Peer != buyerA.String() || buyers[0].State != store.BuyerOffered {
			t.Fatalf("buyers = %+v", buyers)
		}
		// The gate still refuses everyone who isn't a queued buyer.
		m.mu.Lock()
		gate := m.running[id].gate
		m.mu.Unlock()
		if _, err := gate.send(ctx, friend, &waE2E.Message{Conversation: proto.String("hi")}); err == nil {
			t.Fatal("gate let a message through to the friend")
		}

		// A quoted reply queues behind the plain DM.
		f.conn.emit(f.reply(buyerB, "me"))
		synctest.Wait()
		// Chatter and payments from anyone else don't release the QR.
		time.Sleep(time.Second)
		f.conn.emit(plainDM(buyerA, "ok one sec", time.Now()))
		f.conn.emit(payment(stranger, "pay-stranger"))
		f.conn.emit(payment(friend, "pay-friend"))
		f.conn.emit(payment(buyerB, "pay-b-early"))
		synctest.Wait()
		for _, msg := range f.conn.messages() {
			if msg.qr {
				t.Fatalf("QR sent without a matching payment from the buyer: %+v", msg)
			}
		}
		// A's two minutes run out; B gets the same prompt and pays.
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if got := sentTo(f.conn.messages(), buyerB); len(got) != 2 || got[0].text != "ok 1" {
			t.Fatalf("B's turn didn't start: %+v", got)
		}
		time.Sleep(time.Second)
		f.conn.emit(payment(buyerB, "pay-b"))
		synctest.Wait()
		if toB := sentTo(f.conn.messages(), buyerB); len(toB) != 3 || !toB[2].qr {
			t.Fatalf("buyer B got %+v, want the prompt then the QR", toB)
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
		// Once sold, a new plain DM gets nothing.
		n := len(f.conn.messages())
		f.conn.emit(plainDM(buyerA, "still available?", time.Now()))
		synctest.Wait()
		if len(f.conn.messages()) != n {
			t.Fatal("a DM after the sale was answered")
		}
	})
}
