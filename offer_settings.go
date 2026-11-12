package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"messwa/internal/config"
	"messwa/internal/portal"
	"messwa/internal/repost"
)

// sellSettings is the [sell] section after one run's flags are applied.
type sellSettings struct {
	// Groups the offer goes to, by exact name or JID, in posting order.
	Groups        []string
	PostGap       time.Duration
	ShowPrice     bool
	MentionExtras bool
	// AcceptPlainDMs lets DMs about the offer that don't quote it join the
	// buyer queue; see plainDMs.
	AcceptPlainDMs bool
	// Buyers is who may buy; with unsaved_only, saved contacts are skipped.
	Buyers config.Buyers
	// Repost is when a copy that dropped out of view gets a fresh one.
	Repost repost.Rules
	// AcceptPaymentImages counts an image from the paying buyer as their
	// payment; see sale.acceptImages.
	AcceptPaymentImages bool
}

// sellFlags are the deliver-qr flags that override [sell] for one run. A
// nil field means the flag wasn't passed.
type sellFlags struct {
	groups         []string
	groupCount     *int
	postGap        *time.Duration
	showPrice      *bool
	mentionExtras  *bool
	acceptPlainDMs *bool
	buyers         *string
	repost         *bool
	repostAfter    *time.Duration
	repostMessages *int
	repostListings *int
	maxReposts     *int
	paymentImages  *bool
}

// pickGroups reports whether any flag picks offer groups.
func (f sellFlags) pickGroups() bool { return len(f.groups) > 0 || f.groupCount != nil }

// stringsFlag collects a flag that may be given more than once.
type stringsFlag []string

func (s *stringsFlag) String() string { return strings.Join(*s, ", ") }
func (s *stringsFlag) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("must not be empty")
	}
	*s = append(*s, v)
	return nil
}

// addSellFlags registers the [sell] override flags on fs. Call the returned
// function after fs.Parse to read which of them were passed.
func addSellFlags(fs *flag.FlagSet) func() sellFlags {
	var groups stringsFlag
	fs.Var(&groups, "group", "post the offer to this exact group name or JID and wait for a private reply; repeat for more groups (overrides [sell] groups)")
	count := fs.Int("group-count", 0, "post the offer to the first N configured groups (overrides [sell] group_count)")
	gap := fs.Duration("post-gap", 0, "pause between copies of the offer (overrides [sell] post_gap)")
	price := fs.Bool("show-price", true, "put the price in the group post (overrides [sell] show_price)")
	extras := fs.Bool("mention-extras", false, "list the meal's registered extras in the group post (overrides [sell] mention_extras)")
	plain := fs.Bool("accept-plain-dms", false, "let DMs that ask about the offer without quoting it join the buyer queue (overrides [sell] accept_plain_dms)")
	buyers := fs.String("buyers", "", "who may buy from a group offer: anyone or unsaved_only (overrides [sell] buyers)")
	again := fs.Bool("repost", true, "post a fresh copy in a group once the last one dropped out of view (overrides [sell] repost)")
	after := fs.Duration("repost-after", 0, "repost once a copy is this old; 0 turns this off (overrides [sell] repost_after)")
	messages := fs.Int("repost-messages", 0, "repost once this many messages came in after a copy; 0 turns this off (overrides [sell] repost_messages)")
	listings := fs.Int("repost-listings", 0, "repost once this many other sellers listed the same meal at the same mess after a copy; 0 turns this off (overrides [sell] repost_listings)")
	maxReposts := fs.Int("max-reposts", 0, "most fresh copies per group (overrides [sell] max_reposts)")
	images := fs.Bool("accept-payment-images", false, "count an image from the paying buyer, sent after the payment prompt, as their payment; nothing verifies it (overrides [sell] accept_payment_images)")
	return func() sellFlags {
		f := sellFlags{groups: groups}
		fs.Visit(func(fl *flag.Flag) {
			switch fl.Name {
			case "group-count":
				f.groupCount = count
			case "post-gap":
				f.postGap = gap
			case "show-price":
				f.showPrice = price
			case "mention-extras":
				f.mentionExtras = extras
			case "accept-plain-dms":
				f.acceptPlainDMs = plain
			case "buyers":
				f.buyers = buyers
			case "repost":
				f.repost = again
			case "repost-after":
				f.repostAfter = after
			case "repost-messages":
				f.repostMessages = messages
			case "repost-listings":
				f.repostListings = listings
			case "max-reposts":
				f.maxReposts = maxReposts
			case "accept-payment-images":
				f.paymentImages = images
			}
		})
		return f
	}
}

// resolveSellSettings applies one run's flags over cfg's [sell] section. An
// empty Groups means no group was picked.
func resolveSellSettings(cfg *config.Config, f sellFlags) (sellSettings, error) {
	cfg = cfg.Live()
	s := sellSettings{
		PostGap:             cfg.Sell.PostGap.Duration,
		ShowPrice:           cfg.Sell.ShowPrice,
		MentionExtras:       cfg.Sell.MentionExtras,
		AcceptPlainDMs:      cfg.Sell.AcceptPlainDMs,
		Buyers:              cfg.Sell.Buyers,
		Repost:              repost.FromConfig(cfg.Sell),
		AcceptPaymentImages: cfg.Sell.AcceptPaymentImages,
	}
	firstN := func(n int, name string) ([]string, error) {
		if n < 1 || n > len(cfg.Groups) {
			return nil, fmt.Errorf("%s must be between 1 and the %d configured groups", name, len(cfg.Groups))
		}
		return cfg.Groups[:n], nil
	}
	var groups []string
	var err error
	switch {
	case len(f.groups) > 0 && f.groupCount != nil:
		return s, errors.New("choose -group or -group-count, not both")
	case len(f.groups) > 0:
		groups = f.groups
	case f.groupCount != nil:
		groups, err = firstN(*f.groupCount, "-group-count")
	case len(cfg.Sell.Groups) > 0:
		groups = cfg.Sell.Groups
	case cfg.Sell.GroupCount > 0:
		groups, err = firstN(cfg.Sell.GroupCount, "sell.group_count")
	}
	if err != nil {
		return s, err
	}
	seen := map[string]bool{}
	for _, g := range groups {
		g = strings.TrimSpace(g)
		if !seen[g] {
			seen[g] = true
			s.Groups = append(s.Groups, g)
		}
	}
	if f.postGap != nil {
		if *f.postGap < config.MinPostGap {
			return s, fmt.Errorf("-post-gap must be at least %s", config.MinPostGap)
		}
		s.PostGap = *f.postGap
	}
	if f.showPrice != nil {
		s.ShowPrice = *f.showPrice
	}
	if f.mentionExtras != nil {
		s.MentionExtras = *f.mentionExtras
	}
	if f.acceptPlainDMs != nil {
		s.AcceptPlainDMs = *f.acceptPlainDMs
	}
	if f.paymentImages != nil {
		s.AcceptPaymentImages = *f.paymentImages
	}
	if f.buyers != nil {
		if s.Buyers, err = config.ParseBuyers(*f.buyers); err != nil {
			return s, fmt.Errorf("-buyers: %w", err)
		}
	}
	set := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	if f.repost != nil {
		s.Repost.On = *f.repost
	}
	if f.repostAfter != nil {
		s.Repost.After = *f.repostAfter
	}
	set(&s.Repost.Messages, f.repostMessages)
	set(&s.Repost.Listings, f.repostListings)
	set(&s.Repost.Max, f.maxReposts)
	r := s.Repost
	if err := config.ValidateRepost(r.After, r.Messages, r.Listings, r.Max, "-"); err != nil {
		return s, err
	}
	return s, nil
}

// offerDetails is what a built group offer says.
type offerDetails struct {
	Mess string
	// Diet is "veg", "nonveg", "egg" or empty.
	Diet string
	Meal string
	// Price is the asking price as posted, e.g. "₹60".
	Price     string
	ShowPrice bool
	// Day labels a meal that isn't today's, e.g. "5 Nov 2026". Empty for today.
	Day    string
	Extras []string
}

// offerText builds the group post, e.g.
// "sell kadamba non-veg lunch ₹60 (5 Nov 2026) + extras: Chicken 65, Lassi".
func offerText(o offerDetails) string {
	parts := []string{"sell", portalMealLabel(o.Mess, o.Diet, o.Meal)}
	if o.ShowPrice && o.Price != "" {
		parts = append(parts, o.Price)
	}
	if o.Day != "" {
		parts = append(parts, "("+o.Day+")")
	}
	text := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	if len(o.Extras) > 0 {
		text += " + extras: " + strings.Join(o.Extras, ", ")
	}
	return text
}

// offerExtras picks the extra names to mention for a meal at mess: the ones
// not yet availed and registered at that mess, once each, in portal order.
func offerExtras(rows []portal.ExtraRegistration, mess string) []string {
	var names []string
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Availed() {
			continue
		}
		if extraMess, _ := (portal.Registration{Mess: r.Mess}).MessDiet(); extraMess != "" && mess != "" && extraMess != mess {
			continue
		}
		name := strings.Join(strings.Fields(r.Name), " ")
		if key := strings.ToLower(name); name != "" && !seen[key] {
			seen[key] = true
			names = append(names, name)
		}
	}
	return names
}
