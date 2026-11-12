package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWidgetDiscovery(t *testing.T) {
	cfg, err := LoadConfig(testEnv())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	client := newHTTPClient(time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/review/custom_widget/123" {
			t.Fatal("unexpected request", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("shelf") != "to-read" || q.Get("num_books") != "100" || q.Get("sort") != "date_added" ||
			q.Get("order") != "d" {
			t.Fatal(q)
		}
		if q.Get("page") != "" || q.Get("key") != "" || r.Header.Get("X-Api-Key") != "" ||
			r.Header.Get("Cookie") != "" {
			t.Fatal("credentials or pagination sent")
		}
		if _, err := strconv.ParseInt(q.Get("widget_id"), 10, 64); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, q.Get("widget_id"))
		return response(200, widget(Book{ID: "review:1", Title: "Dune", Author: "Frank Herbert"})), nil
	}))
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := url.Parse("https://www.goodreads.com")
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "must-not-be-sent"}})
	client.Jar = jar
	g := &Goodreads{Config: cfg, Client: client}
	for range 2 {
		result, err := g.Discover(t.Context())
		if err != nil || !result.Successful || len(result.Books) != 1 || len(result.Warnings) == 0 {
			t.Fatal(result, err)
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatal("requests repeated or cache identity reused", ids)
	}
}

func TestWidgetDiscoveryFailuresAndLimits(t *testing.T) {
	cfg, err := LoadConfig(testEnv())
	if err != nil {
		t.Fatal(err)
	}
	books := []Book{}
	for i := range 100 {
		books = append(books, Book{ID: "review:" + strconv.Itoa(i+1), Title: "Book", Author: "Writer"})
	}
	cases := []struct {
		name, body string
		status     int
		success    bool
		count      int
		warning    string
	}{
		{"empty", widget(), 200, false, 0, "empty"},
		{"capped", widget(books...), 200, true, 100, "100"},
		{"login", "<html>Sign in</html>", 200, false, 0, "invalid"},
		{"private", "denied", 403, false, 0, "failed"},
		{"redirect", "", 302, false, 0, "failed"},
		{"oversized", strings.Repeat("x", maxResponseBytes+1), 200, false, 0, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := newHTTPClient(
				time.Second,
				roundTripFunc(
					func(*http.Request) (*http.Response, error) { calls++; return response(tc.status, tc.body), nil },
				),
			)
			result, err := (&Goodreads{Config: cfg, Client: client}).Discover(t.Context())
			if err != nil || result.Successful != tc.success || len(result.Books) != tc.count || calls != 1 ||
				!strings.Contains(strings.Join(result.Warnings, " "), tc.warning) {
				t.Fatal(result, err, calls)
			}
		})
	}
	t.Run("rate_limit", func(t *testing.T) {
		client := newHTTPClient(time.Second, roundTripFunc(func(*http.Request) (*http.Response, error) {
			r := response(429, "")
			r.Header.Set("Retry-After", "120")
			return r, nil
		}))
		_, err := (&Goodreads{Config: cfg, Client: client}).Discover(t.Context())
		remote, ok := remoteError(err)
		if !ok || remote.RetryAfter != 2*time.Minute {
			t.Fatal(err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		client := newHTTPClient(time.Second, roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("request after cancellation")
			return nil, context.Canceled
		}))
		_, err := (&Goodreads{Config: cfg, Client: client}).Discover(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestCSV(t *testing.T) {
	input := `Book Id,Title,Author,ISBN,ISBN13,Exclusive Shelf,Bookshelves
1,Dune,Frank Herbert,"=""0441172717""","=""9780441172719""",to-read,
2,Other,Someone,,,read,favourites
`
	books, err := ReadExport(strings.NewReader(input), "to-read")
	if err != nil || len(books) != 1 || books[0].ISBN13 != "9780441172719" {
		t.Fatal(books, err)
	}
	books, err = ReadExport(strings.NewReader(input), "favourites")
	if err != nil || len(books) != 1 || books[0].ID != "2" {
		t.Fatal(books, err)
	}
	if _, err := ReadExport(strings.NewReader("email,password\na,b"), "to-read"); err == nil {
		t.Fatal("invalid CSV accepted")
	}
}
