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
	// P4-G6: the query log names principals and queries — operator-tier
	"interaction-list": capAdmin,
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
	"session-prune": capAdmin,

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
	// D9: the pid binding is only meaningful on the device that minted it —
	// a pid number from another machine names an unrelated local process —
	// and only for the process incarnation that existed at mint time.
	// BoundDevice absent (a record written before D9) means the pid is not
	// trusted at all: such a record is reaped on expiry only.
	BoundDevice string `json:"bound_device,omitempty"`
	BoundProc   string `json:"bound_proc,omitempty"` // opaque incarnation token (see procIdentity)

	lastUsed time.Time // in-memory idle tracking; resets on daemon restart
}

// Principal is the hierarchy string recorded on telemetry rows.
func (s *Session) Principal() string { return s.Parent + ">" + s.Name }

// sessions is the daemon-side handle table, persisted in device-local state
// (cache-class: losing it just ends sessions early).
//
// D9 persistence shape: `sessions.json` is a compacted SNAPSHOT and
// `sessions.journal` is an append-only JSONL tail of creates and revokes since
// that snapshot. Every mint used to rewrite the whole array, so the cost of
// the Nth mint grew with N — on the dev node that was 2,673 records rewritten
// per 90-second respawn. Journal replay is idempotent (tokens are unique and
// never reused), so a crash between writing the snapshot and dropping the
// journal is harmless.
type sessions struct {
	mu       sync.Mutex // dispatch runs concurrently for reads
	path     string
	journal  string
	profiles map[string]*Profile
	byToken  map[string]*Session
	deviceID string // this device: pid bindings from any other device are not trusted

	journalEntries int // lines in the journal since the last compaction
	writtenRecords int // D9 test instrumentation: session records serialized since load
}

// journalEntry is one line of the append-only tail: exactly one of Session
// (a mint) or Revoke (a token that is gone) is set.
type journalEntry struct {
	Op      string   `json:"op"`
	Session *Session `json:"session,omitempty"`
	Token   string   `json:"token,omitempty"`
}

func loadSessions(deviceDir, deviceID string, profiles map[string]*Profile, now time.Time) (*sessions, error) {
	s := &sessions{
		path:     filepath.Join(deviceDir, config.SessionsFileName),
		journal:  filepath.Join(deviceDir, config.SessionsJournalFileName),
		profiles: profiles,
		byToken:  map[string]*Session{},
		deviceID: deviceID,
	}
	blob, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		var stored []*Session
		if err := json.Unmarshal(blob, &stored); err != nil {
			return nil, fmt.Errorf("%s corrupt: %w", config.SessionsFileName, err)
		}
		for _, sess := range stored {
			sess.lastUsed = now // idle window restarts across daemon restarts
			s.byToken[sess.Token] = sess
		}
	}
	jblob, jerr := os.ReadFile(s.journal)
	if jerr != nil && !os.IsNotExist(jerr) {
		return nil, jerr
	}
	for _, line := range strings.Split(string(jblob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e journalEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// A torn final line is the expected damage on an unclean exit;
			// stop replaying rather than failing to start (cache-class file).
			break
		}
		switch {
		case e.Op == "create" && e.Session != nil && e.Session.Token != "":
			e.Session.lastUsed = now
			s.byToken[e.Session.Token] = e.Session
		case e.Op == "revoke" && e.Token != "":
			delete(s.byToken, e.Token)
		}
	}
	// D9: load is the natural reaping point — a record nobody can ever use
	// again must not survive a restart, and before this it always did (expiry
	// was checked ONLY when the expired token was presented again, which a
	// dead client never does).
	s.reapLocked(now)
	if err := s.compactLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// writeSnapshot rewrites sessions.json from the live table. O(live sessions),
// so it is a compaction step, never the per-mint path.
func (s *sessions) writeSnapshot() error {
	list := make([]*Session, 0, len(s.byToken))
	for _, sess := range s.byToken {
		list = append(list, sess)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })
	blob, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	s.writtenRecords += len(list)
	return os.WriteFile(s.path, blob, config.KeyFilePerm)
}

// compactLocked folds the journal into the snapshot and drops it.
func (s *sessions) compactLocked() error {
	if err := s.writeSnapshot(); err != nil {
		return err
	}
	if err := os.Remove(s.journal); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.journalEntries = 0
	return nil
}

// appendJournal records one mutation in O(1) and compacts once the tail has
// grown past the live set (amortized O(1) per mint, with a floor so a small
// mesh does not compact on nearly every mint).
func (s *sessions) appendJournal(e journalEntry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.journal, os.O_WRONLY|os.O_CREATE|os.O_APPEND, config.KeyFilePerm)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	s.writtenRecords++
	s.journalEntries++
	limit := len(s.byToken)
	if limit < config.SessionJournalMinCompact {
		limit = config.SessionJournalMinCompact
	}
	if s.journalEntries >= limit {
		return s.compactLocked()
	}
	return nil
}

// reapLocked drops every record that can never be used again: past its TTL, or
// bound to a process on this device that is gone. It returns a count per
// reason, for `cairn session prune` to report.
//
// RULING-NEEDED (PROGRESS.md "Author rulings needed — D9 session
// idle-across-restart"): IDLE is deliberately NOT a reaping reason here, and
// loadSessions still resets `lastUsed` — persisting the idle clock across a
// restart would make the README's "auto-revoked on exit or idle" true, but it
// changes behaviour a comment calls deliberate, so it needs the author, not
// this fix.
func (s *sessions) reapLocked(now time.Time) map[string]int {
	removed := map[string]int{}
	for token, sess := range s.byToken {
		if reason, dead := s.deadLocked(sess, now); dead {
			delete(s.byToken, token)
			removed[reason]++
		}
	}
	return removed
}

// deadLocked judges one record. Every undecidable case reads as LIVE: reaping
// a session that is still in use breaks a running agent, while failing to reap
// one costs at most its remaining TTL.
func (s *sessions) deadLocked(sess *Session, now time.Time) (string, bool) {
	exp, err := time.Parse(config.WallTimeFormat, sess.ExpiresAt)
	if err != nil {
		// resolve() has always treated an unparseable expiry as expired;
		// a record nothing can validate is not a live grant.
		return "unreadable", true
	}
	if !now.Before(exp) {
		return "expired", true
	}
	// The pid is trusted only for a record this device minted (a pid number
	// from another device names an unrelated local process), and a record
	// written before D9 carries no device, so it is judged on expiry alone.
	if sess.BoundPID <= 0 || s.deviceID == "" || sess.BoundDevice != s.deviceID {
		return "", false
	}
	switch pidState(sess.BoundPID) {
	case procGone:
		return "process-gone", true
	case procForeign:
		return "process-recycled", true
	}
	// Alive — but is it the SAME process? A recycled pid must not resurrect a
	// dead session's record.
	if sess.BoundProc != "" {
		if cur, ok := procIdentity(sess.BoundPID); ok && cur != sess.BoundProc {
			return "process-recycled", true
		}
	}
	return "", false
}

// prune forces a sweep and compaction, reporting what went (D9: the operator's
// way to clear a backlog accumulated before this daemon build).
func (s *sessions) prune(now time.Time) (map[string]int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := s.reapLocked(now)
	return removed, len(s.byToken), s.compactLocked()
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
	// D9: sweep on create, so the table cannot grow without bound while
	// clients respawn. Cheap: it is a walk of the live set, and the create
	// path already holds the lock.
	s.reapLocked(now)
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
	if pid > 0 {
		sess.BoundDevice = s.deviceID
		if ident, ok := procIdentity(pid); ok {
			sess.BoundProc = ident
		}
	}
	s.byToken[sess.Token] = sess
	return sess, s.appendJournal(journalEntry{Op: "create", Session: sess})
}

func (s *sessions) revoke(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byToken[token]; !ok {
		return fmt.Errorf("unknown session")
	}
	delete(s.byToken, token)
	return s.appendJournal(journalEntry{Op: "revoke", Token: token})
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
		_ = s.appendJournal(journalEntry{Op: "revoke", Token: token})
		return nil, nil, fmt.Errorf("session expired")
	}
	if now.Sub(sess.lastUsed) > config.SessionIdleTimeout {
		delete(s.byToken, token)
		_ = s.appendJournal(journalEntry{Op: "revoke", Token: token})
		return nil, nil, fmt.Errorf("session idle-revoked (idle > %v)", config.SessionIdleTimeout)
	}
	prof, ok := s.profiles[sess.Profile]
	if !ok {
		return nil, nil, fmt.Errorf("session profile %q no longer exists", sess.Profile)
	}
	sess.lastUsed = now
	return sess, prof, nil
}

// list returns summaries (token prefixes only — handles stay opaque). It
// sweeps first: D9's complaint was that `cairn session list` showed 2,673
// records, 1,524 of them expired, which makes it useless as the kill switch it
// is meant to be. What it prints is now what is actually grantable.
func (s *sessions) list(now time.Time) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if removed := s.reapLocked(now); len(removed) > 0 {
		_ = s.compactLocked() // best-effort: the in-memory table is authoritative
	}
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

// SessionRecordsWrittenForTest reports how many session records this daemon has
// serialized since load. D9's acceptance is that minting the 1000th session
// costs no more than the 10th; a counter is how a test asserts that without
// timing anything.
func (d *Daemon) SessionRecordsWrittenForTest() int {
	d.sessions.mu.Lock()
	defer d.sessions.mu.Unlock()
	return d.sessions.writtenRecords
}
