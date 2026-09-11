package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ---------------------------------------------------------------------------
// Surviving a restart
//
// Everything in SessionStore lives in memory, deliberately: a container
// holding credentials should leave nothing on disk. The cost of that stance
// was a manual re-login after every update, and updates are exactly when a
// person least wants to be sent to a browser - the connector is down, they
// are trying to fix it, and the fix requires the thing that is down.
//
// So persistence is opt-in and narrow. Setting SECONDBRAIN_STATE_KEY turns it
// on; leaving it unset keeps the old behaviour exactly. What is written is the
// minimum that makes a restart invisible to a client that follows the OAuth
// rules: the client registrations, the refresh tokens, and the two tables that
// make refresh-token reuse detectable. Access tokens are not written. They
// live twelve hours, a client holding a refresh token mints a new one without
// being asked, and the shortest-lived credential is the one least worth
// putting on a disk.
//
// Refresh tokens are stored as the SHA-256 hashes the server already keeps -
// the plaintext exists only in the response that issued it and is never known
// here. The encryption is therefore not what protects the tokens; it protects
// the rest of the file, which names users and the redirect URIs of every
// client that ever connected. A file that maps "andreas" to a claude.ai
// callback is not catastrophic, but it is nobody's business either.
// ---------------------------------------------------------------------------

const stateFileName = ".secondbrain-state"

const stateSchemaVersion = 1

type stateSnapshot struct {
	Version  int             `json:"version"`
	SavedAt  time.Time       `json:"saved_at"`
	Clients  []stateClient   `json:"clients"`
	Refresh  []stateToken    `json:"refresh"`
	Dead     []stateFamily   `json:"dead_families"`
	Consumed []stateConsumed `json:"consumed_refresh"`
}

type stateClient struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	RedirectURIs []string  `json:"redirect_uris"`
	Created      time.Time `json:"created"`
	LastSeen     time.Time `json:"last_seen"`
}

type stateToken struct {
	Hash     string    `json:"hash"`
	User     string    `json:"user"`
	ClientID string    `json:"client_id"`
	Family   string    `json:"family"`
	Expiry   time.Time `json:"expiry"`
}

type stateFamily struct {
	Family string    `json:"family"`
	At     time.Time `json:"at"`
}

type stateConsumed struct {
	Hash   string    `json:"hash"`
	Family string    `json:"family"`
	At     time.Time `json:"at"`
}

// StateStore reads and writes the snapshot. A nil *StateStore is a working
// no-op, which is what the unconfigured case gets.
type StateStore struct {
	path string
	key  [32]byte
}

// NewStateStore returns nil when persistence is not configured.
func NewStateStore(dataDir, secret string) *StateStore {
	if secret == "" {
		return nil
	}
	return &StateStore{
		path: filepath.Join(dataDir, stateFileName),
		key:  sha256.Sum256([]byte(secret)),
	}
}

func (st *StateStore) Path() string {
	if st == nil {
		return ""
	}
	return st.path
}

func (st *StateStore) Save(snap *stateSnapshot) error {
	if st == nil {
		return nil
	}
	snap.Version = stateSchemaVersion
	snap.SavedAt = time.Now().UTC()
	plain, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	sealed, err := seal(st.key, plain)
	if err != nil {
		return err
	}
	return writeFilePrivate(st.path, sealed)
}

// Load returns nil, nil when there is nothing to restore. A file that cannot
// be decrypted is a changed key, not a corrupt server: it is reported and
// ignored rather than fatal, because refusing to start over an unreadable
// cache would turn a forgotten environment variable into an outage.
func (st *StateStore) Load() (*stateSnapshot, error) {
	if st == nil {
		return nil, nil
	}
	raw, err := os.ReadFile(st.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	plain, err := unseal(st.key, raw)
	if err != nil {
		return nil, fmt.Errorf("could not decrypt %s: %w", st.path, err)
	}
	var snap stateSnapshot
	if err := json.Unmarshal(plain, &snap); err != nil {
		return nil, fmt.Errorf("could not parse %s: %w", st.path, err)
	}
	if snap.Version != stateSchemaVersion {
		return nil, fmt.Errorf("%s was written by schema version %d, this build speaks %d",
			st.path, snap.Version, stateSchemaVersion)
	}
	return &snap, nil
}

// ---------------------------------------------------------------------------
// Sealing
// ---------------------------------------------------------------------------

func seal(key [32]byte, plain []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

func unseal(key [32]byte, sealed []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, fmt.Errorf("file is too short to contain a nonce")
	}
	nonce, body := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, nil)
}

func newGCM(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// writeFilePrivate writes through a temporary file so a crash cannot leave a
// half-written snapshot, and never widens the mode of an existing file.
func writeFilePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sbstate-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
