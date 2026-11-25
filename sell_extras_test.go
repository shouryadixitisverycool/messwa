package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"messwa/internal/portal"
	"messwa/internal/store"
)

type fakeExtraPortal struct {
	fakeMealPortal
	extras []portal.ExtraRegistration
}

func (p *fakeExtraPortal) ExtraRegistrations(context.Context, string, string) ([]portal.ExtraRegistration, error) {
	return p.extras, nil
}

func (p *fakeExtraPortal) RequireExtra(_ context.Context, date, meal, extraName string, need int) (portal.ExtraRegistration, int, error) {
	want := portal.NormalizeExtraName(extraName)
	var first portal.ExtraRegistration
	total, unavailed := 0, 0
	for _, r := range p.extras {
		if !strings.EqualFold(portal.NormalizeExtraName(r.Name), want) {
			continue
		}
		total++
		if r.Availed() {
			continue
		}
		if unavailed == 0 {
			first = r
		}
		unavailed++
	}
	if total == 0 {
		return portal.ExtraRegistration{}, 0, errors.New("no extra registered")
	}
	if unavailed < need {
		return portal.ExtraRegistration{}, unavailed, errors.New("not enough unavailed extras")
	}
	return first, unavailed, nil
}

func (p *fakeExtraPortal) QRForExtra(ctx context.Context, date, meal, extraName string, need int, accountHash string) (portal.CurrentQR, int, error) {
	row, unavailed, err := p.RequireExtra(ctx, date, meal, extraName, need)
	if err != nil {
		return portal.CurrentQR{}, unavailed, err
	}
	if accountHash != "" && accountHash != "account" {
		return portal.CurrentQR{}, unavailed, errors.New("portal account changed")
	}
	mess, _ := (portal.Registration{Mess: row.Mess}).MessDiet()
	return portal.CurrentQR{PNG: placeholderQR, AccountHash: "account", Registration: portal.Registration{Date: date, Meal: meal, Mess: mess, Category: "extra"}}, unavailed, nil
}

func TestStartExtrasVerifiesCountsPoolsAndExcludesFromMainMeal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.MentionExtras = true
		used := "2026-11-11T12:40:00Z"
		ep := &fakeExtraPortal{
			fakeMealPortal: fakeMealPortal{mess: "kadamba-nonveg"},
			extras: []portal.ExtraRegistration{
				{ID: "e1", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
				{ID: "e2", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
				{ID: "e3", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500", AvailedAt: &used},
			},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.negotiatingManager(ctx, 0)
		m.portalFor = func(context.Context, int64) (mealPortal, error) { return ep, nil }

		// Asking for 3 eggs when only 2 are unavailed must be rejected.
		if _, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 3, 10, 15, time.Time{}, true, false); err == nil {
			t.Fatal("expected oversell of 3 eggs (2 unavailed) to fail")
		}

		// Sell 2 eggs separately: verified against portal, pooled into ONE group post!
		res, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 2, 10, 15, time.Time{}, true, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Count != 2 || res.VerifiedTotal != 3 || res.VerifiedUnavailed != 2 || res.VerifiedAvailable != 0 || len(res.IDs) != 2 {
			t.Fatalf("res = %+v", res)
		}
		synctest.Wait()

		// Trying to sell 1 more egg must now fail because 0 remain available.
		if _, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 1, 10, 15, time.Time{}, true, false); err == nil {
			t.Fatal("expected selling a 3rd egg after allocating both unavailed eggs to fail")
		}

		posts, edits, _ := poolMessages(f)
		latestText := ""
		if len(edits) > 0 {
			latestText = edits[len(edits)-1].text
		} else if len(posts) > 0 {
			latestText = posts[0].text
		}
		if len(posts) != 1 || latestText != "sell kadamba lunch egg (extra only) x2 15" {
			t.Fatalf("egg posts=%+v edits=%+v", posts, edits)
		}

		// Direct Start on an extra slot without StartExtras/schedule must be rejected.
		if _, err := m.Start(ctx, 0, f.date, "lunch#e9", 10, 15, ""); err == nil {
			t.Fatal("expected Start on bare extra slot lunch#e9 to fail")
		}

		// Now sell the main Kadamba non-veg meal with mention_extras=true:
		// because both unavailed eggs are already allocated to separate slots,
		// the main meal post must NOT advertise "+ extras: Egg" and must NOT
		// pool with the eggs!
		mealID, err := m.Start(ctx, 0, f.date, "lunch", 40, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		posts, _, _ = poolMessages(f)
		if len(posts) != 2 || posts[1].text != "sell kadamba non-veg lunch 60" {
			t.Fatalf("main meal post = %+v (task %d)", posts, mealID)
		}
	})
}

func TestStartExtrasFixedPricePoolsAndSellsBoth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ep := &fakeExtraPortal{
			fakeMealPortal: fakeMealPortal{mess: "kadamba-nonveg"},
			extras: []portal.ExtraRegistration{
				{ID: "e1", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
				{ID: "e2", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
			},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		m.portalFor = func(context.Context, int64) (mealPortal, error) { return ep, nil }

		res, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 2, 15, 15, time.Time{}, true, false)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		posts, edits, reactions := poolMessages(f)
		latestText := ""
		if len(edits) > 0 {
			latestText = edits[len(edits)-1].text
		} else if len(posts) > 0 {
			latestText = posts[0].text
		}
		if len(posts) != 1 || latestText != "sell kadamba lunch egg (extra only) x2 15" {
			t.Fatalf("fixed pool posts=%+v edits=%+v", posts, edits)
		}

		// Two buyers reply: A pays for egg #1, then B is passed to egg #2 and pays for it.
		f.conn.emit(f.reply(buyerA, "egg?"))
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "need egg"))
		synctest.Wait()
		time.Sleep(2 * time.Second)
		f.conn.emit(paymentOf(buyerA, "pay-egg-a", "15.00", time.Now()))
		synctest.Wait()
		if task := f.task(res.IDs[0]); task.State != store.TaskSold {
			t.Fatalf("egg 1 state = %s (%s)", task.State, task.Detail)
		}
		time.Sleep(2 * time.Second)
		f.conn.emit(paymentOf(buyerB, "pay-egg-b", "15.00", time.Now()))
		synctest.Wait()
		if task := f.task(res.IDs[1]); task.State != store.TaskSold {
			t.Fatalf("egg 2 state = %s (%s)", task.State, task.Detail)
		}
		_, _, reactions = poolMessages(f)
		if len(reactions) != 1 || reactions[0].reaction != soldReaction {
			t.Fatalf("reactions = %+v", reactions)
		}
	})
}

func TestExtraPlainDMAndBuyPostIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, filepath.Join(t.TempDir(), "data.db"))
		eggTopic := newOfferTopicForExtra(f.cfg, "kadamba", "", "dinner", "Egg", "")
		mealTopic := newOfferTopicForExtra(f.cfg, "kadamba", "nonveg", "dinner", "", "")

		if eggTopic.asksAbout("price?") {
			t.Fatal("egg extra task must not match generic plain DM")
		}
		if eggTopic.asksAbout("kadamba dinner available?") {
			t.Fatal("egg extra task must not match generic kadamba dinner DM")
		}
		if !eggTopic.asksAbout("kadamba egg?") || !eggTopic.asksAbout("2 eggs available?") {
			t.Fatal("egg extra task should match DMs mentioning egg/eggs")
		}
		if eggTopic.asksAbout("hey I really love eating boiled eggs at home every single morning") {
			t.Fatal("egg extra task must not match casual non-buying DM mentioning eggs")
		}
		if eggTopic.asksAbout("I ate eggs at Kadamba yesterday") {
			t.Fatal("egg extra task must not match non-buying Kadamba DM mentioning eggs")
		}
		if eggTopic.asksAbout("kadamba dinner no eggs available?") {
			t.Fatal("egg extra task must not match negated extra DM")
		}
		if eggTopic.asksAbout("kadamba nonveg dinner with egg available?") {
			t.Fatal("egg extra task must not match explicit nonveg dinner DM")
		}
		if mealTopic.asksAbout("kadamba egg?") {
			t.Fatal("nonveg dinner task must not hijack bare egg DM")
		}
		if !mealTopic.asksAbout("egg dinner available?") {
			t.Fatal("regular dinner task should still accept DMs naming the meal")
		}
		if mealTopic.asksAbout("Can I buy only the egg for Kadamba dinner?") {
			t.Fatal("regular dinner task must not match explicit egg-only DM")
		}
		if !eggTopic.asksAbout("Can I buy only the egg for Kadamba dinner?") {
			t.Fatal("egg extra task should match explicit egg-only DM")
		}

		// Real portal extra names like "Omelette Full" must match "omelette?" and "egg available?".
		omTopic := newOfferTopicForExtra(f.cfg, "kadamba", "", "dinner", "Omelette Full", "")
		if !omTopic.asksAbout("omelette?") || !omTopic.asksAbout("egg available?") {
			t.Fatal("Omelette Full task should match omelette/egg asks")
		}

		genericBuy := store.Listing{MealDate: f.date, Mess: "kadamba", Text: "buy kadamba dinner"}
		eggBuy := store.Listing{MealDate: f.date, Mess: "kadamba", Diet: "egg", Text: "buy kadamba dinner egg"}
		noEggBuy := store.Listing{MealDate: f.date, Mess: "kadamba", Text: "buy kadamba dinner no eggs"}
		nonvegWithEggBuy := store.Listing{MealDate: f.date, Mess: "kadamba", Diet: "nonveg", Text: "buy kadamba nonveg dinner with egg"}
		extraOnlyBuy := store.Listing{MealDate: f.date, Mess: "kadamba", Diet: "extra", Text: "buy kadamba dinner egg only"}
		if buyPostMatchesForExtra(genericBuy, f.date, "kadamba", "", "Egg") {
			t.Fatal("egg task must not match generic kadamba dinner buy post")
		}
		if buyPostMatchesForExtra(noEggBuy, f.date, "kadamba", "", "Egg") {
			t.Fatal("egg task must not match buy post saying no eggs")
		}
		if buyPostMatchesForExtra(nonvegWithEggBuy, f.date, "kadamba", "", "Egg") {
			t.Fatal("egg task must not match nonveg dinner buy post")
		}
		if !buyPostMatchesForExtra(eggBuy, f.date, "kadamba", "", "Egg") || !buyPostMatchesForExtra(extraOnlyBuy, f.date, "kadamba", "", "Egg") {
			t.Fatal("egg task should match buy posts asking for egg")
		}
		if buyPostMatchesForExtra(eggBuy, f.date, "kadamba", "nonveg", "") || buyPostMatchesForExtra(extraOnlyBuy, f.date, "kadamba", "", "") {
			t.Fatal("regular dinner task must not match egg/extra-only buy post")
		}
	})
}

func TestStartExtrasRepriceSplitsPoolAndKicksMainMeal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.MentionExtras = true
		ep := &fakeExtraPortal{
			fakeMealPortal: fakeMealPortal{mess: "kadamba-nonveg"},
			extras: []portal.ExtraRegistration{
				{ID: "e1", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
				{ID: "e2", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
			},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		m.portalFor = func(context.Context, int64) (mealPortal, error) { return ep, nil }

		// Start main meal FIRST (with mention_extras=true): posts "+ extras: Egg".
		if _, err := m.Start(ctx, 0, f.date, "lunch", 50, 50, ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		posts, _, _ := poolMessages(f)
		if len(posts) != 1 || posts[0].text != "sell kadamba non-veg lunch 50 + extras: Egg" {
			t.Fatalf("initial main meal post = %+v", posts)
		}

		// Now start 2 fixed-price eggs at ₹15: main meal post must edit out "+ extras: Egg".
		res, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 2, 15, 15, time.Time{}, true, false)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		_, edits, _ := poolMessages(f)
		editedOut := false
		for _, e := range edits {
			if e.text == "sell kadamba non-veg lunch 50" {
				editedOut = true
			}
		}
		if !editedOut {
			t.Fatalf("expected main meal post to edit out + extras: Egg, got edits=%+v", edits)
		}

		// Reprice the fixed-price pool (via egg #1) to ₹20: both active eggs in
		// the pool update together and edit the single shared post to x2 20.
		if err := m.SetPrices(ctx, res.IDs[0], 20, 20); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if t1, t2 := f.task(res.IDs[0]), f.task(res.IDs[1]); t1.MinPaise != 2000 || t2.MinPaise != 2000 {
			t.Fatalf("expected pool-wide fixed reprice to 2000, got t1=%d t2=%d", t1.MinPaise, t2.MinPaise)
		}
		_, edits, _ = poolMessages(f)
		saw20 := false
		for _, e := range edits {
			if e.text == "sell kadamba lunch egg (extra only) x2 20" {
				saw20 = true
			}
		}
		if !saw20 {
			t.Fatalf("expected shared post edited to x2 20, got edits=%+v", edits)
		}
	})
}

func TestStartExtrasDeliveryTimeOversellGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ep := &fakeExtraPortal{
			fakeMealPortal: fakeMealPortal{mess: "kadamba-nonveg"},
			extras: []portal.ExtraRegistration{
				{ID: "e1", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
				{ID: "e2", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
			},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		m.portalFor = func(context.Context, int64) (mealPortal, error) { return ep, nil }

		res, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 2, 15, 15, time.Time{}, true, false)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		// Buyer A replies, then 1 of the 2 eggs is cancelled on the portal before buyer B pays.
		f.conn.emit(f.reply(buyerA, "egg?"))
		synctest.Wait()
		f.conn.emit(f.reply(buyerB, "egg please"))
		synctest.Wait()
		ep.extras = ep.extras[:1] // only 1 unavailed egg left on the portal

		time.Sleep(2 * time.Second)
		f.conn.emit(paymentOf(buyerA, "pay-egg-only1", "15.00", time.Now()))
		synctest.Wait()
		if task := f.task(res.IDs[0]); task.State != store.TaskSold {
			t.Fatalf("egg 1 state = %s (%s), want sold", task.State, task.Detail)
		}
		// Egg 2 receives buyer B via passOn, checks capacity, sees 0 left (1 unavailed - 1 unscanned sold),
		// and refuses to arm/oversell instead of sending a duplicate QR!
		if task := f.task(res.IDs[1]); task.State == store.TaskSold {
			t.Fatalf("egg 2 must not oversell after 1 egg was cancelled on portal: %+v", task)
		}
	})
}

func TestStartExtrasManualQROversellGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ep := &fakeExtraPortal{
			fakeMealPortal: fakeMealPortal{mess: "kadamba-nonveg"},
			extras: []portal.ExtraRegistration{
				{ID: "e1", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
				{ID: "e2", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
			},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		m.portalFor = func(context.Context, int64) (mealPortal, error) { return ep, nil }

		res, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 2, 15, 15, time.Time{}, true, false)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		// Sell egg #1 manually with QR, then cancel 1 egg on the portal before buyer scans.
		if err := m.MarkSold(ctx, res.IDs[0], buyerA.String(), "Buyer A", 15, "", true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		ep.extras = ep.extras[:1]

		// Attempting Confirm + Send QR on egg #2 must fail because 1 sold (unscanned)
		// consumes the 1 remaining unavailed portal registration.
		if err := m.MarkSold(ctx, res.IDs[1], buyerB.String(), "Buyer B", 15, "", true); err == nil {
			t.Fatal("expected manual MarkSold with QR on egg #2 to fail after portal cancellation")
		}
	})
}

func TestStartExtrasBlockedAfterMainMealSoldWithMentionedExtras(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.MentionExtras = true
		ep := &fakeExtraPortal{
			fakeMealPortal: fakeMealPortal{mess: "kadamba-nonveg"},
			extras: []portal.ExtraRegistration{
				{ID: "e1", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
			},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		m.portalFor = func(context.Context, int64) (mealPortal, error) { return ep, nil }

		mealID, err := m.Start(ctx, 0, f.date, "lunch", 50, 50, "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := m.MarkSold(ctx, mealID, buyerA.String(), "Buyer A", 50, "", true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		// Main meal was sold advertising "+ extras: Egg", so selling Egg
		// separately afterwards must be rejected.
		if _, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 1, 15, 15, time.Time{}, true, false); err == nil {
			t.Fatal("expected StartExtras to fail after main meal sold with Egg advertised")
		}
	})
}

// A buyer who pays for the last egg after it was cancelled on the portal is
// turned down by the claim guard; the payment must be recorded for a refund.
func TestPaidEggRefusedByClaimGuardIsRecordedForRefund(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ep := &fakeExtraPortal{
			fakeMealPortal: fakeMealPortal{mess: "kadamba-nonveg"},
			extras: []portal.ExtraRegistration{
				{ID: "e1", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
			},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		m.portalFor = func(context.Context, int64) (mealPortal, error) { return ep, nil }

		res, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 1, 15, 15, time.Time{}, true, false)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "egg?"))
		synctest.Wait()
		ep.extras = nil // the egg is cancelled on the portal after the payment prompt

		time.Sleep(2 * time.Second)
		f.conn.emit(paymentOf(buyerA, "pay-egg-gone", "15.00", time.Now()))
		synctest.Wait()
		time.Sleep(5 * time.Second)
		synctest.Wait()

		if task := f.task(res.IDs[0]); task.State == store.TaskSold {
			t.Fatalf("egg sold with nothing left on the portal: %+v", task)
		}
		buyers, err := f.st.SellTaskBuyers(context.Background(), res.IDs[0])
		if err != nil {
			t.Fatal(err)
		}
		var refund string
		for _, b := range buyers {
			if b.Peer == buyerA.String() {
				refund = b.Refund
			}
		}
		if refund != "15.00" {
			t.Fatalf("refund = %q, want 15.00 recorded for the refused payment; buyers %+v", refund, buyers)
		}
	})
}

// A main meal still selling with an old "+ extras: Egg" post that can no
// longer be edited blocks selling the egg on its own.
func TestStartExtrasBlockedByActiveMainMealWithOldPost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.MentionExtras = true
		ep := &fakeExtraPortal{
			fakeMealPortal: fakeMealPortal{mess: "kadamba-nonveg"},
			extras: []portal.ExtraRegistration{
				{ID: "e1", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
			},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		m.portalFor = func(context.Context, int64) (mealPortal, error) { return ep, nil }

		if _, err := m.Start(ctx, 0, f.date, "lunch", 50, 50, ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(postEditWindow + time.Minute)
		synctest.Wait()

		if _, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 1, 15, 15, time.Time{}, true, false); err == nil {
			t.Fatal("expected StartExtras to fail while an uneditable main-meal post still offers Egg")
		}
	})
}

// Once a buyer replied to a fresh main-meal post offering Egg, editing the
// post can't take the egg back from them, so it can't sell on its own.
func TestStartExtrasBlockedByMainMealBuyerOnFreshPost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		f.cfg.Sell.MentionExtras = true
		ep := &fakeExtraPortal{
			fakeMealPortal: fakeMealPortal{mess: "kadamba-nonveg"},
			extras: []portal.ExtraRegistration{
				{ID: "e1", Date: f.date, Meal: "lunch", Mess: "kadamba-nonveg", Name: "Egg", Rate: "1500"},
			},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := f.manager(ctx)
		m.portalFor = func(context.Context, int64) (mealPortal, error) { return ep, nil }

		if _, err := m.Start(ctx, 0, f.date, "lunch", 50, 50, ""); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.conn.emit(f.reply(buyerA, "lunch?"))
		synctest.Wait()

		if _, err := m.StartExtras(ctx, 0, f.date, "lunch", "Egg", 1, 15, 15, time.Time{}, true, false); err == nil {
			t.Fatal("expected StartExtras to fail while a main-meal buyer holds a post offering Egg")
		}
	})
}
