package daemon

// P2-4: saved searches — named, re-runnable queries (spec §12). Device-local
// operator convenience (not replicated, not an event, survives reindex). The
// daemon is the single writer; the file is a small JSON list rewritten
// atomically on each mutation.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// SavedSearch is one named query.
type SavedSearch struct {
	Name      string `json:"name"`
	Query     string `json:"query"`
	CreatedAt string `json:"created_at"`
}

var savedNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

func (d *Daemon) savedSearchPath() string {
	return filepath.Join(d.loaded.DeviceDir, config.SavedSearchesFileName)
}

func (d *Daemon) loadSavedSearches() ([]SavedSearch, error) {
	blob, err := d.fs.ReadFile(d.savedSearchPath())
	if err != nil {
		return nil, nil // absent = empty list
	}
	var out []SavedSearch
	if err := json.Unmarshal(blob, &out); err != nil {
		return nil, fmt.Errorf("%s corrupt: %w", config.SavedSearchesFileName, err)
	}
	return out, nil
}

func (d *Daemon) writeSavedSearches(list []SavedSearch) error {
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	blob, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	// saved-searches.json is MUTABLE (unlike write-once objects/segments), so
	// overwrite-by-remove then atomic write, as the views path does.
	d.fs.Remove(d.savedSearchPath())
	return fsx.WriteFileAtomic(d.fs, d.savedSearchPath(), blob, config.FilePerm)
}

// SavedAdd creates or replaces a named query (idempotent by name).
func (d *Daemon) SavedAdd(name, query string) error {
	if err := d.writable(); err != nil {
		return err
	}
	if !savedNameRe.MatchString(name) {
		return fmt.Errorf("invalid saved-search name %q (letters/digits/._- , ≤64)", name)
	}
	if query == "" {
		return fmt.Errorf("saved search %q has an empty query", name)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	list, err := d.loadSavedSearches()
	if err != nil {
		return err
	}
	next := make([]SavedSearch, 0, len(list)+1)
	for _, s := range list {
		if s.Name != name {
			next = append(next, s)
		}
	}
	next = append(next, SavedSearch{Name: name, Query: query, CreatedAt: d.now().UTC().Format(config.WallTimeFormat)})
	return d.writeSavedSearches(next)
}

// SavedList returns all saved searches (name-sorted).
func (d *Daemon) SavedList() ([]SavedSearch, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list, err := d.loadSavedSearches()
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// SavedRemove deletes a saved search; error if it does not exist.
func (d *Daemon) SavedRemove(name string) error {
	if err := d.writable(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	list, err := d.loadSavedSearches()
	if err != nil {
		return err
	}
	next := make([]SavedSearch, 0, len(list))
	found := false
	for _, s := range list {
		if s.Name == name {
			found = true
			continue
		}
		next = append(next, s)
	}
	if !found {
		return fmt.Errorf("no saved search named %q", name)
	}
	return d.writeSavedSearches(next)
}

// SavedRun executes a saved search by name through the ordinary search path.
func (d *Daemon) SavedRun(name string, opts SearchOptions) (*SearchOutput, error) {
	d.mu.Lock()
	list, err := d.loadSavedSearches()
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, s := range list {
		if s.Name == name {
			opts.Query = s.Query
			return d.Search(opts)
		}
	}
	return nil, fmt.Errorf("no saved search named %q", name)
}
