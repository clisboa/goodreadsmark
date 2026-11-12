//go:build integration

package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityImportOrder(t *testing.T) {
	for _, order := range []string{"csv_first", "widget_first"} {
		t.Run(order, func(t *testing.T) {
			store := openTestStore(t)
			csv := dune()
			review := csv
			review.ID = "review:987"
			review.ISBN13 = ""
			first, second := csv, review
			if order == "widget_first" {
				first, second = review, csv
			}
			if err := store.Discover(t.Context(), []Book{first}, 1); err != nil {
				t.Fatal(err)
			}
			if err := store.StartSubmission(
				t.Context(),
				Submission{BookID: first.ID, Release: releaseOf("Dune"), At: 2},
			); err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{"queued", "completed", "uncertain"} {
				if err := store.Transition(
					t.Context(),
					first.ID,
					Change{Status: state, Reason: "preserve"},
				); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err := store.Discover(t.Context(), []Book{second, first}, 3); err != nil {
						t.Fatal(err)
					}
				}
				rows, err := store.Records(t.Context())
				if err != nil || len(rows) != 1 || rows[0].Book.ID != first.ID || rows[0].Status != state ||
					rows[0].Attempts != 1 ||
					rows[0].Book.ISBN13 != csv.ISBN13 ||
					rows[0].Identity.ID != "abc" {
					t.Fatal(rows, err)
				}
				if err := store.Retry(t.Context(), second.ID, false); err == nil {
					t.Fatal("alias bypassed retry guard", state)
				}
			}
			if err := store.Retry(t.Context(), second.ID, true); err != nil {
				t.Fatal(err)
			}
			row, _, err := store.Get(t.Context(), first.ID)
			if err != nil || row.Status != "discovered" {
				t.Fatal(row, err)
			}
		})
	}
}

func TestIdentityExactMatching(t *testing.T) {
	cases := []struct {
		name, title, author string
		merge               bool
	}{
		{"unicode_case_space", "  DÚNE\t(Book, #1) ", "RENE\u0301  Writer", true},
		{"subtitle", "Dúne: New Subtitle (Book, #1)", "René Writer", false},
		{"series", "Dúne (Book, #2)", "René Writer", false},
		{"punctuation", "Dúne (Book #1)", "René Writer", false},
		{"author", "Dúne (Book, #1)", "Different Writer", false},
		{"missing_author", "Dúne (Book, #1)", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			a := Book{ID: "1", Title: "Dúne (Book, #1)", Author: "René Writer"}
			b := Book{ID: "review:1", Title: tc.title, Author: tc.author}
			if err := store.Discover(t.Context(), []Book{a, b}, 1); err != nil {
				t.Fatal(err)
			}
			rows, err := store.Records(t.Context())
			want := 2
			if tc.merge {
				want = 1
			}
			if err != nil || len(rows) != want {
				t.Fatal(rows, err)
			}
		})
	}
}

func TestIdentityAmbiguityAndEditions(t *testing.T) {
	store := openTestStore(t)
	a := dune()
	b := a
	b.ID = "2"
	r := a
	r.ID = "review:987"
	if err := store.Discover(t.Context(), []Book{a, b, r}, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Records(t.Context())
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	row, _, err := store.Get(t.Context(), r.ID)
	if err != nil || row.Status != "held" || row.Reason != "ambiguous_goodreads_identity" {
		t.Fatal(row, err)
	}
	if err := store.Discover(t.Context(), []Book{r}, 2); err != nil {
		t.Fatal(err)
	}
	row, _, err = store.Get(t.Context(), r.ID)
	if err != nil || row.Status != "held" {
		t.Fatal(row, err)
	}
	due, err := store.Due(t.Context(), DueOptions{Now: 5, Limit: 20, MaxAttempts: 3})
	if err != nil || len(due) != 2 {
		t.Fatal(due, err)
	}
}

func TestIdentityDoesNotMergeTwoNumericEditionsIntoWidget(t *testing.T) {
	store := openTestStore(t)
	r := dune()
	r.ID = "review:987"
	a := dune()
	b := a
	b.ID = "2"
	if err := store.Discover(t.Context(), []Book{r, a, b}, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Records(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if err := store.Retry(t.Context(), "1", false); err != nil {
		t.Fatal(err)
	}
	if err := store.Retry(t.Context(), "2", false); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityAliasesPersistAndAreScoped(t *testing.T) {
	store := openTestStore(t)
	a := dune()
	r := a
	r.ID = "review:987"
	if err := store.Discover(t.Context(), []Book{a, r}, 1); err != nil {
		t.Fatal(err)
	}
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := OpenStore(t.Context(), StoreOptions{Path: path, Scope: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Discover(t.Context(), []Book{r}, 2); err != nil {
		t.Fatal(err)
	}
	if row, ok, err := other.Get(t.Context(), r.ID); err != nil || !ok || row.Book.ID != r.ID {
		t.Fatal(row, err)
	}
	closeTestStore(t, other)
	reopened, err := OpenStore(t.Context(), StoreOptions{Path: path, Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, reopened)
	if err := reopened.Discover(t.Context(), []Book{r}, 3); err != nil {
		t.Fatal(err)
	}
	rows, err := reopened.Records(t.Context())
	if err != nil || len(rows) != 1 || rows[0].Book.ID != "1" {
		t.Fatal(rows, err)
	}
}

func TestIdentityDiscoveryRollsBackAliasesAndBooks(t *testing.T) {
	store := openTestStore(t)
	bad := dune()
	bad.ID = "invalid"
	if err := store.Discover(t.Context(), []Book{dune(), bad}, 1); err == nil {
		t.Fatal("invalid discovery accepted")
	}
	rows, err := store.Records(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatal("partial book batch committed", rows, err)
	}
	var aliases int
	if err := store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM source_aliases").
		Scan(&aliases); err != nil ||
		aliases != 0 {
		t.Fatal("partial alias batch committed", aliases, err)
	}
}

func TestIdentityBoundAliasNeverMovesToAnotherRecord(t *testing.T) {
	store := openTestStore(t)
	a := dune()
	b := Book{ID: "2", Title: "Other", Author: "Other Writer"}
	r := a
	r.ID = "review:987"
	if err := store.Discover(t.Context(), []Book{a, b, r}, 1); err != nil {
		t.Fatal(err)
	}
	r.Title, r.Author = b.Title, b.Author
	if err := store.Discover(t.Context(), []Book{r}, 2); err != nil {
		t.Fatal(err)
	}
	target, err := store.resolveAlias(t.Context(), store.db, r.ID)
	if err != nil || target != a.ID {
		t.Fatal("alias rebound", target, err)
	}
	rows, err := store.Records(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
}

func legacyPath(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/python-v1.fixture")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func schemaVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	var version int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestMigrationReadOnlyAndUpgrade(t *testing.T) {
	path := legacyPath(t)
	reader, err := OpenStore(t.Context(), StoreOptions{Path: path, Scope: "test", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	closeTestStore(t, reader)
	if got := schemaVersion(t, path); got != 1 {
		t.Fatal("read-only migrated database", got)
	}
	writer, err := OpenStore(t.Context(), StoreOptions{Path: path, Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	closeTestStore(t, writer)
	if got := schemaVersion(t, path); got != 2 {
		t.Fatal("missing migration", got)
	}
}

func TestMigrationRollback(t *testing.T) {
	path := legacyPath(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	// Test-only fault injection: fail the scope insert after migration DDL has executed.
	if _, err := db.ExecContext(
		t.Context(),
		`CREATE TRIGGER fail_scope BEFORE INSERT ON scopes BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`,
	); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenStore(t.Context(), StoreOptions{Path: path, Scope: "new-scope"})
	if err == nil {
		closeTestStore(t, opened)
		t.Fatal("injected failure ignored")
	}
	if got := schemaVersion(t, path); got != 1 {
		t.Fatal("failed migration changed version", got)
	}
	var tables int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='source_aliases'").
		Scan(&tables); err != nil ||
		tables != 0 {
		t.Fatal("migration was not atomic", tables, err)
	}
}

func TestMigrationRejectsDamagedV2AndUnknown(t *testing.T) {
	for _, mode := range []string{"missing_aliases", "orphan_alias", "bad_alias_key", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			store := openTestStore(t)
			path := store.path
			closeTestStore(t, store)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			query := "DROP TABLE source_aliases"
			switch mode {
			case "unknown":
				query = "PRAGMA user_version=99"
			case "orphan_alias":
				query = "INSERT INTO source_aliases(scope,alias_id,book_id) VALUES ('test','review:99','missing')"
			case "bad_alias_key":
				query = "DROP TABLE source_aliases; CREATE TABLE source_aliases(scope TEXT,alias_id TEXT,book_id TEXT)"
			}
			_, execErr := db.ExecContext(t.Context(), query)
			closeErr := db.Close()
			if execErr != nil || closeErr != nil {
				t.Fatal(execErr, closeErr)
			}
			for _, readOnly := range []bool{false, true} {
				opened, err := OpenStore(t.Context(), StoreOptions{Path: path, Scope: "test", ReadOnly: readOnly})
				if err == nil {
					closeTestStore(t, opened)
					t.Fatal("damaged schema accepted")
				}
			}
		})
	}
}
