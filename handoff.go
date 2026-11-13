package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/config"
	"messwa/internal/store"
)

// handOff records that you take over the sale with this buyer and tells you
// which chat to open. The caller must stop sending to the buyer: nothing has
// gone to them yet, so they see your reply as the first one. resumeFlags are
// the image flags the resume command needs, e.g. " -portal-qr -user asha".
// others are buyers who also replied before the handoff.
func handOff(ctx context.Context, st *store.Store, log *slog.Logger, out io.Writer, d store.Delivery, name, resumeFlags string, others []types.JID) error {
	if err := st.HandOffDelivery(ctx, d, name); err != nil {
		return err
	}
	log.Info("handed the sale to you; take over the buyer's chat",
		"date", d.Date, "meal", d.Meal, "buyer", d.Peer, "name", name, "asked", d.Amount)
	_, err := io.WriteString(out, handoffNotice(d, name, resumeFlags, others))
	return err
}

func handoffNotice(d store.Delivery, name, resumeFlags string, others []types.JID) string {
	var b strings.Builder
	jid, _ := types.ParseJID(d.Peer)
	who := d.Peer
	if jid.Server == types.DefaultUserServer {
		who = "+" + jid.User
	}
	if name != "" {
		who = fmt.Sprintf("%s (%s)", name, who)
	}
	fmt.Fprintf(&b, "\nHANDOFF %s %s: take over the chat with %s.\n", d.Date, d.Meal, who)
	if jid.Server == types.DefaultUserServer {
		fmt.Fprintf(&b, "Open https://wa.me/%s\n", jid.User)
	} else {
		b.WriteString("WhatsApp hides this buyer's number. Find the chat by name; it is the newest reply to your offer.\n")
	}
	fmt.Fprintf(&b, "The bot has sent them nothing and is no longer listening. Your offer asked INR %s.\n", d.Amount)
	b.WriteString("Once you agree a price, let the bot take the payment and send the QR:\n")
	fmt.Fprintf(&b, "  messwa deliver-qr -date %s -meal %s -peer %s -amount PRICE%s\n", d.Date, d.Meal, d.Peer, resumeFlags)
	b.WriteString("If the sale falls through, free the meal:\n")
	accountFlag := ""
	if _, user, ok := strings.Cut(resumeFlags, " -user "); ok {
		accountFlag = " -user " + user
	}
	fmt.Fprintf(&b, "  messwa release-handoff -date %s -meal %s%s\n", d.Date, d.Meal, accountFlag)
	if len(others) > 0 {
		list := make([]string, len(others))
		for i, o := range others {
			list[i] = o.String()
			if o.Server == types.DefaultUserServer {
				list[i] = "+" + o.User
			}
		}
		fmt.Fprintf(&b, "Also replied to the offer: %s\n", strings.Join(list, ", "))
	}
	return b.String()
}

// releaseHandoff frees a handed-off meal so a new deliver-qr can offer it.
// With -unpaid it also frees a reservation still waiting for payment.
// It only touches the database, so it is safe while run is up.
func releaseHandoff(ctx context.Context, cfg *config.Config, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("release-handoff", flag.ContinueOnError)
	date := fs.String("date", "", "meal date, YYYY-MM-DD")
	meal := fs.String("meal", "", "configured meal name")
	unpaid := fs.Bool("unpaid", false, "also free a reservation still waiting for payment, such as one a stopped deliver-qr -peer left armed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *date == "" || *meal == "" {
		return errors.New("provide -date and -meal")
	}
	st, err := openCommandStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	if *unpaid {
		state, err := st.ReleaseUnpaid(ctx, *date, *meal)
		if err != nil {
			return err
		}
		if state == "armed" {
			fmt.Fprintf(out, "Released %s %s, which was waiting for a payment. It can be offered again. Stop any deliver-qr still waiting on it: a payment it sees now gets no QR.\n", *date, *meal)
		} else {
			fmt.Fprintf(out, "Released %s %s. It can be offered again.\n", *date, *meal)
		}
		return nil
	}
	freed, err := st.ReleaseHandoff(ctx, *date, *meal)
	if err != nil {
		return err
	}
	if !freed {
		return errors.New("no handed-off sale for that meal; armed, paid and sent meals are left alone. To free an unpaid armed reservation, add -unpaid")
	}
	fmt.Fprintf(out, "Released %s %s. It can be offered again.\n", *date, *meal)
	return nil
}
