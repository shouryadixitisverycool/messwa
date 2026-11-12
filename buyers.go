package main

import (
	"context"
	"log/slog"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/config"
)

// turnsAway reports whether the [sell] buyers setting refuses this person.
// With unsaved_only it refuses anyone in your address book, the same
// "saved" that decides which DMs keep their text. peers are every JID the
// person is known by, such as their number and the LID chat they wrote
// from; each is also checked under its other form.
func turnsAway(ctx context.Context, conn saleConn, buyers config.Buyers, peers ...types.JID) bool {
	return buyers.UnsavedOnly() && conn.SavedContact(ctx, peers...)
}

// logSavedSkip records that a saved contact replied and got nothing.
func logSavedSkip(log *slog.Logger, peer types.JID, attrs ...any) {
	log.Info("a saved contact replied to the offer; skipped them and sent nothing", append([]any{"buyer", peer.String()}, attrs...)...)
}

// savedContactFilter is a queue join hook for deliver-qr: with
// unsaved_only, saved contacts never join the buyer queue.
func savedContactFilter(ctx context.Context, conn saleConn, log *slog.Logger, buyers config.Buyers) func(peer, chat types.JID) bool {
	if !buyers.UnsavedOnly() {
		return nil
	}
	return func(peer, chat types.JID) bool {
		if turnsAway(ctx, conn, buyers, peer, chat) {
			logSavedSkip(log, peer)
			return false
		}
		return true
	}
}
