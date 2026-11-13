package mcpserver

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"messwa/internal/config"
)

// DeviceAccount is implemented by the runtime that owns this user's client.
type DeviceAccount interface {
	Linked() bool
	Status() any
	Link(context.Context) error
	Unlink(context.Context) error
}

func (s *Server) BindAccount(id int64, device DeviceAccount) { s.boundUser = id; s.Device = device }
func (s *Server) Scoped() bool                               { return s.boundUser > 0 }
func (s *Server) Config() *config.Config                     { return s.cfg }
func (s *Server) checkAccount(id int64) error {
	if s.boundUser > 0 && id != s.boundUser {
		return errors.New("account mismatch")
	}
	return nil
}

// MCPServer returns the stable server used by this account's HTTP sessions.
func (s *Server) MCPServer() *mcp.Server {
	s.transportOnce.Do(func() { s.transportMCP = s.MCP() })
	return s.transportMCP
}
