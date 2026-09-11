package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// merge3
//
// This is the riskiest code on the branch: it decides whether two people's
// edits to one note are reconciled or refused, and getting it subtly wrong
// loses somebody's paragraph without anything failing. So the cases below are
// the ones that actually happen - two edits far apart, two edits on the same
// line, and the same edit made twice.
// ---------------------------------------------------------------------------

const mergeBase = "line1\nline2\nline3\nline4\nline5\n"

func TestMergeDisjointChanges(t *testing.T) {
	ours := "ONE\nline2\nline3\nline4\nline5\n"
	theirs := "line1\nline2\nline3\nline4\nFIVE\n"

	got, conflicts := merge3(mergeBase, ours, theirs)
	if conflicts != 0 {
		t.Fatalf("changes to different lines should not conflict, got %d", conflicts)
	}
	want := "ONE\nline2\nline3\nline4\nFIVE\n"
	if got != want {
		t.Fatalf("merged text\n got: %q\nwant: %q", got, want)
	}
}

func TestMergeSameLineConflicts(t *testing.T) {
	ours := "line1\nline2\nOURS\nline4\nline5\n"
	theirs := "line1\nline2\nTHEIRS\nline4\nline5\n"

	_, conflicts := merge3(mergeBase, ours, theirs)
	if conflicts == 0 {
		t.Fatal("both sides rewrote line3; that must be refused, not merged")
	}
}

func TestMergeIdenticalChangeAppliedOnce(t *testing.T) {
	same := "line1\nline2\nTHREE\nline4\nline5\n"

	got, conflicts := merge3(mergeBase, same, same)
	if conflicts != 0 {
		t.Fatalf("the same change on both sides is not a conflict, got %d", conflicts)
	}
	if n := strings.Count(got, "THREE"); n != 1 {
		t.Fatalf("the change should appear once, appears %d times: %q", n, got)
	}
}

func TestMergeDisjointInsertions(t *testing.T) {
	ours := "line1\nOURS\nline2\nline3\nline4\nline5\n"
	theirs := "line1\nline2\nline3\nline4\nTHEIRS\nline5\n"

	got, conflicts := merge3(mergeBase, ours, theirs)
	if conflicts != 0 {
		t.Fatalf("insertions at different points should merge, got %d conflicts", conflicts)
	}
	if !strings.Contains(got, "OURS") || !strings.Contains(got, "THEIRS") {
		t.Fatalf("both insertions should survive: %q", got)
	}
}

func TestMergeNoChangeOnOneSide(t *testing.T) {
	theirs := "line1\nline2\nline3\nline4\nFIVE\n"

	got, conflicts := merge3(mergeBase, mergeBase, theirs)
	if conflicts != 0 {
		t.Fatalf("unexpected conflict: %d", conflicts)
	}
	if got != theirs {
		t.Fatalf("with no change of ours the other side wins\n got: %q\nwant: %q", got, theirs)
	}
}

// ---------------------------------------------------------------------------
// Client address
// ---------------------------------------------------------------------------

func cfgWithProxies(t *testing.T, header string, proxies ...string) *Config {
	t.Helper()
	nets, err := parseTrustedProxies(proxies)
	if err != nil {
		t.Fatalf("parseTrustedProxies(%v): %v", proxies, err)
	}
	return &Config{ClientIPHeader: header, TrustedProxies: proxies, trustedNets: nets}
}

func request(remote string, headers map[string]string) *http.Request {
	r := &http.Request{RemoteAddr: remote, Header: http.Header{}}
	for k, v := range headers {
		r.Header.Add(k, v)
	}
	return r
}

func TestClientIPIgnoresHeaderWithoutTrustedProxy(t *testing.T) {
	// The default configuration must behave exactly as it did before this
	// existed: believe nobody, use the connection peer.
	c := &Config{}
	r := request("203.0.113.9:5555", map[string]string{"X-Forwarded-For": "1.2.3.4"})
	if got := c.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("an unconfigured server must not believe a forwarding header, got %q", got)
	}
}

func TestClientIPFromUntrustedPeerIsIgnored(t *testing.T) {
	c := cfgWithProxies(t, "", "10.0.0.0/8")
	r := request("198.51.100.4:5555", map[string]string{"X-Forwarded-For": "1.2.3.4"})
	if got := c.ClientIP(r); got != "198.51.100.4" {
		t.Fatalf("a header from an untrusted peer is a claim, not a fact; got %q", got)
	}
}

func TestClientIPWalksChainRightToLeft(t *testing.T) {
	c := cfgWithProxies(t, "", "10.0.0.0/8", "172.18.0.1")
	r := request("10.1.2.3:5555", map[string]string{
		"X-Forwarded-For": "198.51.100.7, 172.18.0.1, 10.1.2.3",
	})
	if got := c.ClientIP(r); got != "198.51.100.7" {
		t.Fatalf("expected the first untrusted hop from the right, got %q", got)
	}
}

func TestClientIPBareAddressIsTrusted(t *testing.T) {
	// "172.18.0.1" is what an operator reads out of docker inspect; it has
	// to work without being told to append /32.
	c := cfgWithProxies(t, "", "172.18.0.1")
	r := request("172.18.0.1:5555", map[string]string{"X-Forwarded-For": "203.0.113.5"})
	if got := c.ClientIP(r); got != "203.0.113.5" {
		t.Fatalf("a bare address should be accepted as a single host, got %q", got)
	}
}

func TestClientIPNamedHeader(t *testing.T) {
	c := cfgWithProxies(t, "CF-Connecting-IP", "10.0.0.0/8")
	r := request("10.1.2.3:5555", map[string]string{
		"CF-Connecting-IP": "198.51.100.9",
		"X-Forwarded-For":  "should.be.ignored",
	})
	if got := c.ClientIP(r); got != "198.51.100.9" {
		t.Fatalf("expected the named header to win, got %q", got)
	}
}

func TestClientIPFallsBackToPeer(t *testing.T) {
	c := cfgWithProxies(t, "", "10.0.0.0/8")
	r := request("10.1.2.3:5555", nil)
	if got := c.ClientIP(r); got != "10.1.2.3" {
		t.Fatalf("with no header the peer is the only honest answer, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

func TestIdemKeyIgnoresArgumentOrder(t *testing.T) {
	s := NewIdemStore(time.Minute)
	a, _, ok1 := s.Key("u", "note_write", map[string]any{"path": "a.md", "content": "x"})
	b, _, ok2 := s.Key("u", "note_write", map[string]any{"content": "x", "path": "a.md"})
	if !ok1 || !ok2 {
		t.Fatal("both calls should be usable")
	}
	if a != b {
		t.Fatal("the same call written in a different order is the same call")
	}
}

func TestIdemKeyDistinguishesToolAndUser(t *testing.T) {
	s := NewIdemStore(time.Minute)
	args := map[string]any{"path": "a.md"}
	base, _, _ := s.Key("u", "note_write", args)
	otherTool, _, _ := s.Key("u", "note_delete", args)
	otherUser, _, _ := s.Key("v", "note_write", args)
	if base == otherTool {
		t.Fatal("different tools must not share a key")
	}
	if base == otherUser {
		t.Fatal("different users must not share a key")
	}
}

func TestIdemExplicitKeySurvivesDifferentArguments(t *testing.T) {
	s := NewIdemStore(time.Minute)
	k1, explicit, _ := s.Key("u", "note_write", map[string]any{
		idemArgKey: "abc", "content": "one",
	})
	if !explicit {
		t.Fatal("a client-supplied key should be reported as explicit")
	}
	k2, _, _ := s.Key("u", "note_write", map[string]any{
		idemArgKey: "abc", "content": "two",
	})
	if k1 != k2 {
		t.Fatal("an explicit key identifies the operation, not the arguments")
	}
}

func TestIdemWindowOffMeansNoAutomaticKey(t *testing.T) {
	s := NewIdemStore(0)
	if _, _, usable := s.Key("u", "note_write", map[string]any{"path": "a.md"}); usable {
		t.Fatal("with the window off there should be no automatic key")
	}
	if _, explicit, usable := s.Key("u", "note_write", map[string]any{idemArgKey: "k"}); !usable || !explicit {
		t.Fatal("an explicit key must still work with the window off")
	}
}

func TestIdemRememberAndReplay(t *testing.T) {
	s := NewIdemStore(time.Minute)
	args := map[string]any{"path": "a.md"}
	key, explicit, _ := s.Key("u", "note_write", args)

	if _, hit := s.Lookup(key); hit {
		t.Fatal("nothing remembered yet")
	}
	s.Remember(key, explicit, map[string]any{"path": "a.md", "bytes": 10})

	prev, hit := s.Lookup(key)
	if !hit {
		t.Fatal("the first result should be remembered")
	}
	replayed, ok := markReplayed(prev).(map[string]any)
	if !ok || replayed["replayed"] != true {
		t.Fatal("a replay must say so; a caller that cannot tell will think its second intention was carried out")
	}
	if replayed["path"] != "a.md" {
		t.Fatal("the original result should be preserved")
	}
	if orig, _ := prev.(map[string]any); orig["replayed"] != nil {
		t.Fatal("markReplayed must copy, not mutate the stored result")
	}
}

func TestIdemExpiry(t *testing.T) {
	s := NewIdemStore(time.Millisecond)
	key, explicit, _ := s.Key("u", "note_write", map[string]any{"path": "a.md"})
	s.Remember(key, explicit, map[string]any{"ok": true})
	time.Sleep(5 * time.Millisecond)
	if _, hit := s.Lookup(key); hit {
		t.Fatal("the window should have closed")
	}
}

// ---------------------------------------------------------------------------
// State persistence
// ---------------------------------------------------------------------------

func TestSealRoundTrip(t *testing.T) {
	var key [32]byte
	copy(key[:], "0123456789abcdef0123456789abcdef")
	sealed, err := seal(key, []byte("hello"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	plain, err := unseal(key, sealed)
	if err != nil || string(plain) != "hello" {
		t.Fatalf("unseal: %q, %v", plain, err)
	}

	var wrong [32]byte
	copy(wrong[:], "fedcba9876543210fedcba9876543210")
	if _, err := unseal(wrong, sealed); err == nil {
		t.Fatal("a different key must not decrypt the file")
	}
}

func TestStateStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := NewStateStore(dir, "a-key-long-enough-for-this")
	if st == nil {
		t.Fatal("a configured key should give a store")
	}
	if NewStateStore(dir, "") != nil {
		t.Fatal("no key means no persistence at all")
	}

	s := NewSessionStore()
	c := s.RegisterClient("test client", []string{"https://example.com/cb"})
	_, _ = s.IssueTokens("andreas", c.ID, "", time.Hour)

	if err := st.Save(s.Export()); err != nil {
		t.Fatalf("save: %v", err)
	}
	snap, err := st.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if snap == nil || len(snap.Clients) != 1 || len(snap.Refresh) != 1 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap.Version != stateSchemaVersion {
		t.Fatalf("schema version %d", snap.Version)
	}

	// A changed key is an operator mistake, and it must read as one rather
	// than as an empty file.
	if _, err := NewStateStore(dir, "an-entirely-different-key").Load(); err == nil {
		t.Fatal("a changed key should be reported, not silently ignored")
	}
}

func TestStateStoreLoadMissingFile(t *testing.T) {
	snap, err := NewStateStore(t.TempDir(), "a-key-long-enough-for-this").Load()
	if err != nil || snap != nil {
		t.Fatalf("a first start has nothing to restore: %+v, %v", snap, err)
	}
}

func TestSessionSurvivesRestart(t *testing.T) {
	s := NewSessionStore()
	c := s.RegisterClient("test client", []string{"https://example.com/cb"})
	_, refresh := s.IssueTokens("andreas", c.ID, "", time.Hour)

	restored := NewSessionStore()
	clients, refreshCount := restored.Import(s.Export())
	if clients != 1 || refreshCount != 1 {
		t.Fatalf("restored %d clients and %d refresh tokens", clients, refreshCount)
	}
	if restored.Client(c.ID) == nil {
		t.Fatal("the client registration should survive")
	}

	// The point of the whole feature: the token the client is holding still
	// works after a restart.
	if _, ok, _ := restored.RotateRefresh(refresh, c.ID); !ok {
		t.Fatal("a refresh token should still rotate after a restart")
	}
	// And reuse detection survives with it.
	if _, ok, reuse := restored.RotateRefresh(refresh, c.ID); ok || !reuse {
		t.Fatal("replaying the rotated token must still be caught as reuse")
	}
}

func TestDirtyFlag(t *testing.T) {
	s := NewSessionStore()
	if s.TakeDirty() {
		t.Fatal("a fresh store has nothing to write")
	}
	s.RegisterClient("x", []string{"https://e.com/cb"})
	if !s.TakeDirty() {
		t.Fatal("registering a client should mark the store dirty")
	}
	if s.TakeDirty() {
		t.Fatal("TakeDirty must clear the flag")
	}
}
