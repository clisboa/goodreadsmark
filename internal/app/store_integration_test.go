//go:build integration

package app

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func closeTestStore(t *testing.T, store *Store) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Error(err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.Context(), StoreOptions{Path: filepath.Join(t.TempDir(), "state.sqlite"), Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func TestStorePythonCompatibility(t *testing.T) {
	data, err := os.ReadFile("testdata/python-v1.fixture")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.sqlite")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	vector, err := os.ReadFile("testdata/python-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct{ Scope string }
	if err := json.Unmarshal(vector, &metadata); err != nil {
		t.Fatal(err)
	}
	cfg := Config{BaseURL: "https://books.example/prefix", UserID: "123", Shelf: "to-read", ContentType: "ebook"}
	if cfg.Scope() != metadata.Scope {
		t.Fatal("legacy scope lost")
	}
	store, err := OpenStore(t.Context(), StoreOptions{Path: path, Scope: cfg.Scope()})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, store)
	for id, want := range map[string]string{"1": "completed", "2": "uncertain", "3": "discovered", "4": "failed"} {
		row, exists, err := store.Get(t.Context(), id)
		if err != nil || !exists || row.Status != want {
			t.Fatal(id, row, err)
		}
	}
	initialized, err := store.Initialized(t.Context())
	if err != nil || !initialized {
		t.Fatal("legacy initialization lost")
	}
	failed, err := store.FailedReleases(t.Context(), "4")
	if err != nil || !failed[Identity{Source: "test", ID: "release-4"}] {
		t.Fatal("attempt history lost")
	}
	if err := store.Transition(t.Context(), "3", Change{Status: "review", Reason: "go_compatibility"}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreIntentLockAndReadOnly(t *testing.T) {
	store := openTestStore(t)
	if err := store.Discover(t.Context(), []Book{dune(), dune()}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.StartSubmission(
		t.Context(),
		Submission{BookID: "1", Release: releaseOf("Dune"), At: 2},
	); err != nil {
		t.Fatal(err)
	}
	row, exists, err := store.Get(t.Context(), "1")
	if err != nil || !exists || row.Status != "submitting" || row.Attempts != 1 {
		t.Fatal(row, err)
	}
	if other, err := OpenStore(t.Context(), StoreOptions{Path: store.path, Scope: "test"}); err == nil {
		closeTestStore(t, other)
		t.Fatal("second writer accepted")
	}
	reader, err := OpenStore(t.Context(), StoreOptions{Path: store.path, Scope: "test", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, reader)
	if rows, err := reader.Records(t.Context()); err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	info, err := os.Stat(store.path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("state not private", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(t.Context(), StoreOptions{Path: store.path, Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, reopened)
	row, _, err = reopened.Get(t.Context(), "1")
	if err != nil || row.Status != "submitting" {
		t.Fatal("intent lost", err)
	}
}

func TestStoreRetryAndBaseline(t *testing.T) {
	store := openTestStore(t)
	if err := store.Discover(t.Context(), []Book{dune()}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedBaseline(t.Context(), []Book{dune()}, 2); err != nil {
		t.Fatal(err)
	}
	row, _, err := store.Get(t.Context(), "1")
	if err != nil || row.Status != "baseline" {
		t.Fatal(row, err)
	}
	if err := store.Retry(t.Context(), "1", false); err != nil {
		t.Fatal(err)
	}
	if err := store.StartSubmission(
		t.Context(),
		Submission{BookID: "1", Release: releaseOf("Dune"), At: 3},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(t.Context(), "1", Change{Status: "uncertain", Reason: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Retry(t.Context(), "1", false); err == nil {
		t.Fatal("uncertain automatically retried")
	}
	if err := store.Retry(t.Context(), "1", true); err != nil {
		t.Fatal(err)
	}
	if err := store.StartSubmission(
		t.Context(),
		Submission{BookID: "1", Release: releaseOf("Dune"), At: 4},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(
		t.Context(),
		"1",
		Change{Status: "failed", Reason: "failed", NextCheck: 10},
	); err != nil {
		t.Fatal(err)
	}
	failed, err := store.FailedReleases(t.Context(), "1")
	if err != nil || !failed[Identity{Source: "test", ID: "abc"}] {
		t.Fatal(failed, err)
	}
	rows, err := store.Due(t.Context(), DueOptions{Now: 9, Limit: 20, MaxAttempts: 3})
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	rows, err = store.Due(t.Context(), DueOptions{Now: 10, Limit: 20, MaxAttempts: 3})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
}

func TestStoreScopeAndRetryGuards(t *testing.T) {
	store := openTestStore(t)
	if err := store.Discover(t.Context(), []Book{dune()}, 1); err != nil {
		t.Fatal(err)
	}
	other, err := OpenStore(t.Context(), StoreOptions{Path: store.path, Scope: "different", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, other)
	rows, err := other.Records(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatal("scopes mixed", err)
	}
	for _, status := range []string{"queued", "submitting", "completed"} {
		if err := store.Transition(t.Context(), "1", Change{Status: status, Reason: "test"}); err != nil {
			t.Fatal(err)
		}
		if err := store.Retry(t.Context(), "1", true); err == nil {
			t.Fatal("retry guard bypassed", status)
		}
	}
}

func TestStoreRejectsIncompleteVersionOneSchema(t *testing.T) {
	data, err := os.ReadFile("testdata/python-v1.fixture")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.sqlite")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	// Corrupt only a synthetic copy; opening it must not silently recreate empty history.
	if _, err := db.ExecContext(t.Context(), "DROP TABLE books"); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenStore(t.Context(), StoreOptions{Path: path, Scope: "test"})
	if err == nil {
		closeTestStore(t, opened)
		t.Fatal("incomplete schema accepted")
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='books'").
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("missing history was silently recreated")
	}
}
