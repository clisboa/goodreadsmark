package app

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Schema v2 preserves Python/Go v1 history and adds source identity aliases.
//
//go:embed schema.sql
var schema string

var processingStatuses = map[string]bool{
	"discovered": true,
	"baseline":   true,
	"review":     true,
	"submitting": true,
	"queued":     true,
	"completed":  true,
	"failed":     true,
	"held":       true,
	"uncertain":  true,
}

type Record struct {
	Book        Book
	Status      string
	NextCheck   float64
	Attempts    int
	Identity    Identity
	SubmittedAt *float64
	Reason      string
}
type StoreOptions struct {
	Path, Scope string
	ReadOnly    bool
}
type Store struct {
	db          *sql.DB
	lock        *os.File
	path, scope string
}
type DueOptions struct {
	Now                float64
	Limit, MaxAttempts int
	Preview            bool
}
type Submission struct {
	BookID  string
	Release Release
	At      float64
}
type Link struct {
	Identity Identity
	At       float64
}
type Change struct {
	Status, Reason string
	NextCheck      float64
}

func OpenStore(ctx context.Context, opts StoreOptions) (store *Store, err error) {
	state := &Store{path: opts.Path, scope: opts.Scope}
	defer func() {
		if err != nil {
			err = errors.Join(err, state.Close())
		}
	}()
	info, statErr := os.Lstat(opts.Path)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, problem("state database must not be a symbolic link")
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, localError("cannot inspect state database", statErr)
	}
	if !opts.ReadOnly {
		if err := os.MkdirAll(filepath.Dir(opts.Path), 0o700); err != nil {
			return nil, localError("cannot create state directory", err)
		}
		fd, err := unix.Open(opts.Path+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, localError("cannot open state lock", err)
		}
		state.lock = os.NewFile(uintptr(fd), opts.Path+".lock")
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return nil, localError("state is already owned by another process", err)
		}
		file, err := os.OpenFile(opts.Path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, localError("cannot create state database", err)
		}
		chmodErr := file.Chmod(0o600)
		closeErr := file.Close()
		if err := errors.Join(chmodErr, closeErr); err != nil {
			return nil, localError("cannot protect state database", err)
		}
	}
	absolute, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, localError("invalid state database path", err)
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	query := url.Values{"_pragma": {"busy_timeout(10000)", "foreign_keys(1)"}}
	if opts.ReadOnly {
		query.Set("mode", "ro")
	}
	uri.RawQuery = query.Encode()
	state.db, err = sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, localError("cannot open state database", err)
	}
	state.db.SetMaxOpenConns(1)
	state.db.SetMaxIdleConns(1)
	state.db.SetConnMaxLifetime(0)
	state.db.SetConnMaxIdleTime(0)
	var version int
	if err := state.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, localError("cannot read state schema", err)
	}
	if version < 0 || version > 2 || (opts.ReadOnly && version == 0) {
		return nil, problem("unsupported state database schema version")
	}
	if version > 0 {
		if err := state.validateSchema(ctx, version); err != nil {
			return nil, err
		}
	}
	if !opts.ReadOnly {
		if version == 0 {
			var tables int
			if err := state.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table'").
				Scan(&tables); err != nil {
				return nil, localError("cannot inspect state schema", err)
			}
			if tables != 0 {
				return nil, problem("unrecognized state database schema")
			}
		}
		if version == 1 {
			var aliases int
			if err := state.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='source_aliases'").
				Scan(&aliases); err != nil {
				return nil, localError("cannot inspect legacy state aliases", err)
			}
			if aliases != 0 {
				return nil, problem("unrecognized legacy state alias table")
			}
		}
		if _, err := state.db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
			return nil, localError("cannot configure state journal", err)
		}
		// Schema creation/migration, version and scope insertion either all commit or all roll back.
		if err := state.write(ctx, func(tx *sql.Tx) error {
			if version < 2 {
				if _, err := tx.ExecContext(ctx, schema); err != nil {
					return localError("cannot migrate state database", err)
				}
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO scopes(scope) VALUES (?)", opts.Scope); err != nil {
				return localError("cannot initialize state scope", err)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func (s *Store) validateSchema(ctx context.Context, version int) error {
	// Validate before IF NOT EXISTS can turn missing history into an empty queue.
	queries := []string{
		"SELECT scope,initialized FROM scopes LIMIT 0",
		"SELECT scope," + recordColumns + ",created_at,previewed_at FROM books LIMIT 0",
		"SELECT id,scope,book_id,source,release_id,outcome,created_at FROM attempts LIMIT 0",
	}
	if version == 2 {
		queries = append(
			queries,
			"SELECT scope,alias_id,book_id FROM source_aliases LIMIT 0",
			`SELECT 1 WHERE (SELECT group_concat(name, ',') FROM
			(SELECT name FROM pragma_table_info('source_aliases') WHERE pk>0 ORDER BY pk)) IS NOT 'scope,alias_id'`,
			"SELECT 1 FROM source_aliases a LEFT JOIN books b ON a.scope=b.scope AND a.book_id=b.book_id WHERE b.book_id IS NULL LIMIT 1",
		)
	}
	for _, query := range queries {
		if err := s.probeSchema(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) probeSchema(ctx context.Context, query string) (err error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return localError("unrecognized or incomplete state database schema", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, localError("cannot close schema validation", closeErr))
		}
	}()
	if rows.Next() {
		return problem("schema validation returned unexpected rows")
	}
	if err := rows.Err(); err != nil {
		return localError("cannot validate state database schema", err)
	}
	return nil
}

func (s *Store) Close() error {
	errs := []error{}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.lock != nil {
		if err := s.lock.Close(); err != nil {
			errs = append(errs, err)
		}
		s.lock = nil
	}
	if err := errors.Join(errs...); err != nil {
		return localError("cannot close state database", err)
	}
	return nil
}

func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return localError("cannot start state transaction", err)
	}
	defer func() {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, localError("state rollback failed", rollbackErr))
		}
	}()
	if err := fn(tx); err != nil {
		return localError("state transaction failed", err)
	}
	if err := tx.Commit(); err != nil {
		return localError("state commit failed", err)
	}
	return nil
}

func (s *Store) Initialized(ctx context.Context) (bool, error) {
	var flag int
	err := s.db.QueryRowContext(ctx, "SELECT initialized FROM scopes WHERE scope=?", s.scope).Scan(&flag)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, localError("cannot read state baseline", err)
	}
	return flag != 0, nil
}

func (s *Store) MarkInitialized(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "UPDATE scopes SET initialized=1 WHERE scope=?", s.scope)
	if err != nil {
		return localError("cannot mark state baseline", err)
	}
	return nil
}

func (s *Store) SeedBaseline(ctx context.Context, books []Book, now float64) error {
	if err := s.Discover(ctx, books, now); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE books SET status='baseline',reason='initial_baseline'
            WHERE scope=? AND attempts=0 AND status IN ('discovered','review')`, s.scope)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE scopes SET initialized=1 WHERE scope=?", s.scope)
		return err
	})
}

const recordColumns = "book_id,title,author,isbn,isbn13,status,next_check,attempts,source,release_id,submitted_at,reason"

func (s *Store) queryRecords(ctx context.Context, query string, args ...any) (records []Record, err error) {
	records = []Record{}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, localError("cannot query state database", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, localError("cannot close state query", closeErr))
		}
	}()
	for rows.Next() {
		var row Record
		if err := rows.Scan(
			&row.Book.ID,
			&row.Book.Title,
			&row.Book.Author,
			&row.Book.ISBN,
			&row.Book.ISBN13,
			&row.Status,
			&row.NextCheck,
			&row.Attempts,
			&row.Identity.Source,
			&row.Identity.ID,
			&row.SubmittedAt,
			&row.Reason,
		); err != nil {
			return nil, localError("cannot decode state record", err)
		}
		records = append(records, row)
	}
	if err := rows.Err(); err != nil {
		return nil, localError("cannot read state records", err)
	}
	return records, nil
}

func (s *Store) Get(ctx context.Context, id string) (Record, bool, error) {
	rows, err := s.queryRecords(ctx, "SELECT "+recordColumns+" FROM books WHERE scope=? AND book_id=?", s.scope, id)
	if err != nil {
		return Record{}, false, err
	}
	if len(rows) == 0 {
		return Record{}, false, nil
	}
	return rows[0], true, nil
}

func (s *Store) Records(ctx context.Context) ([]Record, error) {
	return s.queryRecords(ctx, "SELECT "+recordColumns+" FROM books WHERE scope=? ORDER BY created_at,book_id", s.scope)
}

func (s *Store) Due(ctx context.Context, opts DueOptions) ([]Record, error) {
	order := "next_check,created_at,book_id"
	if opts.Preview {
		order = "previewed_at,created_at,book_id"
	}
	// Security: both interpolated fragments are fixed internal SQL, never operator/book input.
	query := fmt.Sprintf(`SELECT %s FROM books WHERE scope=? AND status IN ('discovered','review','failed')
        AND next_check<=? AND attempts<? ORDER BY %s LIMIT ?`, recordColumns, order)
	return s.queryRecords(ctx, query, s.scope, opts.Now, opts.MaxAttempts, opts.Limit)
}

func (s *Store) MarkPreview(ctx context.Context, id string, now float64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE books SET previewed_at=? WHERE scope=? AND book_id=?", now, s.scope, id)
	if err != nil {
		return localError("cannot update preview ordering", err)
	}
	return nil
}

type attemptRecord struct {
	BookID   string
	Identity Identity
	Outcome  string
	At       float64
}

func (s *Store) attempt(ctx context.Context, tx *sql.Tx, record attemptRecord) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO attempts(scope,book_id,source,release_id,outcome,created_at)
        VALUES (?,?,?,?,?,?)`, s.scope, record.BookID, record.Identity.Source, record.Identity.ID, record.Outcome, record.At)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM attempts WHERE scope=? AND book_id=? AND id NOT IN
        (SELECT id FROM attempts WHERE scope=? AND book_id=? ORDER BY id DESC LIMIT 100)`,
		s.scope, record.BookID, s.scope, record.BookID)
	return err
}

func (s *Store) StartSubmission(ctx context.Context, submission Submission) error {
	key, ok := identity(submission.Release)
	if !ok {
		return problem("release identity is invalid")
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE books SET status='submitting',attempts=attempts+1,
            source=?,release_id=?,submitted_at=?,reason='submission_intent'
            WHERE scope=? AND book_id=? AND status IN ('discovered','review','failed')`,
			key.Source, key.ID, submission.At, s.scope, submission.BookID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return problem("book is not eligible for a new submission")
		}
		return s.attempt(
			ctx,
			tx,
			attemptRecord{BookID: submission.BookID, Identity: key, Outcome: "submitting", At: submission.At},
		)
	})
}

func (s *Store) LinkExisting(ctx context.Context, id string, link Link) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(
			ctx,
			`UPDATE books SET source=?,release_id=?,submitted_at=? WHERE scope=? AND book_id=?`,
			link.Identity.Source,
			link.Identity.ID,
			link.At,
			s.scope,
			id,
		)
		if err != nil {
			return err
		}
		return s.attempt(ctx, tx, attemptRecord{BookID: id, Identity: link.Identity, Outcome: "adopted", At: link.At})
	})
}

func (s *Store) Transition(ctx context.Context, id string, change Change) error {
	if !processingStatuses[change.Status] {
		return problem("invalid processing status")
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(
			ctx,
			`UPDATE books SET status=?,reason=?,next_check=? WHERE scope=? AND book_id=?`,
			change.Status,
			change.Reason,
			change.NextCheck,
			s.scope,
			id,
		)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE attempts SET outcome=? WHERE id=(
            SELECT id FROM attempts WHERE scope=? AND book_id=? ORDER BY id DESC LIMIT 1)
            AND outcome IN ('submitting','queued','uncertain','adopted')`, change.Status, s.scope, id)
		return err
	})
}

func (s *Store) RejectTransient(ctx context.Context, id string, next float64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE books SET status='review',attempts=MAX(attempts-1,0),
            next_check=?,reason='submission_auth_or_rate_limit' WHERE scope=? AND book_id=? AND status='submitting'`, next, s.scope, id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return problem("no pending submission intent to reject")
		}
		_, err = tx.ExecContext(ctx, `UPDATE attempts SET outcome='rejected' WHERE id=(
            SELECT id FROM attempts WHERE scope=? AND book_id=? ORDER BY id DESC LIMIT 1)`, s.scope, id)
		return err
	})
}

func (s *Store) FailedReleases(ctx context.Context, id string) (failed map[Identity]bool, err error) {
	failed = map[Identity]bool{}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT source,release_id FROM attempts WHERE scope=? AND book_id=? AND outcome='failed'`,
		s.scope,
		id,
	)
	if err != nil {
		return nil, localError("cannot query failed releases", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, localError("cannot close failed-release query", closeErr))
		}
	}()
	for rows.Next() {
		var key Identity
		if err := rows.Scan(&key.Source, &key.ID); err != nil {
			return nil, localError("cannot decode failed release", err)
		}
		failed[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, localError("cannot read failed releases", err)
	}
	return failed, nil
}

func (s *Store) ReleaseOwner(ctx context.Context, key Identity) (Record, bool, error) {
	rows, err := s.queryRecords(ctx, `SELECT `+recordColumns+` FROM books WHERE scope=? AND source=? AND release_id=?
        AND status IN ('submitting','queued','completed','uncertain')
        ORDER BY CASE status WHEN 'completed' THEN 0 WHEN 'queued' THEN 1 ELSE 2 END LIMIT 1`, s.scope, key.Source, key.ID)
	if err != nil {
		return Record{}, false, err
	}
	if len(rows) == 0 {
		return Record{}, false, nil
	}
	return rows[0], true, nil
}

func (s *Store) Retry(ctx context.Context, id string, allowUncertain bool) error {
	resolved, err := s.resolveAlias(ctx, s.db, id)
	if err != nil {
		return err
	}
	id = resolved
	row, exists, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return problem("book is not present in this account/shelf state")
	}
	switch row.Status {
	case "queued", "submitting", "completed":
		return problem("refusing to retry an active or completed book")
	case "uncertain":
		if !allowUncertain {
			return problem("uncertain submission requires --allow-uncertain; duplication is possible")
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE books SET status='discovered',attempts=0,next_check=0,
        source='',release_id='',submitted_at=NULL,reason='manual_retry' WHERE scope=? AND book_id=?`, s.scope, id)
	if err != nil {
		return localError("cannot reset book for retry", err)
	}
	return nil
}
