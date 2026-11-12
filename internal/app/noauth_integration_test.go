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

func TestNoAuthCLIDryRun(t *testing.T) {
	env := testEnv()
	delete(env, "SHELFMARK_API_KEY")
	env["STATE_DB"] = filepath.Join(t.TempDir(), "state.sqlite")
	var out, stderr bytes.Buffer
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatal("dry-run attempted a write")
		}
		if _, present := r.Header[http.CanonicalHeaderKey("X-Api-Key")]; present {
			t.Fatal("anonymous run sent a key header")
		}
		if r.URL.Host == "www.goodreads.com" {
			return response(200, widget(Book{ID: "review:1", Title: "Dune", Author: "Frank Herbert"})), nil
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
	if code := Run(
		t.Context(),
		CLIOptions{Env: env, Args: []string{"once"}, Out: &out, Err: &stderr, Transport: transport},
	); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), `"would_submit":1`) || !strings.Contains(out.String(), `"submitted":0`) ||
		!strings.Contains(stderr.String(), "without authentication") {
		t.Fatal(out.String(), stderr.String())
	}
}
