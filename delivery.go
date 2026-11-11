package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"messwa/internal/config"
	"messwa/internal/portal"
	"messwa/internal/store"
	"messwa/internal/wa"
)

//go:embed assets/qr-placeholder.png
var placeholderQR []byte

func deliverQR(ctx context.Context, cfg *config.Config, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("deliver-qr", flag.ContinueOnError)
	date := fs.String("date", "", "meal date, YYYY-MM-DD in the configured timezone")
	meal := fs.String("meal", "", "configured meal name")
	peer := fs.String("peer", "", "reserved buyer's full WhatsApp JID")
	amount := fs.String("amount", "", "agreed INR amount, e.g. 60.00")
	qr := fs.String("qr", "", "PNG or JPEG image to send (defaults to an embedded, labelled test image)")
	portalQR := fs.Bool("portal-qr", false, "fetch the current QR from the portal immediately before delivery, with -user's mess API key")
	user := fs.String("user", "", "with -portal-qr, whose mess API key to use; defaults to the first admin")
	offer := fs.String("offer-text", "", "group post, posted as written, or optional private offer with -peer")
	diagnostics := fs.Bool("diagnostics", false, "log delivery checks for the selected buyer without message contents")
	assumeUnreadable := fs.Bool("assume-unreadable-payment", false, "send the QR when a payment's amount stays unreadable after recovery, assuming the agreed price")
	readSellFlags := addSellFlags(fs)
	handoffFlag := fs.String("handoff", "", "for a group offer, overrides [sell] handoff for this run: never or on_reply")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	overrides := readSellFlags()
	if *peer != "" && overrides.pickGroups() {
		return errors.New("choose either -peer or -group/-group-count")
	}
	var sell sellSettings
	if *peer == "" {
		var err error
		if sell, err = resolveSellSettings(cfg, overrides); err != nil {
			return err
		}
		if len(sell.Groups) == 0 {
			return errors.New("choose -peer, or -group/-group-count, or set groups or group_count under [sell] in the config")
		}
	}
	groupMode := len(sell.Groups) > 0
	if groupMode && !*portalQR && strings.TrimSpace(*offer) == "" {
		return errors.New("a group offer requires -offer-text or -portal-qr")
	}
	if *portalQR && *qr != "" {
		return errors.New("choose either -portal-qr or -qr")
	}
	if *peer != "" && *handoffFlag != "" {
		return errors.New("-handoff applies only to group offers; -peer is already your own buyer")
	}
	if *peer != "" && overrides.buyers != nil {
		return errors.New("-buyers applies only to group offers; -peer is already your own buyer")
	}
	handoff := config.HandoffNever
	if groupMode {
		var err error
		if handoff, err = cfg.Sell.Handoff.Resolve(*handoffFlag); err != nil {
			return err
		}
	}
	slot, err := findSlot(cfg, *date, *meal)
	if err != nil {
		return errors.New("-" + err.Error())
	}
	if !time.Now().Before(slot.End) {
		return errors.New("meal has already ended")
	}
	var jid types.JID
	if *peer != "" {
		jid, err = types.ParseJID(*peer)
		if err != nil || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
			return errors.New("-peer must be a personal WhatsApp JID")
		}
		jid = jid.ToNonAD()
	}
	if *amount, err = normalizeAmount(*amount); err != nil {
		return errors.New("-amount must be a positive INR value with two decimal places")
	}
	data, _, _, _, err := readDeliveryImage(*qr)
	if err != nil {
		return err
	}
	s := &sale{
		log: log, loc: cfg.Location(), date: *date, meal: *meal, end: slot.End,
		image: data, testImage: *qr == "", imageHash: fmt.Sprintf("%x", sha256.Sum256(data)),
		diagnostics: *diagnostics, assumeUnreadable: *assumeUnreadable,
	}
	var extras []string
	if *portalQR {
		portalClient, err := userPortal(ctx, cfg, *user)
		if err != nil {
			return err
		}
		s.image, s.testImage = nil, false
		if err := s.usePortal(ctx, portalClient, cfg.VegOnly); err != nil {
			return err
		}
		if groupMode && sell.MentionExtras && strings.TrimSpace(*offer) == "" {
			extras = readExtras(ctx, portalClient, log, *date, *meal, s.mess)
		}
	}
	c, err := wa.Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	if !c.LoggedIn() {
		return errors.New("not linked; run messwa login first")
	}
	if *peer != "" && (jid == c.WA.Store.GetJID().ToNonAD() || jid == c.WA.Store.GetLID().ToNonAD()) {
		return errors.New("buyer cannot be your own account")
	}
	st, err := store.Open(cfg.DataDB())
	if err != nil {
		return err
	}
	defer st.Close()
	s.st, s.conn = st, waConn{c.WA}
	s.send, s.sendPaid = c.WA.SendMessage, c.WA.SendMessage
	own := newOwnGroupLog(ctx, cfg, st, c)
	commandCtx := ctx
	ctx, cancel := context.WithDeadline(ctx, slot.End)
	defer cancel()
	defer c.WA.Disconnect()
	// The meal is sold once its QR is out, so tick every offer post for it,
	// including ones from an earlier group offer. The deadline is the
	// command's, not the meal's: a sale in the last seconds still gets its ✅.
	tickOffers := func() {
		tickCtx, stop := context.WithTimeout(commandCtx, time.Minute)
		defer stop()
		if _, _, err := reactSold(tickCtx, st, own, waReaction(c), log, *date, *meal); err != nil {
			log.Warn("could not read the offer posts to mark them sold", "err", err)
		}
	}
	if *peer != "" {
		err := s.takeTurn(ctx, jid, s.reservation(jid, *amount), turn{
			arm:    st.ArmDelivery,
			prompt: *offer,
			listening: func() error {
				if err := c.WA.Connect(); err != nil {
					return err
				}
				if (*diagnostics || *offer != "") && !c.WA.WaitForConnection(20*time.Second) {
					return errors.New("WhatsApp connection not ready; offer was not sent")
				}
				return nil
			},
		})
		if err != nil {
			return err
		}
		tickOffers()
		return nil
	}
	if sell.Buyers.UnsavedOnly() {
		fmt.Fprintln(os.Stderr, "Buyers is unsaved_only: replies from saved contacts are skipped and get nothing.")
	}
	if sell.MentionExtras && (!*portalQR || strings.TrimSpace(*offer) != "") {
		log.Info("extras are only listed in the offer built with -portal-qr and no -offer-text")
	}
	exists, err := st.DeliveryExists(ctx, *date, *meal)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("meal already has a delivery reservation; if it was handed off to you, finish it with -peer or free it with release-handoff")
	}
	if err := c.WA.ConnectContext(ctx); err != nil {
		return err
	}
	if !c.WA.WaitForConnection(20 * time.Second) {
		return errors.New("WhatsApp connection not ready")
	}
	targets, err := findGroups(ctx, s.conn, sell.Groups)
	if err != nil {
		return err
	}
	groupPost := *offer
	if strings.TrimSpace(groupPost) == "" {
		groupPost = offerText(offerDetails{Mess: s.mess, Diet: s.diet, Meal: *meal, Price: rupees(*amount),
			ShowPrice: sell.ShowPrice, Day: offerDay(cfg, *date), Extras: extras})
	}
	queue := newOfferQueue()
	queue.join = savedContactFilter(ctx, s.conn, log, sell.Buyers)
	posting := watchGroupOffer(ctx, s.conn, queue)
	defer posting.close()
	// Record each copy as it posts, so the ✅ after a sale reaches copies
	// that went out after the first.
	record := func(p postedOffer) { recordOffers(ctx, st, own, log, *date, *meal, []postedOffer{p}) }
	posting.post(ctx, log, targets, 0, sell.PostGap, directCopy(s.conn, s.send, groupPost), record)
	if err := posting.waitPosted(); err != nil {
		return err
	}
	s.sold = func() {
		// Post no more copies of a sold offer.
		log.Info("offer sold", "copies_posted", len(posting.stop()))
	}
	if handoff.HandsOffOnReply() {
		fmt.Fprintln(os.Stderr, "Handoff is on_reply: the first private reply hands the chat to you and the bot sends that buyer nothing.")
		s.handoff = func(ctx context.Context, buyer types.JID, d store.Delivery, others []types.JID) error {
			// You have a buyer, so post no more copies.
			log.Info("offer handed off", "copies_posted", len(posting.stop()))
			return handOff(ctx, st, log, os.Stdout, d, posting.queue.name(buyer), resumeFlags(*portalQR, *user, *qr), others)
		}
	}
	err = s.sellToQueue(ctx, commandCtx, posting.queue, memoryJournal{amount: *amount, st: st}, 10*time.Minute, nil)
	switch {
	case errors.Is(err, errHandedOff):
		return nil
	case err != nil:
		return err
	}
	tickOffers()
	return nil
}

// resumeFlags are the image flags for resuming a handed-off sale with
// deliver-qr -peer, so it sends the same kind of image the offer promised.
func resumeFlags(portalQR bool, user, qr string) string {
	switch {
	case portalQR && user != "":
		return " -portal-qr -user " + user
	case portalQR:
		return " -portal-qr"
	case qr != "":
		return " -qr " + qr
	}
	return ""
}

// extrasPortal reads the extras registered for a meal.
type extrasPortal interface {
	ExtraRegistrations(ctx context.Context, date, meal string) ([]portal.ExtraRegistration, error)
}

// readExtras lists the extras to mention in an offer. Extras are a
// nice-to-have, so a failed read is logged and never blocks the sale.
func readExtras(ctx context.Context, p extrasPortal, log *slog.Logger, date, meal, mess string) []string {
	rows, err := p.ExtraRegistrations(ctx, date, meal)
	if err != nil {
		log.Warn("could not read extra registrations; posting the offer without extras", "err", err)
		return nil
	}
	return offerExtras(rows, mess)
}

// rupees is an amount as a group post shows it: "₹60", or "₹60.50".
func rupees(amount string) string { return "₹" + strings.TrimSuffix(amount, ".00") }

// offerDay labels a meal that isn't today's, e.g. "5 Nov 2026".
func offerDay(cfg *config.Config, date string) string {
	if date == time.Now().In(cfg.Location()).Format("2006-01-02") {
		return ""
	}
	day, err := time.ParseInLocation("2006-01-02", date, cfg.Location())
	if err != nil {
		return ""
	}
	return day.Format("2 Jan 2006")
}

// normalizeAmount checks an INR amount with two decimal places and drops
// leading zeros without floats, so 060.00 matches the received 60.00.
func normalizeAmount(amount string) (string, error) {
	if !decimalAmount.MatchString(amount) || strings.Trim(amount, "0.") == "" {
		return "", errors.New("not a positive INR amount with two decimal places")
	}
	parts := strings.Split(amount, ".")
	parts[0] = strings.TrimLeft(parts[0], "0")
	if parts[0] == "" {
		parts[0] = "0"
	}
	return parts[0] + "." + parts[1], nil
}

func buyerPassed(message *waE2E.Message) bool {
	text := message.GetConversation()
	if text == "" {
		text = message.GetExtendedTextMessage().GetText()
	}
	switch strings.Trim(strings.ToLower(strings.TrimSpace(text)), ".!?") {
	case "pass", "cancel", "no thanks", "not interested":
		return true
	default:
		return false
	}
}

func portalMealLabel(mess, diet, meal string) string {
	if diet == "nonveg" {
		diet = "non-veg"
	}
	return strings.Join(strings.Fields(mess+" "+diet+" "+meal), " ")
}

func waitForPaymentRecovery(ctx context.Context, request func(context.Context, int)) {
	requestCtx, stopRequests := context.WithTimeout(ctx, 40*time.Second)
	defer stopRequests()
	for attempt := 1; attempt <= 6 && requestCtx.Err() == nil; attempt++ {
		request(requestCtx, attempt)
		select {
		case <-requestCtx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

func deliverOnce(ctx context.Context, st *store.Store, d store.Delivery, paid store.Payment, messageID string, send func() error) error {
	claimed, err := st.ClaimDelivery(ctx, d, paid, messageID)
	if err != nil {
		return err
	}
	if !claimed {
		return errors.New("meal already claimed; no duplicate delivery attempted")
	}
	if err := send(); err != nil {
		return fmt.Errorf("delivery attempt failed or is uncertain; no automatic retry: %w", err)
	}
	if err := st.FinishDelivery(ctx, d, messageID); err != nil {
		return fmt.Errorf("image sent but completion could not be recorded: %w", err)
	}
	return nil
}

func qualifiesForDelivery(e *events.Message, amount string, armedAt, endsAt, now time.Time) bool {
	return deliveryRejection(e, amount, armedAt, endsAt, now) == nil
}

func deliveryRejection(e *events.Message, amount string, armedAt, endsAt, now time.Time) error {
	if e == nil || e.Info.IsFromMe || e.Info.IsGroup || e.Info.ID == "" {
		return errors.New("not an incoming DM with a message ID")
	}
	if e.Info.Chat.Server != types.DefaultUserServer && e.Info.Chat.Server != types.HiddenUserServer {
		return errors.New("not a personal chat")
	}
	if e.Info.Timestamp.Before(armedAt) || !e.Info.Timestamp.Before(endsAt) || !now.Before(endsAt) || e.Info.Timestamp.After(now.Add(5*time.Second)) {
		return errors.New("message time outside the delivery window")
	}
	payment := e.Message.GetSendPaymentMessage()
	if payment == nil {
		return errors.New("no sendPaymentMessage field")
	}
	info := e.SourceWebMsg.GetPaymentInfo()
	status, txnStatus := info.GetStatus(), info.GetTxnStatus()
	if (status != waWeb.PaymentInfo_UNKNOWN_STATUS && status != waWeb.PaymentInfo_COMPLETE) ||
		(txnStatus != waWeb.PaymentInfo_UNKNOWN && txnStatus != waWeb.PaymentInfo_SUCCESS && txnStatus != waWeb.PaymentInfo_COMPLETED) {
		return errors.New("WhatsApp reports that payment is not complete")
	}
	p, err := decodePaymentPayload(payment.GetTransactionData())
	if p != nil && (p.Timestamp.Before(armedAt) || !p.Timestamp.Before(endsAt) || p.Timestamp.After(now.Add(5*time.Second))) {
		return errors.New("transaction time outside the delivery window")
	}
	if errors.Is(err, errTruncatedPayment) && info != nil {
		currency := info.GetCurrency()
		if currency != "" && currency != "INR" {
			return errors.New("payment currency does not match")
		}
		timestamp := time.Unix(int64(info.GetTransactionTimestamp()), 0)
		if info.TransactionTimestamp != nil && (timestamp.Before(armedAt) || !timestamp.Before(endsAt) || timestamp.After(now.Add(5*time.Second))) {
			return errors.New("transaction time outside the delivery window")
		}
		if info.Amount1000 != nil {
			thousandths := info.GetAmount1000()
			reported := fmt.Sprintf("%d.%02d", thousandths/1000, thousandths%1000/10)
			if thousandths%10 != 0 || reported != amount {
				return errors.New("payment amount does not match")
			}
			if status == waWeb.PaymentInfo_COMPLETE && txnStatus == waWeb.PaymentInfo_SUCCESS && currency != "" && info.TransactionTimestamp != nil {
				p = &paymentPayloadSummary{
					Currency:  currency,
					Amount:    reported,
					Timestamp: timestamp,
				}
				err = nil
			}
		} else if primary := info.GetPrimaryAmount(); primary != nil && primary.Value != nil {
			return errors.New("unsupported payment amount format")
		}
	}
	if err != nil {
		return err
	}
	if p.Currency != "INR" || p.Amount != amount {
		return errors.New("payment amount or currency does not match")
	}
	return nil
}

func readDeliveryImage(path string) ([]byte, string, int, int, error) {
	data := placeholderQR
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, "", 0, 0, err
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, 5*1024*1024+1))
		if err != nil {
			return nil, "", 0, 0, err
		}
	}
	return deliveryImageBytes(data)
}

func deliveryImageBytes(data []byte) ([]byte, string, int, int, error) {
	if len(data) > 5*1024*1024 {
		return nil, "", 0, 0, errors.New("image exceeds 5 MiB")
	}
	info, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || info.Width <= 0 || info.Height <= 0 || info.Width > 8192 || info.Height > 8192 {
		return nil, "", 0, 0, errors.New("image must be a valid PNG or JPEG, up to 8192 pixels per side")
	}
	return data, "image/" + format, info.Width, info.Height, nil
}
