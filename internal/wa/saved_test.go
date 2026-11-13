package wa

import (
	"context"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"
)

func testDevice(t *testing.T) *store.Device {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session.db")
	container, err := sqlstore.New(ctx, "sqlite", "file:"+path+"?_pragma=foreign_keys(1)", waLog.Noop)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { container.Close() })
	dev := container.NewDevice()
	own := types.NewJID("919999999999", types.DefaultUserServer)
	dev.ID = &own
	dev.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := container.PutDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	return dev
}

func TestSavedContactFollowsLIDs(t *testing.T) {
	ctx := context.Background()
	dev := testDevice(t)
	friendPN := types.NewJID("911111111111", types.DefaultUserServer)
	friendLID := types.NewJID("111111111111111", types.HiddenUserServer)
	strangerPN := types.NewJID("912222222222", types.DefaultUserServer)
	strangerLID := types.NewJID("222222222222222", types.HiddenUserServer)
	lidSavedPN := types.NewJID("913333333333", types.DefaultUserServer)
	lidSavedLID := types.NewJID("333333333333333", types.HiddenUserServer)
	if err := dev.Contacts.PutContactName(ctx, friendPN, "Asha", "Asha Friend"); err != nil {
		t.Fatal(err)
	}
	if err := dev.Contacts.PutContactName(ctx, lidSavedLID, "Ravi", "Ravi"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dev.Contacts.PutPushName(ctx, strangerPN, "Stranger"); err != nil {
		t.Fatal(err)
	}
	for lid, pn := range map[types.JID]types.JID{friendLID: friendPN, strangerLID: strangerPN, lidSavedLID: lidSavedPN} {
		if err := dev.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		jids  []types.JID
		saved bool
		shown string
	}{
		{"saved by number", []types.JID{friendPN}, true, "Asha Friend"},
		{"saved number seen by LID", []types.JID{friendLID}, true, "Asha Friend"},
		{"saved by LID, seen by number", []types.JID{lidSavedPN}, true, "Ravi"},
		{"device suffix", []types.JID{{User: friendLID.User, Server: types.HiddenUserServer, Device: 12}}, true, "Asha Friend"},
		{"unsaved with a push name", []types.JID{strangerLID}, false, "Stranger"},
		{"unknown", []types.JID{types.NewJID("914444444444", types.DefaultUserServer)}, false, ""},
		{"nothing", nil, false, ""},
	} {
		saved, shown := SavedContact(ctx, dev, tc.jids...)
		if saved != tc.saved || shown != tc.shown {
			t.Errorf("%s: saved=%v name=%q, want %v %q", tc.name, saved, shown, tc.saved, tc.shown)
		}
	}
}

func TestSavedContactBeforePairing(t *testing.T) {
	for _, dev := range []*store.Device{nil, {}} {
		if saved, name := SavedContact(t.Context(), dev, types.NewJID("911111111111", types.DefaultUserServer)); saved || name != "" {
			t.Fatalf("unpaired device returned contact: %v %q", saved, name)
		}
	}
}
