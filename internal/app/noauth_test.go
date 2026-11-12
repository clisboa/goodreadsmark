package app

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOptionalShelfmarkKey(t *testing.T) {
	for _, mode := range []string{"absent", "blank", "whitespace", "empty_file", "unreadable_file", "malformed_file", "conflicting"} {
		t.Run(mode, func(t *testing.T) {
			env := testEnv()
			delete(env, "SHELFMARK_API_KEY")
			wantError := false
			switch mode {
			case "blank":
				env["SHELFMARK_API_KEY"] = ""
			case "whitespace":
				env["SHELFMARK_API_KEY"] = "  "
			case "empty_file", "malformed_file", "conflicting":
				path := filepath.Join(t.TempDir(), "key")
				body := " \n"
				if mode == "malformed_file" {
					body = "bad\nkey"
				}
				if mode == "conflicting" {
					body = "file-secret"
					env["SHELFMARK_API_KEY"] = "direct-secret"
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				env["SHELFMARK_API_KEY_FILE"] = path
				wantError = true
			case "unreadable_file":
				env["SHELFMARK_API_KEY_FILE"] = filepath.Join(t.TempDir(), "missing")
				wantError = true
			}
			cfg, err := LoadConfig(env)
			if (err != nil) != wantError {
				t.Fatal("unexpected credential validation result", err)
			}
			if !wantError {
				keyed, err := LoadConfig(testEnv())
				if err != nil {
					t.Fatal(err)
				}
				if cfg.apiKey != "" || !cfg.DryRun || cfg.Scope() != keyed.Scope() {
					t.Fatal("anonymous mode changed scope/default safety")
				}
			}
		})
	}
}

func TestNoAuthHeadersAndKeyedRequests(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		t.Run(fmt.Sprintf("keyed_%t", keyed), func(t *testing.T) {
			cfg, err := LoadConfig(testEnv())
			if err != nil {
				t.Fatal(err)
			}
			if !keyed {
				cfg.apiKey = ""
			}
			calls := 0
			client := newHTTPClient(time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				value, present := r.Header[http.CanonicalHeaderKey("X-Api-Key")]
				if keyed {
					if len(value) != 1 || value[0] != cfg.apiKey {
						t.Fatal("key omitted")
					}
				} else if present {
					t.Fatal("anonymous request included auth header")
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("unexpected credentials")
				}
				if r.Method == http.MethodPost {
					return response(200, `{"status":"queued"}`), nil
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/releases"):
					return response(200, `{"releases":[]}`), nil
				case strings.HasSuffix(r.URL.Path, "/history"):
					return response(200, "[]"), nil
				case strings.HasSuffix(r.URL.Path, "/snapshot"):
					return response(200, `{"status":{}}`), nil
				default:
					return response(200, "{}"), nil
				}
			}))
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			origin, err := url.Parse(cfg.BaseURL)
			if err != nil {
				t.Fatal(err)
			}
			jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "must-not-be-sent"}})
			client.Jar = jar
			sm := &Shelfmark{Config: cfg, Client: client}
			if _, err := sm.Search(t.Context(), dune()); err != nil {
				t.Fatal(err)
			}
			if _, err := sm.Observations(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := sm.Submit(t.Context(), releaseOf("Dune")); err != nil {
				t.Fatal(err)
			}
			if calls != 5 {
				t.Fatal("unexpected request count", calls)
			}
		})
	}
}

func TestNoAuthFailuresNeverDowngradeOrFollowRedirects(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		for _, status := range []int{401, 403, 302} {
			t.Run(fmt.Sprintf("keyed_%t_status_%d", keyed, status), func(t *testing.T) {
				cfg, err := LoadConfig(testEnv())
				if err != nil {
					t.Fatal(err)
				}
				if !keyed {
					cfg.apiKey = ""
				}
				calls := 0
				client := newHTTPClient(time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if keyed && r.Header.Get("X-Api-Key") != cfg.apiKey {
						t.Fatal("key was dropped")
					}
					res := response(status, "private-body")
					res.Header.Set("Location", "https://other.invalid/login")
					return res, nil
				}))
				_, err = (&Shelfmark{Config: cfg, Client: client}).Search(t.Context(), dune())
				remote, ok := remoteError(err)
				if !ok || remote.Status != status || calls != 1 || strings.Contains(err.Error(), "private-body") {
					t.Fatal(err, calls)
				}
			})
		}
	}
}

func TestNoAuthWarning(t *testing.T) {
	env := testEnv()
	delete(env, "SHELFMARK_API_KEY")
	env["STATE_DB"] = filepath.Join(t.TempDir(), "absent.sqlite")
	var stderr bytes.Buffer
	if code := Run(t.Context(), CLIOptions{Env: env, Args: []string{"status"}, Err: &stderr}); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "without authentication") ||
		!strings.Contains(stderr.String(), "trusted private network") {
		t.Fatal("missing warning", stderr.String())
	}
}
