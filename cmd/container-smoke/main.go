// Command container-smoke is a mock-only production-library verification helper.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"

	"github.com/clisboa/goodreadsmark/internal/app"

	_ "modernc.org/sqlite"
)

type mockTransport struct {
	phase string
	posts int
}

func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func (m *mockTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	second := m.phase == "ambiguous" || m.phase == "hold"
	id, title, author, releaseID := "1", "Dune", "Frank Herbert", "completed-release"
	if second {
		id, title, author, releaseID = "2", "The Hobbit", "J. R. R. Tolkien", "ambiguous-release"
	}
	if request.URL.Host == "www.goodreads.com" {
		if request.Header.Get("X-Api-Key") != "" {
			return nil, errors.New("admin key sent to Goodreads")
		}
		if request.URL.Path != "/review/custom_widget/123" {
			return nil, errors.New("unexpected Goodreads endpoint")
		}
		fragment := fmt.Sprintf(
			`<div class="gr_custom_container_7"><div class="gr_custom_each_container_7"><div class="gr_custom_title_7"><a href="https://www.goodreads.com/review/show/%s">%s</a></div><div class="gr_custom_author_7"><a href="https://www.goodreads.com/author/show/1">%s</a></div></div></div>`,
			id,
			html.EscapeString(title),
			html.EscapeString(author),
		)
		literal, err := json.Marshal(fragment)
		if err != nil {
			return nil, err
		}
		return response("var widget_code = " + string(literal) + ";\nvar widget_div = null;"), nil
	}
	if request.URL.Host != "shelfmark.smoke.invalid" {
		return nil, errors.New("unexpected smoke host")
	}
	if m.phase == "anonymous" {
		if _, present := request.Header[http.CanonicalHeaderKey("X-Api-Key")]; present {
			return nil, errors.New("anonymous smoke sent a key header")
		}
	} else if request.Header.Get("X-Api-Key") != "container-smoke-not-a-real-key" {
		return nil, errors.New("missing smoke key")
	}
	if request.Method == "POST" {
		m.posts++
		if m.phase != "queue" && m.phase != "ambiguous" {
			return nil, errors.New("unexpected POST")
		}
		data, readErr := io.ReadAll(request.Body)
		closeErr := request.Body.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		if !bytes.Contains(data, []byte("9007199254740993")) {
			return nil, errors.New("provider payload lost precision")
		}
		if m.phase == "ambiguous" {
			return nil, errors.New("synthetic lost response")
		}
		return response(`{"status":"queued","priority":0}`), nil
	}
	switch request.URL.Path {
	case "/api/status":
		if m.phase == "complete" {
			return response(
				`{"complete":{"completed-release":{"id":"completed-release","source":"direct_download"}}}`,
			), nil
		}
		return response("{}"), nil
	case "/api/activity/snapshot":
		return response(`{"status":{}}`), nil
	case "/api/activity/history":
		return response("[]"), nil
	case "/api/releases":
		body := fmt.Sprintf(
			`{"releases":[{"source":"direct_download","source_id":%q,"title":%q,"format":"epub","extra":{"author":%q,"large":9007199254740993}}]}`,
			releaseID,
			title,
			author,
		)
		return response(body), nil
	default:
		return nil, errors.New("unexpected smoke endpoint")
	}
}

type stateView struct {
	Books []struct {
		ID, Status string
		Attempts   int
	} `json:"books"`
}

func status(ctx context.Context, opts app.CLIOptions) (stateView, error) {
	var out bytes.Buffer
	opts.Args = []string{"status"}
	opts.Out = &out
	if code := app.Run(ctx, opts); code != 0 {
		return stateView{}, errors.New("smoke status failed")
	}
	var view stateView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		return view, err
	}
	return view, nil
}

func bookStatus(view stateView, id string) string {
	for _, book := range view.Books {
		if book.ID == id {
			return book.Status
		}
	}
	return ""
}

func run(ctx context.Context) error {
	phase := os.Getenv("SMOKE_PHASE")
	before := map[string]string{
		"preview":   "",
		"anonymous": "discovered",
		"queue":     "discovered",
		"complete":  "queued",
		"ambiguous": "",
		"hold":      "uncertain",
	}
	after := map[string]string{
		"preview":   "discovered",
		"anonymous": "discovered",
		"queue":     "queued",
		"complete":  "completed",
		"ambiguous": "uncertain",
		"hold":      "uncertain",
	}
	if _, ok := before[phase]; !ok {
		return errors.New("unknown smoke phase")
	}
	if os.Geteuid() != 10001 {
		return errors.New("production image must run as UID 10001")
	}
	probeErr := os.WriteFile("/app-readonly-probe", []byte("must fail"), 0o600)
	isReadOnly := errors.Is(probeErr, syscall.EROFS) || errors.Is(probeErr, os.ErrPermission)
	if !isReadOnly {
		return errors.New("container root must be read-only")
	}
	if data, err := os.ReadFile("/etc/ssl/certs/ca-certificates.crt"); err != nil || len(data) == 0 {
		return errors.New("CA bundle is missing")
	}
	transport := &mockTransport{phase: phase}
	env := map[string]string{
		"GOODREADS_USER_ID": "123", "SHELFMARK_URL": "https://shelfmark.smoke.invalid",
		"SHELFMARK_API_KEY": "container-smoke-not-a-real-key", "STATE_DB": "/data/goodreadsmark.sqlite",
		"DRY_RUN": "false",
	}
	if phase == "preview" || phase == "anonymous" {
		env["DRY_RUN"] = "true"
	}
	if phase == "anonymous" {
		env["SHELFMARK_API_KEY"] = ""
	}
	var out bytes.Buffer
	opts := app.CLIOptions{Env: env, Out: &out, Err: os.Stderr, Transport: transport}
	prior, err := status(ctx, opts)
	if err != nil {
		return err
	}
	id := "review:1"
	if phase == "ambiguous" || phase == "hold" {
		id = "review:2"
	}
	if bookStatus(prior, id) != before[phase] {
		return errors.New("state did not persist between containers")
	}
	opts.Args = []string{"once"}
	if code := app.Run(ctx, opts); code != 0 {
		return errors.New("smoke cycle failed")
	}
	current, err := status(ctx, opts)
	if err != nil {
		return err
	}
	if bookStatus(current, id) != after[phase] {
		return errors.New("unexpected final processing state")
	}
	expectedPosts := 0
	if phase == "queue" || phase == "ambiguous" {
		expectedPosts = 1
	}
	if transport.posts != expectedPosts {
		return errors.New("unexpected submission count")
	}
	info, err := os.Stat("/data/goodreadsmark.sqlite")
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("state database is not private")
	}
	return json.NewEncoder(os.Stdout).
		Encode(map[string]any{"phase": phase, "status": after[phase], "uid": os.Geteuid(), "mock_posts": transport.posts})
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "container smoke failed:", err)
		os.Exit(1)
	}
}
