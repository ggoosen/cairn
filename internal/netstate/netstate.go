// Package netstate senses the two device conditions spec §7 cares about on a
// mobile/thin node — is the connection METERED, and is the machine on BATTERY —
// without ever guessing.
//
// P3-6 (S9). The `metered` device-config flag has always existed as a manual
// policy switch; this is the automatic half. The one rule that governs every
// line here:
//
//	SENSING MAY ONLY ADD CAUTION, NEVER REMOVE IT.
//
// Every probe returns a tri-state, and the honest answer when a platform cannot
// be read is Unknown — never "no". The daemon combines them as
//
//	effective metered = configured metered  OR  sensed metered == Yes
//
// so an unreadable platform behaves EXACTLY as before this package existed (the
// manual flag alone decides), a sensed "metered" can switch data-spending off
// without operator action, and a sensed "not metered" can never override an
// operator who said metered. There is no code path in which sensing makes a
// node spend data it would not have spent yesterday.
//
// Platform support is honest about its limits (see netstate_linux.go /
// netstate_darwin.go): macOS is the primary platform and Linux best-effort, and
// each probe documents what it can and cannot see.
package netstate

import (
	"context"
	"sync"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// Tri is a tri-state answer. The zero value is Unknown, so a probe that forgets
// to set a field, panics past it, or runs on an unsupported platform yields the
// safe answer by construction.
type Tri int

const (
	Unknown Tri = iota // could not be read — the caller must fall back to config
	Yes
	No
)

func (t Tri) String() string {
	switch t {
	case Yes:
		return "yes"
	case No:
		return "no"
	default:
		return "unknown"
	}
}

// State is one reading of the device's power/network conditions.
type State struct {
	// Metered: the active connection is data-metered (cellular, tethered, or
	// explicitly marked metered by the platform's network manager).
	Metered Tri
	// OnBattery: the machine is running from battery rather than external power.
	// Sensed and REPORTED only — no policy keys off it, because the spec defines
	// no battery policy and inventing one is a product decision, not a platform
	// one (see PROGRESS P3-6).
	OnBattery Tri
	// Source names the probe that answered (e.g. "nmcli", "sysfs", "pmset"), or
	// why nothing did. Diagnostics only — `cairn net` prints it so an operator
	// can tell "not metered" from "could not tell".
	Source string
}

// Sensor reads the current device state. The platform implementations are
// selected at build time; a nil Sensor means "sensing disabled", which is
// indistinguishable from an unreadable platform (both fail safe).
type Sensor interface {
	Sense(ctx context.Context) State
}

// SensorFunc adapts a function to Sensor (the daemon's tests inject fakes).
type SensorFunc func(ctx context.Context) State

func (f SensorFunc) Sense(ctx context.Context) State { return f(ctx) }

// Platform returns the sensor for this build's GOOS. It never returns nil: on an
// unsupported platform it returns one that always answers Unknown.
func Platform() Sensor { return SensorFunc(sensePlatform) }

// Disabled is the sensor for `metered_sense = "off"`: it reads nothing at all
// and says so, so the diagnostic can distinguish "off" from "could not read".
var Disabled Sensor = SensorFunc(func(context.Context) State {
	return State{Source: "sensing disabled (metered_sense = off)"}
})

// Cached wraps a Sensor with a TTL so a probe that shells out (nmcli, pmset)
// runs at most once per config.MeteredSenseTTL no matter how often a search
// path asks. It is safe for concurrent use; a slow probe is serialised rather
// than stampeded, because the cheap thing to do under contention is wait for
// the reading already in flight.
type Cached struct {
	inner Sensor
	ttl   time.Duration
	now   func() time.Time

	mu     sync.Mutex
	last   State
	lastAt time.Time
	primed bool
}

// NewCached wraps s with the standard TTL. A nil inner sensor yields a cache
// that always reports Unknown (sensing off), which is the fail-safe answer.
func NewCached(s Sensor) *Cached {
	return &Cached{inner: s, ttl: config.MeteredSenseTTL, now: time.Now}
}

// Sense returns the cached reading, refreshing it when stale.
func (c *Cached) Sense(ctx context.Context) State {
	if c == nil {
		return State{}
	}
	c.mu.Lock()
	if c.primed && c.now().Sub(c.lastAt) < c.ttl {
		st := c.last
		c.mu.Unlock()
		return st
	}
	if c.inner == nil {
		c.last, c.lastAt, c.primed = State{Source: "no sensor"}, c.now(), true
		st := c.last
		c.mu.Unlock()
		return st
	}
	inner := c.inner
	c.mu.Unlock()

	// The probe runs OUTSIDE the lock (it may exec a helper), bounded so a hung
	// platform tool can never wedge a search.
	pctx, cancel := context.WithTimeout(ctx, config.MeteredSenseTimeout)
	defer cancel()
	st := inner.Sense(pctx)

	c.mu.Lock()
	c.last, c.lastAt, c.primed = st, c.now(), true
	c.mu.Unlock()
	return st
}
