package daemon_test

// P2-4: saved searches — add, list (name-sorted, idempotent replace), run
// (executes through the real search path), remove; and persistence across a
// daemon restart (device-local, survives).

import (
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
)

func TestP24SavedSearches(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzsaved decision about anchors"}); err != nil {
		t.Fatal(err)
	}

	if err := d.SavedAdd("anchors", "zzsaved"); err != nil {
		t.Fatal(err)
	}
	if err := d.SavedAdd("beta", "other"); err != nil {
		t.Fatal(err)
	}
	// idempotent replace by name
	if err := d.SavedAdd("anchors", "zzsaved anchors"); err != nil {
		t.Fatal(err)
	}
	// invalid name refused
	if err := d.SavedAdd("bad name!", "q"); err == nil {
		t.Fatal("invalid saved-search name accepted")
	}

	list, err := d.SavedList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "anchors" || list[1].Name != "beta" {
		t.Fatalf("saved list wrong (want anchors,beta): %+v", list)
	}
	if list[0].Query != "zzsaved anchors" {
		t.Fatalf("replace did not take: %q", list[0].Query)
	}

	// run executes the query
	out, err := d.SavedRun("anchors", daemon.SearchOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out.Results) == 0 {
		t.Fatal("saved run returned no results for a matching query")
	}
	if _, err := d.SavedRun("nope", daemon.SearchOptions{}); err == nil {
		t.Fatal("running an unknown saved search should error")
	}

	// persists across restart (device-local)
	d.Close()
	d2, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	list2, err := d2.SavedList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list2) != 2 {
		t.Fatalf("saved searches did not persist across restart: %+v", list2)
	}

	// remove
	if err := d2.SavedRemove("beta"); err != nil {
		t.Fatal(err)
	}
	if err := d2.SavedRemove("beta"); err == nil {
		t.Fatal("removing a missing saved search should error")
	}
	list3, _ := d2.SavedList()
	if len(list3) != 1 || list3[0].Name != "anchors" {
		t.Fatalf("remove wrong: %+v", list3)
	}
}
