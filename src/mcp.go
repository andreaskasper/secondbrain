package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	mcpProtocolVersion = "2025-06-18"
	serverName         = "secondbrain"
)

// ---------------------------------------------------------------------------
// JSON-RPC
// ---------------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

func rpcOK(id json.RawMessage, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func rpcFail(id json.RawMessage, code int, msg string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// ---------------------------------------------------------------------------
// MCP sessions
// ---------------------------------------------------------------------------

// mcpSessions binds an Mcp-Session-Id to the access token that created it, so
// that a session cannot be used - or ended - by anyone else.
type mcpSessions struct {
	mu   sync.Mutex
	byID map[string]string
	seen map[string]time.Time
}

func newMCPSessions() *mcpSessions {
	return &mcpSessions{byID: map[string]string{}, seen: map[string]time.Time{}}
}

func (m *mcpSessions) create(tokenHash string) string {
	id := randToken(16)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[id] = tokenHash
	m.seen[id] = time.Now()
	return id
}

func (m *mcpSessions) valid(id, tokenHash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.byID[id]
	if !ok {
		return false
	}
	m.seen[id] = time.Now()
	return h == tokenHash
}

// drop ends a session, but only for the token that owns it. Without the
// ownership check any authenticated caller could end a session belonging to
// somebody else simply by naming it.
func (m *mcpSessions) drop(id, tokenHash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.byID[id]; !ok || h != tokenHash {
		return false
	}
	delete(m.byID, id)
	delete(m.seen, id)
	return true
}

func (m *mcpSessions) sweep() {
	cutoff := time.Now().Add(-24 * time.Hour)
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, t := range m.seen {
		if t.Before(cutoff) {
			delete(m.byID, id)
			delete(m.seen, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()

	if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(cfg, origin) {
		logWarn("origin_refused", map[string]any{"origin": origin, "ip": clientIP(r)})
		writeHTTPError(w, http.StatusForbidden, "origin not allowed")
		return
	}

	user, tokenHash, ok := s.authenticate(w, r, cfg)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodDelete:
		if id := r.Header.Get("Mcp-Session-Id"); id != "" {
			s.mcp.drop(id, tokenHash)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet:
		s.streamNotifications(w, r)
		return
	case http.MethodPost:
	default:
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if allowed, _ := s.toolLimiter.Allow(user.Name); !allowed {
		writeHTTPError(w, http.StatusTooManyRequests, "too many requests")
		return
	}

	body, err := readLimited(r, 8<<20)
	if err != nil {
		writeRPC(w, "", rpcFail(nil, codeParseError, "request body too large or unreadable"))
		return
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		writeRPC(w, "", rpcFail(nil, codeInvalidRequest, "empty request"))
		return
	}

	if trimmed[0] == '[' {
		var batch []rpcRequest
		if err := json.Unmarshal(body, &batch); err != nil {
			writeRPC(w, "", rpcFail(nil, codeParseError, "malformed JSON-RPC batch"))
			return
		}
		var out []*rpcResponse
		sessionID := ""
		for _, req := range batch {
			resp, sid := s.dispatch(r, req, user, tokenHash, cfg)
			if sid != "" && sessionID == "" {
				sessionID = sid
			}
			if resp != nil {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPCRaw(w, sessionID, out)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, "", rpcFail(nil, codeParseError, "malformed JSON-RPC request"))
		return
	}
	resp, sessionID := s.dispatch(r, req, user, tokenHash, cfg)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, sessionID, resp)
}

// originAllowed implements the DNS-rebinding guard, opt-in for the same reason
// as in aegis: /mcp is bearer-protected on a public host, so refusing every
// foreign Origin breaks hosted clients without buying anything.
func (s *Server) originAllowed(cfg *Config, origin string) bool {
	if len(cfg.AllowedOrigins) == 0 {
		return true
	}
	if strings.EqualFold(origin, cfg.Issuer()) {
		return true
	}
	for _, allowed := range cfg.AllowedOrigins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, cfg *Config) (*User, string, bool) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		s.challenge(w, cfg)
		return nil, "", false
	}
	raw := strings.TrimSpace(auth[len(prefix):])
	tok := s.sessions.LookupAccess(raw)
	if tok == nil {
		s.challenge(w, cfg)
		return nil, "", false
	}
	user, ok := cfg.Users[tok.User]
	if !ok {
		s.challenge(w, cfg)
		return nil, "", false
	}
	return user, hashToken(raw), true
}

func (s *Server) challenge(w http.ResponseWriter, cfg *Config) {
	w.Header().Set("WWW-Authenticate",
		fmt.Sprintf(`Bearer resource_metadata=%q`, cfg.endpoint("/.well-known/oauth-protected-resource")))
	writeHTTPError(w, http.StatusUnauthorized, "authentication required")
}

func (s *Server) streamNotifications(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusNotImplemented, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

func (s *Server) dispatch(r *http.Request, req rpcRequest, user *User, tokenHash string, cfg *Config) (*rpcResponse, string) {
	isNotification := len(req.ID) == 0

	sid := r.Header.Get("Mcp-Session-Id")
	if sid != "" && req.Method != "initialize" {
		if !s.mcp.valid(sid, tokenHash) {
			return rpcFail(req.ID, codeInvalidRequest, "unknown or mismatched session"), ""
		}
	}

	switch req.Method {
	case "initialize":
		// Reuse a session the caller already holds instead of minting a new
		// one on every reconnect; otherwise a flaky client leaves a trail of
		// live sessions behind it.
		newSID := sid
		if newSID == "" || !s.mcp.valid(newSID, tokenHash) {
			newSID = s.mcp.create(tokenHash)
		}
		return rpcOK(req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"serverInfo":      map[string]any{"name": serverName, "version": version},
			// listChanged is false on purpose: the tool list is static for
			// the life of a connection, and advertising a notification that
			// is never sent invites a client to wait for one.
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"instructions": s.instructions(user),
		}), newSID

	case "notifications/initialized", "notifications/cancelled":
		return nil, ""

	case "ping":
		if isNotification {
			return nil, ""
		}
		return rpcOK(req.ID, map[string]any{}), ""

	case "tools/list":
		return rpcOK(req.ID, map[string]any{"tools": toolDefinitions(user)}), ""

	case "tools/call":
		return s.callTool(req, user, cfg), ""

	default:
		if isNotification {
			return nil, ""
		}
		return rpcFail(req.ID, codeMethodNotFound, "unknown method: "+req.Method), ""
	}
}

// instructions is what the model reads before it does anything. The generic
// half explains the tools; the rest comes from each vault's own
// instructions.md, so the conventions of a knowledge base travel with it
// rather than living in a system prompt somewhere else.
func (s *Server) instructions(u *User) string {
	var b strings.Builder
	b.WriteString(`secondbrain stores knowledge as plain Markdown files in one or more vaults.

Ground rules:
  - Search before you write. Duplicate notes are the main way a knowledge base
    decays, and note_search plus note_related will find an existing note faster
    than you can write a new one.
  - Read before you edit, and pass the content_hash you were given back in.
    Other people and other programs write into these files too.
  - Prefer note_edit and note_section_edit over note_write. Rewriting a whole
    note to change a sentence is how paragraphs disappear.
  - Use dry_run on anything that touches more than one file. It returns the
    diff without writing.
  - note_outline is cheap and note_read is not. Look at the shape of a long
    note before pulling all of it into context.

Vault selection: every tool takes an optional "vault". Omitting it uses `)
	b.WriteString(`"` + s.Config().DefaultVault + `"` + ".\n")

	vaults := s.vaults.List(u)
	if len(vaults) > 0 {
		names := make([]string, 0, len(vaults))
		for _, v := range vaults {
			names = append(names, v.Name)
		}
		b.WriteString("Available vaults: " + strings.Join(names, ", ") + "\n")
	}
	if u != nil && u.ReadOnly {
		b.WriteString("\nThis connection is read-only. No tool that modifies the vault is offered.\n")
	}
	for _, v := range vaults {
		if instr := v.Instructions(); instr != "" {
			b.WriteString("\n--- conventions for vault \"" + v.Name + "\" ---\n")
			b.WriteString(instr)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

func writeRPC(w http.ResponseWriter, sessionID string, resp *rpcResponse) {
	writeRPCRaw(w, sessionID, resp)
}

func writeRPCRaw(w http.ResponseWriter, sessionID string, v any) {
	if sessionID != "" {
		w.Header().Set("Mcp-Session-Id", sessionID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func readLimited(r *http.Request, limit int64) ([]byte, error) {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("body too large")
	}
	return b, nil
}

func (m *mcpSessions) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byID)
}
