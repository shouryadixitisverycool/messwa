package main

import (
	"context"
	"path/filepath"
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

// replyTo is a private reply from buyer quoting the post id in group.
func replyTo(buyer, group types.JID, id, text string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: buyer}, ID: "reply-" + buyer.User + id, Timestamp: time.Now()},
		Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String(id), RemoteJID: proto.String(group.String())},
		}},
	}
}

func TestSellTaskHandsOffFirstReply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		if _, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "sometimes"); err == nil {
			t.Fatal("started a task with an unknown handoff")
		}
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "on_reply")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if task := f.task(id); task.Handoff != "on_reply" {
			t.Fatalf("task handoff = %q", task.Handoff)
		}
		reply := f.reply(buyerA, "interested")
		reply.Info.PushName = "Asha"
		f.conn.emit(reply)
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "me too"))
		synctest.Wait()

		if got := sentTo(f.conn.messages(), buyerA); len(got) != 0 {
			t.Fatalf("handed-off buyer was messaged: %+v", got)
		}
		if got := sentTo(f.conn.messages(), buyerB); len(got) != 0 {
			t.Fatalf("a later buyer was messaged: %+v", got)
		}
		task := f.task(id)
		if task.State != store.TaskHandedOff || !strings.Contains(task.Detail, "Asha (+911111111111)") || !strings.Contains(task.Detail, "INR 60.00") {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
		d, state, err := f.st.TaskDelivery(ctx, id)
		if err != nil || d == nil || state != "handed_off" || d.Peer != buyerA.String() || d.Amount != "60.00" {
			t.Fatalf("delivery = %+v %q %v", d, state, err)
		}
		if name, _ := f.st.TaskBuyerName(ctx, id); name != "Asha" {
			t.Fatalf("buyer name = %q", name)
		}
		buyers, _ := f.st.SellTaskBuyers(ctx, id)
		if len(buyers) != 1 || buyers[0].State != store.BuyerHandedOff {
			t.Fatalf("buyers = %+v", buyers)
		}
		// Nothing is ticked: the sale isn't done.
		for _, msg := range sentTo(f.conn.messages(), f.group) {
			if msg.reaction != "" {
				t.Fatalf("handoff reacted to the offer: %+v", msg)
			}
		}
	})
}

func TestSellTaskPostsCopiesAndResumesTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		groups := []types.JID{f.group,
			types.NewJID("120363000000000002", types.GroupServer),
			types.NewJID("120363000000000003", types.GroupServer),
			types.NewJID("120363000000000004", types.GroupServer)}
		f.cfg.Groups = nil
		f.conn.groups = nil
		for i, g := range groups {
			f.cfg.Groups = append(f.cfg.Groups, g.String())
			f.conn.groups = append(f.conn.groups, &types.GroupInfo{JID: g, GroupName: types.GroupName{Name: "Mess " + string(rune('A'+i))}})
		}
		f.cfg.Sell.GroupCount = len(groups)
		f.cfg.Sell.ShowPrice = false
		gap := f.cfg.Sell.PostGap.Duration
		posted := func() []sentMessage {
			var out []sentMessage
			for _, msg := range f.conn.messages() {
				if msg.to.Server == types.GroupServer && msg.reaction == "" {
					out = append(out, msg)
				}
			}
			return out
		}

		first, stopRun := context.WithCancel(t.Context())
		id, err := f.manager(first).Start(first, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if p := posted(); len(p) != 1 || p[0].to != groups[0] || p[0].text != "sell bakul non-veg lunch" {
			t.Fatalf("first copy = %+v", p)
		}
		time.Sleep(gap)
		synctest.Wait()
		if p := posted(); len(p) != 2 || p[1].to != groups[1] {
			t.Fatalf("after one gap = %+v", p)
		}
		secondCopy := posted()[1].id

		// run stops mid-gap and starts again: the posted copies aren't
		// repeated, and the next group gets its copy a gap later.
		stopRun()
		synctest.Wait()
		second, cancel := context.WithCancel(t.Context())
		defer cancel()
		if err := f.manager(second).Resume(second, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if p := posted(); len(p) != 2 {
			t.Fatalf("resume reposted: %+v", p)
		}
		time.Sleep(gap)
		synctest.Wait()
		if p := posted(); len(p) != 3 || p[2].to != groups[2] {
			t.Fatalf("resume didn't post the next copy: %+v", p)
		}

		// A reply to a copy from before the restart still queues the buyer.
		f.conn.emit(replyTo(buyerA, groups[1], secondCopy, "interested"))
		synctest.Wait()
		time.Sleep(time.Second)
		f.conn.emit(payment(buyerA, "pay-a"))
		synctest.Wait()
		if toA := sentTo(f.conn.messages(), buyerA); len(toA) != 2 || !toA[1].qr {
			t.Fatalf("buyer A got %+v", toA)
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}

		// A sold offer posts no more copies, and every copy gets a ✅.
		time.Sleep(3 * gap)
		synctest.Wait()
		if p := posted(); len(p) != 3 {
			t.Fatalf("copies after the sale: %+v", p)
		}
		ticked := map[string]bool{}
		for _, msg := range f.conn.messages() {
			if msg.reaction == soldReaction {
				ticked[msg.target] = true
			}
		}
		for _, p := range posted() {
			if !ticked[p.id] {
				t.Errorf("copy in %s was not ticked", p.to)
			}
		}
		recorded, _ := f.st.OfferPosts(second, f.date, "lunch")
		if len(recorded) != 3 {
			t.Fatalf("offer_posts has %d rows, want 3", len(recorded))
		}
	})
}
