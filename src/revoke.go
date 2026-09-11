package main

import (
	"fmt"
	"net/http"
	"os"
)

// ---------------------------------------------------------------------------
// Giving a token back
//
// Until this existed there was exactly one way to invalidate a credential:
// restart the server, because every token lived in memory and nothing
// survived the process. That was never a good answer, but it was an answer.
//
// Token persistence took it away. A refresh token now survives the restart
// that used to be the cure, which means the feature that made updates
// painless also removed the only recourse after a leak. Adding persistence
// without adding this was the mistake; this is the other half of it.
//
// The endpoint is RFC 7009, and the two rules that matter both come from
// there. An unknown token is answered with 200 and not with an error,
// because an endpoint that says "no such token" is an endpoint that can be
// asked which tokens exist. And the token_type_hint is a hint: when it does
// not match, the other kind is tried anyway, so a client that guesses wrong
// still ends up with a revoked token rather than a silent no-op.
// ---------------------------------------------------------------------------

// revokeRate is generous. Revocation is rare in normal use, and the limit is
// here to stop somebody grinding through guesses, not to ration a client
// that is cleaning up after itself.
const revokeRate = "60/m"

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg := s.Config()
	ip := cfg.ClientIP(r)
	if ok, _ := s.revokeLimiter.Allow(ip); !ok {
		writeHTTPError(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	token := r.PostForm.Get("token")
	if token == "" {
		// The one case RFC 7009 does call an error: the request is
		// malformed rather than the token unknown.
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	clientID := r.PostForm.Get("client_id")

	kind, revoked := s.revokeToken(token, clientID, r.PostForm.Get("token_type_hint"))

	outcome := "unknown"
	if revoked {
		outcome = kind
		// Written out now rather than at the next tick of the flusher. A
		// revocation that is lost because the container stopped thirty
		// seconds later is a revocation that did not happen, and this is
		// the one operation where that matters more than the disk write.
		s.flushState()
	}
	s.metrics.ObserveRevocation(outcome)
	logInfo("token_revoked", map[string]any{
		"client_id": clientID,
		"type":      kind,
		"revoked":   revoked,
		"ip":        ip,
	})

	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// revokeToken tries both kinds, starting with the one the caller suggested.
// It returns which kind was actually revoked.
func (s *Server) revokeToken(token, clientID, hint string) (string, bool) {
	type attempt struct {
		kind string
		fn   func(string, string) bool
	}
	order := []attempt{
		{"refresh_token", s.sessions.RevokeRefresh},
		{"access_token", s.sessions.RevokeAccess},
	}
	if hint == "access_token" {
		order[0], order[1] = order[1], order[0]
	}
	for _, a := range order {
		if a.fn(token, clientID) {
			return a.kind, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// The store side
// ---------------------------------------------------------------------------

// RevokeAccess drops a single access token.
//
// Only that one token: a client revoking an access token is dropping a
// credential, not ending its session, and taking the refresh token with it
// would log somebody out of a client that was trying to be tidy.
func (s *SessionStore) RevokeAccess(token, clientID string) bool {
	h := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[h]
	if !ok {
		return false
	}
	// An empty client_id means the caller authenticated with nothing but
	// possession of the token, which RFC 7009 allows for a public client.
	// When one is supplied it has to match, or a client that learned
	// somebody else's token could retire it.
	if clientID != "" && t.ClientID != clientID {
		return false
	}
	delete(s.tokens, h)
	return true
}

// RevokeRefresh retires a refresh token and everything issued alongside it.
//
// The whole family, deliberately. A client handing back its refresh token is
// saying the session is over, and leaving a twelve-hour access token alive
// after that would make "log out" mean "log out in a while".
func (s *SessionStore) RevokeRefresh(token, clientID string) bool {
	h := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()

	if t, ok := s.refresh[h]; ok {
		if clientID != "" && t.ClientID != clientID {
			return false
		}
		s.killFamilyLocked(t.Family)
		return true
	}
	// A token that has already been rotated still names its family. Whoever
	// presents it is either the legitimate client, racing its own refresh,
	// or somebody who kept a copy - and killing the family is the right
	// answer to both. The client cannot be checked here because a spent
	// token is remembered by family alone, which is why this errs towards
	// revoking rather than towards refusing.
	if c, seen := s.consumedRefresh[h]; seen {
		s.killFamilyLocked(c.family)
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// The operator side
//
// The endpoint above serves a client that holds a token. An operator who
// suspects a leak usually holds no token at all - what they have is a
// container and a bad feeling. For them the whole point of the old
// in-memory-only design was that stopping the process ended every session,
// and persistence is what took that away. This gives it back on demand.
// ---------------------------------------------------------------------------

func cmdRevokeAll(path string) int {
	cfg, err := LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration is not usable: %v\n", err)
		return 1
	}
	if cfg.StateKey == "" {
		fmt.Println("Persistence is off, so nothing is kept between runs.")
		fmt.Println("Restarting the server already invalidates every token.")
		return 0
	}
	st := NewStateStore(cfg.DataDir, cfg.StateKey)
	switch err := os.Remove(st.Path()); {
	case err == nil:
		fmt.Printf("Removed %s\n", st.Path())
		fmt.Println("Restart the server to finish: tokens already in memory")
		fmt.Println("stay valid until the process stops.")
		return 0
	case os.IsNotExist(err):
		fmt.Printf("%s does not exist - there is nothing stored to remove.\n", st.Path())
		fmt.Println("A restart still clears whatever is held in memory.")
		return 0
	default:
		fmt.Fprintf(os.Stderr, "could not remove %s: %v\n", st.Path(), err)
		return 1
	}
}
