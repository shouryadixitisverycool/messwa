package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/dashboard"
	"messwa/internal/ingest"
	"messwa/internal/mcpserver"
	"messwa/internal/store"
	"messwa/internal/wa"
	"rsc.io/qr"
)

type deviceAccounts struct {
	readOnly bool
	clock    func() time.Time
	mu       sync.Mutex
	ctx      context.Context
	cfg      *config.Config
	log      *slog.Logger
	st       *store.Store
	acc      *accounts.Accounts
	hub      *dashboard.Hub
	users    map[int64]*deviceAccount
}
type deviceAccount struct {
	id int64
	// started and unlinking are guarded by deviceAccounts.mu.
	started   bool
	unlinking bool

	mu      sync.Mutex
	ctx     context.Context
	stop    context.CancelFunc
	client  *wa.Runtime
	srv     *mcpserver.Server
	st      *store.Store
	sell    *sellManager
	pairing bool
	// pairStart, stopPair and done belong to the running pairing session;
	// done closes when it ends. code is the phone-number pairing code for
	// it, when one was asked for.
	pairStart time.Time
	stopPair  context.CancelFunc
	done      chan struct{}
	code      string
	qr        string
	err       string
}

func accountConfig(ctx context.Context, cfg *config.Config, st *store.Store, id int64) (*config.Config, error) {
	dir, err := st.AccountDir(ctx, cfg.StateDir, id)
	if err != nil {
		return nil, err
	}
	// A fresh live pointer prevents one user's settings affecting another.
	c, err := cfg.Live().WithSettings(nil)
	if err != nil {
		return nil, err
	}
	c.IsolateSettings()
	c.StateDir = dir
	if dir != cfg.StateDir {
		c.MCP.SellTasks = false
		c.Sell = config.Default().Sell
		c.Sales = config.Sales{}
	}
	if err = config.EnsureDir(dir); err != nil {
		return nil, err
	}
	return c, nil
}

func (m *deviceAccounts) server(ctx context.Context, id int64) (*mcpserver.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, err := m.acc.User(ctx, id)
	if err != nil {
		return nil, err
	}
	if u == nil || u.Disabled {
		return nil, fmt.Errorf("account unavailable")
	}
	if a := m.users[id]; a != nil {
		m.settle(a, u)
		return a.srv, nil
	}
	cfg, err := accountConfig(ctx, m.cfg, m.st, id)
	if err != nil {
		return nil, err
	}
	var st *store.Store
	if cfg.StateDir == m.cfg.StateDir {
		st = m.st
	} else {
		st, err = store.OpenPrivate(cfg.DataDB(), m.st)
		if err != nil {
			return nil, err
		}
	}
	ready := false
	defer func() {
		if !ready && st != m.st {
			st.Close()
		}
	}()
	saved, _, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	cfg.ApplySaved(config.Settings(saved))
	if err = st.AssignLegacyTasks(ctx, id); err != nil {
		return nil, err
	}
	accountLog := m.log.With("user_id", id)
	c, err := wa.Open(m.ctx, cfg, accountLog)
	if err != nil {
		return nil, err
	}
	accountCtx, stop := context.WithCancel(m.ctx)
	a := &deviceAccount{ctx: accountCtx, st: st, stop: stop}
	in := ingest.New(cfg, st)
	in.OnChange(m.hub.Notify)
	client := wa.NewRuntime(accountCtx, c, in, st)
	a.client = client
	send := func(ctx context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
		if err := accountCtx.Err(); err != nil {
			return whatsmeow.SendResponse{}, err
		}
		user, err := m.acc.User(ctx, id)
		if err != nil {
			return whatsmeow.SendResponse{}, err
		}
		if user == nil || user.Disabled {
			return whatsmeow.SendResponse{}, fmt.Errorf("account disabled")
		}
		var resp whatsmeow.SendResponse
		err = client.Do(func(c *wa.Client) error {
			var err error
			resp, err = c.WA.SendMessage(ctx, to, msg, extra...)
			return err
		})
		return resp, err
	}
	sell := newSellManager(accountCtx, cfg, accountLog, st, accountConn{client}, send, client.WaitForConnection,
		func(ctx context.Context, userID int64) (mealPortal, error) {
			if userID != id {
				return nil, fmt.Errorf("account mismatch")
			}
			return proxyPortal{stateDir: cfg.StateDir, real: func(ctx context.Context) (mealPortal, error) { return m.acc.Portal(ctx, id) }, friends: st.FriendMeal}, nil
		}, newOwnGroupLog(accountCtx, cfg, st, client))
	sell.notify = m.hub.Notify
	a.sell = sell
	a.srv = mcpserver.New(cfg, st, client)
	a.srv.SetAccounts(m.acc)
	if m.clock != nil {
		a.srv.SetClock(m.clock)
	}
	a.srv.SetSellTasks(sell)
	a.srv.BindAccount(id, a)
	if m.readOnly {
		a.srv.Device = nil
		a.srv.SetSellTasks(nil)
	}
	ready = true
	a.id = id
	m.users[id] = a
	go reloadSettings(accountCtx, m.log, a.srv)
	m.settle(a, u)
	return a.srv, nil
}

// settle brings a user's WhatsApp client in line with their sign-up status.
// An approved account's linked client connects and resumes its sell tasks,
// once. One an admin declined gets its device unlinked. Accounts still
// waiting stay as they are: they can link, but run doesn't connect them at
// start. Every dashboard request looks the user up, so an approval from the
// CLI takes effect on that user's next page load. m.mu must be held.
func (m *deviceAccounts) settle(a *deviceAccount, u *accounts.User) {
	if m.readOnly {
		return
	}
	switch {
	case u.IsActive() && !a.started && a.client.LoggedIn():
		a.started = true
		if err := a.sell.Resume(a.ctx, a.srv.Config().MCP.SellTasks); err != nil {
			m.log.Error("resume user's tasks", "user", a.id, "err", err)
		}
		if !a.client.IsConnected() {
			a.client.Reconnect()
		}
	case u.Status == accounts.Declined && a.client.LoggedIn() && !a.unlinking:
		a.unlinking = true
		go func() {
			if err := a.Unlink(a.ctx); err != nil {
				m.log.Error("unlink declined user's WhatsApp", "user", a.id, "err", err)
			}
			m.mu.Lock()
			a.unlinking = false
			m.mu.Unlock()
		}()
	}
}

// decided is the accounts.OnDecision hook: it settles the user's client
// right away instead of on their next page load.
func (m *deviceAccounts) decided(ctx context.Context, id int64) {
	if _, err := m.server(ctx, id); err != nil {
		m.log.Error("settle user's WhatsApp after a decision", "user", id, "err", err)
	}
}

func (a *deviceAccount) Linked() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.client.LoggedIn() }
func (a *deviceAccount) Status() any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{"linked": a.client.LoggedIn(), "connected": a.client.IsConnected(), "pairing": a.pairing, "qr": a.qr, "code": a.code, "error": a.err}
}
func (a *deviceAccount) Link(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client.LoggedIn() {
		return fmt.Errorf("already linked")
	}
	if a.pairing {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	var ch <-chan whatsmeow.QRChannelItem
	err := a.client.Do(func(c *wa.Client) error { var err error; ch, err = c.WA.GetQRChannel(ctx); return err })
	if err != nil {
		cancel()
		return err
	}
	a.pairing = true
	a.pairStart = time.Now()
	a.stopPair = cancel
	a.done = make(chan struct{})
	a.err = ""
	a.code = ""
	done := a.done
	go func() {
		defer close(done)
		defer cancel()
		if err := a.client.Do(func(c *wa.Client) error { return c.WA.Connect() }); err != nil {
			a.mu.Lock()
			a.err = err.Error()
			a.pairing = false
			a.mu.Unlock()
			return
		}
		for item := range ch {
			a.mu.Lock()
			if item.Event == "code" {
				code, err := qr.Encode(item.Code, qr.M)
				if err == nil {
					a.qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())
				}
			}
			if item.Error != nil {
				a.err = item.Error.Error()
			}
			a.mu.Unlock()
		}
		a.mu.Lock()
		a.pairing = false
		a.qr = ""
		a.code = ""
		a.mu.Unlock()
	}()
	return nil
}

// LinkCode links by phone number instead of QR: it returns a code to type
// into WhatsApp on the phone with that number. A code only lasts as long as
// its pairing session, about 160 seconds from the start, so a session older
// than a minute is restarted first.
func (a *deviceAccount) LinkCode(ctx context.Context, phone string) (string, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if len(digits) == 10 {
		digits = "91" + digits // a bare Indian mobile number
	}
	if len(digits) < 11 || len(digits) > 15 || digits[0] == '0' {
		return "", fmt.Errorf("enter your WhatsApp number with its country code, like +91 98765 43210")
	}
	a.mu.Lock()
	old, stop := a.done, a.stopPair
	stale := a.pairing && time.Since(a.pairStart) > time.Minute
	a.mu.Unlock()
	if stale {
		stop()
		select {
		case <-old:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err := a.Link(ctx); err != nil {
		return "", err
	}
	// The session is ready for a code once it has shown its first QR.
	for deadline := time.Now().Add(20 * time.Second); ; {
		a.mu.Lock()
		ready, pairing := a.qr != "", a.pairing
		a.mu.Unlock()
		if ready {
			break
		}
		if !pairing || time.Now().After(deadline) {
			return "", fmt.Errorf("couldn't reach WhatsApp to get a code; try again")
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	var code string
	err := a.client.Do(func(c *wa.Client) error {
		var err error
		code, err = c.WA.PairPhone(ctx, digits, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
		return err
	})
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.code = code
	a.mu.Unlock()
	return code, nil
}
func (a *deviceAccount) Unlink(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pairing {
		return fmt.Errorf("wait for pairing to finish before unlinking")
	}
	tasks, err := a.st.SellTasks(ctx, time.Time{})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.State == store.TaskActive {
			if err := a.sell.Cancel(ctx, t.ID, false); err != nil {
				return err
			}
		}
	}
	if err := a.client.Unlink(ctx); err != nil {
		return err
	}
	a.qr = ""
	a.err = ""
	return nil
}
func (m *deviceAccounts) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.users {
		if a.stop != nil {
			a.stop()
		}
		a.client.Close()
		if a.st != m.st {
			a.st.Close()
		}
	}
}

func (m *deviceAccounts) prune(ctx context.Context, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, a := range m.users {
		if a.st != m.st {
			if err := a.st.Prune(ctx, now.Add(-a.srv.Config().Live().DMs.Retention.Duration), now.Add(-14*24*time.Hour)); err != nil {
				m.log.Error("prune user's data", "user", id, "err", err)
			}
		}
	}
}
