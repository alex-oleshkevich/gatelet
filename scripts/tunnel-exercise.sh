#!/usr/bin/env bash
set -euo pipefail

base_url="${1:-${GATELET_EXERCISE_URL:-https://alex.tun.aresa.me}}"
base_url="${base_url%/}"

tmpdir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

small_json="$tmpdir/small.json"
large_json="$tmpdir/large.json"
upload_file="$tmpdir/upload.txt"
binary_file="$tmpdir/blob.bin"

cat >"$small_json" <<'JSON'
{
  "case": "small-json",
  "under_500_chars": true,
  "message": "This body should render fully in the Gatelet detail pane.",
  "features": ["json", "headers", "query", "body-preview"],
  "tail": "SMALL-END"
}
JSON

cat >"$large_json" <<'JSON'
{
  "case": "large-json",
  "under_500_chars": false,
  "message": "This payload is intentionally long so the detail pane truncates near five hundred characters while the body view still has enough captured content to inspect. It includes nested values, arrays, unicode-like escaped text, and a visible tail marker. The point is to make row selection, detail preview, formatted JSON, plain toggle, and body scrolling easy to check from one request.",
  "items": [
    {"id": 1, "name": "alpha", "active": true},
    {"id": 2, "name": "bravo", "active": false},
    {"id": 3, "name": "charlie", "active": true}
  ],
  "metadata": {
    "source": "scripts/tunnel-exercise.sh",
    "expected_detail": "truncated",
    "expected_body_view": "scrollable"
  },
  "tail": "LARGE-END"
}
JSON

cat >"$upload_file" <<'TEXT'
Gatelet multipart upload fixture.
This file should appear as a multipart request with a small text body.
TEXT

printf '\x00\x01\x02GATELET-BINARY\xff\xfe\xfd\n' >"$binary_file"

section() {
  printf '\n== %s ==\n' "$1"
}

run_curl() {
  local label="$1"
  shift
  section "$label"
  printf '+ curl'
  printf ' %q' "$@"
  printf '\n'
  curl --silent --show-error --output /dev/null \
    --write-out 'status=%{http_code} bytes=%{size_download} time=%{time_total}s\n' \
    "$@"
}

common_headers=(
  -H 'X-Gatelet-Exercise: true'
  -H 'X-Request-Trace: trace-sshtun-exercise'
  -H 'Cookie: gatelet_session=exercise; theme=dark'
)

run_curl 'GET query + custom headers' \
  -X GET \
  "${common_headers[@]}" \
  -H 'Accept: application/json' \
  "$base_url/exercise/get?search=gatelet&sort=desc&page=2"

run_curl 'HEAD target health style request' \
  -I \
  "${common_headers[@]}" \
  "$base_url/exercise/head"

run_curl 'POST small formatted JSON under 500 chars' \
  -X POST \
  "${common_headers[@]}" \
  -H 'Content-Type: application/json' \
  --data-binary "@$small_json" \
  "$base_url/exercise/json-small?preview=full"

run_curl 'POST large formatted JSON over 500 chars' \
  -X POST \
  "${common_headers[@]}" \
  -H 'Content-Type: application/json' \
  --data-binary "@$large_json" \
  "$base_url/exercise/json-large?preview=truncated"

run_curl 'POST urlencoded form' \
  -X POST \
  "${common_headers[@]}" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data 'name=alex&feature=tui&mode=form&note=check+form+body' \
  "$base_url/exercise/form"

run_curl 'POST multipart upload' \
  -X POST \
  "${common_headers[@]}" \
  -F 'meta={"case":"multipart","ok":true};type=application/json' \
  -F "file=@$upload_file;type=text/plain" \
  "$base_url/exercise/upload"

run_curl 'POST binary body' \
  -X POST \
  "${common_headers[@]}" \
  -H 'Content-Type: application/octet-stream' \
  --data-binary "@$binary_file" \
  "$base_url/exercise/binary"

run_curl 'PUT with authorization header' \
  -X PUT \
  "${common_headers[@]}" \
  -H 'Authorization: Bearer gatelet-test-token' \
  -H 'Content-Type: application/json' \
  --data '{"case":"put","resource":"demo","tail":"PUT-END"}' \
  "$base_url/exercise/resource/42"

run_curl 'PATCH partial update' \
  -X PATCH \
  "${common_headers[@]}" \
  -H 'Content-Type: application/merge-patch+json' \
  --data '{"case":"patch","enabled":true,"tail":"PATCH-END"}' \
  "$base_url/exercise/resource/42"

run_curl 'DELETE request' \
  -X DELETE \
  "${common_headers[@]}" \
  "$base_url/exercise/resource/42?hard=false"

section 'done'
printf 'Sent exercise traffic to %s\n' "$base_url"
