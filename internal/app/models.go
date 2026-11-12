// Package app implements the Goodreads-to-Shelfmark bridge.
package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

var Version = "0.3.1"

type Book struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author"`
	ISBN   string `json:"isbn"`
	ISBN13 string `json:"isbn13"`
}

type (
	Identity  struct{ Source, ID string }
	Release   map[string]json.RawMessage
	Discovery struct {
		Books      []Book
		Warnings   []string
		Successful bool
	}
)

type Observation struct {
	Identity  Identity
	Status    string
	AddedTime *float64
}
type Activity struct {
	Items    map[Identity]Observation
	Warnings []string
}
type SearchResult struct {
	Releases []Release
	Owned    bool
	Warnings []string
}
type CycleResult struct {
	Discovered  int      `json:"discovered"`
	Searched    int      `json:"searched"`
	Submitted   int      `json:"submitted"`
	WouldSubmit int      `json:"would_submit"`
	Warnings    []string `json:"warnings"`
}

type safeError struct {
	message string
	cause   error
}

func (e *safeError) Error() string               { return e.message }
func (e *safeError) Unwrap() error               { return e.cause }
func problem(message string) error               { return &safeError{message: message} }
func localError(message string, err error) error { return &safeError{message: message, cause: err} }

type RemoteError struct {
	Message    string
	Status     int
	RetryAfter time.Duration
	Ambiguous  bool
	cause      error
}

func (e *RemoteError) Error() string { return e.Message }
func (e *RemoteError) Unwrap() error { return e.cause }
func (e *RemoteError) Auth() bool    { return e.Status == 401 || e.Status == 403 }

func remoteError(err error) (*RemoteError, bool) {
	var remote *RemoteError
	ok := errors.As(err, &remote)
	return remote, ok
}

func safeMessage(err error) string {
	var safe *safeError
	if errors.As(err, &safe) {
		return safe.message
	}
	if remote, ok := remoteError(err); ok {
		return remote.Message
	}
	return "operation failed; check configuration, dataset permissions and disk space"
}

func quietLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
