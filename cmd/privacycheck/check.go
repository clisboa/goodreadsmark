package main

import (
	"bytes"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var (
	homePattern = regexp.MustCompile(
		`/(?:Users|home)/[^/\s]+/|~/(?:projects|Documents|Downloads)/|[A-Za-z]:[\\/]Users[\\/]`,
	)
	addressPattern = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	urlPattern     = regexp.MustCompile(`https?://[^\s"'<>\x60)]+`)
	emailPattern   = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	accountPattern = regexp.MustCompile(
		`goodreads\.com/(?:user/show|review/(?:list|list_rss|custom_widget|show))/([0-9]+)`,
	)
	identityPattern = regexp.MustCompile(`(?m)^(?:author|committer|tagger) .*<([^>]+)> [0-9]+ [+-][0-9]{4}$`)
)

func privatePath(name string) string {
	clean := path.Clean(name)
	if clean != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") ||
		strings.ContainsFunc(name, unicode.IsControl) {
		return "unsafe repository path"
	}
	base := strings.ToLower(path.Base(name))
	if (base == ".env" || strings.HasPrefix(base, ".env.")) && base != ".env.example" {
		return "private environment file"
	}
	for _, part := range strings.Split(name, "/") {
		if slices.Contains([]string{"data", "backups", "private", "artifacts", ".firecrawl", "superpowers"}, part) {
			return "private operational directory"
		}
	}
	if strings.HasPrefix(name, "docs/verification") {
		return "private verification history"
	}
	if strings.Contains(base, "book-list") || strings.Contains(base, "book_list") || strings.HasPrefix(base, "live-") {
		return "personal operational report"
	}
	if strings.HasSuffix(base, "-wal") || strings.HasSuffix(base, "-shm") {
		return "database sidecar"
	}
	if strings.HasSuffix(base, ".csv") || strings.HasSuffix(base, ".log") || strings.Contains(base, ".sqlite") ||
		strings.HasSuffix(base, ".db") ||
		strings.Contains(base, ".db-") {
		return "state, export or log file"
	}
	if strings.HasSuffix(base, ".json") && !strings.HasPrefix(name, "internal/app/testdata/") {
		return "unreviewed JSON data"
	}
	if strings.HasSuffix(base, ".fixture") && name != "internal/app/testdata/python-v1.fixture" {
		return "unreviewed binary fixture"
	}
	return ""
}

func reservedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, root := range []string{"example.com", "example.net", "example.org", "example", "invalid", "test"} {
		if host == root || strings.HasSuffix(host, "."+root) {
			return true
		}
	}
	return false
}

func noreply(email string) bool {
	email = strings.ToLower(email)
	return strings.HasSuffix(email, "@users.noreply.github.com") || email == "noreply@github.com"
}

func contentFindings(name string, data []byte) []string {
	findings := []string{}
	if homePattern.Match(data) {
		findings = append(findings, "workstation home path")
	}
	for _, candidate := range addressPattern.FindAll(data, -1) {
		address, err := netip.ParseAddr(string(candidate))
		if err == nil && address.IsPrivate() {
			findings = append(findings, "private network address")
			break
		}
	}
	for _, candidate := range urlPattern.FindAll(data, -1) {
		parsed, err := url.Parse(string(candidate))
		if err != nil {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if address, err := netip.ParseAddr(host); err == nil && address.IsPrivate() {
			findings = append(findings, "private network address in URL")
			break
		}
		if parsed.User != nil && !reservedHost(host) {
			findings = append(findings, "credentials in a public URL literal")
			break
		}
		privateSuffix := strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".lan") ||
			strings.HasSuffix(host, ".internal")
		bare := !strings.Contains(host, ".") && !strings.Contains(host, ":") && host != "localhost"
		if !reservedHost(host) && (privateSuffix || bare) {
			findings = append(findings, "private or bare network hostname")
			break
		}
	}
	for _, match := range accountPattern.FindAllSubmatch(data, -1) {
		if !slices.Contains([]string{"1", "42", "123", "987", "12345678"}, string(match[1])) {
			findings = append(findings, "non-example Goodreads account or review URL")
			break
		}
	}
	// Dependency copyright/contact notices are upstream public licensing material.
	if name != "THIRD_PARTY_NOTICES.md" {
		for _, email := range emailPattern.FindAll(data, -1) {
			value := string(email)
			_, domain, _ := strings.Cut(value, "@")
			if !noreply(value) && !reservedHost(domain) {
				findings = append(findings, "non-example email address")
				break
			}
		}
	}
	return findings
}

func identityFindings(data []byte) []string {
	findings := []string{}
	for _, match := range identityPattern.FindAllSubmatch(bytes.TrimSpace(data), -1) {
		if !noreply(string(match[1])) {
			findings = append(findings, "non-noreply Git identity")
		}
	}
	return findings
}
