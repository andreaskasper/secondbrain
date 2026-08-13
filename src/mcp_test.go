package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// testServer builds a server complete enough to serve /mcp and /healthz, and
// hands back one live access token for it.
func testServer(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	v := testVault(t)
	cfg := &Config{
		Listen:           ":0",
		PublicURL:        "https://sb.example",
		DataDir:          filepath.Dir(v.Root),
		DefaultVault:     "default",
		MaxResponseBytes: 1 << 20,
		TokenTTL:         12 * time.Hour,
		CodeTTL:          60 * time.Second,
		Users:            map[string]*User{"test": {Name: "test"}},
		Source:           "test",
	}
	v.metrics = NewMetrics()
	s := &Server{
		vaults: &VaultManager{
			root: cfg.DataDir, defaultVault: "default", cfg: cfg,
			vaults: map[string]*Vault{"default": v},
		},
		sessions:  NewSessionStore(),
		metrics:   v.metrics,
		stop:      make(chan struct{}),
		startedAt: time.Now(),
	}
	s.setConfig(cfg)
	rate, _ := ParseRate("600/m")
	s.toolLimiter = NewKeyedLimiter(rate)
	s.loginLimiter = NewKeyedLimiter(rate)
	s.registerLimiter = NewKeyedLimiter(rate)
	s.metricsLimiter = NewKeyedLimiter(rate)
	t.Cleanup(func() { close(s.stop) })

	access, _ := s.sessions.IssueTokens("test", "client-1", "", cfg.TokenTTL)
	return s, s.routes(), access
}

// post sends one JSON-RPC body to /mcp. sessionID, when not empty, is sent as
// Mcp-Session-Id - which is how a client that predates the stateless change
// behaves.
func post(t *testing.T, h http.Handler, token, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		r.Header.Set("Mcp-Session-Id", sessionID)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeOne(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not a JSON object: %v\n%s", err, w.Body.String())
	}
	return out
}

func mustNotBeRPCError(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", w.Code, w.Body.String())
	}
	out := decodeOne(t, w)
	if e, ok := out["error"]; ok {
		t.Fatalf("unexpected JSON-RPC error: %v", e)
	}
	return out
}

// ---------------------------------------------------------------------------
// The bug this file exists for
// ---------------------------------------------------------------------------

// A session id bound to the access token hash meant that the ordinary,
// expected event of a token refresh killed every session the server had
// issued. The server no longer issues any.
func TestInitializeIssuesNoSessionID(t *testing.T) {
	_, h, tok := testServer(t)

	w := post(t, h, tok, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	out := mustNotBeRPCError(t, w)

	if got := w.Header().Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("server handed out a session id %q; it must not", got)
	}
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result object: %s", w.Body.String())
	}
	if res["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("protocolVersion = %v, want %s", res["protocolVersion"], mcpProtocolVersion)
	}
	if _, ok := res["instructions"].(string); !ok {
		t.Error("initialize should carry instructions for the model")
	}
}

// The regression test proper: a client holding a session id from before the
// change keeps sending it. That used to be answered with "unknown or
// mismatched session" forever. It must now simply work.
func TestStaleSessionIDIsIgnored(t *testing.T) {
	_, h, tok := testServer(t)

	stale := randToken(16)
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{}}`,
	} {
		w := post(t, h, tok, stale, body)
		out := mustNotBeRPCError(t, w)
		if _, ok := out["result"]; !ok {
			t.Fatalf("no result for %s: %s", body, w.Body.String())
		}
	}
}

// The exact sequence that broke: work, wait past token_ttl, refresh, work
// again. The refreshed token is a different token with a different hash, and
// that must not matter.
func TestWorkSurvivesTokenRotation(t *testing.T) {
	s, h, first := testServer(t)

	if w := post(t, h, first, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`); w.Code != 200 {
		t.Fatalf("initialize failed: %d %s", w.Code, w.Body.String())
	}

	// Mint the refresh token the same way the token endpoint does, then
	// rotate it.
	_, refresh := s.sessions.IssueTokens("test", "client-2", "", time.Hour)
	tok, ok, reuse := s.sessions.RotateRefresh(refresh, "client-2")
	if !ok || reuse {
		t.Fatalf("rotate failed: ok=%v reuse=%v", ok, reuse)
	}
	second, _ := s.sessions.IssueTokens(tok.User, "client-2", tok.Family, time.Hour)

	if second == first {
		t.Fatal("a refresh must produce a different access token, otherwise this test proves nothing")
	}

	w := post(t, h, second, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	out := mustNotBeRPCError(t, w)
	res := out["result"].(map[string]any)
	if len(res["tools"].([]any)) == 0 {
		t.Error("tools/list came back empty after a token rotation")
	}
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

func TestGetMCPIsRefused(t *testing.T) {
	_, h, tok := testServer(t)

	r := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp = %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, "POST") {
		t.Errorf("Allow header = %q, should name the methods that do work", allow)
	}
}

func TestDeleteMCPIsAccepted(t *testing.T) {
	_, h, tok := testServer(t)

	r := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Mcp-Session-Id", randToken(16))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /mcp = %d, want 204", w.Code)
	}
}

func TestUnauthenticatedIsChallenged(t *testing.T) {
	_, h, _ := testServer(t)

	for _, tok := range []string{"", "not-a-real-token"} {
		w := post(t, h, tok, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q gave %d, want 401", tok, w.Code)
		}
		if !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata") {
			t.Errorf("401 must point the client at the resource metadata, got %q",
				w.Header().Get("WWW-Authenticate"))
		}
	}
}

func TestExpiredTokenIsChallenged(t *testing.T) {
	s, h, _ := testServer(t)
	expired, _ := s.sessions.IssueTokens("test", "client-3", "", -time.Second)

	w := post(t, h, expired, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an expired token gave %d, want 401", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Batches
// ---------------------------------------------------------------------------

func TestBatchAnswersRequestsAndSwallowsNotifications(t *testing.T) {
	_, h, tok := testServer(t)

	w := post(t, h, tok, randToken(16), `[
		{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}},
		{"jsonrpc":"2.0","method":"notifications/initialized"},
		{"jsonrpc":"2.0","id":2,"method":"ping"}
	]`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Mcp-Session-Id"); got != "" {
		t.Errorf("batch handed out a session id %q", got)
	}

	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("batch reply is not an array: %v\n%s", err, w.Body.String())
	}
	if len(out) != 2 {
		t.Fatalf("got %d replies, want 2 - the notification must not be answered", len(out))
	}
	for _, r := range out {
		if e, ok := r["error"]; ok {
			t.Errorf("unexpected error in batch: %v", e)
		}
	}
}

func TestBatchOfOnlyNotificationsIsAccepted(t *testing.T) {
	_, h, tok := testServer(t)

	w := post(t, h, tok, "", `[{"jsonrpc":"2.0","method":"notifications/initialized"}]`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "" {
		t.Errorf("202 should carry no body, got %q", body)
	}
}

func TestUnknownMethodIsAnErrorButANotificationIsNot(t *testing.T) {
	_, h, tok := testServer(t)

	w := post(t, h, tok, "", `{"jsonrpc":"2.0","id":9,"method":"does/not/exist"}`)
	out := decodeOne(t, w)
	e, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("unknown method should be an error: %s", w.Body.String())
	}
	if int(e["code"].(float64)) != codeMethodNotFound {
		t.Errorf("code = %v, want %d", e["code"], codeMethodNotFound)
	}

	w = post(t, h, tok, "", `{"jsonrpc":"2.0","method":"does/not/exist"}`)
	if w.Code != http.StatusAccepted {
		t.Errorf("an unknown *notification* gave %d, want 202 - a notification is never answered", w.Code)
	}
}

func TestMalformedJSONIsAParseError(t *testing.T) {
	_, h, tok := testServer(t)

	w := post(t, h, tok, "", `{"jsonrpc":"2.0",`)
	out := decodeOne(t, w)
	e, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("malformed JSON should be an error: %s", w.Body.String())
	}
	if int(e["code"].(float64)) != codeParseError {
		t.Errorf("code = %v, want %d", e["code"], codeParseError)
	}
}

// ---------------------------------------------------------------------------
// Diagnosis surface
// ---------------------------------------------------------------------------

// The facts on /healthz are the ones that were missing when a client stopped
// working overnight and the cause had to be dug out of container uptime and
// log lines over SSH.
func TestHealthzExplainsTheServer(t *testing.T) {
	_, h, _ := testServer(t)

	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	out := decodeOne(t, w)
	for _, key := range []string{"status", "version", "started", "uptime_seconds", "protocol_version", "token_ttl", "stateless"} {
		if _, ok := out[key]; !ok {
			t.Errorf("/healthz is missing %q", key)
		}
	}
	if out["token_ttl"] != "12h0m0s" {
		t.Errorf("token_ttl = %v, want 12h0m0s", out["token_ttl"])
	}
	if out["stateless"] != true {
		t.Error("stateless should be reported as true")
	}
}

func TestOriginAllowList(t *testing.T) {
	s, h, tok := testServer(t)

	cfg := *s.Config()
	cfg.AllowedOrigins = []string{"https://claude.ai"}
	s.setConfig(&cfg)

	send := func(origin string) int {
		r := httptest.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := send("https://claude.ai"); code != http.StatusOK {
		t.Errorf("allowed origin gave %d, want 200", code)
	}
	if code := send("https://evil.example"); code != http.StatusForbidden {
		t.Errorf("foreign origin gave %d, want 403", code)
	}
}
