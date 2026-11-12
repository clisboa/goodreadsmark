package main

import (
	"strings"
	"testing"
)

func TestPrivatePaths(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		bad        bool
	}{
		{"environment", ".env", true},
		{"private_env", ".env.production", true},
		{"example", ".env.example", false},
		{"database", "state.sqlite", true},
		{"sidecar", "internal/app/testdata/python-v1.fixture-wal", true},
		{"backup", "backups/snapshot.bin", true},
		{"data", "data/library.json", true},
		{"csv", "export.csv", true},
		{"logs", "session.log", true},
		{"notes", "docs/superpowers/specs/design.md", true},
		{"verification", "docs/verification.md", true},
		{"book_list", "book-list.md", true},
		{"source", "internal/app/config.go", false},
		{"fixture", "internal/app/testdata/python-v1.fixture", false},
		{"json_fixture", "internal/app/testdata/scopes.json", false},
		{"license", "THIRD_PARTY_NOTICES.md", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (privatePath(tc.path) != "") != tc.bad {
				t.Fatal(tc.path)
			}
		})
	}
}

func TestPrivateContent(t *testing.T) {
	for _, tc := range []struct {
		name, path, text string
		bad              bool
	}{
		{"home", "README.md", "/" + "Users" + "/example/private", true},
		{"linux_home", "README.md", "/" + "home" + "/example/private", true},
		{"workstation_shortcut", "README.md", "~" + "/projects" + "/example", true},
		{"private_ip", "example.yml", "192." + "168." + "3.4", true},
		{"private_ten", "example.yml", "10." + "1.2.3", true},
		{"private_ipv6", "example.yml", "http://" + "[fd00::1]", true},
		{"documentation_ip", "README.md", "192.0.2.10", false},
		{"loopback", "test.go", "127.0.0.1", false},
		{"internal_host", "example.yml", "http://" + "service" + ".lan", true},
		{"reserved_host", "example.yml", "https://shelfmark.example.com", false},
		{"bare_host", "example.yml", "http://" + "nas-host" + ":8080", true},
		{"realistic_account", "example.yml", "https://www.goodreads.com/" + "user/" + "show/87654321", true},
		{"sample_account", "example.yml", "https://www.goodreads.com/user/show/12345678-name", false},
		{"private_email", "README.md", "person" + "@" + "mail.com", true},
		{"reserved_email", "test.go", "person@example.com", false},
		{"noreply_email", "README.md", "123+example@users.noreply.github.com", false},
		{"dependency_notice", "THIRD_PARTY_NOTICES.md", "author" + "@" + "upstream.org", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (len(contentFindings(tc.path, []byte(tc.text))) > 0) != tc.bad {
				t.Fatal(tc.name)
			}
		})
	}
}

func TestGitIdentityPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		bad       bool
	}{
		{"noreply", "author Contributors <123+example@users.noreply.github.com> 1 +0000\n", false},
		{"webflow", "committer GitHub <noreply@github.com> 1 +0000\n", false},
		{"personal", "author Example <" + "person" + "@" + "mail.com> 1 +0000\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (len(identityFindings([]byte(tc.raw))) > 0) != tc.bad {
				t.Fatal(tc.name)
			}
		})
	}
	if len(contentFindings("main.go", []byte(strings.Repeat("safe", 100)))) != 0 {
		t.Fatal("ordinary content rejected")
	}
}
