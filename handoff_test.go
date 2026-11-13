package main

import (
	"bytes"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/store"
)

func TestHandOff(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := store.Delivery{Date: "2026-11-12", Meal: "lunch", Peer: "919876543210@s.whatsapp.net", Amount: "60.00", ImageHash: "portal:x", ArmedAt: time.Now()}
	others := []types.JID{types.NewJID("911111111111", types.DefaultUserServer), types.NewJID("77", types.HiddenUserServer)}
	var out bytes.Buffer
	if err := handOff(t.Context(), st, slog.New(slog.NewTextHandler(io.Discard, nil)), &out, d, "Asha", resumeFlags(true, "asha", ""), others); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"HANDOFF 2026-11-12 lunch: take over the chat with Asha (+919876543210).",
		"https://wa.me/919876543210",
		"asked INR 60.00",
		"messwa deliver-qr -date 2026-11-12 -meal lunch -peer 919876543210@s.whatsapp.net -amount PRICE -portal-qr -user asha\n",
		"messwa release-handoff -date 2026-11-12 -meal lunch -user asha\n",
		"Also replied to the offer: +911111111111, 77@lid\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("notice lacks %q:\n%s", want, out.String())
		}
	}
	ds, err := st.Deliveries(t.Context())
	if err != nil || len(ds) != 1 || ds[0].State != "handed_off" || ds[0].Peer != d.Peer {
		t.Fatalf("ledger %+v, %v", ds, err)
	}

	// A buyer known only by LID gets no wa.me link.
	d.Meal, d.Peer = "dinner", "77@lid"
	notice := handoffNotice(d, "", "", nil)
	if strings.Contains(notice, "wa.me") || !strings.Contains(notice, "chat with 77@lid.") || strings.Contains(notice, "Also replied") {
		t.Errorf("LID notice:\n%s", notice)
	}
}

func TestResumeFlags(t *testing.T) {
	for _, tc := range []struct {
		portal   bool
		user, qr string
		want     string
	}{
		{true, "", "", " -portal-qr"},
		{true, "asha", "", " -portal-qr -user asha"},
		{false, "", "state/qr.png", " -qr state/qr.png"},
		{false, "", "", ""},
		{false, "asha", "", " -user asha"},
		{false, "asha", "state/qr.png", " -qr state/qr.png -user asha"},
	} {
		if got := resumeFlags(tc.portal, tc.user, tc.qr); got != tc.want {
			t.Errorf("resumeFlags(%v, %q, %q) = %q", tc.portal, tc.user, tc.qr, got)
		}
	}
}

func TestOfferQueueKeepsFirstName(t *testing.T) {
	q := offerQueue{seen: make(map[types.JID]bool), ready: make(chan struct{}, 1)}
	a := types.NewJID("100", types.DefaultUserServer)
	q.addFrom(a, a, "Asha")
	q.addFrom(a, a, "Someone else")
	if q.name(a) != "Asha" {
		t.Fatalf("name %q", q.name(a))
	}
	if peer, err := q.next(t.Context()); err != nil || peer != a {
		t.Fatalf("next %v, %v", peer, err)
	}
}
