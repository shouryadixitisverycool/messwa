package notify

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"messwa/internal/config"
	"messwa/internal/wa"
)

// ChannelWhatsApp is the registered channel name for WhatsApp self-alerts.
const ChannelWhatsApp = "whatsapp"

// WhatsAppSender is the minimal WhatsApp capability needed to send a
// self-alert message to the user's own number.
type WhatsAppSender interface {
	Connected() bool
	SelfJID(ctx context.Context) (types.JID, error)
	SendText(ctx context.Context, to types.JID, text string) error
}

// WhatsAppChannel sends attention alerts to the user's own WhatsApp number
// ("Message Yourself" / notes-to-self chat) from their linked companion device.
//
// Companion-device self-messaging behavior (whatsmeow):
//
//   - whatsmeow.Client.SendMessage requires a non-AD target JID (Device == 0,
//     via jid.ToNonAD()). Passing the user's own phone-number JID (or LID)
//     routes through sendDM, which encrypts the payload as a DeviceSentMessage
//     (fromMe=true, DestinationJID=self) to all of the user's other devices,
//     including their primary phone.
//   - The message lands immediately at the top of the user's "Message Yourself"
//     chat on WhatsApp, and messwa's own ingest (internal/wa/wa.go allowedChat)
//     already drops notes-to-self so self-alerts never pollute dm_peers or
//     trigger sell-task handlers.
//   - Protocol caveat: because WhatsApp treats DeviceSentMessage as a message
//     authored by the user on another linked device (fromMe=true), the primary
//     mobile app typically syncs it into "Message Yourself" without firing an
//     audible/banner push notification. This is a WhatsApp protocol limitation
//     when sending from the user's own linked account (including to a 1-person
//     group, which is also fromMe=true). Sending to the user's own JID is
//     therefore the right WhatsApp delivery target, and the channel-agnostic
//     Dispatcher is designed so a Web Push channel can plug in alongside it for
//     OS-level push notifications and offline-WhatsApp coverage.
type WhatsAppChannel struct {
	sender WhatsAppSender
}

// NewWhatsAppChannel constructs a WhatsApp self-alert channel.
func NewWhatsAppChannel(sender WhatsAppSender) *WhatsAppChannel {
	return &WhatsAppChannel{sender: sender}
}

func (c *WhatsAppChannel) Name() string { return ChannelWhatsApp }

func (c *WhatsAppChannel) Enabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Alerts.WhatsApp
}

// SupportsKind excludes "whatsapp" (link offline), which cannot be sent over
// a disconnected WhatsApp link.
func (c *WhatsAppChannel) SupportsKind(kind string) bool {
	return kind != "whatsapp"
}

func (c *WhatsAppChannel) Send(ctx context.Context, batch Batch) error {
	text := FormatBatch(batch)
	if text == "" {
		return nil
	}
	return c.SendText(ctx, batch.UserID, text)
}

// SendText implements TextSender so standalone notices (such as the Sunday
// weekly recap hook) can reuse the same WhatsApp self-message path.
func (c *WhatsAppChannel) SendText(ctx context.Context, _ int64, text string) error {
	if text == "" {
		return nil
	}
	if c.sender == nil || !c.sender.Connected() {
		return fmt.Errorf("%w: WhatsApp disconnected", ErrChannelUnavailable)
	}
	selfJID, err := c.sender.SelfJID(ctx)
	if err != nil {
		return fmt.Errorf("%w: resolve own WhatsApp JID: %v", ErrChannelUnavailable, err)
	}
	selfJID = selfJID.ToNonAD()
	if selfJID.IsEmpty() || selfJID.User == "" {
		return fmt.Errorf("%w: WhatsApp not linked", ErrChannelUnavailable)
	}
	if err := c.sender.SendText(ctx, selfJID, text); err != nil {
		if isUnavailableErr(err) {
			return fmt.Errorf("%w: %v", ErrChannelUnavailable, err)
		}
		return err
	}
	return nil
}

func isUnavailableErr(err error) bool {
	return errors.Is(err, ErrChannelUnavailable) ||
		errors.Is(err, whatsmeow.ErrNotConnected) ||
		errors.Is(err, whatsmeow.ErrNotLoggedIn) ||
		errors.Is(err, wa.ErrUnavailable)
}
