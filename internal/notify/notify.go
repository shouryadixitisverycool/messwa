// Package notify dispatches attention inbox items to pluggable per-user
// notification channels (WhatsApp self-alerts first, Web Push later).
package notify

import (
	"context"
	"errors"
	"time"

	"messwa/internal/config"
	"messwa/internal/store"
)

// ErrChannelUnavailable indicates that a channel cannot deliver right now
// (for example, a user's WhatsApp companion session is disconnected or not
// linked). The dispatcher records this as store.AlertOffline for the channel
// without retry-storming or blocking other channels (such as Web Push).
var ErrChannelUnavailable = errors.New("channel unavailable")

// Item is one attention item to notify a user about. Fields mirror
// mcpserver.AttentionItem so channels have full context without coupling
// notify to mcpserver.
type Item struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Date       string `json:"date,omitempty"`
	Meal       string `json:"meal,omitempty"`
	Task       int64  `json:"task,omitempty"`
	Account    bool   `json:"account,omitempty"`
	Resolvable bool   `json:"resolvable"`
	Rule       int64  `json:"rule,omitempty"`
	Buyer      string `json:"buyer,omitempty"`
}

// Batch is a group of attention items delivered together to one user.
type Batch struct {
	UserID  int64
	Items   []Item
	BaseURL string // Dashboard base URL without trailing slash; may be empty.
}

// Channel delivers alerts over one transport (for example, WhatsApp
// self-messages or Web Push). Adding a new channel requires only implementing
// this interface and passing it to Dispatcher.Register; no dispatcher changes
// are needed.
type Channel interface {
	// Name is the stable identifier stored in alert_deliveries and alert_sends
	// (for example, "whatsapp" or "web_push").
	Name() string

	// Enabled reports whether this channel is enabled in the user's live config.
	Enabled(cfg *config.Config) bool

	// SupportsKind reports whether this channel can carry items of kind.
	// WhatsApp returns false for "whatsapp" (link offline) since it cannot send
	// over a dead link; Web Push returns true for all kinds.
	SupportsKind(kind string) bool

	// Send delivers batch to the user. Return an error wrapping
	// ErrChannelUnavailable when the transport is offline/unlinked so the
	// dispatcher records store.AlertOffline instead of retry-storming.
	Send(ctx context.Context, batch Batch) error
}

// TextSender is an optional interface a Channel can implement to receive
// standalone text messages such as the Sunday weekly recap.
type TextSender interface {
	SendText(ctx context.Context, userID int64, text string) error
}

// DeliveryStore persists alert delivery and rate-limit state across restarts.
// *store.Store implements this interface.
type DeliveryStore interface {
	AlertDeliveries(ctx context.Context, userID int64, channel string) (map[string]store.AlertDelivery, error)
	AlertDeliveriesForItems(ctx context.Context, userID int64, channel string, itemIDs []string) (map[string]store.AlertDelivery, error)
	AlertDeliveriesByStatus(ctx context.Context, userID int64, status store.AlertStatus) ([]store.AlertDelivery, error)
	RecordAlertSentBatch(ctx context.Context, userID int64, channel string, itemIDs []string, at time.Time) error
	RecordAlertOutcome(ctx context.Context, userID int64, channel string, itemIDs []string, status store.AlertStatus, detail string, retryAfter, at time.Time) error
	CountAlertSendsSince(ctx context.Context, userID int64, channel string, since time.Time) (int, error)
	OldestAlertSendSince(ctx context.Context, userID int64, channel string, since time.Time) (time.Time, error)
}
