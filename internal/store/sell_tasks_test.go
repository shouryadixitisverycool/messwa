package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestSellTaskClaims(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "data.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	task := SellTask{Date: "2026-11-11", Meal: "lunch", MinPaise: 3000, MaxPaise: 6000, CreatedAt: now, EndsAt: now.Add(time.Hour)}
	if _, err := s.CreateSellTask(ctx, SellTask{Date: "2026-11-11", Meal: "lunch", MinPaise: 6000, MaxPaise: 3000}); err == nil {
		t.Fatal("task with max below min was created")
	}
	id, err := s.CreateSellTask(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSellTask(ctx, task); err == nil {
		t.Fatal("second active task for one meal")
	}

	if err := s.ClaimOfferPost(ctx, id, "g@g.us", "offer", 0, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimOfferPost(ctx, id, "g@g.us", "offer2", 0, now); err == nil {
		t.Fatal("group posted twice")
	}
	if err := s.ClaimOfferPost(ctx, id, "g@g.us", "repost", 2, now); err == nil {
		t.Fatal("repost skipped a copy number")
	}
	if ok, _ := s.OfferClaimed(ctx, id, "g@g.us", "offer"); !ok {
		t.Fatal("claimed post not sendable")
	}
	s.SetOfferState(ctx, id, "offer", OfferPosted)
	if ok, _ := s.OfferClaimed(ctx, id, "g@g.us", "offer"); ok {
		t.Fatal("posted offer could be sent again")
	}
	// A repost is the next copy number, claimed once.
	if err := s.ClaimOfferPost(ctx, id, "g@g.us", "repost", 1, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimOfferPost(ctx, id, "g@g.us", "repost-again", 1, now.Add(10*time.Minute)); err == nil {
		t.Fatal("repost claimed twice")
	}
	if ok, _ := s.OfferClaimed(ctx, id, "g@g.us", "repost"); !ok {
		t.Fatal("claimed repost not sendable")
	}
	if offers, _ := s.SellTaskOffers(ctx, id); len(offers) != 2 || offers[0].Seq != 0 || offers[1].Seq != 1 || offers[1].ID != "repost" {
		t.Fatalf("offers = %+v", offers)
	}

	if ok, _ := s.AddSellTaskBuyer(ctx, id, "1@s.whatsapp.net", "9@lid", now); !ok {
		t.Fatal("buyer not added")
	}
	if ok, _ := s.AddSellTaskBuyer(ctx, id, "1@s.whatsapp.net", "", now); ok {
		t.Fatal("buyer added twice")
	}
	if ok, _ := s.IsSellTaskBuyer(ctx, id, "9@lid"); !ok {
		t.Fatal("buyer's LID chat not recognised")
	}
	if ok, _ := s.IsSellTaskBuyer(ctx, id, "2@s.whatsapp.net"); ok {
		t.Fatal("stranger counted as a buyer")
	}
	if ok, _ := s.MoveSellTaskBuyer(ctx, id, "1@s.whatsapp.net", BuyerQueued, BuyerOffered, "60.00", now); !ok {
		t.Fatal("prompt not claimed")
	}
	if ok, _ := s.MoveSellTaskBuyer(ctx, id, "1@s.whatsapp.net", BuyerQueued, BuyerOffered, "60.00", now); ok {
		t.Fatal("prompt claimed twice")
	}

	d := Delivery{Date: "2026-11-11", Meal: "lunch", Peer: "1@s.whatsapp.net", Amount: "60.00", ImageHash: "h", ArmedAt: now}
	if _, err := s.ArmTaskDelivery(ctx, id, d); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// After a restart the claims hold.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, state, err := s.TaskDelivery(ctx, id); err != nil || got == nil || state != "armed" || got.Amount != "60.00" {
		t.Fatalf("delivery after restart = %+v %q %v", got, state, err)
	}
	if buyers, _ := s.SellTaskBuyers(ctx, id); len(buyers) != 1 || buyers[0].State != BuyerOffered || buyers[0].Alt != "9@lid" {
		t.Fatalf("buyers after restart = %+v", buyers)
	}
	if ok, _ := s.EndSellTask(ctx, id, TaskCancelled, "cancelled", now); !ok {
		t.Fatal("task not ended")
	}
	if ok, _ := s.AddSellTaskBuyer(ctx, id, "3@s.whatsapp.net", "", now); ok {
		t.Fatal("ended task took a new buyer")
	}
	if err := s.ClaimOfferPost(ctx, id, "h@g.us", "offer3", 0, now); err == nil {
		t.Fatal("ended task claimed a post")
	}
	if ok, _ := s.EndSellTask(ctx, id, TaskSold, "", now); ok {
		t.Fatal("ended task ended again")
	}
	// The meal still has its delivery record, so it can't be put up again.
	if _, err := s.CreateSellTask(ctx, task); err == nil {
		t.Fatal("new task for a meal with a delivery record")
	}
}

func TestPaise(t *testing.T) {
	for in, want := range map[string]int64{"60.00": 6000, "0.50": 50, "1.05": 105} {
		if got, err := ParsePaise(in); err != nil || got != want {
			t.Errorf("ParsePaise(%q) = %d, %v", in, got, err)
		}
		if FormatPaise(want) != in {
			t.Errorf("FormatPaise(%d) = %q", want, FormatPaise(want))
		}
	}
	for _, bad := range []string{"60", "60.0", "-1.00", "a.00", ".00"} {
		if _, err := ParsePaise(bad); err == nil {
			t.Errorf("ParsePaise(%q) accepted", bad)
		}
	}
}

// A data file from before reposts keeps its copies, as copy 0, and takes
// reposts afterwards.
func TestSellOffersMigrate(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE sell_task_offers (task_id INTEGER NOT NULL, group_jid TEXT NOT NULL, offer_id TEXT NOT NULL, state TEXT NOT NULL, posted_at INTEGER NOT NULL, PRIMARY KEY(task_id, group_jid))`,
		`INSERT INTO sell_task_offers VALUES (1,'b@g.us','second','failed',200), (1,'a@g.us','first','posted',100)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	offers, err := s.SellTaskOffers(ctx, 1)
	if err != nil || len(offers) != 2 || offers[0].ID != "first" || offers[1].ID != "second" || offers[1].Seq != 0 || offers[1].State != OfferFailed {
		t.Fatalf("offers after migrating = %+v, %v", offers, err)
	}
	now := time.Now()
	id, err := s.CreateSellTask(ctx, SellTask{Date: "2026-11-11", Meal: "lunch", MinPaise: 100, MaxPaise: 100, CreatedAt: now, EndsAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for seq, offer := range []string{"one", "two"} {
		if err := s.ClaimOfferPost(ctx, id, "c@g.us", offer, seq, now); err != nil {
			t.Fatalf("copy %d: %v", seq, err)
		}
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatalf("reopening a migrated file: %v", err)
	}
}

func TestAfterPost(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const chat, me = "g@g.us", "me@s.whatsapp.net"
	at := time.Unix(1794467890, 0)
	n := 0
	post := func(sender, mess, diet string, ts time.Time, text string) string {
		t.Helper()
		n++
		id := fmt.Sprintf("m%d", n)
		if _, err := s.AppendLog(ctx, LogEntry{Chat: chat, ID: id, Type: "post", Sender: sender, TS: ts, Text: text}); err != nil {
			t.Fatal(err)
		}
		if mess != "" {
			l := Listing{Seller: sender, Kind: "sell", Meal: "lunch", MealDate: "2026-11-12", Mess: mess, Diet: diet, Text: text, FirstSeen: ts, LastSeen: ts}
			if err := s.CreateListing(ctx, &l); err != nil {
				t.Fatal(err)
			}
			if err := s.AddPost(ctx, Post{Chat: chat, ID: id, ListingID: l.ID, Sender: sender, TS: ts, Text: text}); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	if _, known, err := s.After(ctx, chat, "missing"); known || err != nil {
		t.Fatalf("a post not in the log was known: %v", err)
	}
	post("1@s.whatsapp.net", "kadamba", "", at.Add(-time.Minute), "sell kadamba lunch")
	// A post from the same second that synced before ours counts as before it.
	post("2@s.whatsapp.net", "", "", at, "anyone selling?")
	ours := post(me, "kadamba", "veg", at, "sell kadamba veg lunch")
	post("3@s.whatsapp.net", "", "", at, "hi")
	post("4@s.whatsapp.net", "kadamba", "", at.Add(time.Minute), "kadamba lunch 40")
	post("5@s.whatsapp.net", "kadamba", "nonveg", at.Add(time.Minute), "sell kadamba nv lunch")
	post("6@s.whatsapp.net", "bakul", "", at.Add(time.Minute), "sell bakul lunch")
	post(me, "kadamba", "veg", at.Add(2*time.Minute), "sell kadamba veg lunch again")
	post("4@s.whatsapp.net", "kadamba", "", at.Add(3*time.Minute), "kadamba lunch 35")
	// History that syncs late lands by its time, not its row.
	post("7@s.whatsapp.net", "kadamba", "veg", at.Add(-2*time.Minute), "late sync kadamba")
	got, known, err := s.After(ctx, chat, ours)
	if err != nil || !known {
		t.Fatalf("After = %v %v", known, err)
	}
	// Messages: hi, kadamba 40, nv, bakul, ours again, kadamba 35.
	// Listings: only 4's kadamba listing; the non-veg one and ours don't.
	if got.Messages != 6 || got.Listings != 1 {
		t.Fatalf("After = %+v, want 6 messages and 1 listing", got)
	}
}

func TestClaimBuyerLine(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "data.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	id, err := s.CreateSellTask(ctx, SellTask{Date: "2026-11-12", Meal: "lunch", MinPaise: 2000, MaxPaise: 3000, CreatedAt: now, EndsAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	const buyer = "1@s.whatsapp.net"
	if ok, _ := s.ClaimBuyerLine(ctx, id, buyer, "pay_other"); ok {
		t.Fatal("line claimed for someone who isn't a buyer")
	}
	s.AddSellTaskBuyer(ctx, id, buyer, "", now)
	// A buyer mid-turn can still hear a line.
	s.MoveSellTaskBuyer(ctx, id, buyer, BuyerQueued, BuyerOffered, "30.00", now)
	for _, kind := range []string{"pay_other", "screenshot"} {
		if ok, err := s.ClaimBuyerLine(ctx, id, buyer, kind); !ok || err != nil {
			t.Fatalf("%s not claimed: %v", kind, err)
		}
	}
	// Each is said once, also after a restart.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, kind := range []string{"pay_other", "screenshot"} {
		if ok, _ := s.ClaimBuyerLine(ctx, id, buyer, kind); ok {
			t.Fatalf("%s claimed twice", kind)
		}
	}
	if ok, _ := s.ClaimBuyerLine(ctx, id, buyer, "pay"); !ok {
		t.Fatal("a kind that prefixes a claimed one counted as claimed")
	}
	if _, err := s.ClaimBuyerLine(ctx, id, buyer, "a,b"); err == nil {
		t.Fatal("kind with a comma accepted")
	}
}
