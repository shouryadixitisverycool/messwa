package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Everything the bot says to a buyer in a DM is here. It's written the way
// people in the mess groups write: lowercase, a bare number with no ₹, the
// meal never named again after the group post, and a "?" to ask. Most
// replies have a few variants so a buyer who comes back every meal doesn't
// see the same lines each time. A reply may be two messages, like "25" then
// "final offer"; it counts once against the per-buyer limit.

// reply is one bot reply: one or more messages sent back to back.
type reply []string

// partGap is the pause between the messages of one reply.
var partGap = 1200 * time.Millisecond

// pick chooses a variant. Tests pin it to the first.
var pick = func(n int) int { return rand.IntN(n) }

func oneOf(variants ...reply) reply { return variants[pick(len(variants))] }

func msg(parts ...string) reply { return reply(parts) }

// sayOpening is the first price. day is "" for today's meal, else "tmrw"
// or "13 nov". The bot never says the price is negotiable; buyers assume
// it is.
func sayOpening(price int, day string) reply {
	r := oneOf(msg(fmt.Sprint(price)), msg(fmt.Sprintf("its %d", price)))
	if day != "" {
		r = reply{r[0] + ", its for " + day}
	}
	return r
}

// sayCounter comes down to price. The first counter offers it, later ones
// ask.
func sayCounter(price, round int) reply {
	if round <= 1 {
		return oneOf(msg(fmt.Sprintf("how about %d", price)), msg(fmt.Sprintf("can do %d", price)), msg(fmt.Sprintf("%d works?", price)), msg(fmt.Sprintf("i can do %d", price)))
	}
	return oneOf(msg(fmt.Sprintf("%d?", price)), msg(fmt.Sprintf("%d then?", price)), msg(fmt.Sprintf("ok %d?", price)), msg(fmt.Sprintf("would %d work?", price)))
}

// sayLowest is the floor.
func sayLowest(price int) reply {
	return oneOf(
		msg(fmt.Sprint(price), "final offer"),
		msg(fmt.Sprintf("%d last", price)),
		msg(fmt.Sprintf("cant go below %d", price)),
		msg(fmt.Sprintf("%d only", price), "cant do lower sorry"),
	)
}

// sayWhich answers "nv?" or "kadamba?". what is the mess and diet, e.g.
// "kadamba veg".
func sayWhich(what string, price int) reply {
	return oneOf(msg(fmt.Sprintf("%s for %d", what, price)), msg(fmt.Sprintf("its %s, %d", what, price)), msg(what, fmt.Sprint(price)))
}

// sayRestate answers the first message the bot couldn't read.
func sayRestate(price int) reply {
	return oneOf(msg(fmt.Sprintf("%d, want it?", price)), msg(fmt.Sprintf("%d, do you want it?", price)))
}

// sayQueued tells a buyer who agreed that someone else holds the turn.
func sayQueued(price int) reply {
	return msg(fmt.Sprintf("ok %d", price), "someone else is paying rn, if they dont in 2 min its yours")
}

// sayOutbid tells a buyer asked to pay that someone offered more, so they
// don't pay now. If the meal frees up, their next turn asks them again. A
// buyer handed to you has no 2 minute window.
func sayOutbid(handedOff bool) reply {
	if handedOff {
		return msg("wait dont pay yet", "someone offered more, ill msg u if it falls through")
	}
	return msg("wait dont pay yet", "someone offered more, if they dont pay in 2 min ill msg u")
}

// sayQueuedHandoff tells a buyer who agreed that someone else is buying
// from you by hand, with no deadline.
func sayQueuedHandoff(price int) reply {
	return msg(fmt.Sprintf("ok %d", price), "someone else is buying rn, ill msg u if it falls through")
}

// sayPayOther answers a buyer who wants to pay some way the bot can't see.
// The bot says it once; asking again hands the buyer to you.
func sayPayOther() reply {
	return oneOf(msg("only whatsapp pay sorry"), msg("whatsapp pay only pls"), msg("can only take whatsapp pay sorry"), msg("has to be whatsapp pay sorry"))
}

// sayNoScreenshots answers an image during a buyer's payment turn, once,
// when [sell] accept_payment_images is off.
func sayNoScreenshots() reply {
	return oneOf(
		msg("cant check screenshots, pay using whatsapp pay pls"),
		msg("cant verify screenshots, whatsapp pay pls"),
		msg("cant confirm payment from a screenshot, use whatsapp pay pls"),
	)
}

// soldLines are saySold's variants. A task that has ended may send these
// and nothing else; see taskGate.sendSold.
var soldLines = []string{"sold sorry", "already sold sorry", "someone else bought it sorry", "its sold bro sorry"}

// saySold tells a buyer the meal went to someone else.
func saySold() reply { return msg(soldLines[pick(len(soldLines))]) }

// paymentPrompt asks a buyer to pay amount ("30.00"). testImage says the QR
// is the embedded placeholder. It names WhatsApp Pay and rules out the
// rest, because a GPay or UPI payment to the number never reaches the bot.
func paymentPrompt(amount string, testImage bool) reply {
	price := "ok " + strings.TrimSuffix(amount, ".00")
	instruction := oneOf(
		msg("pay using whatsapp pay pls, not gpay/upi"),
		msg("whatsapp pay pls, not gpay/upi"),
		msg("use whatsapp pay pls, not gpay/upi"),
		msg("send through whatsapp pay pls, not gpay/upi"),
	)[0]
	if testImage {
		notice := oneOf(msg("its the test qr"), msg("this ones a test qr"), msg("just the test qr btw"))[0]
		return msg(price, instruction, notice)
	}
	promise := oneOf(
		msg("ill send the qr right after"),
		msg("ill send the qr once youve paid"),
		msg("once the payment comes through ill send the qr"),
		msg("ill send the qr as soon as i get the payment"),
	)[0]
	return msg(price, instruction, promise)
}

// qrCaption labels the meal QR, e.g. "kadamba lunch", so a buyer can see
// it's the mess they paid for.
func qrCaption(mess, meal string) string {
	return strings.Join(strings.Fields(mess+" "+meal), " ")
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
