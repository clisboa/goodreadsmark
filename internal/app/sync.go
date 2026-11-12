package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

type Catalog interface {
	Discover(context.Context) (Discovery, error)
}
type Acquirer interface {
	Observations(context.Context) (Activity, error)
	Search(context.Context, Book) (SearchResult, error)
	Submit(context.Context, Release) error
}
type Engine struct {
	Config   Config
	Store    *Store
	Catalog  Catalog
	Acquirer Acquirer
	Now      func() time.Time
	Logger   *slog.Logger
}

func (e *Engine) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return quietLogger()
}

func (e *Engine) now() float64 {
	now := time.Now()
	if e.Now != nil {
		now = e.Now()
	}
	return float64(now.Unix()) + float64(now.Nanosecond())/1e9
}

func (e *Engine) outcome(ctx context.Context, row Record, observed Observation, now float64) error {
	change := Change{}
	switch {
	case observed.Status == "complete":
		change.Status = "completed"
		change.Reason = "confirmed_complete"
	case activeStatus(observed.Status):
		change.Status = "queued"
		change.Reason = "confirmed_active"
	case observed.Status == "error":
		change = Change{
			Status:    "failed",
			Reason:    "confirmed_download_failure",
			NextCheck: now + float64(e.Config.RetryHours)*3600,
		}
	case observed.Status == "cancelled":
		change.Status = "held"
		change.Reason = "cancelled_in_shelfmark"
	default:
		return problem("unrecognized download status")
	}
	if err := e.Store.Transition(ctx, row.Book.ID, change); err != nil {
		return err
	}
	if observed.Status == "error" && row.Attempts >= e.Config.MaxAttempts {
		return e.Store.Transition(ctx, row.Book.ID, Change{Status: "held", Reason: "download_attempt_limit"})
	}
	return nil
}

func (e *Engine) reconcile(ctx context.Context, activity Activity, now float64) error {
	rows, err := e.Store.Records(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		switch row.Status {
		case "submitting", "queued", "uncertain":
		default:
			continue
		}
		observed, known := activity.Items[row.Identity]
		if known && observed.AddedTime != nil && row.SubmittedAt != nil {
			if *observed.AddedTime < *row.SubmittedAt-5 {
				known = false
			}
		}
		if !known {
			if err := e.Store.Transition(
				ctx,
				row.Book.ID,
				Change{Status: "uncertain", Reason: "submission_outcome_unknown"},
			); err != nil {
				return err
			}
			e.logger().Warn("inspect Shelfmark before manual retry", "book", row.Book.ID, "status", "uncertain")
			continue
		}
		if err := e.outcome(ctx, row, observed, now); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) finish(result CycleResult) CycleResult {
	for _, warning := range result.Warnings {
		e.logger().Warn(warning)
	}
	e.logger().
		Info("cycle", "discovered", result.Discovered, "searched", result.Searched, "submitted", result.Submitted, "would_submit", result.WouldSubmit, "dry_run", e.Config.DryRun)
	return result
}

func (e *Engine) Cycle(ctx context.Context) (CycleResult, error) {
	result := CycleResult{Warnings: []string{}}
	activity, err := e.Acquirer.Observations(ctx)
	if err != nil {
		return result, err
	}
	if !e.Config.DryRun {
		if err := e.reconcile(ctx, activity, e.now()); err != nil {
			return result, err
		}
	}
	discovery, err := e.Catalog.Discover(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if globalRemote(err) {
			return result, err
		}
		discovery = Discovery{
			Books:      []Book{},
			Warnings:   []string{"Goodreads discovery failed; existing pending books remain scheduled"},
			Successful: false,
		}
	}
	now := e.now()
	result.Warnings = append(result.Warnings, discovery.Warnings...)
	if err := e.Store.Discover(ctx, discovery.Books, now); err != nil {
		return result, err
	}
	identityWarnings, err := e.Store.IdentityWarnings(ctx)
	if err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, identityWarnings...)
	result.Discovered = len(discovery.Books)
	initialized, err := e.Store.Initialized(ctx)
	if err != nil {
		return result, err
	}
	if !e.Config.ImportExisting && !initialized {
		switch {
		case e.Config.DryRun:
			result.Warnings = append(
				result.Warnings,
				"dry-run: existing books would become an excluded baseline; no baseline was consumed",
			)
		case discovery.Successful:
			if err := e.Store.SeedBaseline(ctx, discovery.Books, now); err != nil {
				return result, err
			}
		default:
			result.Warnings = append(result.Warnings, "initial baseline deferred until Goodreads discovery succeeds")
		}
		return e.finish(result), nil
	}
	if !e.Config.DryRun && discovery.Successful {
		if err := e.Store.MarkInitialized(ctx); err != nil {
			return result, err
		}
	}
	rows, err := e.Store.Due(
		ctx,
		DueOptions{Now: now, Limit: e.Config.MaxSearches, MaxAttempts: e.Config.MaxAttempts, Preview: e.Config.DryRun},
	)
	if err != nil {
		return result, err
	}
	attempts := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		atWriteLimit := attempts >= e.Config.MaxDownloads
		atPreviewLimit := result.WouldSubmit >= e.Config.MaxDownloads
		if atWriteLimit || atPreviewLimit {
			break
		}
		posted, err := e.process(ctx, row, activity, &result)
		if posted {
			attempts++
		}
		if err != nil {
			return result, err
		}
	}
	result.Warnings = append(result.Warnings, activity.Warnings...)
	return e.finish(result), nil
}

func (e *Engine) process(ctx context.Context, row Record, activity Activity, result *CycleResult) (bool, error) {
	now := e.now()
	cfg := e.Config
	if cfg.DryRun {
		if err := e.Store.MarkPreview(ctx, row.Book.ID, now); err != nil {
			return false, err
		}
	}
	result.Searched++
	found, err := e.Acquirer.Search(ctx, row.Book)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		delay := time.Duration(cfg.RetryHours) * time.Hour
		if remote, ok := remoteError(err); ok {
			delay = max(delay, remote.RetryAfter)
		}
		if !cfg.DryRun {
			change := Change{Status: "review", Reason: "search_failed", NextCheck: now + delay.Seconds()}
			if stateErr := e.Store.Transition(ctx, row.Book.ID, change); stateErr != nil {
				return false, errors.Join(err, stateErr)
			}
		}
		if globalRemote(err) {
			return false, err
		}
		result.Warnings = append(result.Warnings, "a Shelfmark search failed; it can be retried later")
		return false, nil
	}
	result.Warnings = append(result.Warnings, found.Warnings...)
	if found.Owned {
		if !cfg.DryRun {
			if err := e.Store.Transition(
				ctx,
				row.Book.ID,
				Change{Status: "completed", Reason: "already_in_library"},
			); err != nil {
				return false, err
			}
		}
		e.logger().Info("already in library", "book", row.Book.ID)
		return false, nil
	}
	excluded, err := e.Store.FailedReleases(ctx, row.Book.ID)
	if err != nil {
		return false, err
	}
	chosen := SelectRelease(
		row.Book,
		found.Releases,
		MatchOptions{Formats: cfg.Formats, Languages: cfg.Languages, Excluded: excluded},
	)
	if chosen == nil {
		e.logger().Info("no confident release", "book", row.Book.ID, "status", "review")
		if !cfg.DryRun {
			change := Change{
				Status:    "review",
				Reason:    "no_confident_release",
				NextCheck: now + float64(cfg.RetryHours)*3600,
			}
			if err := e.Store.Transition(ctx, row.Book.ID, change); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	key, ok := identity(chosen)
	if !ok {
		return false, problem("selected release has no identity")
	}
	observed, known := activity.Items[key]
	owner, tracked, err := e.Store.ReleaseOwner(ctx, key)
	if err != nil {
		return false, err
	}
	if known || tracked {
		if cfg.DryRun {
			return false, nil
		}
		if err := e.Store.LinkExisting(ctx, row.Book.ID, Link{Identity: key, At: now}); err != nil {
			return false, err
		}
		if known {
			return false, e.outcome(ctx, row, observed, now)
		}
		return false, e.Store.Transition(
			ctx,
			row.Book.ID,
			Change{Status: owner.Status, Reason: "release_already_tracked"},
		)
	}
	if cfg.DryRun {
		result.WouldSubmit++
		e.logger().Info("would_queue", "book", row.Book.ID, "format", releaseFormat(chosen), "dry_run", true)
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := e.Store.StartSubmission(ctx, Submission{BookID: row.Book.ID, Release: chosen, At: now}); err != nil {
		return false, err
	}
	err = e.Acquirer.Submit(ctx, chosen)
	if err == nil {
		if err := e.Store.Transition(
			ctx,
			row.Book.ID,
			Change{Status: "queued", Reason: "submission_accepted"},
		); err != nil {
			return true, err
		}
		result.Submitted++
		e.logger().Info("queued", "book", row.Book.ID)
		return true, nil
	}
	// Cancellation can leave submitting durable. Restart reconciliation holds it safely.
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	remote, isRemote := remoteError(err)
	if !isRemote || remote.Ambiguous {
		if stateErr := e.Store.Transition(
			ctx,
			row.Book.ID,
			Change{Status: "uncertain", Reason: "submission_outcome_unknown"},
		); stateErr != nil {
			return true, errors.Join(err, stateErr)
		}
		result.Warnings = append(result.Warnings, "a submission outcome is uncertain; it is held to prevent duplicates")
		if globalRemote(err) {
			return true, err
		}
		return true, nil
	}
	if remote.Auth() || remote.Status == http.StatusTooManyRequests {
		delay := max(time.Duration(cfg.RetryHours)*time.Hour, remote.RetryAfter)
		stateErr := e.Store.RejectTransient(ctx, row.Book.ID, now+delay.Seconds())
		return true, errors.Join(err, stateErr)
	}
	change := Change{Status: "failed", Reason: "submission_rejected", NextCheck: now + float64(cfg.RetryHours)*3600}
	if stateErr := e.Store.Transition(ctx, row.Book.ID, change); stateErr != nil {
		return true, errors.Join(err, stateErr)
	}
	if row.Attempts+1 >= cfg.MaxAttempts {
		if stateErr := e.Store.Transition(
			ctx,
			row.Book.ID,
			Change{Status: "held", Reason: "download_attempt_limit"},
		); stateErr != nil {
			return true, stateErr
		}
	}
	return true, nil
}
