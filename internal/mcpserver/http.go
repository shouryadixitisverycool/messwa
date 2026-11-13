package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPHandler serves MCP over streamable HTTP. Every request needs a bearer
// token: a user's own MCP token, or legacy (MESSWA_MCP_TOKEN), which acts as
// the owner. With anonymous set, a request with no Authorization header also
// acts as the owner; run allows that only on loopback. Call SetAccounts
// first.
func (s *Server) HTTPHandler(legacy string, anonymous bool) (http.Handler, error) {
	if s.acc == nil {
		return nil, errors.New("mcpserver: HTTPHandler needs SetAccounts")
	}
	srv := s.MCP()
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if s.AccountServer == nil {
			return srv
		}
		info := mcpauth.TokenInfoFromContext(r.Context())
		if info == nil {
			return nil
		}
		id, err := strconv.ParseInt(info.UserID, 10, 64)
		if err != nil {
			return nil
		}
		account, err := s.AccountServer(r.Context(), id)
		if err != nil {
			return nil
		}
		return account.MCPServer()
	}, nil)

	// Tokenless requests get a random per-process token, so they pass
	// through the same verifier and session binding as everyone else.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	internal := "internal_" + hex.EncodeToString(b)
	same := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

	verify := func(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		if (anonymous && same(token, internal)) || (legacy != "" && same(token, legacy)) {
			u, err := s.acc.Owner(ctx)
			if err != nil {
				return nil, err
			}
			if s.AccountServer == nil {
				return &mcpauth.TokenInfo{UserID: ownerID}, nil
			}
			return &mcpauth.TokenInfo{UserID: strconv.FormatInt(u.ID, 10)}, nil
		}
		u, err := s.acc.VerifyToken(ctx, token)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return nil, fmt.Errorf("%w: unknown or revoked token", mcpauth.ErrInvalidToken)
		}
		return &mcpauth.TokenInfo{UserID: strconv.FormatInt(u.ID, 10)}, nil
	}
	authed := mcpauth.RequireBearerToken(verify, &mcpauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if anonymous && r.Header.Get("Authorization") == "" {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+internal)
		}
		authed.ServeHTTP(w, r)
	}), nil
}
