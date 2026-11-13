package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/store"
)

var (
	// friendPN is saved under their number but replies from their LID chat.
	friendPN  = types.NewJID("914444444444", types.DefaultUserServer)
	friendLID = types.NewJID("444444444444444", types.HiddenUserServer)
	// lidFriend is saved only under their LID.
	lidFriendPN  = types.NewJID("915555555555", types.DefaultUserServer)
	lidFriendLID = types.NewJID("555555555555555", types.HiddenUserServer)
)

// saveContacts adds buyerA and both friends to the fake address book.
func (f *sellFixture) saveContacts() {
	f.conn.mu.Lock()
	defer f.conn.mu.Unlock()
	f.conn.saved = map[types.JID]bool{buyerA: true, friendPN: true, lidFriendLID: true}
	f.conn.lids[friendLID] = friendPN
	f.conn.lids[lidFriendLID] = lidFriendPN
}

func TestSellTaskSkipsSavedContacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.Buyers = config.BuyersUnsavedOnly
		f.saveContacts()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		// Three saved contacts reply first: by number, by LID with the
		// contact saved under the number, and by LID with the contact saved
		// under the LID. Then an unsaved number.
		for _, from := range []types.JID{buyerA, friendLID, lidFriendLID, buyerB} {
			f.conn.emit(f.reply(from, "interested"))
			synctest.Wait()
		}
		buyers, err := f.st.SellTaskBuyers(ctx, id)
		if err != nil || len(buyers) != 1 || buyers[0].Peer != buyerB.String() {
			t.Fatalf("queued buyers = %+v, %v; want only the unsaved number", buyers, err)
		}
		if toB := sentTo(f.conn.messages(), buyerB); len(toB) != 3 {
			t.Fatalf("unsaved buyer got %+v, want the payment prompt", toB)
		}

		// The gate refuses a saved contact even if one got recorded as a
		// buyer some other way, by number or by LID.
		m.mu.Lock()
		gate := m.running[id].gate
		m.mu.Unlock()
		if _, err := f.st.AddSellTaskBuyer(ctx, id, friendPN.String(), friendLID.String(), time.Now()); err != nil {
			t.Fatal(err)
		}
		for _, to := range []types.JID{friendPN, friendLID} {
			if _, err := gate.send(ctx, to, &waE2E.Message{Conversation: proto.String("hi")}); err != errSavedContact {
				t.Fatalf("gate send to saved contact %s: %v", to, err)
			}
		}

		time.Sleep(time.Second)
		f.conn.emit(payment(buyerB, "pay-b"))
		synctest.Wait()
		if toB := sentTo(f.conn.messages(), buyerB); len(toB) != 4 || !toB[3].qr {
			t.Fatalf("buyer B got %+v, want the prompt then the QR", toB)
		}
		for _, saved := range []types.JID{buyerA, friendPN, friendLID, lidFriendPN, lidFriendLID} {
			if got := sentTo(f.conn.messages(), saved); len(got) != 0 {
				t.Fatalf("saved contact %s was messaged: %+v", saved, got)
			}
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
	})
}

func TestSellTaskSellsToSavedContactsByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.saveContacts()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if _, err := f.manager(ctx).Start(ctx, 0, f.date, "lunch", 1, 1, ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "interested"))
		synctest.Wait()
		if toA := sentTo(f.conn.messages(), buyerA); len(toA) != 3 {
			t.Fatalf("saved contact got %+v, want the payment prompt with buyers = anyone", toA)
		}
	})
}

// Switching to unsaved_only across a restart skips saved contacts still
// waiting, but a saved buyer whose turn was under way can still pay and
// gets the QR.
func TestResumedTaskSkipsSavedContactsStillQueued(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.saveContacts()
		first, stopRun := context.WithCancel(t.Context())
		id, err := f.manager(first).Start(first, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for _, from := range []types.JID{buyerA, friendPN, buyerB} {
			f.conn.emit(f.reply(from, "interested"))
			synctest.Wait()
		}
		stopRun()
		synctest.Wait()

		f.cfg.Sell.Buyers = config.BuyersUnsavedOnly
		second, cancel := context.WithCancel(t.Context())
		defer cancel()
		if err := f.manager(second).Resume(second, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		buyers, _ := f.st.SellTaskBuyers(second, id)
		states := map[string]string{}
		for _, b := range buyers {
			states[b.Peer] = b.State
		}
		if states[friendPN.String()] != store.BuyerSkipped || states[buyerB.String()] != store.BuyerQueued {
			t.Fatalf("buyer states after resume = %v", states)
		}

		time.Sleep(time.Second)
		f.conn.emit(payment(buyerA, "pay-a"))
		synctest.Wait()
		if toA := sentTo(f.conn.messages(), buyerA); len(toA) != 4 || !toA[3].qr {
			t.Fatalf("buyer A got %+v, want the prompt then the QR", toA)
		}
		if got := sentTo(f.conn.messages(), friendPN); len(got) != 0 {
			t.Fatalf("skipped saved contact was messaged: %+v", got)
		}
		if toB := sentTo(f.conn.messages(), buyerB); len(toB) != 1 || toB[0].text != "sold sorry" {
			t.Fatalf("buyer B got %+v, want sold sorry", toB)
		}
	})
}

// deliver-qr -group's queue filter.
func TestSavedContactFilter(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &sellFixture{conn: newFakeConn(types.NewJID("1", types.GroupServer))}
	f.saveContacts()
	if savedContactFilter(t.Context(), f.conn, log, config.BuyersAnyone) != nil {
		t.Fatal("buyers = anyone filters the queue")
	}
	q := newOfferQueue()
	q.join = savedContactFilter(t.Context(), f.conn, log, config.BuyersUnsavedOnly)
	// watchGroupOffer queues a LID buyer by number when it knows it, with
	// the LID chat they replied from.
	q.addFrom(buyerA, buyerA, "")
	q.addFrom(friendPN, friendLID, "")
	q.addFrom(lidFriendPN, lidFriendLID, "")
	q.addFrom(lidFriendLID, lidFriendLID, "")
	q.addFrom(buyerB, buyerB, "")
	if peer, err := q.next(t.Context()); err != nil || peer != buyerB {
		t.Fatalf("first buyer = %v, %v; want the unsaved number", peer, err)
	}
	if waiting := q.closeAndDrain(); len(waiting) != 0 {
		t.Fatalf("saved contacts queued: %v", waiting)
	}
}
