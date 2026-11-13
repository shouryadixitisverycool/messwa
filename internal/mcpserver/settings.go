package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"messwa/internal/config"
)

// sellToolNames are the tools addSellTools adds.
var sellToolNames = []string{"start_sell_task", "sell_task_status", "cancel_sell_task", "set_sell_task_prices"}

// Settings returns the settings saved on the dashboard. Keys without a value
// come from config.toml.
func (s *Server) Settings(ctx context.Context) (config.Settings, error) {
	if s.st == nil {
		return config.Settings{}, nil
	}
	saved, _, err := s.st.Settings(ctx)
	return config.Settings(saved), err
}

// SaveSettings replaces the saved settings with all after checking them
// against config.toml, and makes them live. by is the admin saving them.
// Nothing changes when they don't check out.
func (s *Server) SaveSettings(ctx context.Context, all config.Settings, by int64) error {
	if s.st == nil {
		return errors.New("no database")
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if _, err := s.cfg.File().WithSettings(all); err != nil {
		return err
	}
	if err := s.st.SaveSettings(ctx, all, by); err != nil {
		return err
	}
	return s.applySettingsLocked(ctx)
}

// ReloadSettings picks up settings another process saved, such as messwa
// dashboard while run is up. It does nothing when they haven't changed.
func (s *Server) ReloadSettings(ctx context.Context) error {
	if s.st == nil {
		return nil
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	v, err := s.st.SettingsVersion(ctx)
	if err != nil || v == s.settingsVer {
		return err
	}
	return s.applySettingsLocked(ctx)
}

func (s *Server) applySettingsLocked(ctx context.Context) error {
	saved, v, err := s.st.Settings(ctx)
	if err != nil {
		return err
	}
	// Record the version even when it fails, so a bad save is reported once.
	s.settingsVer = v
	if _, err := s.cfg.Update(config.Settings(saved)); err != nil {
		return err
	}
	s.syncSellTools()
	return nil
}

// syncSellTools adds or removes the sell task tools to match the live
// sell_tasks setting. Connected clients get a tools/list_changed notice.
func (s *Server) syncSellTools() {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	if s.mcp == nil {
		return
	}
	switch on := s.sellEnabled(); {
	case on && !s.sellTools:
		s.addSellTools(s.mcp)
	case !on && s.sellTools:
		s.mcp.RemoveTools(sellToolNames...)
	default:
		return
	}
	s.sellTools = !s.sellTools
}

// trackMCP remembers srv as the server syncSellTools changes.
func (s *Server) trackMCP(srv *mcp.Server, sellTools bool) {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	s.mcp, s.sellTools = srv, sellTools
}

// GroupNames returns the name of each group messwa has seen, by JID.
func (s *Server) GroupNames(ctx context.Context) (map[string]string, error) {
	if s.st == nil {
		return map[string]string{}, nil
	}
	return s.st.GroupNames(ctx)
}
