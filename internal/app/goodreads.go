package app

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var isbnNoise = regexp.MustCompile(`[^0-9Xx]`)

func cleanISBN(value string) string {
	value = strings.ToUpper(isbnNoise.ReplaceAllString(value, ""))
	if len(value) != 10 && len(value) != 13 {
		return ""
	}
	return value
}

type Goodreads struct {
	Config Config
	Client *http.Client
}

func (g *Goodreads) Discover(ctx context.Context) (Discovery, error) {
	result := Discovery{
		Books:    []Book{},
		Warnings: []string{"Widget completeness is unknown; use a Goodreads CSV export for full backfill."},
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	query := url.Values{
		"shelf": {g.Config.Shelf}, "num_books": {"100"},
		"sort": {"date_added"}, "order": {"d"},
		"show_author": {"1"}, "show_title": {"1"}, "show_cover": {"1"},
		"show_rating": {"0"}, "show_review": {"0"}, "show_tags": {"0"},
		"widget_id": {strconv.FormatInt(time.Now().UnixNano(), 10)},
	}
	endpoint := "https://www.goodreads.com/review/custom_widget/" + g.Config.UserID + "?" + query.Encode()
	client := *g.Client
	client.Jar = nil // Goodreads discovery is deliberately unauthenticated, even with an injected shared client.
	data, err := request(ctx, requestOptions{Client: &client, Method: "GET", URL: endpoint, Service: "Goodreads"})
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if remote, ok := remoteError(err); ok && (remote.Status == 429 || remote.RetryAfter > 0) {
			return result, err
		}
		result.Warnings = append(
			result.Warnings,
			"Goodreads widget request failed; existing pending books remain scheduled.",
		)
		return result, nil
	}
	books, parseErr := ParseWidget(data)
	if parseErr != nil {
		result.Warnings = append(
			result.Warnings,
			"Goodreads returned an invalid widget; check public shelf accessibility.",
		)
		//nolint:nilerr // Unsuccessful discovery is explicit; existing pending books can still be processed.
		return result, nil
	}
	result.Books = books
	if len(books) == 0 {
		result.Warnings = append(
			result.Warnings,
			"Goodreads widget is empty; initial baseline deferred because visibility is unknown.",
		)
		return result, nil
	}
	result.Successful = true
	if len(books) >= 100 {
		result.Warnings = append(
			result.Warnings,
			"Goodreads widget reached its 100-book cap; older books may be truncated. Import CSV for full coverage.",
		)
	}
	return result, nil
}

func ReadExport(input io.Reader, shelf string) ([]Book, error) {
	reader := csv.NewReader(input)
	header, err := reader.Read()
	if err != nil {
		return nil, problem("CSV is not a Goodreads library export")
	}
	columns := map[string]int{}
	for i, name := range header {
		columns[strings.TrimPrefix(name, "\ufeff")] = i
	}
	for _, name := range []string{"Book Id", "Title", "Author", "Exclusive Shelf", "Bookshelves"} {
		if _, ok := columns[name]; !ok {
			return nil, problem("CSV is not a Goodreads library export")
		}
	}
	books := []Book{}
	seen := map[string]int{}
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return books, nil
		}
		if err != nil {
			return nil, problem("cannot read the Goodreads CSV export")
		}
		for _, value := range record {
			if !utf8.ValidString(value) {
				return nil, problem("CSV contains invalid UTF-8")
			}
		}
		field := func(name string) string {
			index, ok := columns[name]
			if !ok {
				return ""
			}
			return strings.TrimSpace(record[index])
		}
		matches := field("Exclusive Shelf") == shelf
		for _, tag := range strings.Split(field("Bookshelves"), ",") {
			if strings.TrimSpace(tag) == shelf {
				matches = true
			}
		}
		if !matches {
			continue
		}
		book := Book{
			ID:     field("Book Id"),
			Title:  field("Title"),
			Author: field("Author"),
			ISBN:   cleanISBN(field("ISBN")),
			ISBN13: cleanISBN(field("ISBN13")),
		}
		if !numericID.MatchString(book.ID) || book.Title == "" {
			return nil, problem("CSV contains a shelf book with an invalid ID or title")
		}
		if index, ok := seen[book.ID]; ok {
			books[index] = book
			continue
		}
		seen[book.ID] = len(books)
		books = append(books, book)
	}
}
