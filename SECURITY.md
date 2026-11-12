# Security and privacy

Do not publish API keys, private endpoints, account IDs, exports, book lists,
history databases, backups, cookies or unredacted logs in issues, commits or
workflow output. Reproduce problems using reserved example hosts and synthetic
data.

Use GitHub's private vulnerability reporting for sensitive reports when
available. Do not post a working credential to demonstrate a vulnerability.

Shelfmark API keys are admin-equivalent. Anonymous Shelfmark access is intended
only for a trusted private network. Never expose an unauthenticated instance
publicly. Keep TLS verification enabled.

Use a dedicated persistent dataset, least-privilege filesystem permissions and
fixed image versions/digests. Back up state before upgrading or moving it.
Do not run multiple writers against the same database.

Pull requests never publish images. Release publication is gated by checks and
uses a job-scoped GitHub token; no NAS credential is used by CI. Privacy and
secret scanning are heuristic and do not replace review.
