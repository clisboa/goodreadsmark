package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestCLIHelpVersionAndUsage(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		code     int
		contains string
	}{
		{"help", []string{"--help"}, 0, "Usage"},
		{"version", []string{"--version"}, 0, Version},
		{"bad_command", []string{"unknown"}, 2, ""},
		{"bad_flag", []string{"--bad"}, 2, ""},
		{"bad_retry", []string{"retry"}, 2, ""},
		{"no_credentials", []string{"once"}, 1, "GOODREADS_USER_ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			code := Run(t.Context(), CLIOptions{Args: tc.args, Env: map[string]string{}, Out: &out, Err: &stderr})
			if code != tc.code {
				t.Fatal(code, stderr.String())
			}
			if tc.contains != "" && !strings.Contains(out.String()+stderr.String(), tc.contains) {
				t.Fatal(out.String(), stderr.String())
			}
		})
	}
}

func TestCLIStatusWithoutDatabase(t *testing.T) {
	env := testEnv()
	env["STATE_DB"] = t.TempDir() + "/missing.sqlite"
	var out bytes.Buffer
	if code := Run(t.Context(), CLIOptions{Args: []string{"status"}, Env: env, Out: &out}); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(out.String(), `"counts":{}`) {
		t.Fatal(out.String())
	}
}

func TestRetryIdentifierGrammar(t *testing.T) {
	for _, tc := range []struct {
		id    string
		valid bool
	}{
		{"123", true},
		{"review:987", true},
		{"0", false},
		{"review:0", false},
		{"review:", false},
		{"review:../1", false},
		{"book:1", false},
		{"review:01", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			cmd, _, done := parseCommand(
				CLIOptions{Args: []string{"retry", tc.id}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}},
			)
			if done == tc.valid || (tc.valid && cmd.argument != tc.id) {
				t.Fatal(cmd, done)
			}
		})
	}
}

func TestRetiredSettingsNoticeIsRedacted(t *testing.T) {
	env := testEnv()
	env["STATE_DB"] = t.TempDir() + "/missing.sqlite"
	env["GOODREADS_RSS_KEY"] = "obsolete-private-secret"
	env["GOODREADS_RSS_KEY_FILE"] = "/nonexistent-private-path"
	var stderr bytes.Buffer
	if code := Run(t.Context(), CLIOptions{Args: []string{"status"}, Env: env, Err: &stderr}); code != 0 {
		t.Fatal(code)
	}
	message := stderr.String()
	if !strings.Contains(message, "retired") || strings.Contains(message, "obsolete-private-secret") ||
		strings.Contains(message, "/nonexistent-private-path") {
		t.Fatal(message)
	}
}

func TestLoopCancellationAndDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitContext(ctx, time.Hour); err == nil {
		t.Fatal("cancellation ignored")
	}
	cfg := Config{PollMinutes: 30}
	if delay := loopDelay(cfg, &RemoteError{RetryAfter: 2 * time.Hour}); delay != 2*time.Hour {
		t.Fatal(delay)
	}
}
