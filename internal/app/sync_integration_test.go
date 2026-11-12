//go:build integration

package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type fakeCatalog struct{ books []Book }

func (f *fakeCatalog) Discover(context.Context) (Discovery, error) {
	return Discovery{Books: f.books, Warnings: []string{}, Successful: true}, nil
}

type fakeAcquirer struct {
	releases               map[string][]Release
	current                map[Identity]Observation
	owned                  map[string]bool
	searched               []string
	posts                  []Release
	searchErr, errorOnPost error
	onSubmit               func()
}

func (f *fakeAcquirer) Observations(context.Context) (Activity, error) {
	return Activity{Items: f.current, Warnings: []string{}}, nil
}

func (f *fakeAcquirer) Search(ctx context.Context, book Book) (SearchResult, error) {
	f.searched = append(f.searched, book.ID)
	return SearchResult{Releases: f.releases[book.ID], Owned: f.owned[book.ID], Warnings: []string{}}, f.searchErr
}

func (f *fakeAcquirer) Submit(ctx context.Context, release Release) error {
	f.posts = append(f.posts, release)
	if f.onSubmit != nil {
		f.onSubmit()
	}
	return f.errorOnPost
}

func setupEngine(t *testing.T, count int) (*Engine, *fakeCatalog, *fakeAcquirer) {
	t.Helper()
	cfg, err := LoadConfig(testEnv())
	if err != nil {
		t.Fatal(err)
	}
	cfg.DryRun = false
	catalog := &fakeCatalog{books: []Book{}}
	acquirer := &fakeAcquirer{
		releases: map[string][]Release{},
		current:  map[Identity]Observation{},
		owned:    map[string]bool{},
		searched: []string{},
		posts:    []Release{},
	}
	for i := 1; i <= count; i++ {
		book := dune()
		book.ID = fmt.Sprint(i)
		catalog.books = append(catalog.books, book)
		r := releaseOf("Dune")
		r["source_id"] = rawJSON("release-" + book.ID)
		acquirer.releases[book.ID] = []Release{r}
	}
	engine := &Engine{
		Config:   cfg,
		Store:    openTestStore(t),
		Catalog:  catalog,
		Acquirer: acquirer,
		Now:      func() time.Time { return time.Unix(100, 0) },
	}
	return engine, catalog, acquirer
}

func TestEngineBackfillAndCaps(t *testing.T) {
	engine, _, sm := setupEngine(t, 8)
	first, err := engine.Cycle(t.Context())
	if err != nil || first.Submitted != 3 {
		t.Fatal(first, err)
	}
	engine.Now = func() time.Time { return time.Unix(200, 0) }
	second, err := engine.Cycle(t.Context())
	if err != nil || second.Submitted != 3 || len(sm.posts) != 6 {
		t.Fatal(second, err)
	}
}

func TestEngineDryRunAndPreviewFairness(t *testing.T) {
	engine, _, sm := setupEngine(t, 5)
	engine.Config.DryRun = true
	result, err := engine.Cycle(t.Context())
	if err != nil || result.WouldSubmit != 3 || len(sm.posts) != 0 {
		t.Fatal(result, err)
	}
	rows, err := engine.Store.Records(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Attempts != 0 || row.Status != "discovered" {
			t.Fatal("dry-run consumed state")
		}
	}
	initialized, err := engine.Store.Initialized(t.Context())
	if err != nil || initialized {
		t.Fatal("dry-run consumed baseline")
	}
	engine.Now = func() time.Time { return time.Unix(200, 0) }
	if _, err := engine.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	engine.Config.DryRun = false
	result, err = engine.Cycle(t.Context())
	if err != nil || result.Submitted != 3 {
		t.Fatal(result, err)
	}
}

func TestEngineUnknownAndFailedOutcomes(t *testing.T) {
	cases := []struct{ name, status, want string }{
		{
			"absent",
			"",
			"uncertain",
		},
		{"success", "complete", "completed"},
		{"failure", "error", "failed"},
		{"cancel", "cancelled", "held"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, _, sm := setupEngine(t, 1)
			if _, err := engine.Cycle(t.Context()); err != nil {
				t.Fatal(err)
			}
			if tc.status != "" {
				key := Identity{Source: "test", ID: "release-1"}
				sm.current[key] = Observation{Identity: key, Status: tc.status}
			}
			if _, err := engine.Cycle(t.Context()); err != nil {
				t.Fatal(err)
			}
			row, _, err := engine.Store.Get(t.Context(), "1")
			if err != nil || row.Status != tc.want || len(sm.posts) != 1 {
				t.Fatal(row, err)
			}
		})
	}
}

func TestEngineAmbiguousSubmissionLimit(t *testing.T) {
	engine, _, sm := setupEngine(t, 8)
	sm.errorOnPost = &RemoteError{Message: "timeout", Ambiguous: true}
	result, err := engine.Cycle(t.Context())
	if err != nil || result.Submitted != 0 || len(sm.posts) != 3 {
		t.Fatal(result, err)
	}
	rows, err := engine.Store.Records(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range rows {
		if row.Status == "uncertain" {
			count++
		}
	}
	if count != 3 {
		t.Fatal("uncertain writes exceeded cycle limit")
	}
}

func TestEngineRetryAlternativeAndLimit(t *testing.T) {
	engine, _, sm := setupEngine(t, 1)
	if _, err := engine.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	key := Identity{Source: "test", ID: "release-1"}
	sm.current[key] = Observation{Identity: key, Status: "error"}
	if _, err := engine.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	alternative := releaseOf("Dune")
	alternative["source_id"] = rawJSON("alternative")
	sm.releases["1"] = append(sm.releases["1"], alternative)
	engine.Now = func() time.Time { return time.Unix(100+86400, 0) }
	result, err := engine.Cycle(t.Context())
	if err != nil || result.Submitted != 1 || len(sm.posts) != 2 {
		t.Fatal(result, err)
	}
	if text(sm.posts[1], "source_id") != "alternative" {
		t.Fatal("failed release selected again")
	}
}

func TestEngineRateRefundAndOwnership(t *testing.T) {
	engine, _, sm := setupEngine(t, 1)
	engine.Config.MaxAttempts = 1
	sm.errorOnPost = &RemoteError{Message: "rate limited", Status: 429, RetryAfter: time.Minute}
	if _, err := engine.Cycle(t.Context()); !globalRemote(err) {
		t.Fatal("rate rejection ignored")
	}
	row, _, err := engine.Store.Get(t.Context(), "1")
	if err != nil || row.Attempts != 0 {
		t.Fatal("rejection spent download budget", err)
	}
	sm.errorOnPost = nil
	sm.owned["1"] = true
	engine.Now = func() time.Time { return time.Unix(100+86400, 0) }
	if _, err := engine.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, _, err = engine.Store.Get(t.Context(), "1")
	if err != nil || row.Status != "completed" || row.Reason != "already_in_library" {
		t.Fatal(row, err)
	}
}

func TestEngineBaselineAndAuthStop(t *testing.T) {
	engine, catalog, sm := setupEngine(t, 2)
	engine.Config.ImportExisting = false
	catalog.books = catalog.books[:1]
	if result, err := engine.Cycle(t.Context()); err != nil || result.Submitted != 0 {
		t.Fatal(result, err)
	}
	next := dune()
	next.ID = "2"
	catalog.books = append(catalog.books, next)
	sm.searchErr = &RemoteError{Message: "unauthorized", Status: 401}
	if _, err := engine.Cycle(t.Context()); !globalRemote(err) {
		t.Fatal("auth ignored")
	}
	if len(sm.posts) != 0 {
		t.Fatal("download after auth failure")
	}
}

func TestEngineCancellationKeepsIntent(t *testing.T) {
	engine, _, sm := setupEngine(t, 1)
	sm.errorOnPost = &RemoteError{Message: "cancelled", Ambiguous: true, cause: context.Canceled}
	_, err := engine.Cycle(t.Context())
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestEngineAdditionalSafetyCases(t *testing.T) {
	t.Run("search_budget_fairness", func(t *testing.T) {
		engine, _, sm := setupEngine(t, 5)
		engine.Config.MaxSearches = 2
		sm.releases = map[string][]Release{}
		if _, err := engine.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		engine.Now = func() time.Time { return time.Unix(200, 0) }
		if _, err := engine.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, id := range sm.searched {
			seen[id] = true
		}
		if len(sm.searched) != 4 || len(seen) != 4 {
			t.Fatal("backlog starved", sm.searched)
		}
	})
	t.Run("same_release_two_editions", func(t *testing.T) {
		engine, _, sm := setupEngine(t, 2)
		sm.releases["2"] = sm.releases["1"]
		if _, err := engine.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		row, _, err := engine.Store.Get(t.Context(), "2")
		if err != nil || row.Status != "queued" || len(sm.posts) != 1 {
			t.Fatal(row, err)
		}
	})
	t.Run("dry_run_preserves_intent", func(t *testing.T) {
		engine, catalog, _ := setupEngine(t, 1)
		engine.Config.DryRun = true
		if err := engine.Store.Discover(t.Context(), catalog.books, 1); err != nil {
			t.Fatal(err)
		}
		if err := engine.Store.StartSubmission(
			t.Context(),
			Submission{BookID: "1", Release: releaseOf("Dune"), At: 2},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		row, _, err := engine.Store.Get(t.Context(), "1")
		if err != nil || row.Status != "submitting" {
			t.Fatal(row, err)
		}
	})
	t.Run("stale_evidence_held", func(t *testing.T) {
		engine, _, sm := setupEngine(t, 1)
		if _, err := engine.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		old := float64(1)
		key := Identity{Source: "test", ID: "release-1"}
		sm.current[key] = Observation{Identity: key, Status: "complete", AddedTime: &old}
		if _, err := engine.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		row, _, err := engine.Store.Get(t.Context(), "1")
		if err != nil || row.Status != "uncertain" || len(sm.posts) != 1 {
			t.Fatal(row, err)
		}
	})
	t.Run("failure_ceiling", func(t *testing.T) {
		engine, _, sm := setupEngine(t, 1)
		engine.Config.MaxAttempts = 1
		if _, err := engine.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		key := Identity{Source: "test", ID: "release-1"}
		sm.current[key] = Observation{Identity: key, Status: "error"}
		if _, err := engine.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		row, _, err := engine.Store.Get(t.Context(), "1")
		if err != nil || row.Status != "held" {
			t.Fatal(row, err)
		}
	})
	t.Run("cancelled_post_leaves_durable_intent", func(t *testing.T) {
		engine, _, sm := setupEngine(t, 1)
		ctx, cancel := context.WithCancel(t.Context())
		sm.onSubmit = cancel
		sm.errorOnPost = &RemoteError{Message: "cancelled", Ambiguous: true, cause: context.Canceled}
		if _, err := engine.Cycle(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation not propagated", err)
		}
		row, _, err := engine.Store.Get(t.Context(), "1")
		if err != nil || row.Status != "submitting" {
			t.Fatal("intent not durable", row, err)
		}
		sm.onSubmit = nil
		sm.errorOnPost = nil
		if _, err := engine.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.posts) != 1 {
			t.Fatal("cancelled submission replayed")
		}
	})
}
