// Package object implements the content-addressed object store and text
// classes (spec §5.4, rulings §5): BLAKE3 addressing over raw uncompressed
// bytes, objects/<first2>/<rest> layout, never-overwrite with
// verify-on-collision, immutable objects, ephemeral TTL housekeeping with
// typed content_expired, and the text-class downgrade policy.
package object

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lukechampine.com/blake3"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// ErrNotFound: the object does not exist and no expiry explanation applies.
var ErrNotFound = errors.New("object not found")

// ErrBadHash: the supplied string is not a well-formed BLAKE3 hex address.
var ErrBadHash = errors.New("malformed object hash (want 64 lowercase hex chars)")

// ValidHash reports whether h is a well-formed BLAKE3 hex address (exactly
// config.ObjectHashHexLen lowercase hex chars). Every externally-supplied
// hash MUST pass this before it becomes a store path: Path slices hash[:2]
// and joins into the filesystem, both unsafe on arbitrary input.
func ValidHash(h string) bool {
	if len(h) != config.ObjectHashHexLen {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ExpiredError is the TYPED content_expired result (rulings §5): the
// referencing event survives forever; the ephemeral object's bytes are gone.
type ExpiredError struct {
	Hash string
}

func (e *ExpiredError) Error() string {
	return fmt.Sprintf("content_expired: ephemeral object %s passed its TTL and was removed; the referencing event is preserved", e.Hash)
}

// Store is the content-addressed object store under <portable>/objects.
type Store struct {
	fs  fsx.FS
	dir string        // <portable>/objects
	ttl time.Duration // ephemeral TTL (config-tunable, RULINGS.md R10)
}

func NewStore(fsys fsx.FS, portableDir string) *Store {
	return &Store{fs: fsys, dir: filepath.Join(portableDir, config.ObjectsDirName), ttl: config.EphemeralTTLDefault}
}

// SetTTL overrides the ephemeral TTL from portable config.
func (s *Store) SetTTL(ttl time.Duration) {
	if ttl > 0 {
		s.ttl = ttl
	}
}

// Hash returns the BLAKE3 hex address of raw uncompressed bytes.
func Hash(data []byte) string {
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Path returns objects/<first2>/<rest> for a hash.
func (s *Store) Path(hash string) string {
	return filepath.Join(s.dir, hash[:2], hash[2:])
}

// Put durably stores data at its content address (temp → fsync → rename →
// dir-fsync) and returns the hash. MUST complete before the referencing
// event is appended (rulings §3.1). Idempotent: if the object exists it is
// verified against the new bytes (hash addressing makes a true collision a
// corruption signal, not a dedup) — mismatch is a hard error and the stored
// object is never touched.
func (s *Store) Put(data []byte) (string, error) {
	hash := Hash(data)
	path := s.Path(hash)

	if existing, err := s.fs.ReadFile(path); err == nil {
		if len(existing) != len(data) || !bytes.Equal(existing, data) {
			return "", fmt.Errorf("object %s exists with DIFFERENT content (%d vs %d bytes) — store corruption, refusing to touch it", hash, len(existing), len(data))
		}
		return hash, nil // already durable; never overwrite
	}

	if err := s.fs.MkdirAll(filepath.Dir(path), config.DirPerm); err != nil {
		return "", err
	}
	if err := fsx.WriteFileAtomic(s.fs, path, data, config.FilePerm); err != nil {
		// A concurrent identical publish can lose the rename race benignly.
		if errors.Is(err, os.ErrExist) {
			if existing, rerr := s.fs.ReadFile(path); rerr == nil && bytes.Equal(existing, data) {
				return hash, nil
			}
		}
		return "", err
	}
	return hash, nil
}

// Get returns the object bytes, verifying them against the address (a
// corrupt object must never be served as valid).
func (s *Store) Get(hash string) ([]byte, error) {
	if !ValidHash(hash) {
		return nil, ErrBadHash
	}
	data, err := s.fs.ReadFile(s.Path(hash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if Hash(data) != hash {
		return nil, fmt.Errorf("object %s failed content verification (stored bytes hash to %s)", hash, Hash(data))
	}
	return data, nil
}

// Exists reports whether the object is present (no content verification).
func (s *Store) Exists(hash string) bool {
	if !ValidHash(hash) {
		return false
	}
	_, err := s.fs.Stat(s.Path(hash))
	return err == nil
}

// Ref is one event's reference to an object: the class and creation time
// come from the referencing event (the projection supplies these in M3+;
// tests supply them directly). Retention is decided across ALL refs.
type Ref struct {
	Hash      string
	TextClass string // "canonical" | "eager-searchable" | "ephemeral"
	CreatedAt time.Time
}

// Fetch returns object bytes with expiry semantics: a missing object whose
// every reference is ephemeral and past TTL is the typed content_expired
// result; a missing object that SHOULD exist is ErrNotFound (doctor
// reports it).
func (s *Store) Fetch(hash string, refs []Ref, now time.Time) ([]byte, error) {
	data, err := s.Get(hash)
	if err == nil {
		return data, nil
	}
	if errors.Is(err, ErrNotFound) && s.allEphemeralExpired(hash, refs, now) {
		return nil, &ExpiredError{Hash: hash}
	}
	return nil, err
}

// HousekeepEphemeral deletes objects whose every reference is ephemeral and
// past TTL (rulings §5: expired ephemeral text — event survives, object is
// deleted). Any canonical/eager or unexpired reference keeps the object.
// The caller (daemon housekeeping loop, M4) supplies refs from the
// projection. Returns the hashes actually deleted.
func (s *Store) HousekeepEphemeral(refs []Ref, now time.Time) ([]string, error) {
	byHash := map[string][]Ref{}
	for _, r := range refs {
		byHash[r.Hash] = append(byHash[r.Hash], r)
	}
	var deleted []string
	for hash, hr := range byHash {
		if !s.allEphemeralExpired(hash, hr, now) {
			continue
		}
		if !s.Exists(hash) {
			continue
		}
		if err := s.fs.Remove(s.Path(hash)); err != nil {
			return deleted, fmt.Errorf("removing expired ephemeral %s: %w", hash, err)
		}
		deleted = append(deleted, hash)
	}
	return deleted, nil
}

func (s *Store) allEphemeralExpired(hash string, refs []Ref, now time.Time) bool {
	found := false
	for _, r := range refs {
		if r.Hash != hash {
			continue
		}
		found = true
		if r.TextClass != ClassEphemeral {
			return false
		}
		if now.Sub(r.CreatedAt) <= s.ttl {
			return false
		}
	}
	return found
}
