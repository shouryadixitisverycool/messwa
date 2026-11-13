package main

import (
	"context"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"messwa/internal/store"
)

// poolMessages are the offer posts, edits and reactions sent to the group,
// by kind.
func poolMessages(f *sellFixture) (posts, edits, reactions []sentMessage) {
	for _, m := range sentTo(f.conn.messages(), f.group) {
		switch {
		case m.edited != "":
			edits = append(edits, m)
		case m.reaction != "":
			reactions = append(reactions, m)
		default:
			posts = append(posts, m)
		}
	}
	return
}

func TestPoolPostsOnceAndRoutesByFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		// The market says 45, so the bot holds at 45 within the pool's
		// range.
		m := f.negotiatingManager(ctx, 45)
		first, err := m.Start(ctx, 0, f.date, "lunch", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		second, err := m.Start(ctx, 0, f.date, "lunch#2", 40, 50, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		// One post, edited to show both meals at the average max price.
		posts, edits, _ := poolMessages(f)
		if len(posts) != 1 || posts[0].text != "sell bakul non-veg lunch 60" {
			t.Fatalf("posts = %+v", posts)
		}
		if len(edits) != 1 || edits[0].edited != posts[0].id || edits[0].text != "sell bakul non-veg lunch x2 55" {
			t.Fatalf("edits = %+v", edits)
		}
		if offers, _ := f.st.SellTaskOffers(ctx, second); len(offers) != 0 {
			t.Fatalf("the second task posted: %+v", offers)
		}

		// A buyer offers 45: under the first task's floor, within the
		// second's. The bot agrees, and the second task asks them to pay.
		f.conn.emit(f.reply(buyerA, "45?"))
		settle()
		toA := texts(sentTo(f.conn.messages(), buyerA))
		if len(toA) != 3 || toA[0] != "ok 45" {
			t.Fatalf("A got %q", toA)
		}
		if b := buyerState(t, f, first, buyerA); b.State != store.BuyerMoved {
			t.Fatalf("A in the first task = %+v", b)
		}
		if b := buyerState(t, f, second, buyerA); b.State != store.BuyerOffered || b.Amount != "45.00" {
			t.Fatalf("A in the second task = %+v", b)
		}
		f.conn.emit(paymentOf(buyerA, "pay-a", "45.00", time.Now()))
		synctest.Wait()
		if toA = texts(sentTo(f.conn.messages(), buyerA)); toA[len(toA)-1] != "[QR]" {
			t.Fatalf("A got %q", toA)
		}
		if task := f.task(second); task.State != store.TaskSold {
			t.Fatalf("second task is %s (%s)", task.State, task.Detail)
		}
		if task := f.task(first); task.State != store.TaskActive {
			t.Fatalf("first task is %s (%s)", task.State, task.Detail)
		}

		// One meal left: the post goes back to the first task's price, and
		// nothing is ticked yet.
		posts, edits, reactions := poolMessages(f)
		if len(posts) != 1 || len(reactions) != 0 || edits[len(edits)-1].text != "sell bakul non-veg lunch 60" {
			t.Fatalf("posts = %+v, edits = %+v, reactions = %+v", posts, edits, reactions)
		}

		// The first task sells at its own floor, and the post gets ✅.
		f.conn.emit(f.reply(buyerB, "50?"))
		settle()
		f.conn.emit(paymentOf(buyerB, "pay-b", "50.00", time.Now()))
		synctest.Wait()
		if task := f.task(first); task.State != store.TaskSold {
			t.Fatalf("first task is %s (%s)", task.State, task.Detail)
		}
		if d, _, _ := f.st.TaskDelivery(ctx, first); d == nil || d.Amount != "50.00" || d.Peer != buyerB.String() {
			t.Fatalf("first delivery = %+v", d)
		}
		_, _, reactions = poolMessages(f)
		if len(reactions) != 1 || reactions[0].reaction != soldReaction || reactions[0].target != posts[0].id {
			t.Fatalf("reactions = %+v", reactions)
		}
	})
}

func TestPoolPassesBuyersWhenTheLeaderSells(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 0)
		first, err := m.Start(ctx, 0, f.date, "lunch", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		second, err := m.Start(ctx, 0, f.date, "lunch#2", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		// B asks and hears the price; A pays it straight away.
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		f.conn.emit(f.reply(buyerA, "60"))
		settle()
		f.conn.emit(paymentOf(buyerA, "pay-a", "60.00", time.Now()))
		synctest.Wait()
		if task := f.task(first); task.State != store.TaskSold {
			t.Fatalf("first task is %s (%s)", task.State, task.Detail)
		}

		// B isn't told it sold: the second task takes over the haggle and
		// the post, which now offers one meal.
		toB := texts(sentTo(f.conn.messages(), buyerB))
		if len(toB) != 1 || toB[0] != "60" {
			t.Fatalf("B got %q", toB)
		}
		if b := buyerState(t, f, second, buyerB); b.State != store.BuyerNegotiating || b.BotMsgs != 1 {
			t.Fatalf("B in the second task = %+v", b)
		}
		posts, edits, reactions := poolMessages(f)
		if len(posts) != 1 || len(reactions) != 0 || edits[len(edits)-1].text != "sell bakul non-veg lunch 60" {
			t.Fatalf("posts = %+v, edits = %+v, reactions = %+v", posts, edits, reactions)
		}

		f.conn.emit(dm(buyerB, "ok 60"))
		settle()
		f.conn.emit(paymentOf(buyerB, "pay-b", "60.00", time.Now()))
		synctest.Wait()
		if toB = texts(sentTo(f.conn.messages(), buyerB)); toB[len(toB)-1] != "[QR]" {
			t.Fatalf("B got %q", toB)
		}
		if task := f.task(second); task.State != store.TaskSold {
			t.Fatalf("second task is %s (%s)", task.State, task.Detail)
		}
		if _, _, reactions = poolMessages(f); len(reactions) != 1 || reactions[0].target != posts[0].id {
			t.Fatalf("reactions = %+v", reactions)
		}
	})
}

func TestPoolPrices(t *testing.T) {
	task := func(lo, hi int64) store.SellTask { return store.SellTask{MinPaise: lo * 100, MaxPaise: hi * 100} }
	for _, c := range []struct {
		tasks     []store.SellTask
		floor, at int64
	}{
		{[]store.SellTask{task(50, 57)}, 50, 57},
		{[]store.SellTask{task(50, 60), task(40, 50)}, 40, 55},
		{[]store.SellTask{task(50, 55), task(50, 60)}, 50, 60}, // 57.5 rounds to 60
		{[]store.SellTask{task(60, 65), task(30, 35)}, 30, 50},
	} {
		floor, ask := poolPrices(c.tasks)
		if floor != c.floor*100 || ask != c.at*100 {
			t.Errorf("poolPrices(%+v) = %d, %d; want %d, %d", c.tasks, floor, ask, c.floor*100, c.at*100)
		}
	}
}

func TestPoolCancelledLeaderHandsOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 0)
		first, err := m.Start(ctx, 0, f.date, "lunch", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		second, err := m.Start(ctx, 0, f.date, "lunch#2", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "price?"))
		settle()

		// Cancelling with revoke keeps the post: the second task still
		// sells with it, and takes the buyer.
		if err := m.Cancel(ctx, first, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for _, msg := range sentTo(f.conn.messages(), f.group) {
			if msg.revoked != "" {
				t.Fatalf("the shared post was deleted: %+v", msg)
			}
		}
		if b := buyerState(t, f, second, buyerA); b.State != store.BuyerNegotiating {
			t.Fatalf("A in the second task = %+v", b)
		}
		// A reply to the first task's post now reaches the second task.
		f.conn.emit(f.reply(buyerB, "price?"))
		settle()
		if b := buyerState(t, f, second, buyerB); b.State != store.BuyerNegotiating {
			t.Fatalf("B in the second task = %+v", b)
		}
		if posts, _, _ := poolMessages(f); len(posts) != 1 {
			t.Fatalf("posts = %+v", posts)
		}

		// The last task cancelled with revoke deletes the post.
		if err := m.Cancel(ctx, second, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		revoked := 0
		for _, msg := range sentTo(f.conn.messages(), f.group) {
			if msg.revoked != "" {
				revoked++
			}
		}
		if revoked != 1 {
			t.Fatalf("revoked %d posts, want the one shared post", revoked)
		}
	})
}

func TestPoolResumesWithOnePost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		run, stop := context.WithCancel(t.Context())
		m := f.negotiatingManager(run, 0)
		first, err := m.Start(run, 0, f.date, "lunch", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if _, err := m.Start(run, 0, f.date, "lunch#2", 40, 50, ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		stop()
		synctest.Wait()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m2 := f.negotiatingManager(ctx, 0)
		if err := m2.Resume(ctx, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if posts, _, _ := poolMessages(f); len(posts) != 1 {
			t.Fatalf("posts after the restart = %+v", posts)
		}
		f.conn.emit(f.reply(buyerA, "price?"))
		settle()
		if b := buyerState(t, f, first, buyerA); b.State != store.BuyerNegotiating {
			t.Fatalf("A = %+v", b)
		}
		// The leader opens at the pool's average.
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 1 || got[0] != "55" {
			t.Fatalf("A got %q", got)
		}
	})
}

// A quote supported only by a cheaper task cannot sell a more expensive
// meal after that task leaves the pool.
func TestPoolQuoteDoesNotUndercutRemainingSeller(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 40)
		first, err := m.Start(ctx, 0, f.date, "lunch", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		second, err := m.Start(ctx, 0, f.date, "lunch#2", 30, 50, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "30?"))
		settle()
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) != 1 || got[0] != "how about 45" {
			t.Fatalf("A got %q", got)
		}
		if err := m.Cancel(ctx, second, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(dm(buyerA, "ok"))
		settle()
		if b := buyerState(t, f, first, buyerA); b.State != store.BuyerParked || b.Amount != "" {
			t.Fatalf("A = %+v", b)
		}
		if task := f.task(first); task.State != store.TaskActive {
			t.Fatalf("task = %+v", task)
		}
		if d, _, err := f.st.TaskDelivery(ctx, first); err != nil || d != nil {
			t.Fatalf("delivery = %+v, %v", d, err)
		}
	})
}

func TestPoolMarksPostsSoldWhenRemainingMealExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		// Lunch ends at 09:00 UTC. Start near the end so the last meal
		// expires shortly after the cheaper one sells.
		time.Sleep(8*time.Hour + 59*time.Minute)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 45)
		first, err := m.Start(ctx, 0, f.date, "lunch", 50, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		second, err := m.Start(ctx, 0, f.date, "lunch#2", 40, 50, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "45?"))
		settle()
		f.conn.emit(paymentOf(buyerA, "pay-a", "45.00", time.Now()))
		synctest.Wait()
		if task := f.task(second); task.State != store.TaskSold {
			t.Fatalf("second task = %+v", task)
		}
		if _, _, reactions := poolMessages(f); len(reactions) != 0 {
			t.Fatalf("the pool still has a meal: %+v", reactions)
		}
		time.Sleep(time.Until(f.task(first).EndsAt) + time.Second)
		synctest.Wait()
		if task := f.task(first); task.State != store.TaskExpired {
			t.Fatalf("first task = %+v", task)
		}
		posts, _, reactions := poolMessages(f)
		if len(posts) != 1 || len(reactions) != 1 || reactions[0].target != posts[0].id || reactions[0].reaction != soldReaction {
			t.Fatalf("posts = %+v, reactions = %+v", posts, reactions)
		}
	})
}
