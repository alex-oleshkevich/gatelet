#!/usr/bin/env bash
set -euo pipefail

image="${GATELET_E2E_IMAGE:-gatelet:e2e}"
network="${GATELET_E2E_NETWORK:-gatelet-e2e-$$}"
token="${GATELET_E2E_TOKEN:-e2e-token-$(date +%s)}"
target="gatelet-e2e-target-$$"
daemon="gatelet-e2e-daemon-$$"
client="gatelet-e2e-client-$$"

cleanup() {
  docker rm -f "$client" "$daemon" "$target" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_for_log() {
  local container="$1"
  local pattern="$2"
  local deadline=$((SECONDS + 20))
  while (( SECONDS < deadline )); do
    if docker logs "$container" 2>&1 | grep -Fq "$pattern"; then
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for log pattern '$pattern' in $container" >&2
  docker logs "$container" >&2 || true
  return 1
}

wait_for_http() {
  local url="$1"
  local host="$2"
  local deadline=$((SECONDS + 20))
  while (( SECONDS < deadline )); do
    if docker run --rm --network "$network" curlimages/curl:8.8.0 \
      -fsS -H "Host: $host" "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for $url with Host: $host" >&2
  return 1
}

docker build -t "$image" .
docker network create "$network" >/dev/null

docker run -d --name "$target" --network "$network" hashicorp/http-echo:1.0 \
  -listen=:5678 \
  -text='gatelet e2e ok' >/dev/null

docker run -d --name "$daemon" --network "$network" \
  -e GATELET_TOKEN="$token" \
  "$image" \
  --domain e2e.test \
  --http :8080 \
  --control :4443 >/dev/null

wait_for_log "$daemon" "gateletd listening"

docker run -d --name "$client" --network "$network" \
  -e GATELET_TOKEN="$token" \
  --entrypoint gatelet \
  "$image" \
  alex \
  --server "$daemon:4443" \
  --to "http://$target:5678" \
  --domain e2e.test \
  --control-plaintext >/dev/null

wait_for_log "$daemon" "tunnel registered"
wait_for_http "http://$daemon:8080/" "alex.e2e.test"

body="$(docker run --rm --network "$network" curlimages/curl:8.8.0 \
  -fsS -H 'Host: alex.e2e.test' "http://$daemon:8080/hello?name=alex")"
if [[ "$body" != "gatelet e2e ok" ]]; then
  echo "unexpected GET body: $body" >&2
  exit 1
fi

post_body="$(docker run --rm --network "$network" curlimages/curl:8.8.0 \
  -fsS -X POST -d 'abc=123' -H 'Host: alex.e2e.test' "http://$daemon:8080/post")"
if [[ "$post_body" != "gatelet e2e ok" ]]; then
  echo "unexpected POST body: $post_body" >&2
  exit 1
fi

wait_for_log "$client" "GET /hello?name=alex 200 0B"
wait_for_log "$client" "POST /post 200 7B"

status="$(docker run --rm --network "$network" curlimages/curl:8.8.0 \
  -sS -o /dev/null -w '%{http_code}' -H 'Host: missing.e2e.test' "http://$daemon:8080/")"
if [[ "$status" != "404" ]]; then
  echo "unknown tunnel status = $status, want 404" >&2
  exit 1
fi

echo "docker e2e passed"
