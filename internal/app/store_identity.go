package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type (
	identityText   struct{ Title, Author string }
	identityRecord struct {
		Book               Book
		Numeric, Ambiguous bool
	}
)

type identityIndex struct {
	Records map[string]identityRecord
	Keys    map[identityText][]string
}

type discoveryItem struct {
	Book Book
	At   float64
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validBookID(id string) bool {
	return numericID.MatchString(id) ||
		(strings.HasPrefix(id, "review:") && numericID.MatchString(strings.TrimPrefix(id, "review:")))
}

func sourceAlias(id string) string {
	if strings.HasPrefix(id, "review:") {
		return id
	}
	return "book:" + id
}

func identityOf(book Book) identityText {
	normalize := func(s string) string {
		return norm.NFC.String(cases.Fold().String(norm.NFC.String(strings.Join(strings.Fields(s), " "))))
	}
	return identityText{Title: normalize(book.Title), Author: normalize(book.Author)}
}

func (s *Store) resolveAlias(ctx context.Context, query rowQuerier, id string) (string, error) {
	var target string
	err := query.QueryRowContext(ctx, "SELECT book_id FROM source_aliases WHERE scope=? AND alias_id=?", s.scope, sourceAlias(id)).
		Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return id, nil
	}
	if err != nil {
		return "", localError("cannot resolve Goodreads source identity", err)
	}
	return target, nil
}

func (s *Store) identityIndex(ctx context.Context, tx *sql.Tx) (index *identityIndex, err error) {
	index = &identityIndex{Records: map[string]identityRecord{}, Keys: map[identityText][]string{}}
	rows, err := tx.QueryContext(ctx, `SELECT b.book_id,b.title,b.author,b.isbn,b.isbn13,b.reason,
 EXISTS(SELECT 1 FROM source_aliases a WHERE a.scope=b.scope AND a.book_id=b.book_id AND a.alias_id LIKE 'book:%')
 FROM books b WHERE b.scope=?`, s.scope)
	if err != nil {
		return nil, localError("cannot inspect Goodreads identities", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, localError("cannot close identity query", closeErr))
		}
	}()
	for rows.Next() {
		record := identityRecord{}
		var reason string
		if err := rows.Scan(
			&record.Book.ID,
			&record.Book.Title,
			&record.Book.Author,
			&record.Book.ISBN,
			&record.Book.ISBN13,
			&reason,
			&record.Numeric,
		); err != nil {
			return nil, localError("cannot read Goodreads identity", err)
		}
		record.Numeric = record.Numeric || numericID.MatchString(record.Book.ID)
		record.Ambiguous = reason == "ambiguous_goodreads_identity"
		index.put(record)
	}
	if err := rows.Err(); err != nil {
		return nil, localError("cannot read Goodreads identities", err)
	}
	return index, nil
}

func (index *identityIndex) put(record identityRecord) {
	id := record.Book.ID
	if old, exists := index.Records[id]; exists {
		key := identityOf(old.Book)
		index.Keys[key] = slices.DeleteFunc(index.Keys[key], func(candidate string) bool { return candidate == id })
	}
	key := identityOf(record.Book)
	index.Keys[key] = append(index.Keys[key], id)
	index.Records[id] = record
}

func (index *identityIndex) candidates(book Book) []string {
	key := identityOf(book)
	matches := []string{}
	if key.Title == "" || key.Author == "" {
		return matches
	}
	for _, id := range index.Keys[key] {
		record := index.Records[id]
		// A numeric export may enrich a widget-only record, not conflate known book editions.
		if numericID.MatchString(book.ID) && (record.Numeric || record.Ambiguous) {
			continue
		}
		matches = append(matches, id)
	}
	return matches
}

func (s *Store) Discover(ctx context.Context, books []Book, now float64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		index, err := s.identityIndex(ctx, tx)
		if err != nil {
			return err
		}
		for _, book := range books {
			if !validBookID(book.ID) || strings.TrimSpace(book.Title) == "" {
				return problem("invalid Goodreads discovery identity or title")
			}
			if err := s.discoverIdentity(ctx, tx, index, discoveryItem{Book: book, At: now}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) discoverIdentity(ctx context.Context, tx *sql.Tx, index *identityIndex, item discoveryItem) error {
	book := item.Book
	alias := sourceAlias(book.ID)
	target, err := s.resolveAlias(ctx, tx, book.ID)
	if err != nil {
		return err
	}
	_, exists := index.Records[target]
	ambiguous := false
	if !exists {
		candidates := index.candidates(book)
		switch len(candidates) {
		case 0:
		case 1:
			target = candidates[0]
		default:
			ambiguous = true
		}
	}
	incomingNumeric := numericID.MatchString(book.ID)
	book.ID = target
	status, reason := "discovered", ""
	if ambiguous {
		status, reason = "held", "ambiguous_goodreads_identity"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO books(scope,book_id,title,author,isbn,isbn13,created_at,status,reason)
 VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(scope,book_id) DO UPDATE SET
 title=excluded.title,
 author=CASE WHEN excluded.author!='' THEN excluded.author ELSE books.author END,
 isbn=CASE WHEN excluded.isbn!='' THEN excluded.isbn ELSE books.isbn END,
 isbn13=CASE WHEN excluded.isbn13!='' THEN excluded.isbn13 ELSE books.isbn13 END`,
		s.scope,
		book.ID,
		book.Title,
		book.Author,
		book.ISBN,
		book.ISBN13,
		item.At,
		status,
		reason,
	)
	if err != nil {
		return localError("cannot persist Goodreads discovery", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO source_aliases(scope,alias_id,book_id) VALUES (?,?,?)
 ON CONFLICT(scope,alias_id) DO NOTHING`, s.scope, alias, target); err != nil {
		return localError("cannot persist Goodreads source identity", err)
	}
	old := index.Records[target]
	if book.Author == "" {
		book.Author = old.Book.Author
	}
	if book.ISBN == "" {
		book.ISBN = old.Book.ISBN
	}
	if book.ISBN13 == "" {
		book.ISBN13 = old.Book.ISBN13
	}
	index.put(
		identityRecord{Book: book, Numeric: old.Numeric || incomingNumeric, Ambiguous: old.Ambiguous || ambiguous},
	)
	return nil
}

func (s *Store) IdentityWarnings(ctx context.Context) ([]string, error) {
	var held int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM books WHERE scope=? AND status='held'
 AND reason='ambiguous_goodreads_identity'`, s.scope).Scan(&held)
	if err != nil {
		return nil, localError("cannot inspect ambiguous Goodreads identities", err)
	}
	warnings := []string{}
	if held > 0 {
		warnings = append(
			warnings,
			fmt.Sprintf(
				"%d Goodreads identities are held because multiple stored books match; inspect status before retrying.",
				held,
			),
		)
	}
	return warnings, nil
}
