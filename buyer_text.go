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
// "thats the least"; it counts once against the per-buyer limit.

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
	r := oneOf(msg(fmt.Sprint(price)), msg(fmt.Sprintf("its %d", price)), msg(fmt.Sprintf("selling for %d", price)))
	if day != "" {
		r = reply{r[0] + ", its for " + day}
	}
	return r
}

// sayCounter comes down to price. The first counter offers it, later ones
// ask.
func sayCounter(price, round int) reply {
	if round <= 1 {
		return oneOf(msg(fmt.Sprintf("how about %d", price)), msg(fmt.Sprintf("can do %d", price)))
	}
	return oneOf(msg(fmt.Sprintf("%d?", price)), msg(fmt.Sprintf("%d then?", price)))
}

// sayLowest is the floor.
func sayLowest(price int) reply {
	return oneOf(
		msg(fmt.Sprintf("%d only", price), "thats the least"),
		msg(fmt.Sprint(price), "thats the least"),
		msg(fmt.Sprintf("cant go below %d", price)),
	)
}

// sayWhich answers "nv?" or "kadamba?". what is the mess and diet, e.g.
// "kadamba veg".
func sayWhich(what string, price int) reply {
	return oneOf(msg(fmt.Sprintf("%s, %d", what, price)), msg(fmt.Sprintf("its %s, %d", what, price)))
}

// sayRestate answers the first message the bot couldn't read.
func sayRestate(price int) reply {
	return oneOf(msg(fmt.Sprintf("%d bro, want it?", price)), msg(fmt.Sprintf("%d, want it?", price)))
}

// sayQueued tells a buyer who agreed that someone else holds the turn.
func sayQueued(price int) reply {
	return oneOf(
		msg(fmt.Sprintf("ok %d", price), "someone else is paying rn, if they dont in 2 min its yours"),
		msg(fmt.Sprintf("ok %d", price), "someone else is paying rn, its yours if they dont in 2 min"),
	)
}

// sayPayOther answers a buyer who wants to pay some way the bot can't see.
// The bot says it once; asking again hands the buyer to you.
func sayPayOther() reply {
	return oneOf(msg("only whatsapp pay sorry"), msg("only whatsapp pay here sorry"))
}

// sayNoScreenshots answers an image during a buyer's payment turn, once,
// when [sell] accept_payment_images is off.
func sayNoScreenshots() reply {
	return oneOf(msg("cant see screenshots, pay on whatsapp pay here pls"), msg("cant check screenshots, pls pay on whatsapp pay in this chat"))
}

// soldLines are saySold's variants. A task that has ended may send these
// and nothing else; see taskGate.sendSold.
var soldLines = []string{"sold sorry", "sold, sorry"}

// saySold tells a buyer the meal went to someone else.
func saySold() reply { return msg(soldLines[pick(len(soldLines))]) }

// paymentPrompt asks a buyer to pay amount ("30.00"). testImage says the QR
// is the embedded placeholder. It names WhatsApp Pay and rules out the
// rest, because a GPay or UPI payment to the number never reaches the bot.
func paymentPrompt(amount string, testImage bool) reply {
	price := "ok " + strings.TrimSuffix(amount, ".00")
	if testImage {
		return msg(price, "pay on whatsapp pay in this chat, not gpay/upi. its the test qr")
	}
	return oneOf(
		msg(price, "pay on whatsapp pay in this chat, not gpay/upi. qr right after"),
		msg(price, "pay with whatsapp pay here, not gpay/upi. qr right after"),
		msg(price, "whatsapp pay in this chat pls, not gpay/upi. ill send the qr right after"),
	)
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
