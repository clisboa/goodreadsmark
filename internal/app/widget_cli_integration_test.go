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

func TestWidgetCLIImportOrder(t *testing.T) {
	for _, order := range []string{"csv_first", "widget_first"} {
		t.Run(order, func(t *testing.T) {
			env := testEnv()
			env["STATE_DB"] = filepath.Join(t.TempDir(), "state.sqlite")
			csv := "Book Id,Title,Author,Exclusive Shelf,Bookshelves\n42,Dune,Frank Herbert,to-read,\n"
			var out, stderr bytes.Buffer
			calls := 0
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("dry-run attempted a write")
				}
				if r.URL.Host == "www.goodreads.com" {
					calls++
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
			run := func(args ...string) {
				t.Helper()
				out.Reset()
				stderr.Reset()
				if code := Run(
					t.Context(),
					CLIOptions{
						Env:       env,
						Args:      args,
						In:        strings.NewReader(csv),
						Out:       &out,
						Err:       &stderr,
						Transport: transport,
					},
				); code != 0 {
					t.Fatal(code, stderr.String())
				}
			}
			if order == "csv_first" {
				run("import-csv", "-")
				run("once")
			} else {
				run("once")
				run("import-csv", "-")
			}
			run("once")
			if calls != 2 || !strings.Contains(out.String(), `"would_submit":1`) {
				t.Fatal(calls, out.String())
			}
			run("status")
			var status struct{ Books []bookStatus }
			if err := json.Unmarshal(out.Bytes(), &status); err != nil || len(status.Books) != 1 {
				t.Fatal(status, err)
			}
			want := "42"
			if order == "widget_first" {
				want = "review:987"
			}
			if status.Books[0].ID != want {
				t.Fatal(status)
			}
			// Both source aliases must be accepted by the real CLI parser and resolve to the same row.
			run("retry", "review:987")
			run("retry", "42")
		})
	}
}

func TestEmptyWidgetCannotEstablishBaseline(t *testing.T) {
	env := testEnv()
	env["STATE_DB"] = filepath.Join(t.TempDir(), "state.sqlite")
	env["IMPORT_EXISTING"] = "false"
	env["DRY_RUN"] = "false"
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("empty widget triggered a write")
		}
		if r.URL.Host == "www.goodreads.com" {
			return response(200, widget()), nil
		}
		switch r.URL.Path {
		case "/prefix/api/activity/history":
			return response(200, "[]"), nil
		case "/prefix/api/activity/snapshot":
			return response(200, `{"status":{}}`), nil
		default:
			return response(200, "{}"), nil
		}
	})
	if code := Run(t.Context(), CLIOptions{Env: env, Args: []string{"once"}, Transport: transport}); code != 0 {
		t.Fatal(code)
	}
	cfg, err := LoadConfig(env)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenStore(t.Context(), StoreOptions{Path: cfg.StateDB, Scope: cfg.Scope(), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, reader)
	if initialized, err := reader.Initialized(t.Context()); err != nil || initialized {
		t.Fatal("empty widget consumed baseline", initialized, err)
	}
}
