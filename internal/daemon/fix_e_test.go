package daemon_test

// DEPLOY-E tests: config-file knobs reach the daemon (E2) and the product
// gates are computed from stored outcomes + ranks (E3).

import (
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/identity"
)

// E2: rank_profile in the device TOML reaches a daemon started with no
// environment — the supervised-service path that the env var never reached.
func TestRankProfileFromDeviceConfig(t *testing.T) {
	t.Setenv("CAIRN_RANK_PROFILE", "") // prove it is the FILE, not the env
	dir := initCairn(t)
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Device.RankProfile = "p2"
	if err := loaded.Device.SaveDevice(loaded.DeviceDir); err != nil {
		t.Fatal(err)
	}

	d := startDaemon(t, dir)
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "profile probe"}); err != nil {
		t.Fatal(err)
	}
	// the live profile is visible in why-ranked arithmetic: P2 explanations
	// carry S/I/N components — but the cheapest truthful probe is the
	// status surface agents and operators actually read
	out, err := d.Search(daemon.SearchOptions{Query: "profile probe", K: 1})
	if err != nil || len(out.Results) != 1 {
		t.Fatalf("search: %v", err)
	}
	why, err := d.WhyRanked(out.InteractionID, out.Results[0].MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(why, "search-P2") {
		t.Fatalf("daemon did not adopt the TOML rank_profile:\n%s", why)
	}
}

// E3: with ≥GateOutcomeMinSamples outcomes the gates render computed
// PASS/FAIL percentages from stored final ranks — no diary required.
func TestGatesComputeSuccessAt5(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "the gate corpus message about anchors"})
	if err != nil {
		t.Fatal(err)
	}

	// 26 found-at-rank-1 + 4 workarounds = 30 outcomes, 86% at-5, 13% workaround
	for i := 0; i < config.GateOutcomeMinSamples; i++ {
		out, err := d.Search(daemon.SearchOptions{Query: "gate corpus anchors", K: 5, BudgetChars: 2000})
		if err != nil || len(out.Results) == 0 {
			t.Fatalf("search %d: %v", i, err)
		}
		if i < 26 {
			if err := d.Outcome(out.InteractionID, "found", res.MessageID); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := d.Outcome(out.InteractionID, "manual_workaround", ""); err != nil {
				t.Fatal(err)
			}
		}
	}

	var b strings.Builder
	if err := d.GatesReport(&b); err != nil {
		t.Fatal(err)
	}
	report := b.String()
	if !strings.Contains(report, "PASS (26/30 at rank ≤5 = 86%") {
		t.Fatalf("Success@5 not computed:\n%s", report)
	}
	if !strings.Contains(report, "PASS (4/30 = 13%") {
		t.Fatalf("workaround rate not computed:\n%s", report)
	}
	if strings.Contains(report, "diary protocol (DOGFOOD.md, M8)") {
		// median time-to-context stays human-measured — that row may remain,
		// but the two computed rows must not claim the diary
		if strings.Contains(report, "found (needs ≥30 genuine handoffs; diary protocol)") {
			t.Fatalf("computed gate still points at the diary:\n%s", report)
		}
	}
}
