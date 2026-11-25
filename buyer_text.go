package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"messwa/internal/config"
	"messwa/internal/wording"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Everything the bot says to a buyer in a DM comes from here. The built-in
// lines in internal/wording are written the way people in the mess groups
// write: lowercase, a bare number with no ₹, the meal never named again
// after the group post, and a "?" to ask. Each account can rewrite them on
// the settings page. Most replies have a few variants so a buyer who comes
// back every meal doesn't see the same lines each time. A reply may be two
// messages, like "25" then "final offer"; it counts once against the
// per-buyer limit.

// reply is one bot reply: one or more messages sent back to back.
type reply []string

// partGap is the pause between the messages of one reply.
var partGap = 1200 * time.Millisecond

// pick chooses a variant. Tests pin it to the first.
var pick = func(n int) int { return rand.IntN(n) }

// payTurn is how long a buyer asked to pay has before the next buyer gets
// the turn.
const payTurn = 2 * time.Minute

// lines renders the bot's replies for one sale, in the account's own
// wording when it has some. The zero value says the built-in lines with
// no meal details.
type lines struct {
	// cfg is the account's config, read live so an edit reaches running
	// sales. Nil means the built-in lines.
	cfg  *config.Config
	vals wording.Values
	end  time.Time
	// extras lists the meal's extras; it may call the portal, so it runs
	// only for a template that uses {extras}.
	extras func() []string
	// day labels the meal's day as of now, for {day}. A sale that starts
	// the evening before keeps its lines past midnight, so this runs per
	// message. Nil means today's meal.
	day func() string
}

// saleLines are the lines for one meal. diet is "veg", "nonveg", "egg" or
// empty; meal may carry a "#" suffix.
func saleLines(cfg *config.Config, mess, diet, meal, date string, end time.Time, extras func() []string) lines {
	return saleLinesFor(cfg, mess, diet, meal, "", date, end, extras)
}

func saleLinesFor(cfg *config.Config, mess, diet, meal, extra, date string, end time.Time, extras func() []string) lines {
	var day func() string
	if cfg != nil {
		day = func() string { return offerDay(cfg, date) }
	}
	shown := diet
	if extra != "" {
		shown = ""
	} else if shown == "nonveg" {
		shown = "non-veg"
	}
	return lines{cfg: cfg, end: end, extras: extras, day: day, vals: wording.Values{
		"meal": portalItemLabel(mess, diet, meal, extra), "mess": mess, "diet": shown, "mealtime": baseMeal(meal),
		"wait": durationWords(payTurn),
	}}
}

// with returns l with more values, as name, value pairs.
func (l lines) with(kv ...string) lines {
	vals := make(wording.Values, len(l.vals)+len(kv)/2)
	for k, v := range l.vals {
		vals[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		vals[kv[i]] = kv[i+1]
	}
	l.vals = vals
	return l
}

// today is the meal's day label right now: "" for today, "tmrw" or
// "13 nov".
func (l lines) today() string {
	if l.day == nil {
		return ""
	}
	return l.day()
}

// forBuyer adds the buyer's WhatsApp name.
func (l lines) forBuyer(name string) lines { return l.with("buyer", strings.TrimSpace(name)) }

func (l lines) book() wording.Book {
	if l.cfg == nil {
		return nil
	}
	s := l.cfg.Live().Sell
	if !s.OwnWording {
		return nil
	}
	return s.Messages
}

// say picks one of slot's variants and fills it in. kv are values for
// this message only.
func (l lines) say(slot string, kv ...string) reply {
	vs := l.book().Variants(slot)
	if r := l.fill(vs[pick(len(vs))], kv); len(r) > 0 {
		return r
	}
	// Everything in the variant was optional and empty.
	s, _ := wording.Find(slot)
	return l.fill(s.Default[0], kv)
}

func (l lines) fill(parts []string, kv []string) reply {
	vals := l.with(kv...).vals
	vals["day"] = l.today()
	joined := strings.Join(parts, "\n")
	if strings.Contains(joined, "{ends}") || strings.Contains(joined, "{left}") {
		if !l.end.IsZero() {
			loc := time.Local
			if l.cfg != nil {
				loc = l.cfg.Location()
			}
			vals["ends"] = l.end.In(loc).Format("3:04")
			vals["left"] = durationWords(max(0, time.Until(l.end)))
		}
	}
	if strings.Contains(joined, "{extras}") && l.extras != nil {
		vals["extras"] = strings.ToLower(strings.Join(l.extras(), ", "))
	}
	var r reply
	for _, p := range parts {
		if t := wording.Fill(p, vals); t != "" {
			r = append(r, t)
		}
	}
	return r
}

// durationWords says d the way a buyer would read it: "2 min", "1 hr",
// "1 hr 5 min".
func durationWords(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	switch {
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m%60 == 0:
		return fmt.Sprintf("%d hr", m/60)
	}
	return fmt.Sprintf("%d hr %d min", m/60, m%60)
}

// opening is the first price. The bot never says the price is negotiable;
// buyers assume it is.
func (l lines) opening(price int) reply { return l.say("opening", "price", fmt.Sprint(price)) }

// have opens a chat with someone whose buy post asked for this meal, as a
// private reply to that post. The price follows separately.
func (l lines) have(price int) reply { return l.say("have", "price", fmt.Sprint(price)) }

// counter comes down to price from was. The first counter offers it,
// later ones ask.
func (l lines) counter(price, was, round int) reply {
	slot := "counter"
	if round > 1 {
		slot = "counter_again"
	}
	return l.say(slot, "price", fmt.Sprint(price), "was", priceWords(was))
}

// nudge follows up once on a buyer who said nothing after the bot's first
// message.
func (l lines) nudge(price int) reply { return l.say("nudge", "price", fmt.Sprint(price)) }

// quietDrop sends only the lower number after silence, including with custom wording.
func (l lines) quietDrop(price, was int) reply {
	return reply{fmt.Sprint(price)}
}

// lowest is the floor.
func (l lines) lowest(price, was int) reply {
	return l.say("lowest", "price", fmt.Sprint(price), "was", priceWords(was))
}

// which answers "nv?" or "kadamba?".
func (l lines) which(price int) reply { return l.say("which", "price", fmt.Sprint(price)) }

// otherMeal answers a buyer who asked for another meal, such as "lunch?"
// to a dinner seller. Before any price it quotes one too.
func (l lines) otherMeal(price int, priced bool) reply {
	if priced {
		return l.say("other_meal")
	}
	return l.say("other_meal_price", "price", fmt.Sprint(price))
}

// restate answers the first message the bot couldn't read.
func (l lines) restate(price int) reply { return l.say("restate", "price", fmt.Sprint(price)) }

// queued tells a buyer who agreed that someone else holds the turn.
func (l lines) queued(price int) reply { return l.say("queued", "price", fmt.Sprint(price)) }

// queuedHandoff tells a buyer who agreed that someone else is buying from
// you by hand, with no deadline.
func (l lines) queuedHandoff(price int) reply {
	return l.say("queued_handoff", "price", fmt.Sprint(price))
}

// outbid tells a buyer asked to pay that someone offered more, so they
// don't pay now. If the meal frees up, their next turn asks them again. A
// buyer handed to you has no payment window.
func (l lines) outbid(handedOff bool) reply {
	if handedOff {
		return l.say("outbid_handoff")
	}
	return l.say("outbid")
}

// dropped tells a buyer asked to pay that autopilot dropped them, so they
// don't pay.
func (l lines) dropped() reply { return l.say("dropped") }

// payOther answers a buyer who wants to pay some way the bot can't see.
// The bot says it once; asking again hands the buyer to you.
func (l lines) payOther() reply { return l.say("pay_other") }

// payOtherUPI is payOther when the pay message carries your UPI QR.
func (l lines) payOtherUPI() reply { return l.say("pay_other_upi") }

// noScreenshots answers an image during a buyer's payment turn, once,
// when [sell] accept_payment_images is off.
func (l lines) noScreenshots() reply { return l.say("no_screenshots") }

// payNumber tells a paying buyer without WhatsApp Pay to pay the number by
// UPI, when [sell] accept_payment_images is on.
func (l lines) payNumber() reply { return l.say("pay_number") }

// askScreenshot asks a paying buyer who says they paid for a screenshot,
// when [sell] accept_payment_images is on.
func (l lines) askScreenshot() reply { return l.say("ask_screenshot") }

// payShort tells a buyer whose payment screenshots add up to less than the
// price how much more to send. paid and short are rupee amounts ("3.00").
func (l lines) payShort(paid, short, price string) reply {
	return l.with("paid", priceWords(paid), "short", priceWords(short), "price", priceWords(price)).say("pay_short")
}

// payUnclear answers an image that doesn't show a finished payment to you.
func (l lines) payUnclear() reply { return l.say("pay_unclear") }

// sold tells a buyer the meal went to someone else. A task that has ended
// may send this and nothing else; see taskGate.sendSold.
func (l lines) sold() reply { return l.say("sold") }

// isSold reports whether text is a sold line, in the account's wording or
// the built-in one.
func isSold(cfg *config.Config, text string) bool {
	l := lines{cfg: cfg}
	for _, vs := range [][][]string{l.book().Variants("sold"), wording.Book(nil).Variants("sold")} {
		for _, v := range vs {
			if len(v) == 1 && wording.Matches(v[0], text) {
				return true
			}
		}
	}
	return false
}

// payPrompt asks a buyer to pay amount ("30.00"). testImage says the QR is
// the embedded placeholder. The built-in lines name WhatsApp Pay and rule
// out the rest, because a GPay or UPI payment to the number never reaches
// the bot.
//
// images is [sell] accept_payment_images: then the prompt leaves out "not
// gpay/upi", since a screenshot of a UPI payment to the number counts.
// upi says the seller's UPI QR follows the prompt (see sale.payUPI): it
// asks them to pay on that QR and send the screenshot, which already says
// what comes next, so no QR promise follows.
func (l lines) payPrompt(amount string, testImage, images, upi bool) reply {
	slot := "pay"
	switch {
	case upi:
		slot = "pay_upi"
	case images:
		slot = "pay_images"
	}
	r := l.say(slot, "price", priceWords(amount))
	if upi && !testImage {
		return r
	}
	if testImage {
		return append(r, l.say("pay_test")...)
	}
	return append(r, l.say("pay_qr")...)
}

// priceWords is a price the way the bot says it: "30", not "30.00". Zero
// is empty, for a price the bot hasn't said.
func priceWords[T int | string](p T) string {
	s := strings.TrimSuffix(fmt.Sprint(p), ".00")
	if s == "0" {
		return ""
	}
	return s
}

// qrCaption labels the meal QR, e.g. "kadamba lunch", so a buyer can see
// it's the mess they paid for.
func qrCaption(mess, meal string) string {
	return qrCaptionFor(mess, meal, "")
}

// qrCaptionFor labels the QR for a meal or separately sold extra, e.g.
// "kadamba dinner egg".
func qrCaptionFor(mess, meal, extra string) string {
	return strings.Join(strings.Fields(mess+" "+meal+" "+strings.ToLower(strings.TrimSpace(extra))), " ")
}

// sendReply sends r's messages in order, partGap apart. It stops at the
// first failure; that message may still have reached WhatsApp.
func sendReply(ctx context.Context, send sendFunc, to types.JID, r reply) error {
	for i, part := range r {
		if i > 0 && partGap > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(partGap):
			}
		}
		if _, err := send(ctx, to, &waE2E.Message{Conversation: proto.String(part)}); err != nil {
			return err
		}
	}
	return nil
}
