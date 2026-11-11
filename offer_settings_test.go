package main

import (
	"context"
	"errors"
	"flag"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"messwa/internal/config"
	"messwa/internal/portal"
)

func TestOfferText(t *testing.T) {
	base := offerDetails{Mess: "kadamba", Diet: "nonveg", Meal: "lunch", Price: "₹60", ShowPrice: true}
	for _, tc := range []struct {
		name string
		edit func(*offerDetails)
		want string
	}{
		// The default settings must post exactly what deliver-qr posted before [sell].
		{"default", func(*offerDetails) {}, "sell kadamba non-veg lunch ₹60"},
		{"other day", func(o *offerDetails) { o.Day = "5 Nov 2026" }, "sell kadamba non-veg lunch ₹60 (5 Nov 2026)"},
		{"no price", func(o *offerDetails) { o.ShowPrice = false }, "sell kadamba non-veg lunch"},
		{"veg", func(o *offerDetails) { o.Mess, o.Diet = "palash", "veg" }, "sell palash veg lunch ₹60"},
		{"no diet", func(o *offerDetails) { o.Diet = "" }, "sell kadamba lunch ₹60"},
		{"extras", func(o *offerDetails) { o.Extras = []string{"Chicken 65", "Lassi"} }, "sell kadamba non-veg lunch ₹60 + extras: Chicken 65, Lassi"},
		{"extras no price other day", func(o *offerDetails) {
			o.ShowPrice, o.Day, o.Extras = false, "5 Nov 2026", []string{"Lassi"}
		}, "sell kadamba non-veg lunch (5 Nov 2026) + extras: Lassi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.edit(&o)
			if got := offerText(o); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOfferExtras(t *testing.T) {
	used := "2026-11-04T12:40:00Z"
	rows := []portal.ExtraRegistration{
		{Name: "Chicken 65", Mess: "kadamba-nonveg"},
		{Name: "Lassi", Mess: "kadamba"},
		{Name: "lassi", Mess: "kadamba"},
		{Name: "Gulab  Jamun", Mess: ""},
		{Name: "Eaten", Mess: "kadamba", AvailedAt: &used},
		{Name: "Elsewhere", Mess: "bakul"},
	}
	want := []string{"Chicken 65", "Lassi", "Gulab Jamun"}
	if got := offerExtras(rows, "kadamba"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := offerExtras(nil, "kadamba"); len(got) != 0 {
		t.Fatalf("no extras gave %q", got)
	}
}

func testSellConfig(t *testing.T, edit func(*config.Config)) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Groups = []string{"1@g.us", "2@g.us", "3@g.us"}
	if edit != nil {
		edit(cfg)
	}
	return cfg
}

func parseSellFlags(t *testing.T, args ...string) sellFlags {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := addSellFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return read()
}

func TestSellFlagsOnlyOverrideWhatWasPassed(t *testing.T) {
	f := parseSellFlags(t)
	if f.pickGroups() || f.postGap != nil || f.showPrice != nil || f.mentionExtras != nil || f.acceptPlainDMs != nil {
		t.Fatalf("unset flags overrode config: %+v", f)
	}
	f = parseSellFlags(t, "-group", "Mess A", "-group", "2@g.us", "-show-price=false", "-mention-extras", "-post-gap", "5s")
	if !reflect.DeepEqual([]string(f.groups), []string{"Mess A", "2@g.us"}) || *f.showPrice || !*f.mentionExtras || *f.postGap != 5*time.Second {
		t.Fatalf("flags = %+v", f)
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	addSellFlags(fs)
	if err := fs.Parse([]string{"-group", " "}); err == nil {
		t.Fatal("empty -group accepted")
	}
}

func TestResolveSellSettings(t *testing.T) {
	// Defaults keep today's behaviour: no groups without -group, price shown,
	// no extras.
	s, err := resolveSellSettings(testSellConfig(t, nil), sellFlags{})
	if err != nil || len(s.Groups) != 0 || !s.ShowPrice || s.MentionExtras || s.AcceptPlainDMs || s.PostGap != 15*time.Second {
		t.Fatalf("defaults = %+v, %v", s, err)
	}
	s, err = resolveSellSettings(testSellConfig(t, nil), parseSellFlags(t, "-accept-plain-dms"))
	if err != nil || !s.AcceptPlainDMs {
		t.Fatalf("-accept-plain-dms = %+v, %v", s, err)
	}
	plain := func(c *config.Config) { c.Sell.AcceptPlainDMs = true }
	s, err = resolveSellSettings(testSellConfig(t, plain), sellFlags{})
	if err != nil || !s.AcceptPlainDMs {
		t.Fatalf("accept_plain_dms = %+v, %v", s, err)
	}
	s, err = resolveSellSettings(testSellConfig(t, plain), parseSellFlags(t, "-accept-plain-dms=false"))
	if err != nil || s.AcceptPlainDMs {
		t.Fatalf("-accept-plain-dms=false over the setting = %+v, %v", s, err)
	}
	s, err = resolveSellSettings(testSellConfig(t, nil), parseSellFlags(t, "-group", "Mess A"))
	if err != nil || !reflect.DeepEqual(s.Groups, []string{"Mess A"}) {
		t.Fatalf("one -group = %+v, %v", s, err)
	}

	named := func(c *config.Config) {
		c.Sell.Groups = []string{"Mess A", "Mess B", "Mess A"}
		c.Sell.ShowPrice = false
		c.Sell.MentionExtras = true
		c.Sell.PostGap.Duration = 30 * time.Second
	}
	s, err = resolveSellSettings(testSellConfig(t, named), sellFlags{})
	if err != nil || !reflect.DeepEqual(s.Groups, []string{"Mess A", "Mess B"}) || s.ShowPrice || !s.MentionExtras || s.PostGap != 30*time.Second {
		t.Fatalf("config = %+v, %v", s, err)
	}
	s, err = resolveSellSettings(testSellConfig(t, named), parseSellFlags(t, "-group", "Test", "-show-price", "-mention-extras=false", "-post-gap", "4s"))
	if err != nil || !reflect.DeepEqual(s.Groups, []string{"Test"}) || !s.ShowPrice || s.MentionExtras || s.PostGap != 4*time.Second {
		t.Fatalf("flags over config = %+v, %v", s, err)
	}
	s, err = resolveSellSettings(testSellConfig(t, named), parseSellFlags(t, "-group-count", "3"))
	if err != nil || !reflect.DeepEqual(s.Groups, []string{"1@g.us", "2@g.us", "3@g.us"}) {
		t.Fatalf("-group-count over sell.groups = %+v, %v", s, err)
	}

	counted := func(c *config.Config) { c.Sell.GroupCount = 2 }
	s, err = resolveSellSettings(testSellConfig(t, counted), sellFlags{})
	if err != nil || !reflect.DeepEqual(s.Groups, []string{"1@g.us", "2@g.us"}) {
		t.Fatalf("group_count = %+v, %v", s, err)
	}

	for name, args := range map[string][]string{
		"both group flags": {"-group", "Mess A", "-group-count", "1"},
		"count too high":   {"-group-count", "4"},
		"count zero":       {"-group-count", "0"},
		"gap too short":    {"-group", "Mess A", "-post-gap", "1s"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveSellSettings(testSellConfig(t, nil), parseSellFlags(t, args...)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestPostOfferCopiesSpacesAndContinues(t *testing.T) {
	groups := []offerTarget{
		{JID: types.NewJID("1", types.GroupServer), Name: "one"},
		{JID: types.NewJID("2", types.GroupServer), Name: "two"},
		{JID: types.NewJID("3", types.GroupServer), Name: "three"},
	}
	var sentAt []time.Time
	post := func(_ context.Context, g offerTarget) (postedOffer, error) {
		sentAt = append(sentAt, time.Now())
		if g.Name == "two" {
			return postedOffer{}, errors.New("uncertain")
		}
		return postedOffer{Chat: g.JID, GroupName: g.Name, ID: "id-" + g.Name}, nil
	}
	var announced []string
	gap := 20 * time.Millisecond
	copies, err := postOfferCopies(t.Context(), groups, gap, post, func(p postedOffer) { announced = append(announced, p.ID) })
	if err == nil || !strings.Contains(err.Error(), "two") {
		t.Fatalf("failed copy not reported: %v", err)
	}
	if len(copies) != 2 || copies[0].ID != "id-one" || copies[1].ID != "id-three" || copies[1].Chat != groups[2].JID {
		t.Fatalf("copies = %+v", copies)
	}
	if !reflect.DeepEqual(announced, []string{"id-one", "id-three"}) {
		t.Fatalf("announced = %v", announced)
	}
	for i := 1; i < len(sentAt); i++ {
		if sentAt[i].Sub(sentAt[i-1]) < gap {
			t.Fatalf("copies %d and %d only %s apart", i-1, i, sentAt[i].Sub(sentAt[i-1]))
		}
	}
}

func TestPostOfferCopiesStopsWhenSold(t *testing.T) {
	groups := []offerTarget{{Name: "one"}, {Name: "two"}}
	ctx, cancel := context.WithCancel(t.Context())
	sent := 0
	copies, err := postOfferCopies(ctx, groups, time.Hour, func(context.Context, offerTarget) (postedOffer, error) {
		sent++
		cancel()
		return postedOffer{ID: "first"}, nil
	}, nil)
	if err != nil || sent != 1 || len(copies) != 1 {
		t.Fatalf("sent %d copies after the sale: %v %v", sent, copies, err)
	}
}

func TestRepliesToAnyCopyShareOneQueue(t *testing.T) {
	postedAt := time.Unix(1793900000, 0)
	groupA := types.NewJID("111", types.GroupServer)
	groupB := types.NewJID("222", types.GroupServer)
	o := &groupOffer{copies: []postedOffer{
		{Chat: groupA, ID: "offer-a", At: postedAt},
		{Chat: groupB, ID: "offer-b", At: postedAt.Add(15 * time.Second)},
	}}
	reply := func(group types.JID, id string, at time.Time) *events.Message {
		return &events.Message{
			Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("456", types.DefaultUserServer)}, Timestamp: at},
			Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        proto.String("interested"),
				ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String(id), RemoteJID: proto.String(group.String())},
			}},
		}
	}
	if !o.matches(reply(groupA, "offer-a", postedAt.Add(time.Second))) || !o.matches(reply(groupB, "offer-b", postedAt.Add(16*time.Second))) {
		t.Fatal("reply to a copy did not match")
	}
	for name, e := range map[string]*events.Message{
		"copy id in the wrong group": reply(groupA, "offer-b", postedAt.Add(16*time.Second)),
		"before that copy":           reply(groupB, "offer-b", postedAt.Add(time.Second)),
		"unknown post":               reply(groupA, "other", postedAt.Add(time.Second)),
	} {
		if o.matches(e) {
			t.Errorf("%s matched", name)
		}
	}

	// One buyer replying to both copies gets one turn.
	q := offerQueue{seen: make(map[types.JID]bool), ready: make(chan struct{}, 1)}
	buyer := types.NewJID("456", types.DefaultUserServer)
	q.add(buyer)
	q.add(buyer)
	if peer, err := q.next(t.Context()); err != nil || peer != buyer {
		t.Fatalf("first buyer = %v, %v", peer, err)
	}
	if waiting := q.closeAndDrain(); len(waiting) != 0 {
		t.Fatalf("buyer queued twice: %v", waiting)
	}
}
