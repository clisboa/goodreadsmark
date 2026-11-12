package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func shelfmarkFixture(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/shelfmark-v1.6.2.json")
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestShelfmarkSearchAndPayload(t *testing.T) {
	cfg, err := LoadConfig(testEnv())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := newHTTPClient(time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("X-Api-Key") != cfg.apiKey {
			t.Fatal("missing key")
		}
		if r.Method == "GET" {
			q := r.URL.Query()
			if r.URL.Path != "/prefix/api/releases" || q.Get("provider") != "manual" || q.Get("title") != "Dune" {
				t.Fatal(r.URL.Path)
			}
			return response(
				200,
				`{"releases":[{"source":"test","source_id":"abc","title":"Dune","format":"epub","extra":{"author":"Frank Herbert","large":9007199254740993}}],"book":{"library":{"ebook":"owned"}}}`,
			), nil
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body["extra"]), "9007199254740993") {
			t.Fatal("payload lost precision")
		}
		return response(200, `{"status":"queued","priority":0}`), nil
	}))
	sm := &Shelfmark{Config: cfg, Client: client}
	book := dune()
	book.Title = "Dune (Dune, #1)"
	result, err := sm.Search(t.Context(), book)
	if err != nil || !result.Owned || len(result.Releases) != 1 {
		t.Fatal(result, err)
	}
	if err := sm.Submit(t.Context(), result.Releases[0]); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("unexpected requests")
	}
}

func TestShelfmarkActivity(t *testing.T) {
	fixtures := shelfmarkFixture(t)
	cfg, err := LoadConfig(testEnv())
	if err != nil {
		t.Fatal(err)
	}
	client := newHTTPClient(time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		key := "history"
		if strings.HasSuffix(r.URL.Path, "/status") {
			key = "status"
		}
		if strings.HasSuffix(r.URL.Path, "/snapshot") {
			key = "snapshot"
		}
		return response(200, string(fixtures[key])), nil
	}))
	activity, err := (&Shelfmark{Config: cfg, Client: client}).Observations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []Identity{{Source: "direct_download", ID: "abc"}, {Source: "prowlarr", ID: "persisted"}, {Source: "direct_download", ID: "archived"}} {
		if _, ok := activity.Items[key]; !ok {
			t.Fatal("missing observation", key)
		}
	}
	if _, ok := activity.Items[Identity{Source: "prowlarr", ID: "abc"}]; ok {
		t.Fatal("source collision")
	}
}

func TestShelfmarkFailureClassification(t *testing.T) {
	cases := []struct {
		name, body string
		code       int
		ambiguous  bool
	}{
		{"server", `{"error":"private"}`, 500, true},
		{"rejected", `{}`, 400, false},
		{"unknown", `{"status":"unknown"}`, 200, true},
		{"null", `null`, 200, true},
		{"invalid", "not json", 200, true},
	}
	cfg, err := LoadConfig(testEnv())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newHTTPClient(
				time.Second,
				roundTripFunc(func(*http.Request) (*http.Response, error) { return response(tc.code, tc.body), nil }),
			)
			err := (&Shelfmark{Config: cfg, Client: client}).Submit(t.Context(), releaseOf("Dune"))
			remote, ok := remoteError(err)
			if !ok || remote.Ambiguous != tc.ambiguous {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("body leaked")
			}
		})
	}
}

func TestShelfmarkHistoryDegradedAndPagination(t *testing.T) {
	cfg, err := LoadConfig(testEnv())
	if err != nil {
		t.Fatal(err)
	}
	fixtures := shelfmarkFixture(t)
	for _, mode := range []string{"missing", "requests", "auth"} {
		t.Run(mode, func(t *testing.T) {
			client := newHTTPClient(time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/status") {
					return response(200, "{}"), nil
				}
				if strings.HasSuffix(r.URL.Path, "/snapshot") {
					return response(404, "private"), nil
				}
				switch mode {
				case "missing":
					return response(404, "private"), nil
				case "auth":
					return response(401, "private"), nil
				default:
					if r.URL.Query().Get("offset") == "0" {
						rows := []map[string]string{}
						for i := range 100 {
							rows = append(
								rows,
								map[string]string{"item_type": "request", "item_key": string(rune('a' + i))},
							)
						}
						data, err := json.Marshal(rows)
						if err != nil {
							t.Fatal(err)
						}
						return response(200, string(data)), nil
					}
					return response(200, string(fixtures["history"])), nil
				}
			}))
			result, err := (&Shelfmark{Config: cfg, Client: client}).Observations(t.Context())
			if mode == "auth" {
				if !globalRemote(err) {
					t.Fatal("auth error ignored")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "missing" && len(result.Warnings) == 0 {
				t.Fatal("missing visibility warning")
			}
			if mode == "requests" && len(result.Items) != 1 {
				t.Fatal("pagination stopped at request-only page")
			}
		})
	}
}

func TestShelfmarkCancelledTransport(t *testing.T) {
	cfg, err := LoadConfig(testEnv())
	if err != nil {
		t.Fatal(err)
	}
	client := newHTTPClient(
		time.Second,
		roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, context.Canceled }),
	)
	err = (&Shelfmark{Config: cfg, Client: client}).Submit(t.Context(), releaseOf("Dune"))
	re, ok := remoteError(err)
	if !ok || !re.Ambiguous {
		t.Fatal(err)
	}
}
