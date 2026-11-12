#!/usr/bin/env bash
# Mock-only smoke checks. All application containers run with networking disabled.
set -euo pipefail

image="${1:-goodreadsmark:0.3.1}"
smoke_image="${2:-goodreadsmark:0.3.1-smoke}"
# Keep the array nonempty: macOS Bash 3.2 treats empty arrays as unset under -u.
platform="${3:-$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')}"
platform_args=(--platform "$platform")

volume="goodreadsmark-smoke-$(date +%s)-$$"
docker volume create "$volume" >/dev/null
cleanup() { docker volume rm "$volume" >/dev/null; }
trap cleanup EXIT

docker run --rm "${platform_args[@]}" --network none --read-only "$image" --version
for phase in preview anonymous queue complete ambiguous hold; do
  docker run --rm "${platform_args[@]}" --network none --read-only \
    --cap-drop ALL --security-opt no-new-privileges:true \
    --tmpfs /tmp:rw,size=16m,mode=1777 \
    --mount "type=volume,source=$volume,target=/data" \
    --env "SMOKE_PHASE=$phase" "$smoke_image"
done

env_args=(
  --env GOODREADS_USER_ID=123
  --env SHELFMARK_URL=https://shelfmark.smoke.invalid
  --env SHELFMARK_API_KEY=container-smoke-not-a-real-key
  --env STATE_DB=/data/goodreadsmark.sqlite
)
printf 'Book Id,Title,Author,Exclusive Shelf,Bookshelves\n3,Imported,Test Author,to-read,\n101,Dune,Frank Herbert,to-read,\n102,The Hobbit,J. R. R. Tolkien,to-read,\n' |
  docker run --rm -i "${platform_args[@]}" --network none --read-only \
    --cap-drop ALL --security-opt no-new-privileges:true \
    --mount "type=volume,source=$volume,target=/data" \
    "${env_args[@]}" "$image" import-csv -

status="$(docker run --rm "${platform_args[@]}" --network none --read-only \
  --mount "type=volume,source=$volume,target=/data" \
  "${env_args[@]}" "$image" status)"
printf '%s\n' "$status"
[[ "$status" == *'"completed":1'* && "$status" == *'"uncertain":1'* && "$status" == *'"discovered":1'* ]]
