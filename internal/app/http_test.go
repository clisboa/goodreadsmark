package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestBoundedTransport(t *testing.T) {
	cases := []struct {
		name, method, body string
		code, limit        int
		ambiguous          bool
	}{
		{"oversize", "GET", "0123456789", 200, 5, false},
		{"redirect", "GET", "private body", 302, 100, false},
		{"read_error", "POST", "private body", 500, 100, true},
		{"rejected", "POST", "private body", 400, 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := newHTTPClient(time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				res := response(tc.code, tc.body)
				res.Header.Set("Location", "https://evil.invalid")
				return res, nil
			}))
			_, err := request(
				t.Context(),
				requestOptions{
					Client:  client,
					Method:  tc.method,
					URL:     "https://host.invalid/?key=private",
					Service: "test",
					Limit:   tc.limit,
					Body:    []byte("{}"),
				},
			)
			remote, ok := remoteError(err)
			if !ok || remote.Ambiguous != tc.ambiguous || calls != 1 {
				t.Fatalf("wrong result: %v", err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestTransportCancellationAndRate(t *testing.T) {
	client := newHTTPClient(
		time.Second,
		roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, context.Canceled }),
	)
	_, err := request(
		t.Context(),
		requestOptions{
			Client:  client,
			Method:  "POST",
			URL:     "https://host.invalid",
			Service: "test",
			Body:    []byte("{}"),
		},
	)
	re, ok := remoteError(err)
	if !ok || !re.Ambiguous || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got := retryAfter("600", time.Now()); got != 600*time.Second {
		t.Fatal(got)
	}
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if got := retryAfter(future.Format(http.TimeFormat), future.Add(-time.Hour)); got != time.Hour {
		t.Fatal(got)
	}
}
