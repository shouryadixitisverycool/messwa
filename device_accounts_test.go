package main

import (
	"context"
	"io"
	"log/slog"
	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/dashboard"
	"messwa/internal/store"
	"messwa/internal/wa"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAccountConfigKeepsLegacySessionAndIsolatesNewDefaults(t *testing.T) {
	ctx := t.Context()
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	cfg.MCP.SellTasks = true
	cfg.Sell.AcceptPaymentImages = true
	st, err := store.Open(cfg.DataDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admin, err := st.AddUser(ctx, "admin", "admin", "hash", "active", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	member, err := st.AddUser(ctx, "member", "member", "hash", "active", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.SessionDB(), []byte("legacy session keys"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := accountConfig(ctx, cfg, st, admin)
	if err != nil {
		t.Fatal(err)
	}
	b, err := accountConfig(ctx, cfg, st, member)
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionDB() != cfg.SessionDB() {
		t.Fatal("upgrade moved legacy session")
	}
	if raw, err := os.ReadFile(a.SessionDB()); err != nil || string(raw) != "legacy session keys" {
		t.Fatal("legacy keys changed")
	}
	if b.StateDir != filepath.Join(cfg.StateDir, "user-2") || b.Sell.AcceptPaymentImages || b.MCP.SellTasks {
		t.Fatalf("new account inherited private defaults: %+v", b)
	}
	if _, err := a.Update(config.Settings{"sell.accept_plain_dms": []byte("true")}); err != nil {
		t.Fatal(err)
	}
	if b.Live().Sell.AcceptPlainDMs || cfg.Live().Sell.AcceptPlainDMs {
		t.Fatal("live settings crossed account boundary")
	}
}

// Exercise the production wiring: dashboard Device, MCP contacts, task
// connection, send closure and connection waiter keep one stable handle.
func TestDeviceAccountUnlinkRewiresAllConsumers(t *testing.T) {
	ctx := t.Context()
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	st, err := store.Open(cfg.DataDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.AddUser(ctx, "admin", "admin", "hash", "active", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := st.AddUser(ctx, "other", "member", "hash", "active", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(st, "0123456789012345678901234567890101234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	m := &deviceAccounts{ctx: ctx, cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil)), st: st, acc: acc, hub: dashboard.NewHub(), users: map[int64]*deviceAccount{}}
	defer m.close()
	srv, err := m.server(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.server(ctx, otherID); err != nil {
		t.Fatal(err)
	}
	a, other := m.users[id], m.users[otherID]
	var old, otherRaw *wa.Client
	_ = a.client.Do(func(c *wa.Client) error { old = c; return nil })
	_ = other.client.Do(func(c *wa.Client) error { otherRaw = c; return nil })
	handle := a.client
	for range 2 {
		if err := srv.Device.Unlink(ctx); err != nil {
			t.Fatal(err)
		}
		if a.client != handle || srv.Device != a || a.sell.conn.(accountConn).Runtime != handle {
			t.Fatal("consumer lost stable account handle")
		}
		if p := reflect.ValueOf(srv).Elem().FieldByName("contacts").Elem().Pointer(); p != reflect.ValueOf(handle).Pointer() {
			t.Fatal("MCP contacts bypass account handle")
		}
		if err := handle.Do(func(c *wa.Client) error {
			if c == old || c.WA == old.WA || c.WA.Store == old.WA.Store {
				t.Fatal("client or store reused")
			}
			if n := reflect.ValueOf(c.WA).Elem().FieldByName("eventHandlers").Len(); n != 3 {
				t.Fatalf("handlers registered %d times", n)
			}
			// QR setup is the first relink step. Cancel before any network access.
			qrCtx, cancel := context.WithCancel(ctx)
			_, err := c.WA.GetQRChannel(qrCtx)
			cancel()
			if err != nil {
				return err
			}
			old = c
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		status := srv.Device.Status().(map[string]any)
		if status["linked"] != false || status["connected"] != false {
			t.Fatal(status)
		}
		if a.sell.connected(0) {
			t.Fatal("fresh device connected without pairing")
		}
		if err := other.client.Do(func(c *wa.Client) error {
			if c != otherRaw {
				t.Fatal("other account replaced")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
