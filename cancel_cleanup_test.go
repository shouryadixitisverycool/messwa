package main

import (
	"context"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/store"
)

func revokes(msgs []sentMessage) []string {
	var out []string
	for _, m := range msgs {
		if m.revoked != "" {
			out = append(out, m.revoked)
		}
	}
	return out
}

func (f *sellFixture) offers(id int64) []store.SellOffer {
	f.t.Helper()
	offers, err := f.st.SellTaskOffers(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return offers
}

func (f *sellFixture) events(id int64, kind string) []store.SellEvent {
	f.t.Helper()
	all, err := f.st.SellTaskEvents(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []store.SellEvent
	for _, e := range all {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestCancelRevokesPostsUnlessKept(t *testing.T) {
	for _, keep := range []bool{false, true} {
		name := "revoke"
		if keep {
			name = "keep_posts"
		}
		t.Run(name, func(t *testing.T) {
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
				post := f.offerID()
				if err := m.Cancel(ctx, id, !keep); err != nil {
					t.Fatal(err)
				}
				got := revokes(f.conn.messages())
				o := f.offers(id)[0]
				posts, _ := f.st.OfferPosts(ctx, f.date, "lunch")
				if keep {
					if len(got) != 0 || !o.RevokedAt.IsZero() || len(posts) != 1 {
						t.Fatalf("keep_posts: revokes=%v revoked_at=%v offer posts=%d", got, o.RevokedAt, len(posts))
					}
					return
				}
				if len(got) != 1 || got[0] != post {
					t.Fatalf("revokes = %v, want the post %s", got, post)
				}
				if o.RevokedAt.IsZero() || o.RevokeError != "" {
					t.Fatalf("copy after revoke = %+v", o)
				}
				// A later sale of the meal must not tick a deleted post.
				if len(posts) != 0 {
					t.Fatalf("revoked post is still among the meal's offer posts: %+v", posts)
				}
				if ev := f.events(id, store.EventRevoked); len(ev) != 1 || ev[0].Text != post {
					t.Fatalf("revoke events = %+v", ev)
				}
			})
		})
	}
}

func TestSoldAndExpiredTasksKeepTheirPosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		sold, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		expires, err := m.Start(ctx, 0, f.date, "dinner", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(replyTo(buyerA, f.group, f.offers(sold)[0].ID, "interested"))
		synctest.Wait()
		time.Sleep(time.Second)
		f.conn.emit(payment(buyerA, "pay-a"))
		synctest.Wait()
		if task := f.task(sold); task.State != store.TaskSold {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
		time.Sleep(time.Until(f.task(expires).EndsAt) + time.Second)
		synctest.Wait()
		if task := f.task(expires); task.State != store.TaskExpired {
			t.Fatalf("task is %s (%s)", task.State, task.Detail)
		}
		if got := revokes(f.conn.messages()); len(got) != 0 {
			t.Fatalf("a sold or expired task revoked %v", got)
		}
		// Cancelling an ended task is refused and revokes nothing.
		if err := m.Cancel(ctx, sold, true); err == nil {
			t.Fatal("cancelled a sold task")
		}
		if got := revokes(f.conn.messages()); len(got) != 0 {
			t.Fatalf("cancel of a sold task revoked %v", got)
		}
	})
}

// TestRevokeCopiesPicksWhatToDelete covers each kind of copy a cancelled
// task can leave: posted, uncertain, never sent, too old, and one whose
// revoke WhatsApp refuses.
func TestRevokeCopiesPicksWhatToDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		refused := types.NewJID("120363000000000009", types.GroupServer)
		f.conn.refuse = map[types.JID]bool{refused: true}
		now := time.Now()
		task := store.SellTask{Date: f.date, Meal: "lunch", MinPaise: 100, MaxPaise: 100, Mess: "bakul", Diet: "non-veg",
			CreatedAt: now.Add(-50 * time.Hour), EndsAt: now.Add(time.Hour)}
		id, err := f.st.CreateSellTask(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		task.ID = id
		g2 := types.NewJID("120363000000000002", types.GroupServer)
		g3 := types.NewJID("120363000000000003", types.GroupServer)
		claim := func(group types.JID, offerID string, seq int, at time.Time) {
			if err := f.st.ClaimOfferPost(ctx, id, group.String(), offerID, seq, at); err != nil {
				t.Fatal(err)
			}
		}
		claim(f.group, "old", 0, now.Add(-49*time.Hour))
		f.st.SetOfferState(ctx, id, "old", store.OfferPosted)
		claim(f.group, "posted", 1, now.Add(-time.Hour))
		f.st.SetOfferState(ctx, id, "posted", store.OfferPosted)
		claim(g2, "uncertain", 0, now.Add(-time.Minute)) // left posting
		claim(g3, "unsent", 0, now.Add(-time.Minute))
		f.st.SetOfferFailed(ctx, id, "unsent", errTaskInactive.Error())
		claim(refused, "refused", 0, now.Add(-time.Minute))
		f.st.SetOfferState(ctx, id, "refused", store.OfferPosted)
		if _, err := f.st.EndSellTask(ctx, id, store.TaskCancelled, "cancelled", now); err != nil {
			t.Fatal(err)
		}

		m.revokeCopies(task)
		got := revokes(f.conn.messages())
		if len(got) != 2 || got[0] != "posted" || got[1] != "uncertain" {
			t.Fatalf("revoked %v, want the posted and the uncertain copy", got)
		}
		state := map[string]store.SellOffer{}
		for _, o := range f.offers(id) {
			state[o.ID] = o
		}
		for _, want := range []struct {
			id      string
			revoked bool
			err     bool
		}{{"posted", true, false}, {"uncertain", true, false}, {"old", false, true}, {"refused", false, true}, {"unsent", false, false}} {
			o := state[want.id]
			if !o.RevokedAt.IsZero() != want.revoked || (o.RevokeError != "") != want.err {
				t.Errorf("%s: revoked_at=%v revoke_error=%q", want.id, o.RevokedAt, o.RevokeError)
			}
		}
		if n := len(f.events(id, store.EventRevokeFailed)); n != 2 {
			t.Errorf("%d revoke_failed events, want 2", n)
		}

		// Running it again sends nothing new for copies already deleted.
		m.revokeCopies(task)
		if again := revokes(f.conn.messages()); len(again) != 2 {
			t.Fatalf("second pass revoked %v", again)
		}
	})
}

// TestCancelLetsGroupPostFinish cancels while the first copy is still
// being sent. The send must finish rather than be cut off, and the post
// is then revoked.
func TestCancelLetsGroupPostFinish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		hold := make(chan struct{})
		f.conn.hold = hold
		id, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(f.conn.messages()) != 0 {
			t.Fatal("post went out while held")
		}
		cancelled := make(chan error, 1)
		go func() { cancelled <- m.Cancel(ctx, id, true) }()
		synctest.Wait()
		select {
		case err := <-cancelled:
			t.Fatalf("cancel returned while the post was in flight: %v", err)
		default:
		}
		close(hold)
		if err := <-cancelled; err != nil {
			t.Fatal(err)
		}
		msgs := f.conn.messages()
		if len(msgs) != 2 || msgs[0].text == "" || msgs[1].revoked != msgs[0].id {
			t.Fatalf("group messages = %+v, want the post then its revoke", msgs)
		}
		if o := f.offers(id)[0]; o.State != store.OfferPosted || o.RevokedAt.IsZero() {
			t.Fatalf("copy = %+v", o)
		}
	})
}

func TestReplyToCancelledPostGoesToMatchingTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		dead, err := m.Start(ctx, 0, f.date, "lunch#2", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		live, err := m.Start(ctx, 0, f.date, "lunch", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		// Same meal at another diet: never takes the reply.
		m.portalFor = func(context.Context, int64) (mealPortal, error) { return fakeMealPortal{mess: "bakul-veg"}, nil }
		other, err := m.Start(ctx, 0, f.date, "lunch#3", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		deadPost := f.offers(dead)[0].ID
		if err := m.Cancel(ctx, dead, false); err != nil {
			t.Fatal(err)
		}

		f.conn.emit(replyTo(buyerA, f.group, deadPost, "interested"))
		synctest.Wait()
		toA := sentTo(f.conn.messages(), buyerA)
		if len(toA) != 3 || toA[0].text != "ok 1" {
			t.Fatalf("buyer A got %+v, want the live task's prompt", toA)
		}
		if b, _ := f.st.SellTaskBuyers(ctx, live); len(b) != 1 || b[0].Peer != buyerA.String() {
			t.Fatalf("live task buyers = %+v", b)
		}
		for _, id := range []int64{dead, other} {
			if b, _ := f.st.SellTaskBuyers(ctx, id); len(b) != 0 {
				t.Fatalf("task %d took the buyer: %+v", id, b)
			}
		}
		if ev := f.events(live, store.EventAdopted); len(ev) != 1 {
			t.Fatalf("adopted events = %+v", ev)
		}

		// With no task left selling that meal, a reply gets nothing.
		if err := m.Cancel(ctx, live, true); err != nil {
			t.Fatal(err)
		}
		n := len(f.conn.messages())
		f.conn.emit(replyTo(buyerB, f.group, deadPost, "still there?"))
		synctest.Wait()
		if got := f.conn.messages()[n:]; len(got) != 0 {
			t.Fatalf("a reply with no matching task got %+v", got)
		}
		if b, _ := f.st.SellTaskBuyers(ctx, other); len(b) != 0 {
			t.Fatalf("the other-diet task took the buyer: %+v", b)
		}
	})
}

// TestResumedRunRoutesRepliesToCancelledPosts checks that a restart keeps
// routing replies to posts of tasks cancelled before it.
func TestResumedRunRoutesRepliesToCancelledPosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		first, stopRun := context.WithCancel(t.Context())
		m := f.manager(first)
		dead, err := m.Start(first, 0, f.date, "lunch#2", 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Start(first, 0, f.date, "lunch", 1, 1, ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		deadPost := f.offers(dead)[0].ID
		if err := m.Cancel(first, dead, false); err != nil {
			t.Fatal(err)
		}
		stopRun()
		synctest.Wait()

		second, cancel := context.WithCancel(t.Context())
		defer cancel()
		if err := f.manager(second).Resume(second, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(replyTo(buyerA, f.group, deadPost, "interested"))
		synctest.Wait()
		if toA := sentTo(f.conn.messages(), buyerA); len(toA) != 3 || toA[0].text != "ok 1" {
			t.Fatalf("buyer A got %+v", toA)
		}
	})
}

func TestSameMeal(t *testing.T) {
	base := store.SellTask{Date: "2026-11-12", Meal: "dinner", Mess: "kadamb", Diet: "non-veg"}
	for _, c := range []struct {
		name string
		edit func(*store.SellTask)
		want bool
	}{
		{"slot suffix", func(t *store.SellTask) { t.Meal = "dinner#3" }, true},
		{"other meal", func(t *store.SellTask) { t.Meal = "lunch" }, false},
		{"other mess", func(t *store.SellTask) { t.Mess = "yuktahar" }, false},
		{"other diet", func(t *store.SellTask) { t.Diet = "veg" }, false},
		{"other day", func(t *store.SellTask) { t.Date = "2026-11-13" }, false},
	} {
		other := base
		c.edit(&other)
		if got := sameMeal(base, other); got != c.want {
			t.Errorf("%s: sameMeal = %v", c.name, got)
		}
	}
}
