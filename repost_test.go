package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/repost"
	"messwa/internal/store"
)

// groupPosts are the offer copies sent to group, oldest first.
func groupPosts(f *fakeConn, group types.JID) []sentMessage {
	var out []sentMessage
	for _, m := range f.messages() {
		if m.to == group && m.reaction == "" {
			out = append(out, m)
		}
	}
	return out
}

func TestSellTaskRepostsUntilSold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		refused := types.NewJID("120363000000000002", types.GroupServer)
		f.cfg.Groups = []string{f.group.String(), refused.String()}
		f.conn.groups = append(f.conn.groups, &types.GroupInfo{JID: refused, GroupName: types.GroupName{Name: "Mess 2"}})
		f.conn.refuse = map[types.JID]bool{refused: true}
		f.cfg.Sell.GroupCount = 2
		f.cfg.Sell.ShowPrice = false
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		id, err := f.manager(ctx).Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(f.cfg.Sell.PostGap.Duration)
		synctest.Wait()
		if p := groupPosts(f.conn, f.group); len(p) != 1 || f.conn.tried[refused] != 1 {
			t.Fatalf("first round: %+v, refused group tried %d times", p, f.conn.tried[refused])
		}

		// Nothing happens in the group for ten minutes, so the offer goes
		// up again there. The group that refused it isn't tried again.
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		p := groupPosts(f.conn, f.group)
		if len(p) != 2 || p[1].text != p[0].text || p[1].id == p[0].id {
			t.Fatalf("after ten minutes: %+v", p)
		}
		if f.conn.tried[refused] != 1 {
			t.Fatalf("refused group tried %d times", f.conn.tried[refused])
		}

		// Ten messages bury the repost, so the next one goes up once it is
		// five minutes old rather than ten.
		offers, _ := f.st.SellTaskOffers(ctx, id)
		repostAt := offers[len(offers)-1].PostedAt
		if _, err := f.st.AppendLog(ctx, store.LogEntry{Chat: f.group.String(), ID: p[1].id, Type: "post", Sender: "919000000000@s.whatsapp.net", TS: repostAt, Text: p[1].text}); err != nil {
			t.Fatal(err)
		}
		for i := range 10 {
			f.st.AppendLog(ctx, store.LogEntry{Chat: f.group.String(), ID: fmt.Sprintf("chat-%d", i), Type: "post", Sender: "918000000000@s.whatsapp.net", TS: repostAt.Add(time.Minute), Text: "anyone selling bakul?"})
		}
		time.Sleep(time.Until(repostAt.Add(5*time.Minute + repostCheck)))
		synctest.Wait()
		if p = groupPosts(f.conn, f.group); len(p) != 3 {
			t.Fatalf("buried repost wasn't reposted: %+v", p)
		}

		// A reply to the first copy still counts.
		f.conn.emit(replyTo(buyerA, f.group, p[0].id, "interested"))
		synctest.Wait()
		time.Sleep(time.Second)
		f.conn.emit(payment(buyerA, "pay-a"))
		synctest.Wait()
		if toA := sentTo(f.conn.messages(), buyerA); len(toA) != 3 || !toA[2].qr {
			t.Fatalf("buyer A got %+v", toA)
		}
		if task := f.task(id); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}

		// Every copy, reposts included, gets a ✅, and none follow the sale.
		time.Sleep(30 * time.Minute)
		synctest.Wait()
		if p = groupPosts(f.conn, f.group); len(p) != 3 {
			t.Fatalf("copies after the sale: %+v", p)
		}
		ticked := map[string]bool{}
		for _, m := range f.conn.messages() {
			if m.reaction == soldReaction {
				ticked[m.target] = true
			}
		}
		for _, c := range p {
			if !ticked[c.id] {
				t.Errorf("copy %s was not ticked", c.id)
			}
		}
		offers, _ = f.st.SellTaskOffers(ctx, id)
		states := map[string]string{}
		for _, o := range offers {
			states[fmt.Sprintf("%s/%d", o.Group, o.Seq)] = o.State
		}
		want := map[string]string{f.group.String() + "/0": store.OfferPosted, f.group.String() + "/1": store.OfferPosted,
			f.group.String() + "/2": store.OfferPosted, refused.String() + "/0": store.OfferFailed}
		if fmt.Sprint(states) != fmt.Sprint(want) {
			t.Fatalf("recorded copies = %v, want %v", states, want)
		}
	})
}

func TestSellTaskRepostsResumeAndCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.MaxReposts = 2
		first, stopRun := context.WithCancel(t.Context())
		id, err := f.manager(first).Start(first, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		time.Sleep(10*time.Minute + 30*time.Second)
		synctest.Wait()
		if p := groupPosts(f.conn, f.group); len(p) != 2 {
			t.Fatalf("before the restart: %+v", p)
		}

		// run restarts: the repost isn't repeated, and the next one is due
		// ten minutes after it, not after the restart.
		stopRun()
		synctest.Wait()
		second, cancel := context.WithCancel(t.Context())
		defer cancel()
		if err := f.manager(second).Resume(second, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Until(started.Add(19 * time.Minute)))
		synctest.Wait()
		if p := groupPosts(f.conn, f.group); len(p) != 2 {
			t.Fatalf("resume reposted early: %+v", p)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if p := groupPosts(f.conn, f.group); len(p) != 3 {
			t.Fatalf("resumed task didn't repost: %+v", p)
		}

		// max_reposts caps the group at two fresh copies.
		time.Sleep(time.Hour)
		synctest.Wait()
		if p := groupPosts(f.conn, f.group); len(p) != 3 {
			t.Fatalf("reposted past max_reposts: %+v", p)
		}
		offers, _ := f.st.SellTaskOffers(second, id)
		if len(offers) != 3 || offers[2].Seq != 2 {
			t.Fatalf("recorded copies = %+v", offers)
		}
	})
}

func TestSellTaskStopsRepostingNearTheMealsEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.MaxReposts = 12
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		// Start 50 minutes before lunch ends: copies at 0, 10, 20 and 30
		// minutes, and none in the last 15.
		slot, err := findSlot(f.cfg, f.date, "lunch")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(slot.End.Add(-50 * time.Minute)))
		id, err := f.manager(ctx).Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(slot.End.Add(time.Minute)))
		synctest.Wait()
		if p := groupPosts(f.conn, f.group); len(p) != 4 {
			t.Fatalf("copies = %d, want 4", len(p))
		}
		if task := f.task(id); task.State != store.TaskExpired {
			t.Fatalf("task is %s", task.State)
		}
	})
}

func TestSellTaskRepostsSwitchedOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.Repost = false
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if _, err := f.manager(ctx).Start(ctx, 0, f.date, "lunch", 1, 1, ""); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if p := groupPosts(f.conn, f.group); len(p) != 1 {
			t.Fatalf("reposted with repost = false: %+v", p)
		}
	})
}

// A task keeps sends for its buyers: reposts stop once they would leave
// fewer than repostReserve.
func TestRepostsKeepSendsForBuyers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.MaxReposts = 12
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		m.limits.mu.Lock()
		m.limits.totals[id] = taskSendCap - repostReserve
		m.limits.mu.Unlock()
		time.Sleep(time.Hour)
		synctest.Wait()
		if p := groupPosts(f.conn, f.group); len(p) != 1 {
			t.Fatalf("reposted into the buyers' reserve: %+v", p)
		}
	})
}

// deliver-qr reposts from the copies it sent this run, and gives up on a
// first reply ten minutes after the latest copy.
func TestRepostsFromMemoryAndFirstReplyWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		group := types.NewJID("120363000000000001", types.GroupServer)
		conn := newFakeConn(group)
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		targets := []offerTarget{{JID: group, Name: "Mess sell"}}
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		posting := watchGroupOffer(ctx, conn, newOfferQueue())
		defer posting.close()
		send := directCopy(conn, conn.send, "sell bakul lunch")
		posting.post(ctx, log, targets, 0, 15*time.Second, send, nil)
		posting.keepInView(ctx, &reposter{
			log: log, rules: repost.Rules{On: true, After: 10 * time.Minute, Messages: 10, Max: 1}, end: time.Now().Add(3 * time.Hour), gap: 15 * time.Second,
			groups: func(context.Context) ([]offerTarget, error) { return targets, nil },
			copies: posting.memoryCopies, send: send,
		})
		go waitForFirstReply(ctx, posting, firstReplyWait, cancel)
		if err := posting.waitPosted(); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		time.Sleep(10*time.Minute + repostCheck)
		synctest.Wait()
		p := groupPosts(conn, group)
		if len(p) != 2 || ctx.Err() != nil {
			t.Fatalf("after ten minutes: %+v, %v", p, context.Cause(ctx))
		}
		// A reply to the first copy still matches.
		if !posting.matches(replyTo(buyerA, group, p[0].id, "interested")) {
			t.Fatal("reply to the first copy didn't match")
		}
		time.Sleep(time.Until(start.Add(19 * time.Minute)))
		synctest.Wait()
		if ctx.Err() != nil {
			t.Fatal("gave up within ten minutes of the repost")
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if context.Cause(ctx) != errNoFirstReply {
			t.Fatalf("still waiting 10 minutes after the last copy: %v", context.Cause(ctx))
		}
		if p := groupPosts(conn, group); len(p) != 2 {
			t.Fatalf("reposted past max_reposts: %+v", p)
		}
	})
}

// A sell task reads its repost rules every round, so reposting switched on
// from the settings page reaches an offer that is already up.
func TestRepostFollowsLiveRules(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		group := types.NewJID("120363000000000001", types.GroupServer)
		conn := newFakeConn(group)
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		targets := []offerTarget{{JID: group, Name: "Mess sell"}}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		posting := watchGroupOffer(ctx, conn, newOfferQueue())
		defer posting.close()
		send := directCopy(conn, conn.send, "sell bakul lunch")
		posting.post(ctx, log, targets, 0, 15*time.Second, send, nil)
		var mu sync.Mutex
		rules := repost.Rules{After: 10 * time.Minute, Max: 2}
		posting.keepInView(ctx, &reposter{
			log: log, end: time.Now().Add(3 * time.Hour), gap: 15 * time.Second,
			live:   func() repost.Rules { mu.Lock(); defer mu.Unlock(); return rules },
			groups: func(context.Context) ([]offerTarget, error) { return targets, nil },
			copies: posting.memoryCopies, send: send,
		})
		if err := posting.waitPosted(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Minute)
		synctest.Wait()
		if p := groupPosts(conn, group); len(p) != 1 {
			t.Fatalf("reposted while reposting was off: %+v", p)
		}
		mu.Lock()
		rules.On = true
		mu.Unlock()
		time.Sleep(repostCheck)
		synctest.Wait()
		if p := groupPosts(conn, group); len(p) != 2 {
			t.Fatalf("no repost after turning reposting on: %+v", p)
		}
	})
}
