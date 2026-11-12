# Releases

Only version-tag pushes publish images. Pull requests and main-branch pushes
run verification without package-write permissions. Publishing does not connect
to, configure or redeploy a NAS.

## Before a release

1. Review changes for private settings, real account/network identifiers, data
   files and secrets. Use only synthetic fixtures.
2. Use a GitHub noreply commit identity. Never push unrelated private history,
   private branches or Git bundles.
3. Run `make verify vuln privacy`, workflow validation and the offline container
   smoke checks. Review dependency notices when dependencies change.
4. Ensure main verification passes. Select a new strict `vMAJOR.MINOR.PATCH`
   tag matching the intended binary version.

Push only that tag. The release workflow verifies the exact tagged revision
again, then builds the **final** target for Linux AMD64 and ARM64. It publishes
fixed version and commit tags, OCI source/revision/version/license labels,
provenance and an SBOM to GHCR. It does not publish the smoke helper image.
Existing version tags must not be overwritten.

CI uses its ephemeral GITHUB_TOKEN with package-write access only in the
publishing job. No personal publishing token or NAS secret is required. Action
references are pinned to reviewed official release commits.

## First GHCR publication

New GHCR packages can default to private even when the source repository is
public. After the first successful release, the owner must verify visibility at:

https://github.com/users/clisboa/packages/container/goodreadsmark/settings

If necessary, use the supported package settings to change visibility to
**Public**. Do not assume a successful authenticated CI push means anonymous
NAS pulls work. Link package access to this repository and keep Actions access
limited to the intended repository.

Test anonymously using an empty Docker credential directory:

```sh
config_dir="$(mktemp -d)"
DOCKER_CONFIG="$config_dir" docker pull ghcr.io/clisboa/goodreadsmark:0.3.1
rm -r "$config_dir"
```

Record the manifest digest and use that fixed version/digest in deployments.
Do not enable unattended floating-tag updates.

## Repository settings

Require verification checks and review before merging to main; restrict who can
push release tags. Enable secret scanning/push protection where available and
keep private vulnerability reporting enabled. Do not enable privileged
pull_request_target publication or give pull-request jobs package-write access.

A release is not accepted until its workflow succeeds, both platform manifests
exist, anonymous pull works, and the public source/image privacy review passes.
