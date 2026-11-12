# goodreadsmark

A small Go companion that monitors a Goodreads shelf and asks an existing
[Shelfmark](https://github.com/calibrain/shelfmark) installation to acquire
confident EPUB/KEPUB matches.

**Version 0.3.1 · Shelfmark v1.6.2 · Linux / macOS · MIT**

No Goodreads password, Hardcover account, browser automation, inbound port or
Shelfmark modifications. The production image is non-root, static and shell-free.

## What it does

- Polls a public Goodreads shelf, with configurable account and shelf.
- Matches title, author, ebook format and optional language conservatively.
- Keeps SQLite acquisition history, durable submission intent and fair retries.
- Holds ambiguous outcomes rather than blindly resubmitting.
- Recognizes explicit ebook ownership from Shelfmark's Library Check.
- Defaults to **dry-run**, **30-minute polling**, **three submissions** and
  **20 searches per cycle**.

Only use sources and downloads you are authorized to access. Audiobooks are not
supported. KEPUB must be explicitly identified; conversion belongs in an existing
Calibre/Kobo workflow, not this companion.

## Goodreads coverage

The public widget exposes at most **100 entries** and ignores pagination.
A short or empty widget does not prove complete coverage. More than 100 additions
between successful polls can be missed. Goodreads RSS is not used.

For complete initial backfill, export the Goodreads library and use
`import-csv`. Missing widget entries never imply removal or cancellation.

Widget links identify reviews, not canonical books. New internal IDs look like
`review:987`; existing numeric IDs remain unchanged. Durable aliases associate
CSV and widget records only for a single exact full-title/author match after
Unicode/case/whitespace normalization. Ambiguous candidates are held. Metadata
differences can leave duplicate identities unresolved; this is not proof of
canonical edition identity.

## Quick start with Docker Compose

Copy the example without overwriting existing settings:

```sh
cp -n .env.example .env
chmod 600 .env
```

Edit locally:

1. Set `GOODREADS_USER_ID` to the numeric profile ID and `GOODREADS_SHELF` to the
   shelf slug. It must be accessible through the public widget without login.
2. Set `SHELFMARK_URL` to a reachable HTTP(S) base URL, including any proxy subpath
   but no embedded credentials or query parameters. Do not use localhost to reach
   a different container or host.
3. Leave `SHELFMARK_API_KEY` empty only when Shelfmark intentionally has
   authentication disabled on a trusted private network. Otherwise use its
   configured API key. A keyed installation needs a real admin user for
   persisted activity access.
4. Keep `DRY_RUN=true`.

```sh
docker compose pull
docker compose run --rm goodreadsmark once
docker compose run --rm goodreadsmark status
```

Dry-run performs reads/searches and persists discovery/history, but never submits
a download. Once matches and delivery have been reviewed, explicitly set
`DRY_RUN=false`, initially with `MAX_DOWNLOADS_PER_RUN=1`:

```sh
docker compose up -d
docker compose logs -f goodreadsmark
```

Use fixed image versions/digests, not automatic floating-tag upgrades.
See [TrueNAS deployment](docs/truenas.md) for a dedicated NAS dataset.

## Full backfill

Keep `IMPORT_EXISTING=true` and stop the continuous writer before importing:

```sh
docker compose stop goodreadsmark
docker compose run --rm -T goodreadsmark import-csv - < /path/to/export.csv
docker compose up -d
```

Import makes no network requests or download submissions. Keep exports private.
With `IMPORT_EXISTING=false`, only the initially observed widget window becomes
an excluded baseline; it is not a whole-shelf historical baseline.

## Native CLI

Requires Go 1.27.4+ to build; no Go/Python runtime is needed afterwards.

```sh
make build
./bin/goodreadsmark --env-file .env once
./bin/goodreadsmark --env-file .env status
./bin/goodreadsmark --env-file .env import-csv /path/to/export.csv
```

No environment file is loaded implicitly. `--env-file` accepts literal
KEY=VALUE assignments, without executing shell text or expanding variables.
Process environment takes precedence.

Commands: `run`, `once`, `status`, `import-csv PATH|-`, and
`retry BOOK_ID [--allow-uncertain]`. Status is read-only and may run alongside
the writer. Import/retry require exclusive access.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| GOODREADS_USER_ID | Required | Positive numeric account ID. |
| GOODREADS_SHELF | to-read | Public shelf slug. |
| SHELFMARK_URL | Required | Reachable HTTP(S) base URL. |
| SHELFMARK_API_KEY | Empty | Optional admin key; blank selects intentional anonymous access. |
| PREFERRED_FORMATS | epub,kepub | Ordered ebook allowlist. |
| PREFERRED_LANGUAGES | Empty | Inherit Shelfmark defaults, or codes such as en,pt. |
| CONTENT_TYPE | ebook | Ebooks only. |
| IMPORT_EXISTING | true | Process visible existing books; false excludes an initial window. |
| DRY_RUN | true | Preview, never submit. |
| POLL_INTERVAL_MINUTES | 30 | Delay between cycles. |
| MAX_DOWNLOADS_PER_RUN | 3 | Submission budget, including ambiguous attempts. |
| MAX_SEARCHES_PER_RUN | 20 | Independent search budget. |
| RETRY_INTERVAL_HOURS | 24 | Delay for unavailable books or confirmed failures. |
| MAX_DOWNLOAD_ATTEMPTS | 3 | Automatic attempt ceiling. |
| STATE_DB | /data/goodreadsmark.sqlite | Persistent state; example uses ./data for native runs. |
| LOG_LEVEL | INFO | DEBUG, INFO, WARNING, ERROR or CRITICAL. |

The API key also supports `SHELFMARK_API_KEY_FILE`. Set a direct nonempty value
or file path, not both. A configured unreadable, empty or malformed file fails
rather than silently switching to anonymous access. Mount secrets read-only and
readable by UID 10001. The supplied Compose template uses the direct variable.

## State, safety and recovery

- Back up the dataset before upgrading. Schema v1 migrates atomically to v2;
  older binaries require a pre-upgrade backup. Version 0.3.1 retains schema v2.
- Preserve the normalized Shelfmark URL, Goodreads account and shelf when moving
  state: these define its scope. Changing them creates separate history.
- A POST can succeed even when its response is lost. An uncertain book is held.
  Inspect Shelfmark before using `retry ID --allow-uncertain`; duplication is
  possible. Never delete state to retry a book.
- Retry accepts internal IDs shown by status and known source aliases. It does
  not override active/completed protection. Manual retry resets the attempt
  budget but retains failed-release exclusions.
- TLS verification stays enabled; redirects and automatic POST replays are
  refused. 401/403 never trigger a credential downgrade. No browser cookies are
  reused.
- An API key is admin-equivalent. No-auth access is only for a trusted private
  network. HTTP emits an unencrypted-transport warning.
- History and visible activity are not a universal library scan. Incomplete
  evidence is not proof of failure.

## Development and privacy

```sh
make verify
make vuln
make privacy
make cross
```

Integration tests use synthetic SQLite/HTTP fixtures, not live accounts.
`cmd/privacycheck` screens tracked content and reachable history for common
private-data patterns and non-noreply commit metadata. Gitleaks is an additional
secret check in CI. These are safeguards, not a substitute for review.

Never commit real environment files, account settings, book exports, history,
logs, backups or workstation-specific notes. Docker's build context is
allowlisted. See [security guidance](SECURITY.md) and [release process](docs/releases.md).

## License

[MIT](LICENSE). Third-party license texts are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
and included in the production image. This is an independent project, not an
official Goodreads or Shelfmark integration.
