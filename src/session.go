package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// All state below lives in memory. It is lost on restart unless the operator
// sets a state key, in which case the subset that makes a restart invisible -
// clients, refresh tokens, and the two tables behind reuse detection - is
// written out encrypted by the caller through Export and Import. See
// statestore.go for why that subset and not more.

const (
	maxClients      = 1000
	maxTokens       = 10000
	refreshTokenTTL = 30 * 24 * time.Hour
	csrfTTL         = 10 * time.Minute
)

// ---------------------------------------------------------------------------
// Records
// ---------------------------------------------------------------------------

type Client struct {
	ID           string
	Name         string
	RedirectURIs []string
	Created      time.Time
	lastSeen     time.Time
}

type AuthCode struct {
	User          string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Expiry        time.Time
}

type Token struct {
	User     string
	ClientID string
	Family   string
	Expiry   time.Time
}

type csrfEntry struct {
	clientID    string
	redirectURI string
	state       string
	challenge   string
	expiry      time.Time
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

type SessionStore struct {
	mu sync.Mutex

	clients map[string]*Client
	codes   map[string]*AuthCode
	tokens  map[string]*Token
	refresh map[string]*Token
	csrf    map[string]*csrfEntry

	// consumedRefresh remembers which family a spent refresh token belonged
	// to. Without it, presenting an already-rotated token would look like an
	// unknown token, and the theft it signals would go unnoticed.
	consumedRefresh map[string]consumedToken

	// deadFamilies marks refresh-token families invalidated by a reuse.
	deadFamilies map[string]time.Time

	// dirty records that something worth persisting changed since the last
	// snapshot. It is a flag rather than a write because the flusher runs on
	// a timer: a client refreshing its token should not wait for a disk.
	dirty atomic.Bool
}

func (s *SessionStore) markDirty() { s.dirty.Store(true) }

// TakeDirty reports whether anything changed and clears the flag.
func (s *SessionStore) TakeDirty() bool { return s.dirty.Swap(false) }

type consumedToken struct {
	family string
	at     time.Time
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		clients:         map[string]*Client{},
		codes:           map[string]*AuthCode{},
		tokens:          map[string]*Token{},
		refresh:         map[string]*Token{},
		csrf:            map[string]*csrfEntry{},
		consumedRefresh: map[string]consumedToken{},
		deadFamilies:    map[string]time.Time{},
	}
}

// ---------------------------------------------------------------------------
// Secret generation and hashing
// ---------------------------------------------------------------------------

// randToken returns n bytes of crypto/rand as base64url.
func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashToken is what we store. The plaintext exists only in the response that
// issued it.
func hashToken(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---------------------------------------------------------------------------
// Clients
// ---------------------------------------------------------------------------

func (s *SessionStore) RegisterClient(name string, redirectURIs []string) *Client {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.clients) >= maxClients {
		s.evictClientsLocked()
	}
	c := &Client{
		ID:           randToken(16),
		Name:         name,
		RedirectURIs: redirectURIs,
		Created:      time.Now(),
		lastSeen:     time.Now(),
	}
	s.clients[c.ID] = c
	s.markDirty()
	return c
}

func (s *SessionStore) Client(id string) *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[id]
	if c != nil {
		c.lastSeen = time.Now()
	}
	return c
}

func (s *SessionStore) evictClientsLocked() {
	var oldestID string
	var oldest time.Time
	for id, c := range s.clients {
		if oldestID == "" || c.lastSeen.Before(oldest) {
			oldestID, oldest = id, c.lastSeen
		}
	}
	if oldestID != "" {
		delete(s.clients, oldestID)
	}
}

func (c *Client) AllowsRedirect(uri string) bool {
	for _, r := range c.RedirectURIs {
		if constantTimeEqual(r, uri) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// CSRF tokens for the login form
// ---------------------------------------------------------------------------

func (s *SessionStore) NewCSRF(clientID, redirectURI, state, challenge string) string {
	tok := randToken(16)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.csrf[hashToken(tok)] = &csrfEntry{
		clientID:    clientID,
		redirectURI: redirectURI,
		state:       state,
		challenge:   challenge,
		expiry:      time.Now().Add(csrfTTL),
	}
	return tok
}

// ConsumeCSRF validates a token and removes it. Single use.
func (s *SessionStore) ConsumeCSRF(tok, clientID, redirectURI string) (*csrfEntry, bool) {
	h := hashToken(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.csrf[h]
	if !ok {
		return nil, false
	}
	delete(s.csrf, h)
	if time.Now().After(e.expiry) {
		return nil, false
	}
	if e.clientID != clientID || e.redirectURI != redirectURI {
		return nil, false
	}
	return e, true
}

// ---------------------------------------------------------------------------
// Authorization codes
// ---------------------------------------------------------------------------

func (s *SessionStore) NewCode(user, clientID, redirectURI, challenge string, ttl time.Duration) string {
	code := randToken(32)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[hashToken(code)] = &AuthCode{
		User:          user,
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		CodeChallenge: challenge,
		Expiry:        time.Now().Add(ttl),
	}
	return code
}

// ConsumeCode removes the code whether or not it turns out to be valid, so a
// failed exchange cannot be retried.
func (s *SessionStore) ConsumeCode(code string) (*AuthCode, bool) {
	h := hashToken(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[h]
	if !ok {
		return nil, false
	}
	delete(s.codes, h)
	if time.Now().After(c.Expiry) {
		return nil, false
	}
	return c, true
}

// ---------------------------------------------------------------------------
// Tokens
// ---------------------------------------------------------------------------

// IssueTokens mints an access token and a refresh token in the same family.
func (s *SessionStore) IssueTokens(user, clientID, family string, ttl time.Duration) (string, string) {
	if family == "" {
		family = randToken(16)
	}
	access := randToken(32)
	refresh := randToken(32)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tokens) >= maxTokens {
		s.evictTokensLocked()
	}
	now := time.Now()
	s.tokens[hashToken(access)] = &Token{User: user, ClientID: clientID, Family: family, Expiry: now.Add(ttl)}
	s.refresh[hashToken(refresh)] = &Token{User: user, ClientID: clientID, Family: family, Expiry: now.Add(refreshTokenTTL)}
	s.markDirty()
	return access, refresh
}

// LookupAccess resolves a bearer token to its session.
func (s *SessionStore) LookupAccess(token string) *Token {
	h := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[h]
	if !ok {
		return nil
	}
	if time.Now().After(t.Expiry) {
		delete(s.tokens, h)
		return nil
	}
	if _, dead := s.deadFamilies[t.Family]; dead {
		delete(s.tokens, h)
		return nil
	}
	return t
}

// RotateRefresh exchanges a refresh token. Reusing an already-rotated token
// kills the whole family: that is the signature of a stolen token.
func (s *SessionStore) RotateRefresh(token, clientID string) (*Token, bool, bool) {
	h := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.refresh[h]
	if !ok {
		// Not live. If we have seen it before, this is a replay of a token
		// that was already exchanged - the signature of a stolen token.
		if c, seen := s.consumedRefresh[h]; seen {
			s.killFamilyLocked(c.family)
			return nil, false, true
		}
		return nil, false, false
	}
	delete(s.refresh, h)
	s.consumedRefresh[h] = consumedToken{family: t.Family, at: time.Now()}
	s.markDirty()

	if _, dead := s.deadFamilies[t.Family]; dead {
		return nil, false, true
	}
	if time.Now().After(t.Expiry) || t.ClientID != clientID {
		// Treat a mismatched client as a compromise of the family.
		s.killFamilyLocked(t.Family)
		return nil, false, true
	}
	return t, true, false
}

// KillFamily invalidates every token in a family.
func (s *SessionStore) KillFamily(family string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.killFamilyLocked(family)
}

func (s *SessionStore) killFamilyLocked(family string) {
	s.deadFamilies[family] = time.Now()
	s.markDirty()
	for h, t := range s.tokens {
		if t.Family == family {
			delete(s.tokens, h)
		}
	}
	for h, t := range s.refresh {
		if t.Family == family {
			delete(s.refresh, h)
		}
	}
}

func (s *SessionStore) evictTokensLocked() {
	var oldestH string
	var oldest time.Time
	for h, t := range s.tokens {
		if oldestH == "" || t.Expiry.Before(oldest) {
			oldestH, oldest = h, t.Expiry
		}
	}
	if oldestH != "" {
		delete(s.tokens, oldestH)
		logWarn("token_evicted", map[string]any{"reason": "token table full"})
	}
}

// ---------------------------------------------------------------------------
// Janitor
// ---------------------------------------------------------------------------

// Sweep removes expired entries. Called every 60s.
func (s *SessionStore) Sweep() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	for h, c := range s.codes {
		if now.After(c.Expiry) {
			delete(s.codes, h)
		}
	}
	for h, t := range s.tokens {
		if now.After(t.Expiry) {
			delete(s.tokens, h)
		}
	}
	for h, t := range s.refresh {
		if now.After(t.Expiry) {
			delete(s.refresh, h)
			s.markDirty()
		}
	}
	for h, e := range s.csrf {
		if now.After(e.expiry) {
			delete(s.csrf, h)
		}
	}
	cutoff := now.Add(-refreshTokenTTL)
	for f, t := range s.deadFamilies {
		if t.Before(cutoff) {
			delete(s.deadFamilies, f)
		}
	}
	for h, c := range s.consumedRefresh {
		if c.at.Before(cutoff) {
			delete(s.consumedRefresh, h)
		}
	}
}

func (s *SessionStore) RunJanitor(stop <-chan struct{}) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.Sweep()
		case <-stop:
			return
		}
	}
}

// Counts reports how much is currently held in memory, for the metrics
// endpoint. Numbers only - no identifiers ever leave this function.
func (s *SessionStore) Counts() (access, refresh, clients int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tokens), len(s.refresh), len(s.clients)
}

// ---------------------------------------------------------------------------
// Snapshot and restore
//
// Only what a client needs in order not to notice a restart. Access tokens
// and authorization codes are deliberately absent: the first is short lived
// and re-mintable from a refresh token, the second lives sixty seconds and is
// in flight during a login nobody is going to restart the server in the
// middle of.
// ---------------------------------------------------------------------------

func (s *SessionStore) Export() *stateSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	snap := &stateSnapshot{}
	for _, c := range s.clients {
		snap.Clients = append(snap.Clients, stateClient{
			ID: c.ID, Name: c.Name, RedirectURIs: c.RedirectURIs,
			Created: c.Created, LastSeen: c.lastSeen,
		})
	}
	for h, t := range s.refresh {
		if now.After(t.Expiry) {
			continue
		}
		snap.Refresh = append(snap.Refresh, stateToken{
			Hash: h, User: t.User, ClientID: t.ClientID, Family: t.Family, Expiry: t.Expiry,
		})
	}
	for f, at := range s.deadFamilies {
		snap.Dead = append(snap.Dead, stateFamily{Family: f, At: at})
	}
	for h, c := range s.consumedRefresh {
		snap.Consumed = append(snap.Consumed, stateConsumed{Hash: h, Family: c.family, At: c.at})
	}
	// Sorted so that two snapshots of the same state are the same bytes,
	// which makes "did anything actually change" answerable by looking.
	sort.Slice(snap.Clients, func(i, j int) bool { return snap.Clients[i].ID < snap.Clients[j].ID })
	sort.Slice(snap.Refresh, func(i, j int) bool { return snap.Refresh[i].Hash < snap.Refresh[j].Hash })
	sort.Slice(snap.Dead, func(i, j int) bool { return snap.Dead[i].Family < snap.Dead[j].Family })
	sort.Slice(snap.Consumed, func(i, j int) bool { return snap.Consumed[i].Hash < snap.Consumed[j].Hash })
	return snap
}

// Import merges a snapshot into an empty store and reports what it restored.
// Expired entries are dropped on the way in rather than left for the janitor,
// so the startup log says how much is actually usable.
func (s *SessionStore) Import(snap *stateSnapshot) (clients, refresh int) {
	if snap == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, c := range snap.Clients {
		if c.ID == "" {
			continue
		}
		last := c.LastSeen
		if last.IsZero() {
			last = c.Created
		}
		s.clients[c.ID] = &Client{
			ID: c.ID, Name: c.Name, RedirectURIs: c.RedirectURIs,
			Created: c.Created, lastSeen: last,
		}
		clients++
	}
	for _, t := range snap.Refresh {
		if t.Hash == "" || now.After(t.Expiry) {
			continue
		}
		s.refresh[t.Hash] = &Token{User: t.User, ClientID: t.ClientID, Family: t.Family, Expiry: t.Expiry}
		refresh++
	}
	cutoff := now.Add(-refreshTokenTTL)
	for _, f := range snap.Dead {
		if f.At.Before(cutoff) {
			continue
		}
		s.deadFamilies[f.Family] = f.At
	}
	for _, c := range snap.Consumed {
		if c.At.Before(cutoff) {
			continue
		}
		s.consumedRefresh[c.Hash] = consumedToken{family: c.Family, at: c.At}
	}
	return clients, refresh
}
