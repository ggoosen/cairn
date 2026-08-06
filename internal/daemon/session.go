package daemon

// N2 — the rulings §7.2 capability tier system, activated (P0 ran tier-1
// only). Sessions are opaque daemon-side records (RULINGS.md R23 — no
// JWT/macaroons): a random token maps to {profile, principal hierarchy,
// TTL, idle window}. The CLI without a handle remains operator tier-1;
// MCP always runs under a handle (R21 — never tier-1).
//
// Isolation honesty (R22): everything here runs as the SAME OS user.
// Profiles prevent ACCIDENTS — an agent surface structurally can't retract
// or re-topic the mesh — they do not defend against a malicious local
// process, which could read the socket path and mint its own session.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ggoosen/cairn/internal/config"
)

// Capabilities are coarse op groups (fine enough for R21's "read-only +
// send" remote default without per-op profile sprawl).
const (
	capRead    = "read"
	capSend    = "send"
	capSignal  = "signal"
	capOutcome = "outcome"
	capAdmin   = "admin"
)

var validCaps = map[string]bool{capRead: true, capSend: true, capSignal: true, capOutcome: true, capAdmin: true}

// opCapability classifies every IPC op. Ops absent here (and unknown ops)
// are treated as admin — the conservative default.
var opCapability = map[string]string{
	"search": capRead, "digest": capRead, "peek": capRead, "fetch": capRead,
	"why-ranked": capRead, "status": capRead, "source-ref": capRead,
	"thread": capRead, "topic-list": capRead, // RETR-D4/D5
	"saved-list": capRead, "saved-run": capRead, // P2-4
	"saved-add": capAdmin, "saved-remove": capAdmin,
	"rank-stats": capAdmin, // P2-3b calibration (analysis, operator-tier)
	"map":        capRead,  // P2-5 local navigation view
	"compact":    capRead,  // P2-6 current-state compaction view

	"publish":          capSend,
	"stage-attachment": capSend, // G6: streamed attachment staging accompanies a publish
	"signal":           capSignal,
	"outcome":          capOutcome,

	"retract": capAdmin, "topic-create": capAdmin, "topic-ensure": capAdmin,
	"link": capAdmin, "unlink": capAdmin, "pin": capAdmin, "unpin": capAdmin,
	"revise": capAdmin, "export": capAdmin, "export-ingest": capAdmin,
	"resolve": capAdmin, "gates": capAdmin, "reserve-release": capAdmin,
	"reserve-status": capAdmin, "emergency-publish": capAdmin,
	"housekeep": capAdmin, "reindex-semantic": capAdmin,
	"sync-now": capAdmin, "sync-status": capRead,
	// SYNC-C1: peer topology is device policy — mutation is operator-tier;
	// listing is read-tier (sync-status already exposes the peer list there)
	"peer-add": capAdmin, "peer-remove": capAdmin, "peer-list": capRead,
	"session-create": capAdmin, "session-revoke": capAdmin, "session-list": capAdmin,

	// durable subscriptions are structural (N3): agent-standard cannot
	// create or mutate them
	"subscribe-durable": capAdmin, "subscription-update": capAdmin,
	"subscription-disable": capAdmin, "subscription-delete": capAdmin,
	"subscription-list": capAdmin,

	// R25/R55 SESSION tier: the local interest lives in view.json, appends NO
	// event, and never escalates capability — any agent that can read its own
	// digest may tune its own view's interest. capRead, deliberately NOT
	// capAdmin (that is the durable tier above, which MCP never reaches).
	"subscribe-local": capRead, "subscription-local-get": capRead,

	// R56: reading the onboarding record is read-tier (authorship verification
	// happens server-side; application is client-side).
	"onboarding-get": capRead,

	// derivatives (N4): reads are read-tier; invalidation is structural
	"derivative-list": capRead, "summary-show": capRead,
	"derivative-invalidate": capAdmin,
}

func capabilityFor(op string) string {
	if c, ok := opCapability[op]; ok {
		return c
	}
	return capAdmin
}

// Profile is a named capability set.
type Profile struct {
	Name string
	Caps map[string]bool
}

func (p *Profile) Allows(capability string) bool { return p != nil && p.Caps[capability] }

func newProfile(name string, caps ...string) *Profile {
	m := map[string]bool{}
	for _, c := range caps {
		m[c] = true
	}
	return &Profile{Name: name, Caps: m}
}

// builtinProfiles are the three ruled tiers. `full` is what a handle-less
// local CLI gets (tier-1 preserved); agent-standard is the Desktop default
// (R21); read-only(+send) is the future remote default.
func builtinProfiles() map[string]*Profile {
	return map[string]*Profile{
		"full":           newProfile("full", capRead, capSend, capSignal, capOutcome, capAdmin),
		"agent-standard": newProfile("agent-standard", capRead, capSend, capSignal, capOutcome),
		"read-only":      newProfile("read-only", capRead),
	}
}

// profilesFile is the strict device-local TOML: additional profiles only —
// redefining a builtin is a load error, not a silent override.
type profilesFile struct {
	Profiles map[string]struct {
		Capabilities []string `toml:"capabilities"`
	} `toml:"profiles"`
}

// LoadProfiles returns builtins merged with <deviceDir>/profiles.toml.
func LoadProfiles(deviceDir string) (map[string]*Profile, error) {
	out := builtinProfiles()
	path := filepath.Join(deviceDir, config.ProfilesFileName)
	blob, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var pf profilesFile
	meta, err := toml.Decode(string(blob), &pf)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.ProfilesFileName, err)
	}
	if undec := meta.Undecoded(); len(undec) > 0 {
		return nil, fmt.Errorf("%s: unknown key %q", config.ProfilesFileName, undec[0].String())
	}
	for name, spec := range pf.Profiles {
		if _, builtin := out[name]; builtin {
			return nil, fmt.Errorf("%s: profile %q is builtin and cannot be redefined", config.ProfilesFileName, name)
		}
		p := &Profile{Name: name, Caps: map[string]bool{}}
		for _, c := range spec.Capabilities {
			if !validCaps[c] {
				return nil, fmt.Errorf("%s: profile %q: unknown capability %q", config.ProfilesFileName, name, c)
			}
			p.Caps[c] = true
		}
		out[name] = p
	}
	return out, nil
}

// Session is one opaque handle record (R23).
type Session struct {
	Token     string `json:"token"`
	Name      string `json:"name"` // leaf principal (the actor it acts as)
	Profile   string `json:"profile"`
	Parent    string `json:"parent"` // creating principal ("operator" in P1)
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	BoundPID  int    `json:"bound_pid,omitempty"`

	lastUsed time.Time // in-memory idle tracking; resets on daemon restart
}

// Principal is the hierarchy string recorded on telemetry rows.
func (s *Session) Principal() string { return s.Parent + ">" + s.Name }

// sessions is the daemon-side handle table, persisted as JSON in device-
// local state (cache-class: losing it just ends sessions early).
type sessions struct {
	mu       sync.Mutex // dispatch runs concurrently for reads
	path     string
	profiles map[string]*Profile
	byToken  map[string]*Session
}

func loadSessions(deviceDir string, profiles map[string]*Profile, now time.Time) (*sessions, error) {
	s := &sessions{
		path:     filepath.Join(deviceDir, config.SessionsFileName),
		profiles: profiles,
		byToken:  map[string]*Session{},
	}
	blob, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var stored []*Session
	if err := json.Unmarshal(blob, &stored); err != nil {
		return nil, fmt.Errorf("%s corrupt: %w", config.SessionsFileName, err)
	}
	for _, sess := range stored {
		sess.lastUsed = now // idle window restarts across daemon restarts
		s.byToken[sess.Token] = sess
	}
	return s, nil
}

func (s *sessions) persist() error {
	list := make([]*Session, 0, len(s.byToken))
	for _, sess := range s.byToken {
		list = append(list, sess)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })
	blob, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, blob, config.KeyFilePerm)
}

func (s *sessions) create(name, profile, parent string, pid int, now time.Time) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[profile]; !ok {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
	if name == "" || strings.ContainsAny(name, "/\\>") || strings.Contains(name, "..") {
		return nil, fmt.Errorf("invalid session name %q", name)
	}
	raw := make([]byte, config.SessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	sess := &Session{
		Token:     hex.EncodeToString(raw),
		Name:      name,
		Profile:   profile,
		Parent:    parent,
		CreatedAt: now.UTC().Format(config.WallTimeFormat),
		ExpiresAt: now.Add(config.SessionTTLDefault).UTC().Format(config.WallTimeFormat),
		BoundPID:  pid,
		lastUsed:  now,
	}
	s.byToken[sess.Token] = sess
	return sess, s.persist()
}

func (s *sessions) revoke(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byToken[token]; !ok {
		return fmt.Errorf("unknown session")
	}
	delete(s.byToken, token)
	return s.persist()
}

// resolve validates a handle and returns it with its profile, touching the
// idle clock. Every failure is a capability error at the caller.
func (s *sessions) resolve(token string, now time.Time) (*Session, *Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byToken[token]
	if !ok {
		return nil, nil, fmt.Errorf("unknown or revoked session")
	}
	exp, err := time.Parse(config.WallTimeFormat, sess.ExpiresAt)
	if err != nil || !now.Before(exp) {
		delete(s.byToken, token)
		_ = s.persist()
		return nil, nil, fmt.Errorf("session expired")
	}
	if now.Sub(sess.lastUsed) > config.SessionIdleTimeout {
		delete(s.byToken, token)
		_ = s.persist()
		return nil, nil, fmt.Errorf("session idle-revoked (idle > %v)", config.SessionIdleTimeout)
	}
	prof, ok := s.profiles[sess.Profile]
	if !ok {
		return nil, nil, fmt.Errorf("session profile %q no longer exists", sess.Profile)
	}
	sess.lastUsed = now
	return sess, prof, nil
}

// list returns summaries (token prefixes only — handles stay opaque).
func (s *sessions) list() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.byToken))
	for _, sess := range s.byToken {
		out = append(out, map[string]any{
			"token_prefix": sess.Token[:8],
			"name":         sess.Name,
			"profile":      sess.Profile,
			"principal":    sess.Principal(),
			"created_at":   sess.CreatedAt,
			"expires_at":   sess.ExpiresAt,
			"bound_pid":    sess.BoundPID,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["created_at"].(string) < out[j]["created_at"].(string) })
	return out
}
