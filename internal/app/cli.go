package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

type CLIOptions struct {
	Args      []string
	Env       map[string]string
	In        io.Reader
	Out, Err  io.Writer
	Transport http.RoundTripper
}
type command struct {
	name, envFile, argument string
	allowUncertain          bool
}

const usage = "Usage: goodreadsmark [--env-file PATH] [run|once|status|retry BOOK_ID [--allow-uncertain]|import-csv PATH|-]\nDefault command: run\nOptions: --help, --version\n"

func Environment(entries []string) map[string]string {
	env := map[string]string{}
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	return env
}

func parseCommand(opts CLIOptions) (command, int, bool) {
	cmd := command{name: "run"}
	flags := flag.NewFlagSet("goodreadsmark", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cmd.envFile, "env-file", "", "explicit environment file")
	version := flags.Bool("version", false, "show version")
	err := flags.Parse(opts.Args)
	if errors.Is(err, flag.ErrHelp) {
		_, writeErr := io.WriteString(opts.Out, usage)
		if writeErr != nil {
			return cmd, 1, true
		}
		return cmd, 0, true
	}
	if err != nil {
		diagnostic(opts.Err, "goodreadsmark: invalid flags; use --help")
		return cmd, 2, true
	}
	if *version {
		_, err := fmt.Fprintln(opts.Out, "goodreadsmark "+Version)
		if err != nil {
			return cmd, 1, true
		}
		return cmd, 0, true
	}
	args := flags.Args()
	if len(args) == 0 {
		return cmd, 0, false
	}
	cmd.name = args[0]
	args = args[1:]
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(opts.Out, usage)
		if err != nil {
			return cmd, 1, true
		}
		return cmd, 0, true
	}
	var valid bool
	switch cmd.name {
	case "run", "once", "status":
		valid = len(args) == 0
	case "import-csv":
		valid = len(args) == 1
		if valid {
			cmd.argument = args[0]
		}
	case "retry":
		rest := []string{}
		for _, arg := range args {
			if arg == "--allow-uncertain" {
				cmd.allowUncertain = true
			} else {
				rest = append(rest, arg)
			}
		}
		valid = len(rest) == 1
		if valid {
			cmd.argument = rest[0]
			valid = validBookID(cmd.argument)
		}
	default:
		valid = false
	}
	if !valid {
		diagnostic(opts.Err, "goodreadsmark: invalid command or arguments; use --help")
		return cmd, 2, true
	}
	return cmd, 0, false
}

func diagnostic(out io.Writer, message string) {
	// The caller already exits nonzero; a broken diagnostic stream needs no retry.
	if _, err := fmt.Fprintln(out, message); err != nil {
		return
	}
}

func Run(ctx context.Context, opts CLIOptions) int {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.Err == nil {
		opts.Err = io.Discard
	}
	if opts.In == nil {
		opts.In = strings.NewReader("")
	}
	cmd, code, done := parseCommand(opts)
	if done {
		return code
	}
	env := opts.Env
	if cmd.envFile != "" {
		loaded, err := ReadEnvFile(cmd.envFile, env)
		if err != nil {
			diagnostic(opts.Err, "goodreadsmark: "+safeMessage(err))
			return 1
		}
		env = loaded
	}
	cfg, err := LoadConfig(env)
	if err != nil {
		diagnostic(opts.Err, "goodreadsmark: "+safeMessage(err))
		return 1
	}
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARNING":
		level = slog.LevelWarn
	case "ERROR", "CRITICAL":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(opts.Err, &slog.HandlerOptions{Level: level}))
	for _, key := range []string{"GOODREADS_RSS_KEY", "GOODREADS_RSS_KEY_FILE", "GOODREADS_MAX_PAGES"} {
		if _, present := env[key]; present {
			logger.Warn(
				"Goodreads RSS credentials and page settings are retired and ignored; discovery uses the public widget",
			)
			break
		}
	}
	if cfg.apiKey == "" {
		logger.Warn("Shelfmark access is without authentication; use only on an intentionally trusted private network")
	}
	if strings.HasPrefix(cfg.BaseURL, "http://") {
		logger.Warn("Shelfmark HTTP transport is unencrypted; use an isolated trusted network or HTTPS")
	}
	if err := cmd.execute(ctx, opts, cfg, logger); err != nil {
		if ctx.Err() != nil {
			return 0
		}
		diagnostic(opts.Err, "goodreadsmark: "+safeMessage(err))
		return 1
	}
	return 0
}

func (cmd command) execute(ctx context.Context, opts CLIOptions, cfg Config, logger *slog.Logger) (err error) {
	if cmd.name == "status" {
		return status(ctx, cfg, opts.Out)
	}
	store, err := OpenStore(ctx, StoreOptions{Path: cfg.StateDB, Scope: cfg.Scope()})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	switch cmd.name {
	case "retry":
		if err := store.Retry(ctx, cmd.argument, cmd.allowUncertain); err != nil {
			return err
		}
		return json.NewEncoder(opts.Out).Encode(map[string]string{"id": cmd.argument, "status": "discovered"})
	case "import-csv":
		input := opts.In
		if cmd.argument != "-" {
			file, openErr := os.Open(cmd.argument)
			if openErr != nil {
				return localError("cannot read the Goodreads CSV export", openErr)
			}
			defer func() { err = errors.Join(err, file.Close()) }()
			input = file
		}
		books, err := ReadExport(input, cfg.Shelf)
		if err != nil {
			return err
		}
		if err := store.Discover(ctx, books, float64(time.Now().UnixNano())/1e9); err != nil {
			return err
		}
		warnings, err := store.IdentityWarnings(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(opts.Out).Encode(map[string]any{
			"imported": len(books), "downloads_submitted": 0, "warnings": warnings,
		})
	}
	goodreadsClient := newHTTPClient(30*time.Second, opts.Transport)
	shelfmarkClient := newHTTPClient(90*time.Second, opts.Transport)
	defer goodreadsClient.CloseIdleConnections()
	defer shelfmarkClient.CloseIdleConnections()
	engine := &Engine{
		Config:   cfg,
		Store:    store,
		Catalog:  &Goodreads{Config: cfg, Client: goodreadsClient},
		Acquirer: &Shelfmark{Config: cfg, Client: shelfmarkClient},
		Logger:   logger,
	}
	return runLoop(ctx, engine, opts.Out, cmd.name == "once")
}

type bookStatus struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	Attempts  int     `json:"attempts"`
	Reason    string  `json:"reason"`
	NextCheck float64 `json:"next_check"`
}

func status(ctx context.Context, cfg Config, out io.Writer) (err error) {
	if _, err := os.Stat(cfg.StateDB); errors.Is(err, os.ErrNotExist) {
		return json.NewEncoder(out).
			Encode(map[string]any{"counts": map[string]int{}, "books": []bookStatus{}, "message": "No state database yet"})
	} else if err != nil {
		return localError("cannot inspect state database", err)
	}
	store, err := OpenStore(ctx, StoreOptions{Path: cfg.StateDB, Scope: cfg.Scope(), ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	records, err := store.Records(ctx)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	books := []bookStatus{}
	for _, row := range records {
		counts[row.Status]++
		books = append(
			books,
			bookStatus{
				ID:        row.Book.ID,
				Title:     row.Book.Title,
				Status:    row.Status,
				Attempts:  row.Attempts,
				Reason:    row.Reason,
				NextCheck: row.NextCheck,
			},
		)
	}
	return json.NewEncoder(out).Encode(map[string]any{"counts": counts, "books": books})
}

func loopDelay(cfg Config, err error) time.Duration {
	delay := time.Duration(cfg.PollMinutes) * time.Minute
	if remote, ok := remoteError(err); ok {
		delay = max(delay, remote.RetryAfter)
	}
	return delay
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func runLoop(ctx context.Context, engine *Engine, out io.Writer, once bool) error {
	cfg, logger := engine.Config, engine.logger()
	for ctx.Err() == nil {
		result, err := engine.Cycle(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if once {
				return err
			}
			if _, ok := remoteError(err); !ok {
				return err
			}
			logger.Error("cycle failed; uncertain submissions remain held", "error", safeMessage(err))
		} else if once {
			return json.NewEncoder(out).Encode(result)
		}
		if waitErr := waitContext(ctx, loopDelay(cfg, err)); waitErr != nil {
			return waitErr
		}
	}
	return ctx.Err()
}
