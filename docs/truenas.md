# TrueNAS SCALE

Run goodreadsmark as a separate Custom App beside an existing Shelfmark v1.6.2
installation. Do not modify the managed Shelfmark application or install build
tools on the NAS host.

## Image and storage

Use `ghcr.io/clisboa/goodreadsmark:0.3.1`, preferably pinned to its published
manifest digest. The public image needs no registry credential. Publishing an
image does not automatically upgrade or redeploy a NAS application.

Create a dedicated dataset, for example `/mnt/POOL/apps/goodreadsmark`, and use
TrueNAS's permission controls to grant UID/GID **10001:10001** read/write/traverse
access. Do not grant access to an entire pool. Mount the dataset at **/data**.

The image has no shell, inbound port, Docker socket requirement or Python
runtime. Use a read-only root and writable /tmp. Keep the default command
`goodreadsmark run`, a restart policy and a 120-second shutdown grace period.

## Configure the app

Use your TrueNAS release's Custom App/YAML controls. A generic example is in
[`deploy/truenas.yaml`](../deploy/truenas.yaml); replace all placeholder values.
The supplied Compose file is for local use with a named volume, not a NAS
dataset definition.

Set the numeric Goodreads account, public shelf and reachable Shelfmark URL.
Do not use localhost to reach a different application. A reverse-proxy base
path is supported; embedded URL credentials and query parameters are not.

For intentionally no-auth Shelfmark on a trusted private network, leave the API
key empty. Otherwise configure the same API key on both sides and retain a real
Shelfmark admin user. Key files must be mounted read-only and readable by UID
10001. No session cookies or Goodreads login are needed. Do not expose an
unauthenticated Shelfmark publicly.

Keep `DRY_RUN=true` for the first NAS run. Review logs and status, then
explicitly enable live operation with `MAX_DOWNLOADS_PER_RUN=1` until delivery
is verified. The usual limits are three submissions and 20 searches per cycle,
with 30-minute polling.

## Move existing state safely

Stop every writer and back up the complete existing dataset before moving it.
Transfer a consistent SQLite backup or the complete stopped database/WAL set,
not an arbitrary live copy of just the main file. Restore it into the dedicated
dataset with the correct ownership.

Keep the normalized Shelfmark URL, Goodreads ID and shelf unchanged: these
define the state scope. A different endpoint alias creates a different scope,
even if it reaches the same server. Verify existing completed/uncertain records
using `goodreadsmark status` before enabling live operation. Never erase state
to fix a permissions or matching problem.

Schema v1 upgrades atomically to v2; older binaries require restoring a
pre-upgrade backup. Version 0.3.1 keeps schema v2.

## Full-shelf backfill and operation

The Goodreads widget only exposes up to 100 entries. Stop the main writer and
use a one-off container with the same /data mount to run
`goodreadsmark import-csv -`, supplying the private export on stdin, or mount
the export read-only and pass its filename. Keep IMPORT_EXISTING=true for
backfill. Do not put exports into the image or repository.

There is no shell in the image. Use an exec facility that invokes the executable
directly, for example `goodreadsmark status`. Import/retry commands need the
continuous writer stopped; status does not.

If a submission is uncertain, inspect Shelfmark activity/history before any
manual retry. Authentication errors never fall back to anonymous access.
Source searches may be slow; a timeout does not prove that a book is unavailable.

Only use sources you are authorized to access. Registry publication, NAS
installation and enabling actual downloads are separate operator actions.
