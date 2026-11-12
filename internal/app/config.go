package app

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

var (
	numericID    = regexp.MustCompile(`^[1-9][0-9]*$`)
	shelfSlug    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	languageCode = regexp.MustCompile(`^[a-z]{2,3}(-[a-z]{2})?$`)
	envKey       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type Config struct {
	UserID, BaseURL, Shelf, ContentType, StateDB, LogLevel          string
	Formats, Languages                                              []string
	ImportExisting, DryRun                                          bool
	PollMinutes, MaxDownloads, MaxSearches, RetryHours, MaxAttempts int
	apiKey                                                          string
}

func (c Config) String() string       { return "Config{credentials:redacted}" }
func (c Config) GoString() string     { return c.String() }
func (c Config) LogValue() slog.Value { return slog.StringValue(c.String()) }

func (c Config) Scope() string {
	parts := []string{c.BaseURL, c.UserID, c.Shelf, c.ContentType}
	for i, part := range parts {
		parts[i] = pythonQuote(part)
	}
	sum := sha256.Sum256([]byte("[" + strings.Join(parts, ", ") + "]"))
	return hex.EncodeToString(sum[:])
}

func pythonQuote(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			switch {
			case r < 32 || r >= 127:
				if r > 0xffff {
					a, b := utf16.EncodeRune(r)
					fmt.Fprintf(&out, `\u%04x\u%04x`, a, b)
				} else {
					fmt.Fprintf(&out, `\u%04x`, r)
				}
			default:
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

func setting(env map[string]string, key, fallback string) string {
	if value, ok := env[key]; ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func secret(env map[string]string, name string, required bool) (string, error) {
	value, file := strings.TrimSpace(env[name]), strings.TrimSpace(env[name+"_FILE"])
	if value != "" && file != "" {
		return "", problem("set " + name + " or " + name + "_FILE, not both")
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", localError("cannot read "+name+"_FILE", err)
		}
		value = strings.TrimSpace(string(data))
		if value == "" {
			return "", problem(name + "_FILE must contain a nonempty key")
		}
	}
	if required && value == "" {
		return "", problem(name + " or " + name + "_FILE is required")
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return "", problem(name + " contains control characters")
		}
		if name == "SHELFMARK_API_KEY" && r > 127 {
			return "", problem(name + " must contain ASCII characters")
		}
	}
	return value, nil
}

func boolSetting(env map[string]string, name string, fallback bool) (bool, error) {
	value := strings.ToLower(setting(env, name, strconv.FormatBool(fallback)))
	switch value {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		return false, problem(name + " must be true or false")
	}
}

func positiveSetting(env map[string]string, name string, fallback int) (int, error) {
	value, err := strconv.Atoi(setting(env, name, strconv.Itoa(fallback)))
	// Every numeric setting must also be safe when converted to an hourly duration.
	if err != nil || value < 1 || int64(value) > int64(time.Duration(1<<63-1)/time.Hour) {
		return 0, problem(name + " must be a positive, representable integer")
	}
	return value, nil
}

func splitUnique(value string, skipEmpty bool) []string {
	result := []string{}
	for _, part := range strings.Split(value, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if skipEmpty && part == "" {
			continue
		}
		if !slices.Contains(result, part) {
			result = append(result, part)
		}
	}
	return result
}

func LoadConfig(env map[string]string) (Config, error) {
	cfg := Config{
		UserID: setting(env, "GOODREADS_USER_ID", ""), BaseURL: setting(env, "SHELFMARK_URL", ""),
		Shelf:       setting(env, "GOODREADS_SHELF", "to-read"),
		ContentType: strings.ToLower(setting(env, "CONTENT_TYPE", "ebook")),
		StateDB:     setting(env, "STATE_DB", "/data/goodreadsmark.sqlite"),
		LogLevel:    strings.ToUpper(setting(env, "LOG_LEVEL", "INFO")),
		Formats:     splitUnique(setting(env, "PREFERRED_FORMATS", "epub,kepub"), false),
		Languages:   splitUnique(setting(env, "PREFERRED_LANGUAGES", ""), true),
	}
	if !numericID.MatchString(cfg.UserID) {
		return cfg, problem("GOODREADS_USER_ID must be a positive numeric account ID")
	}
	if !shelfSlug.MatchString(cfg.Shelf) {
		return cfg, problem("GOODREADS_SHELF must be a shelf slug")
	}
	parsed, err := url.Parse(cfg.BaseURL)
	invalidText := strings.ContainsFunc(
		cfg.BaseURL,
		func(r rune) bool { return unicode.IsSpace(r) || r < 32 || r == 127 || r == '\\' },
	)
	if err != nil || invalidText || !utf8.ValidString(cfg.BaseURL) {
		return cfg, problem("SHELFMARK_URL must be a valid HTTP(S) base URL")
	}
	validScheme := parsed.Scheme == "https" || parsed.Scheme == "http"
	hasCredentials := parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery
	if !validScheme || parsed.Hostname() == "" || hasCredentials {
		return cfg, problem("SHELFMARK_URL must omit credentials and query parameters")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return cfg, problem("SHELFMARK_URL has an invalid port")
		}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.ContentType != "ebook" {
		return cfg, problem("CONTENT_TYPE currently supports ebook only")
	}
	for _, format := range cfg.Formats {
		if format != "epub" && format != "kepub" {
			return cfg, problem("PREFERRED_FORMATS supports epub and kepub only")
		}
	}
	for _, code := range cfg.Languages {
		if !languageCode.MatchString(code) {
			return cfg, problem("PREFERRED_LANGUAGES must contain language codes")
		}
	}
	if !slices.Contains([]string{"DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"}, cfg.LogLevel) {
		return cfg, problem("LOG_LEVEL is invalid")
	}
	if cfg.StateDB == "" || cfg.StateDB == ":memory:" ||
		strings.ContainsFunc(cfg.StateDB, func(r rune) bool { return r < 32 }) {
		return cfg, problem("STATE_DB must be a persistent file path")
	}
	if strings.HasPrefix(cfg.StateDB, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return cfg, localError("cannot resolve STATE_DB home directory", err)
		}
		cfg.StateDB = filepath.Join(home, cfg.StateDB[2:])
	}
	cfg.apiKey, err = secret(env, "SHELFMARK_API_KEY", false)
	if err != nil {
		return cfg, err
	}
	cfg.DryRun, err = boolSetting(env, "DRY_RUN", true)
	if err != nil {
		return cfg, err
	}
	cfg.ImportExisting, err = boolSetting(env, "IMPORT_EXISTING", true)
	if err != nil {
		return cfg, err
	}
	values := []struct {
		key      string
		fallback int
		target   *int
	}{
		{"POLL_INTERVAL_MINUTES", 30, &cfg.PollMinutes},
		{"MAX_DOWNLOADS_PER_RUN", 3, &cfg.MaxDownloads},
		{"MAX_SEARCHES_PER_RUN", 20, &cfg.MaxSearches},
		{"RETRY_INTERVAL_HOURS", 24, &cfg.RetryHours},
		{"MAX_DOWNLOAD_ATTEMPTS", 3, &cfg.MaxAttempts},
	}
	for _, v := range values {
		*v.target, err = positiveSetting(env, v.key, v.fallback)
		if err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// ReadEnvFile loads literal KEY=VALUE entries. It never executes or expands shell text.
func ReadEnvFile(path string, environment map[string]string) (values map[string]string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, localError("cannot read environment file", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, localError("cannot close environment file", closeErr))
		}
	}()
	values = map[string]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || !envKey.MatchString(key) {
			return nil, problem("environment file contains an invalid assignment")
		}
		if strings.HasPrefix(value, "\"") {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return nil, problem("environment file has invalid quoting")
			}
			value = decoded
		} else if strings.HasPrefix(value, "'") {
			if len(value) < 2 || !strings.HasSuffix(value, "'") {
				return nil, problem("environment file has invalid quoting")
			}
			value = value[1 : len(value)-1]
		} else if before, _, found := strings.Cut(value, " #"); found {
			value = strings.TrimSpace(before)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, localError("cannot parse environment file", err)
	}
	for key, value := range environment {
		values[key] = value
	}
	return values, nil
}
