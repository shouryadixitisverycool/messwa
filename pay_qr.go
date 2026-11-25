package main

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"rsc.io/qr"

	"messwa/internal/config"
	"messwa/internal/store"
)

// payUPI reports whether the pay message comes with a UPI QR: screenshots
// count as payment and the seller saved a UPI ID.
func (s *sale) payUPI() bool { return s.acceptImages && s.upi != "" }

// payQRPNG draws the UPI QR that pays amount ("30.00") to s.upi. An empty
// amount leaves it to the buyer. The note names the mess and meal, so the
// payment in your UPI app says what it was for.
func (s *sale) payQRPNG(amount string) ([]byte, error) {
	code, err := qr.Encode(config.UPIPayURI(s.upi, amount, qrCaptionFor(s.mess, baseMeal(s.meal), s.extra)), qr.M)
	if err != nil {
		return nil, err
	}
	return code.PNG(), nil
}

// payQRCaption labels the UPI QR, e.g. "₹30 to name@okaxis". The UPI ID
// is there to copy for a buyer who can't scan an image on their own phone.
func payQRCaption(amount, upi string) string {
	if p := priceWords(amount); p != "" {
		return fmt.Sprintf("₹%s to %s", p, upi)
	}
	return "pay to " + upi
}

// sendPayQR sends buyer the UPI QR for amount and remembers the amount for
// resendPayQR. If the image can't be made or uploaded, the caption goes as
// text, so the buyer still knows where to pay.
func (s *sale) sendPayQR(ctx context.Context, buyer types.JID, amount string) error {
	caption := payQRCaption(amount, s.upi)
	msg, err := s.payQRMessage(ctx, amount, caption)
	if err != nil {
		s.log.Warn("could not make the UPI QR; sending the UPI ID as text", "err", err)
		msg = &waE2E.Message{Conversation: proto.String(caption)}
	}
	if _, err := s.send(ctx, buyer, msg); err != nil {
		return err
	}
	s.payQRMu.Lock()
	if s.payQRAmounts == nil {
		s.payQRAmounts = map[types.JID]string{}
	}
	s.payQRAmounts[buyer] = amount
	s.payQRMu.Unlock()
	s.note(buyer, store.EventPayQR, paise(amount), "")
	return nil
}

// resendPayQR sends buyer the UPI QR again, for the amount it last went
// out with. After a restart that's unknown, and the QR leaves the amount
// to them.
func (s *sale) resendPayQR(ctx context.Context, buyer types.JID) error {
	s.payQRMu.Lock()
	amount := s.payQRAmounts[buyer]
	s.payQRMu.Unlock()
	return s.sendPayQR(ctx, buyer, amount)
}

func (s *sale) payQRMessage(ctx context.Context, amount, caption string) (*waE2E.Message, error) {
	png, err := s.payQRPNG(amount)
	if err != nil {
		return nil, err
	}
	data, mime, width, height, err := deliveryImageBytes(png)
	if err != nil {
		return nil, err
	}
	up, err := s.conn.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return nil, fmt.Errorf("upload UPI QR: %w", err)
	}
	return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String(caption), Mimetype: proto.String(mime), Width: proto.Uint32(uint32(width)), Height: proto.Uint32(uint32(height)),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength,
	}}, nil
}
