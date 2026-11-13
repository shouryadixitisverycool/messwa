package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/store"
)

func taskEvents(t *testing.T, f *sellFixture, id int64, peer types.JID) []string {
	t.Helper()
	es, err := f.st.SellTaskEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		if e.Peer == peer.String() {
			out = append(out, e.Kind)
		}
	}
	return out
}

func TestMarkSoldByYou(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 40)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		offer := f.offerID()
		for _, b := range []types.JID{buyerA, buyerB, stranger} {
			f.conn.emit(f.reply(b, "price?"))
		}
		settle()
		if err := m.MuteBuyer(ctx, id, stranger.String(), true); err != nil {
			t.Fatal(err)
		}
		before := len(f.conn.messages())

		if err := m.MarkSold(ctx, id, "+91 11111 11111", "Utkarsh", 55, "paid by UPI screenshot"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		after := f.conn.messages()[before:]
		// The buyer you sold to and the muted buyer get nothing; B, still
		// haggling, hears it sold.
		if got := texts(sentTo(after, buyerA)); len(got) != 0 {
			t.Fatalf("your buyer got %q", got)
		}
		if got := texts(sentTo(after, stranger)); len(got) != 0 {
			t.Fatalf("muted buyer got %q", got)
		}
		if got := texts(sentTo(after, buyerB)); len(got) != 1 || got[0] != "sold sorry" {
			t.Fatalf("B got %q", got)
		}
		ticks := sentTo(after, f.group)
		if len(ticks) != 1 || ticks[0].reaction != soldReaction || ticks[0].target != offer {
			t.Fatalf("group got %+v", ticks)
		}

		task := f.task(id)
		if task.State != store.TaskSold || !strings.Contains(task.Detail, "sold by you to Utkarsh (+911111111111) for INR 55") ||
			!strings.Contains(task.Detail, "paid by UPI screenshot") {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
		d, state, _ := f.st.TaskDelivery(ctx, id)
		if state != "sent" || d.Amount != "55.00" || d.Peer != buyerA.String() {
			t.Fatalf("delivery = %+v %q", d, state)
		}
		if kind, _ := f.st.PaymentKind(ctx, d.Date, d.Meal); kind != store.PaidManual {
			t.Fatalf("kind = %q", kind)
		}
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerBought {
			t.Fatalf("A = %+v", b)
		}
		if b := buyerState(t, f, id, stranger); b.State != store.BuyerNegotiating || !b.Muted {
			t.Fatalf("muted buyer = %+v", b)
		}
		if ev := taskEvents(t, f, id, buyerA); !slices.Contains(ev, store.EventSoldByYou) {
			t.Fatalf("A's events = %q", ev)
		}
		if err := m.MarkSold(ctx, id, "", "x", 50, ""); err == nil {
			t.Fatal("sold twice")
		}
	})
}

func TestMarkSoldStopsPaymentTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "available?"))
		f.conn.emit(f.reply(buyerB, "available?"))
		synctest.Wait()
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerOffered {
			t.Fatalf("A = %+v", b)
		}

		// Sold to a friend by name: A, mid-turn, and B, queued, hear it sold.
		if err := m.MarkSold(ctx, id, "", "a friend", 1, ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for _, b := range []types.JID{buyerA, buyerB} {
			if got := texts(sentTo(f.conn.messages(), b)); got[len(got)-1] != "sold sorry" {
				t.Fatalf("%s got %q", b, got)
			}
		}
		// A late payment from A no longer gets the QR.
		f.conn.emit(payment(buyerA, "late"))
		synctest.Wait()
		for _, msg := range f.conn.messages() {
			if msg.qr {
				t.Fatal("QR sent after you sold it")
			}
		}
		if name, _ := f.st.TaskBuyerName(ctx, id); name != "a friend" {
			t.Fatalf("name = %q", name)
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
	})
}

func TestMarkSoldAfterBotSale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "available?"))
		synctest.Wait()
		time.Sleep(time.Second)
		f.conn.emit(payment(buyerA, "pay-1"))
		synctest.Wait()
		if err := m.MarkSold(ctx, id, "", "a friend", 1, ""); err == nil {
			t.Fatal("recorded a manual sale over the bot's")
		}
		if d, _, _ := f.st.TaskDelivery(ctx, id); d.Peer != buyerA.String() {
			t.Fatalf("delivery = %+v", d)
		}
	})
}

func TestAddBuyerGetsOpeningQuote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 40)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := m.AddBuyer(ctx, id, "+91 33333 33333", "Dheeraj"); err != nil {
			t.Fatal(err)
		}
		settle()
		if got := texts(sentTo(f.conn.messages(), stranger)); len(got) != 1 || got[0] != "60" {
			t.Fatalf("added buyer got %q", got)
		}
		b := buyerState(t, f, id, stranger)
		if b.Name != "Dheeraj" || b.State != store.BuyerNegotiating || b.Unknowns != 0 {
			t.Fatalf("buyer = %+v", b)
		}
		if ev := taskEvents(t, f, id, stranger); !slices.Contains(ev, store.EventAddedByYou) {
			t.Fatalf("events = %q", ev)
		}
		// They haggle like anyone who replied.
		f.conn.emit(dm(stranger, "40?"))
		settle()
		if got := texts(sentTo(f.conn.messages(), stranger)); len(got) < 2 || got[1] != "ok 40" {
			t.Fatalf("added buyer got %q", got)
		}

		if err := m.AddBuyer(ctx, id, stranger.String(), ""); err == nil {
			t.Fatal("added the same buyer twice")
		}
		if err := m.AddBuyer(ctx, id, "12345", ""); err == nil {
			t.Fatal("added a number without a country code")
		}
		f.conn.saved = map[types.JID]bool{buyerB: true}
		f.cfg.Sell.Buyers = "unsaved_only"
		if err := m.AddBuyer(ctx, id, buyerB.String(), ""); err == nil {
			t.Fatal("added a saved contact with buyers = unsaved_only")
		}
	})
}

func TestAddBuyerFixedPrice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := m.AddBuyer(ctx, id, buyerA.String(), ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerOffered {
			t.Fatalf("A = %+v", b)
		}
		time.Sleep(time.Second)
		f.conn.emit(payment(buyerA, "pay-1"))
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerA)); got[len(got)-1] != "[QR]" {
			t.Fatalf("A got %q", got)
		}
	})
}

func TestMutedBuyerGetsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 40)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "price?"))
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		if err := m.MuteBuyer(ctx, id, "+91 11111 11111", true); err != nil {
			t.Fatal(err)
		}
		if err := m.MuteBuyer(ctx, id, stranger.String(), true); err == nil {
			t.Fatal("muted someone who never replied")
		}
		// A muted buyer's payment must not trigger the QR either.
		time.Sleep(time.Second)
		f.conn.emit(paymentOf(buyerA, "muted-pay", "60.00", time.Now()))
		settle()
		if d, _, _ := f.st.TaskDelivery(ctx, id); d != nil {
			t.Fatalf("muted buyer claimed delivery: %+v", d)
		}
		f.conn.emit(dm(buyerA, "40?"))
		settle()
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 1 {
			t.Fatalf("muted buyer got %q", got)
		}
		if b := buyerState(t, f, id, buyerA); b.State != store.BuyerNegotiating || b.LastOffer != 0 {
			t.Fatalf("muted buyer = %+v", b)
		}
		if err := m.MuteBuyer(ctx, id, buyerA.String(), false); err != nil {
			t.Fatal(err)
		}
		f.conn.emit(dm(buyerA, "40?"))
		settle()
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) < 2 || got[1] != "ok 40" {
			t.Fatalf("unmuted buyer got %q", got)
		}
		if ev := taskEvents(t, f, id, buyerA); !slices.Contains(ev, store.EventMuted) || !slices.Contains(ev, store.EventUnmuted) {
			t.Fatalf("events = %q", ev)
		}

		// Once A is paying, mute B and let A buy: B hears nothing.
		if err := m.MuteBuyer(ctx, id, buyerB.String(), true); err != nil {
			t.Fatal(err)
		}
		f.conn.emit(paymentOf(buyerA, "pay-a", "40.00", time.Now()))
		synctest.Wait()
		if got := texts(sentTo(f.conn.messages(), buyerB)); len(got) != 1 {
			t.Fatalf("muted B got %q", got)
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
	})
}

func TestMutedFixedPricePayment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := m.AddBuyer(ctx, id, buyerA.String(), ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := m.MuteBuyer(ctx, id, buyerA.String(), true); err != nil {
			t.Fatal(err)
		}
		before := len(sentTo(f.conn.messages(), buyerA))
		time.Sleep(time.Second)
		f.conn.emit(payment(buyerA, "muted-fixed"))
		synctest.Wait()
		if got := sentTo(f.conn.messages(), buyerA); len(got) != before {
			t.Fatalf("muted buyer got %+v", got[before:])
		}
		if _, state, _ := f.st.TaskDelivery(ctx, id); state != "armed" {
			t.Fatalf("delivery = %s", state)
		}
		if err := m.MuteBuyer(ctx, id, buyerA.String(), false); err != nil {
			t.Fatal(err)
		}
		f.conn.emit(payment(buyerA, "unmuted-fixed"))
		synctest.Wait()
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task = %+v", task)
		}
	})
}

func TestResendOfferToFailedGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		refused := types.NewJID("120363000000000002", types.GroupServer)
		f.cfg.Groups = []string{f.group.String(), refused.String()}
		f.conn.groups = append(f.conn.groups, &types.GroupInfo{JID: refused, GroupName: types.GroupName{Name: "Mess 2"}})
		f.conn.refuse = map[types.JID]bool{refused: true}
		f.cfg.Sell.GroupCount = 2
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(f.cfg.Sell.PostGap.Duration)
		synctest.Wait()
		if f.conn.tried[refused] != 1 {
			t.Fatalf("refused group tried %d times", f.conn.tried[refused])
		}

		if err := m.ResendOffer(ctx, id, f.group.String()); err == nil || !strings.Contains(err.Error(), "already up") {
			t.Fatalf("resend to a posted group: %v", err)
		}
		if err := m.ResendOffer(ctx, id, "120363000000000099@g.us"); err == nil {
			t.Fatal("resend to a group the task doesn't post in worked")
		}
		// Still refused: the try is recorded as a failed copy, and the
		// group can be tried again.
		if err := m.ResendOffer(ctx, id, "Mess 2"); err == nil || f.conn.tried[refused] != 2 {
			t.Fatalf("resend while refused: %v, tried %d", err, f.conn.tried[refused])
		}
		f.conn.mu.Lock()
		f.conn.refuse = nil
		f.conn.mu.Unlock()
		if err := m.ResendOffer(ctx, id, refused.String()); err != nil {
			t.Fatal(err)
		}
		if p := groupPosts(f.conn, refused); len(p) != 1 {
			t.Fatalf("refused group got %+v", p)
		}
		offers, _ := f.st.SellTaskOffers(ctx, id)
		var states []string
		for _, o := range offers {
			if o.Group == refused.String() {
				states = append(states, fmt.Sprintf("%d:%s", o.Seq, o.State))
			}
		}
		if fmt.Sprint(states) != "[0:failed 1:failed 2:posted]" {
			t.Fatalf("copies in the refused group: %v", states)
		}
		if err := m.ResendOffer(ctx, id, refused.String()); err == nil {
			t.Fatal("resent a copy that is up")
		}
		// A reply to the resent copy reaches the task.
		f.conn.emit(replyTo(buyerA, refused, groupPosts(f.conn, refused)[0].id, "interested"))
		synctest.Wait()
		if got := sentTo(f.conn.messages(), buyerA); len(got) == 0 {
			t.Fatal("the buyer who replied to the resent copy got nothing")
		}
	})
}
