package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv() map[string]string {
	return map[string]string{
		"GOODREADS_USER_ID": "123", "SHELFMARK_URL": "https://books.example/prefix/",
		"SHELFMARK_API_KEY": "synthetic-secret",
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DryRun || !cfg.ImportExisting || cfg.MaxDownloads != 3 {
		t.Fatal("unsafe defaults")
	}
	if strings.Join(cfg.Formats, ",") != "epub,kepub" || cfg.BaseURL != "https://books.example/prefix" {
		t.Fatal("incorrect defaults")
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []struct{ name, key, value string }{
		{"user", "GOODREADS_USER_ID", "alice"},
		{"zero_user", "GOODREADS_USER_ID", "0"},
		{"shelf", "GOODREADS_SHELF", "../read"},
		{"scheme", "SHELFMARK_URL", "ftp://books.example"},
		{"userinfo", "SHELFMARK_URL", "https://u:p@books.example"},
		{"query", "SHELFMARK_URL", "https://books.example?key=x"},
		{"port", "SHELFMARK_URL", "https://books.example:wrong"},
		{"control_url", "SHELFMARK_URL", "https://books.example/\x00"},
		{"bool", "DRY_RUN", "maybe"},
		{"empty_bool", "IMPORT_EXISTING", ""},
		{"limit", "MAX_DOWNLOADS_PER_RUN", "0"},
		{"negative", "POLL_INTERVAL_MINUTES", "-1"},
		{"overflow", "RETRY_INTERVAL_HOURS", "99999999999999999"},
		{"format", "PREFERRED_FORMATS", "pdf"},
		{"empty_format", "PREFERRED_FORMATS", ""},
		{"language", "PREFERRED_LANGUAGES", "all languages"},
		{"audio", "CONTENT_TYPE", "audiobook"},
		{"level", "LOG_LEVEL", "verbose"},
		{"key_control", "SHELFMARK_API_KEY", "a\nb"},
		{"key_unicode", "SHELFMARK_API_KEY", "é"},
		{"state", "STATE_DB", "\x00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv()
			env[tc.key] = tc.value
			if _, err := LoadConfig(env); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestConfigSecretsAndFiles(t *testing.T) {
	env := testEnv()
	env["GOODREADS_RSS_KEY"] = "private-feed-key"
	cfg, err := LoadConfig(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(v, "synthetic-secret") || strings.Contains(v, "private-feed-key") {
			t.Fatal("secret leaked")
		}
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	delete(env, "SHELFMARK_API_KEY")
	env["SHELFMARK_API_KEY_FILE"] = path
	cfg, err = LoadConfig(env)
	if err != nil || cfg.apiKey != "file-secret" {
		t.Fatal("secret file failed", err)
	}
	env["SHELFMARK_API_KEY"] = "conflict"
	if _, err := LoadConfig(env); err == nil {
		t.Fatal("secret conflict accepted")
	}
}

func TestRetiredGoodreadsSettingsAreIgnored(t *testing.T) {
	env := testEnv()
	env["GOODREADS_RSS_KEY"] = "obsolete\nsecret"
	env["GOODREADS_RSS_KEY_FILE"] = filepath.Join(t.TempDir(), "does-not-exist")
	env["GOODREADS_MAX_PAGES"] = "not-a-number"
	if _, err := LoadConfig(env); err != nil {
		t.Fatal("retired credentials were read or validated", err)
	}
}

func TestPythonScopeVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/scopes.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ URL, User, Shelf, Scope string }
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.URL, func(t *testing.T) {
			cfg := Config{BaseURL: v.URL, UserID: v.User, Shelf: v.Shelf, ContentType: "ebook"}
			if got := cfg.Scope(); got != v.Scope {
				t.Fatalf("scope changed: %s != %s", got, v.Scope)
			}
		})
	}
}

func TestEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	contents := "GOODREADS_USER_ID=123\nSHELFMARK_URL='https://books.example'\nSHELFMARK_API_KEY=\"file-key\"\n# comment\nDRY_RUN=false\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := ReadEnvFile(path, map[string]string{"DRY_RUN": "true"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(env)
	if err != nil || !cfg.DryRun {
		t.Fatal("env precedence failed", err)
	}
}
