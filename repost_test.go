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

// buryCopy logs the copy id as posted in group now, with n messages after
// it, as the group log would have them.
func buryCopy(t *testing.T, st *store.Store, group types.JID, id string, n int) {
	t.Helper()
	ctx := context.Background()
	at := time.Now()
	if _, err := st.AppendLog(ctx, store.LogEntry{Chat: group.String(), ID: id, Type: "post", Sender: "919000000000@s.whatsapp.net", TS: at, Text: "sell bakul lunch"}); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		st.AppendLog(ctx, store.LogEntry{Chat: group.String(), ID: fmt.Sprintf("%s-chat-%d", id, i), Type: "post", Sender: "918000000000@s.whatsapp.net", TS: at, Text: "anyone selling?"})
	}
}

func openTestStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// groupPosts are the offer copies sent to group, oldest first.
func groupPosts(f *fakeConn, group types.JID) []sentMessage {
	var out []sentMessage
	for _, m := range f.messages() {
		if m.to == group && m.reaction == "" && m.edited == "" {
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

		// Nothing happens in the group for ten minutes: the copy is still
		// in view, so it stays the only one.
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		p := groupPosts(f.conn, f.group)
		if len(p) != 1 {
			t.Fatalf("reposted in a quiet group: %+v", p)
		}
		// Ten messages bury it, so the offer goes up again there. The group
		// that refused it isn't tried again.
		buryCopy(t, f.st, f.group, p[0].id, 10)
		time.Sleep(repostCheck)
		synctest.Wait()
		p = groupPosts(f.conn, f.group)
		if len(p) != 2 || p[1].text != p[0].text || p[1].id == p[0].id {
			t.Fatalf("after ten messages: %+v", p)
		}
		if f.conn.tried[refused] != 1 {
			t.Fatalf("refused group tried %d times", f.conn.tried[refused])
		}

		// Ten messages bury the repost too, so the next one goes up once it
		// is five minutes old.
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
		if toA := sentTo(f.conn.messages(), buyerA); len(toA) != 4 || !toA[3].qr {
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
		time.Sleep(6 * time.Minute)
		synctest.Wait()
		buryCopy(t, f.st, f.group, groupPosts(f.conn, f.group)[0].id, 10)
		time.Sleep(repostCheck)
		synctest.Wait()
		p := groupPosts(f.conn, f.group)
		if len(p) != 2 {
			t.Fatalf("before the restart: %+v", p)
		}

		// run restarts: the repost isn't repeated, and the next one waits
		// five minutes after it, not after the restart.
		stopRun()
		synctest.Wait()
		second, cancel := context.WithCancel(t.Context())
		defer cancel()
		if err := f.manager(second).Resume(second, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		buryCopy(t, f.st, f.group, p[1].id, 10)
		time.Sleep(time.Until(started.Add(10 * time.Minute)))
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
		buryCopy(t, f.st, f.group, groupPosts(f.conn, f.group)[2].id, 10)
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
		// Start 30 minutes before lunch ends and bury every copy at once:
		// copies about 30, 25 and 20 minutes before the end, and none in
		// the last 15.
		slot, err := findSlot(f.cfg, f.date, "lunch")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(slot.End.Add(-30 * time.Minute)))
		id, err := f.manager(ctx).Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for range 3 {
			p := groupPosts(f.conn, f.group)
			buryCopy(t, f.st, f.group, p[len(p)-1].id, 10)
			time.Sleep(5*time.Minute + repostCheck)
			synctest.Wait()
		}
		time.Sleep(time.Until(slot.End.Add(time.Minute)))
		synctest.Wait()
		if p := groupPosts(f.conn, f.group); len(p) != 3 {
			t.Fatalf("copies = %d, want 3", len(p))
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
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		st := openTestStore(t, path)
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
			log: log, st: st, rules: repost.Rules{On: true, Messages: 10, Max: 1}, end: time.Now().Add(3 * time.Hour), gap: 15 * time.Second,
			groups: func(context.Context) ([]offerTarget, error) { return targets, nil },
			copies: posting.memoryCopies, send: send,
		})
		go waitForFirstReply(ctx, posting, firstReplyWait, cancel)
		if err := posting.waitPosted(); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		// A quiet group gets no repost however long the copy is up.
		time.Sleep(9 * time.Minute)
		synctest.Wait()
		if p := groupPosts(conn, group); len(p) != 1 {
			t.Fatalf("reposted in a quiet group: %+v", p)
		}
		// Ten messages bury it, so the next round reposts.
		buryCopy(t, st, group, groupPosts(conn, group)[0].id, 10)
		time.Sleep(repostCheck)
		synctest.Wait()
		p := groupPosts(conn, group)
		if len(p) != 2 || ctx.Err() != nil {
			t.Fatalf("after ten messages: %+v, %v", p, context.Cause(ctx))
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
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		st := openTestStore(t, path)
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
		rules := repost.Rules{Messages: 10, Max: 2}
		posting.keepInView(ctx, &reposter{
			log: log, st: st, end: time.Now().Add(3 * time.Hour), gap: 15 * time.Second,
			live:   func() repost.Rules { mu.Lock(); defer mu.Unlock(); return rules },
			groups: func(context.Context) ([]offerTarget, error) { return targets, nil },
			copies: posting.memoryCopies, send: send,
		})
		if err := posting.waitPosted(); err != nil {
			t.Fatal(err)
		}
		buryCopy(t, st, group, groupPosts(conn, group)[0].id, 10)
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
