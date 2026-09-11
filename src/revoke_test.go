package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The store
// ---------------------------------------------------------------------------

func revokeFixture(t *testing.T) (*SessionStore, *Client, string, string) {
	t.Helper()
	s := NewSessionStore()
	c := s.RegisterClient("test client", []string{"https://example.com/cb"})
	access, refresh := s.IssueTokens("andreas", c.ID, "", time.Hour)
	return s, c, access, refresh
}

func TestRevokeAccessDropsOnlyThatToken(t *testing.T) {
	s, c, access, refresh := revokeFixture(t)

	if !s.RevokeAccess(access, c.ID) {
		t.Fatal("a live access token should be revocable")
	}
	if s.LookupAccess(access) != nil {
		t.Fatal("the access token should be gone")
	}
	// The session is not over: a client dropping one credential is being
	// tidy, not logging out.
	if _, ok, _ := s.RotateRefresh(refresh, c.ID); !ok {
		t.Fatal("the refresh token should have survived")
	}
}

func TestRevokeAccessTwiceIsNotAnError(t *testing.T) {
	s, c, access, _ := revokeFixture(t)
	if !s.RevokeAccess(access, c.ID) {
		t.Fatal("first revocation should succeed")
	}
	if s.RevokeAccess(access, c.ID) {
		t.Fatal("the second should report nothing was revoked")
	}
}

func TestRevokeAccessRefusesAnotherClient(t *testing.T) {
	s, _, access, _ := revokeFixture(t)
	other := s.RegisterClient("other", []string{"https://other.example/cb"})

	if s.RevokeAccess(access, other.ID) {
		t.Fatal("a client must not retire a token issued to somebody else")
	}
	if s.LookupAccess(access) == nil {
		t.Fatal("the token should still be live")
	}
}

func TestRevokeAccessWithoutClientID(t *testing.T) {
	// RFC 7009 lets a public client authenticate with possession of the
	// token alone. Possession is the real credential here.
	s, _, access, _ := revokeFixture(t)
	if !s.RevokeAccess(access, "") {
		t.Fatal("possession of the token should be enough")
	}
}

func TestRevokeRefreshKillsTheFamily(t *testing.T) {
	s, c, access, refresh := revokeFixture(t)

	if !s.RevokeRefresh(refresh, c.ID) {
		t.Fatal("a live refresh token should be revocable")
	}
	if _, ok, _ := s.RotateRefresh(refresh, c.ID); ok {
		t.Fatal("the refresh token should be dead")
	}
	// The access token issued alongside it must die too, or "log out"
	// means "log out in twelve hours".
	if s.LookupAccess(access) != nil {
		t.Fatal("the access token from the same family should be gone")
	}
}

func TestRevokeRefreshAfterRotation(t *testing.T) {
	s, c, _, refresh := revokeFixture(t)

	// Rotate once, then revoke the spent token - the race a client hits
	// when it refreshes and logs out at nearly the same moment.
	if _, ok, _ := s.RotateRefresh(refresh, c.ID); !ok {
		t.Fatal("rotation should succeed")
	}
	if !s.RevokeRefresh(refresh, c.ID) {
		t.Fatal("a spent refresh token still names its family")
	}
}

func TestRevokeRefreshRefusesAnotherClient(t *testing.T) {
	s, _, _, refresh := revokeFixture(t)
	other := s.RegisterClient("other", []string{"https://other.example/cb"})
	if s.RevokeRefresh(refresh, other.ID) {
		t.Fatal("a client must not end somebody else's session")
	}
}

func TestRevokeUnknownToken(t *testing.T) {
	s := NewSessionStore()
	if s.RevokeAccess("not-a-token", "") || s.RevokeRefresh("not-a-token", "") {
		t.Fatal("nothing should be reported as revoked")
	}
}

// ---------------------------------------------------------------------------
// The endpoint
// ---------------------------------------------------------------------------

func revokeServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{sessions: NewSessionStore(), metrics: NewMetrics(), stop: make(chan struct{})}
	s.setConfig(&Config{PublicURL: "https://notes.example.com"})
	rate, _ := ParseRate(revokeRate)
	s.revokeLimiter = NewKeyedLimiter(rate)
	return s
}

func postRevoke(t *testing.T, s *Server, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/revoke", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleRevoke(w, r)
	return w
}

func TestRevokeEndpointRetiresARefreshToken(t *testing.T) {
	s := revokeServer(t)
	c := s.sessions.RegisterClient("test client", []string{"https://example.com/cb"})
	_, refresh := s.sessions.IssueTokens("andreas", c.ID, "", time.Hour)

	w := postRevoke(t, s, url.Values{"token": {refresh}, "client_id": {c.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if _, ok, _ := s.sessions.RotateRefresh(refresh, c.ID); ok {
		t.Fatal("the token should be dead")
	}
}

func TestRevokeEndpointIgnoresAWrongHint(t *testing.T) {
	// The hint is a hint. A client that guesses wrong should still end up
	// with a revoked token.
	s := revokeServer(t)
	c := s.sessions.RegisterClient("test client", []string{"https://example.com/cb"})
	access, _ := s.sessions.IssueTokens("andreas", c.ID, "", time.Hour)

	w := postRevoke(t, s, url.Values{
		"token":           {access},
		"client_id":       {c.ID},
		"token_type_hint": {"refresh_token"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if s.sessions.LookupAccess(access) != nil {
		t.Fatal("the access token should have been found despite the wrong hint")
	}
}

func TestRevokeEndpointAnswers200ForAnUnknownToken(t *testing.T) {
	// An endpoint that says "no such token" is an endpoint that can be
	// asked which tokens exist.
	s := revokeServer(t)
	w := postRevoke(t, s, url.Values{"token": {"nonsense"}})
	if w.Code != http.StatusOK {
		t.Fatalf("an unknown token must not be an error, got %d", w.Code)
	}
}

func TestRevokeEndpointRejectsAMissingToken(t *testing.T) {
	s := revokeServer(t)
	w := postRevoke(t, s, url.Values{"client_id": {"whatever"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a missing token is a malformed request, got %d", w.Code)
	}
}

func TestRevokeEndpointRejectsGet(t *testing.T) {
	s := revokeServer(t)
	r := httptest.NewRequest(http.MethodGet, "/revoke", nil)
	w := httptest.NewRecorder()
	s.handleRevoke(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
	if got := w.Header().Get("Allow"); got != "POST" {
		t.Fatalf("expected an Allow header naming POST, got %q", got)
	}
}

func TestRevocationEndpointIsAdvertised(t *testing.T) {
	s := revokeServer(t)
	r := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	s.handleAuthServerMetadata(w, r)
	if !strings.Contains(w.Body.String(), `"revocation_endpoint":"https://notes.example.com/revoke"`) {
		t.Fatalf("metadata should name the endpoint: %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// The replay label, whatever shape the result has
// ---------------------------------------------------------------------------

func TestMarkReplayedLabelsAStruct(t *testing.T) {
	res := &WriteResult{Path: "wiki/a.md", Vault: "default", Bytes: 12}
	out, ok := markReplayed(res).(map[string]any)
	if !ok {
		t.Fatalf("a struct result should come back as an object, got %T", markReplayed(res))
	}
	if out["replayed"] != true {
		t.Fatal("the label is the whole point")
	}
	if out["path"] != "wiki/a.md" {
		t.Fatalf("the original fields should survive: %v", out)
	}
}

func TestMarkReplayedWrapsAList(t *testing.T) {
	res := []map[string]any{{"path": "a.md"}, {"path": "b.md"}}
	out, ok := markReplayed(res).(map[string]any)
	if !ok {
		t.Fatal("a list should be wrapped in something that can carry the label")
	}
	if out["replayed"] != true {
		t.Fatal("missing label")
	}
	if out["result"] == nil {
		t.Fatalf("the list should still be reachable: %v", out)
	}
}

func TestMarkReplayedKeepsAnExistingMessage(t *testing.T) {
	out := markReplayed(map[string]any{"message": "no change"}).(map[string]any)
	if out["message"] != "no change" {
		t.Fatalf("an existing message should not be overwritten: %v", out["message"])
	}
}
