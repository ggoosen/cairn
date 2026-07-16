package daemon_test

// P3-3b: a thin node marks its universal search + digest partial (spec §7 —
// only a recent local window is searched); a full node does not.

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

func initThinCairn(t *testing.T) string {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	dir := filepath.Join(t.TempDir(), "cairn")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dir, Role: config.RoleThin, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestP33bThinNodeSearchAndDigestPartial(t *testing.T) {
	dir := initThinCairn(t)
	d := startDaemon(t, dir)
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "hello from a thin node"}); err != nil {
		t.Fatal(err)
	}

	so, err := d.Search(daemon.SearchOptions{Query: "hello", BudgetChars: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if !so.Partial || so.PartialReason == "" {
		t.Fatalf("thin search not marked partial: %+v", so)
	}

	do, err := d.Digest(daemon.DigestOptions{AgentView: "operator", BudgetChars: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if !do.Partial || do.PartialReason == "" {
		t.Fatalf("thin digest not marked partial: %+v", do)
	}
}

func TestP33bFullNodeSearchNotPartial(t *testing.T) {
	dir := initCairn(t) // full node
	d := startDaemon(t, dir)
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "hello from a full node"}); err != nil {
		t.Fatal(err)
	}
	so, err := d.Search(daemon.SearchOptions{Query: "hello", BudgetChars: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if so.Partial {
		t.Fatalf("full node marked its search partial: %+v", so)
	}
}
