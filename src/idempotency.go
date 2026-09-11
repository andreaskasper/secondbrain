package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Repeating a write safely
//
// The failure this exists for is not exotic. A write goes out, the transport
// drops or the client times out waiting, and the client sends the same call
// again. The server did the first one. Now there are two copies of the
// paragraph, and nobody saw an error.
//
// The textbook answer is an idempotency key chosen by the client. That is
// offered here, on every mutating tool, and it is the right mechanism: it
// survives any interval, it is explicit, and it cannot fire by accident.
// It is also, today, used by nobody - no MCP client this server talks to
// sends one. A mechanism that is correct and unused does not stop the
// duplicate paragraph.
//
// So there is a second, automatic path: for a short window, an identical
// repeat of a write - same user, same tool, same arguments to the byte - is
// answered from the first result instead of being applied again. Sixty
// seconds is long enough to cover a retry after a timeout and short enough
// that deliberate repetition is unaffected; a person appending the same line
// twice on purpose is not doing it within the same minute, and if they are,
// the result says replayed: true rather than pretending to have written.
//
// What is deliberately not remembered: dry runs, because they change nothing
// and a caller repeating one wants a fresh diff; and failures, because a call
// that did not write is exactly the call that should be retried.
// ---------------------------------------------------------------------------

// explicitTTL is how long a client-supplied key is honoured. Much longer than
// the automatic window, because a client that bothers to send a key is making
// a claim about a whole operation, not about a retry burst.
const explicitTTL = 24 * time.Hour

// maxIdemEntries bounds the table. Results are small, but "small times
// unbounded" is still unbounded.
const maxIdemEntries = 2000

const idemArgKey = "idempotency_key"

type IdemStore struct {
	mu      sync.Mutex
	window  time.Duration
	entries map[string]idemEntry
}

type idemEntry struct {
	result  any
	expires time.Time
}

func NewIdemStore(window time.Duration) *IdemStore {
	return &IdemStore{window: window, entries: map[string]idemEntry{}}
}

// Key builds the lookup key for a call, and reports whether the caller asked
// for this explicitly. The second return is false when nothing should be
// remembered at all: no explicit key and the automatic window switched off.
func (s *IdemStore) Key(user, tool string, args map[string]any) (string, bool, bool) {
	if s == nil {
		return "", false, false
	}
	if k, ok := args[idemArgKey].(string); ok && k != "" {
		return "k|" + user + "|" + k, true, true
	}
	if s.window <= 0 {
		return "", false, false
	}
	// The automatic key is the call itself. Marshalling a map[string]any
	// sorts the keys, so two calls that differ only in argument order hash
	// the same - which is what "identical call" should mean.
	rest := make(map[string]any, len(args))
	for k, v := range args {
		if k == idemArgKey {
			continue
		}
		rest[k] = v
	}
	blob, err := json.Marshal(rest)
	if err != nil {
		// Unmarshalable arguments cannot be compared, so do not pretend to.
		return "", false, false
	}
	sum := sha256.Sum256(append([]byte(tool+"\x00"), blob...))
	return "a|" + user + "|" + base64.RawURLEncoding.EncodeToString(sum[:16]), false, true
}

// Lookup returns the remembered result for a key.
func (s *IdemStore) Lookup(key string) (any, bool) {
	if s == nil || key == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		delete(s.entries, key)
		return nil, false
	}
	return e.result, true
}

// Remember stores a successful result.
func (s *IdemStore) Remember(key string, explicit bool, result any) {
	if s == nil || key == "" {
		return
	}
	ttl := s.window
	if explicit {
		ttl = explicitTTL
	}
	if ttl <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= maxIdemEntries {
		s.evictLocked()
	}
	s.entries[key] = idemEntry{result: result, expires: time.Now().Add(ttl)}
}

// evictLocked drops everything expired, and if that was not enough, the entry
// closest to expiring. Called with the lock held.
func (s *IdemStore) evictLocked() {
	now := time.Now()
	for k, e := range s.entries {
		if now.After(e.expires) {
			delete(s.entries, k)
		}
	}
	if len(s.entries) < maxIdemEntries {
		return
	}
	var oldestKey string
	var oldest time.Time
	for k, e := range s.entries {
		if oldestKey == "" || e.expires.Before(oldest) {
			oldestKey, oldest = k, e.expires
		}
	}
	delete(s.entries, oldestKey)
}

// markReplayed copies a result and labels it, so the caller can tell a replay
// from a fresh write. Silently returning the old result would be the same
// mistake in the other direction: an agent that cannot see it was replayed
// will believe its second, different intention was carried out.
func markReplayed(result any) any {
	m, ok := result.(map[string]any)
	if !ok {
		return result
	}
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out["replayed"] = true
	if _, exists := out["message"]; !exists {
		out["message"] = "replayed: an identical call was already applied, nothing was written again"
	}
	return out
}
