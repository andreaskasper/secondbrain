package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
// Sessions, and why there are none
//
// An earlier version issued an Mcp-Session-Id at initialize and bound it to
// the hash of the access token that asked for it. That binding was the bug.
// Access tokens expire after token_ttl and the client quietly exchanges its
// refresh token for a new one; the new token hashes differently, so every
// session ever issued became permanently unusable with "unknown or mismatched
// session". Any conversation outliving token_ttl - twelve hours by default -
// died mid-sentence, and retrying could not bring it back.
//
// The session bought nothing in exchange for that. It held no state: every
// tool call names its own vault and path, and authorisation lives entirely in
// the bearer token, which is verified on every single request anyway. So the
// repair is not a better binding but no session at all. The spec permits a
// server to omit the session id at initialize, and a failure mode that cannot
// occur needs no recovery path.
//
// A client still sending a stale Mcp-Session-Id from before this change is
// answered normally rather than with 404. Returning 404 is the correct move
// for a server that does use sessions, but it only helps clients that
// implement the re-initialise path - and the client that uncovered this bug
// does not. Ignoring the header heals every one of them, without asking
// anybody to reconnect.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()

	if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(cfg, origin) {
		logWarn("origin_refused", map[string]any{"origin": origin, "ip": cfg.ClientIP(r)})
		writeHTTPError(w, http.StatusForbidden, "origin not allowed")
		return
	}

	user, ok := s.authenticate(w, r, cfg)
	if !ok {
		return
	}

	// The limiter runs before the method switch rather than after it. It used
	// to sit below, which left GET and DELETE outside it entirely - and GET
	// held a goroutine and a ticker for as long as the caller cared to keep
	// the connection open.
	if allowed, _ := s.toolLimiter.Allow(user.Name); !allowed {
		writeHTTPError(w, http.StatusTooManyRequests, "too many requests")
		return
	}

	switch r.Method {
	case http.MethodDelete:
		// There is no session to end, but a client that politely closes one
		// should get the answer it expects rather than an error.
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet:
		// A server with nothing to push may refuse the stream, and this one
		// has nothing to push: it advertises listChanged:false and never sent
		// a notification, so the stream only ever carried keep-alives.
		//
		// Logged because a client that keeps asking for a stream it will
		// never get is a client whose behaviour somebody should know about,
		// and because a 405 that appears nowhere is another silent refusal.
		logInfo("mcp_stream_refused", map[string]any{
			"user": user.Name, "user_agent": shortUA(r.UserAgent()),
		})
		w.Header().Set("Allow", "POST, DELETE")
		writeHTTPError(w, http.StatusMethodNotAllowed, "this server does not offer a notification stream")
		return
	case http.MethodPost:
	default:
		logInfo("mcp_method_refused", map[string]any{"user": user.Name, "method": r.Method})
		w.Header().Set("Allow", "POST, DELETE")
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := readLimited(r, 8<<20)
	if err != nil {
		writeRPC(w, rpcFail(nil, codeParseError, "request body too large or unreadable"))
		return
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		writeRPC(w, rpcFail(nil, codeInvalidRequest, "empty request"))
		return
	}

	if trimmed[0] == '[' {
		var batch []rpcRequest
		if err := json.Unmarshal(body, &batch); err != nil {
			writeRPC(w, rpcFail(nil, codeParseError, "malformed JSON-RPC batch"))
			return
		}
		var out []*rpcResponse
		for _, req := range batch {
			if resp := s.dispatch(req, user, cfg); resp != nil {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPCRaw(w, out)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, rpcFail(nil, codeParseError, "malformed JSON-RPC request"))
		return
	}
	resp := s.dispatch(req, user, cfg)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, resp)
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

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, cfg *Config) (*User, bool) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		reason := "no_authorization_header"
		if auth != "" {
			reason = "authorization_header_is_not_bearer"
		}
		s.refuse(w, r, cfg, reason)
		return nil, false
	}
	raw := strings.TrimSpace(auth[len(prefix):])
	tok := s.sessions.LookupAccess(raw)
	if tok == nil {
		// Three different situations arrive here and the client cannot tell
		// them apart, on purpose: an expired token, a token from before a
		// restart, and a token from a family killed by a reuse. What the
		// operator needs is not which one it was but that it happened.
		s.refuse(w, r, cfg, "unknown_or_expired_access_token")
		return nil, false
	}
	user, ok := cfg.Users[tok.User]
	if !ok {
		s.refuse(w, r, cfg, "user_no_longer_configured")
		return nil, false
	}
	return user, true
}

// refuse answers 401 and leaves a line in the log saying so.
//
// It used not to. A rejected request produced no output at all, which meant
// the two questions an operator asks when a connector stops working - is it
// reaching us, and is it being turned away - had the same answer in the log:
// silence. Diagnosing that once cost an afternoon and an SSH session. The
// line below is the whole fix. It names the reason, the caller and the
// client, and never the token or any part of it: a bearer token in a log
// file is a bearer token in a backup.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, cfg *Config, reason string) {
	s.metrics.AuthFailure()
	logWarn("auth_failed", map[string]any{
		"reason":     reason,
		"ip":         cfg.ClientIP(r),
		"method":     r.Method,
		"user_agent": shortUA(r.UserAgent()),
	})
	s.challenge(w, cfg)
}

// shortUA keeps the log line readable. A user agent is the only hint about
// which of several clients went quiet, so it is worth recording, and no
// client needs two hundred characters to identify itself.
func shortUA(ua string) string {
	ua = strings.TrimSpace(ua)
	if len(ua) > 80 {
		return ua[:80] + "..."
	}
	return ua
}

func (s *Server) challenge(w http.ResponseWriter, cfg *Config) {
	w.Header().Set("WWW-Authenticate",
		fmt.Sprintf(`Bearer resource_metadata=%q`, cfg.endpoint("/.well-known/oauth-protected-resource")))
	w.Header().Set("Cache-Control", "no-store")
	writeHTTPError(w, http.StatusUnauthorized, "authentication required")
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

func (s *Server) dispatch(req rpcRequest, user *User, cfg *Config) *rpcResponse {
	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		// No Mcp-Session-Id is minted and none is echoed back. See the note
		// at the top of this file for why.
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
		})

	case "notifications/initialized", "notifications/cancelled":
		return nil

	case "ping":
		if isNotification {
			return nil
		}
		return rpcOK(req.ID, map[string]any{})

	case "tools/list":
		return rpcOK(req.ID, map[string]any{"tools": toolDefinitions(user)})

	case "tools/call":
		return s.callTool(req, user, cfg)

	default:
		if isNotification {
			return nil
		}
		return rpcFail(req.ID, codeMethodNotFound, "unknown method: "+req.Method)
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

func writeRPC(w http.ResponseWriter, resp *rpcResponse) {
	writeRPCRaw(w, resp)
}

func writeRPCRaw(w http.ResponseWriter, v any) {
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
