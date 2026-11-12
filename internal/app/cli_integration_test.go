//go:build integration

package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIImportStdinAndStatus(t *testing.T) {
	env := testEnv()
	env["STATE_DB"] = filepath.Join(t.TempDir(), "state.sqlite")
	csv := "Book Id,Title,Author,Exclusive Shelf,Bookshelves\n1,Dune,Frank Herbert,to-read,\n"
	var out, stderr bytes.Buffer
	opts := CLIOptions{Env: env, Out: &out, Err: &stderr, In: strings.NewReader(csv), Args: []string{"import-csv", "-"}}
	if code := Run(t.Context(), opts); code != 0 {
		t.Fatal(code, stderr.String())
	}
	out.Reset()
	opts.Args = []string{"status"}
	if code := Run(t.Context(), opts); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), `"discovered":1`) {
		t.Fatal(out.String())
	}
}

func TestCLIOnceMocked(t *testing.T) {
	env := testEnv()
	env["STATE_DB"] = filepath.Join(t.TempDir(), "state.sqlite")
	var out, stderr bytes.Buffer
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("dry-run submitted a download")
		}
		if r.URL.Host == "www.goodreads.com" {
			if r.Header.Get("X-Api-Key") != "" {
				t.Fatal("key leaked")
			}
			return response(200, widget(Book{ID: "review:987", Title: "Dune", Author: "Frank Herbert"})), nil
		}
		switch r.URL.Path {
		case "/prefix/api/releases":
			data, err := json.Marshal(map[string]any{"releases": []Release{releaseOf("Dune")}})
			if err != nil {
				t.Fatal(err)
			}
			return response(200, string(data)), nil
		case "/prefix/api/activity/history":
			return response(200, "[]"), nil
		case "/prefix/api/activity/snapshot":
			return response(200, `{"status":{}}`), nil
		default:
			return response(200, "{}"), nil
		}
	})
	opts := CLIOptions{Env: env, Args: []string{"once"}, Out: &out, Err: &stderr, Transport: transport}
	if code := Run(t.Context(), opts); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), `"would_submit":1`) {
		t.Fatal(out.String())
	}
	if strings.Contains(stderr.String(), "synthetic-secret") {
		t.Fatal("secret leaked")
	}
}
