#!/usr/bin/env bash
set -euo pipefail

image="${GATELET_E2E_IMAGE:-gatelet:e2e}"
network="${GATELET_E2E_NETWORK:-gatelet-e2e-$$}"
token="${GATELET_E2E_TOKEN:-e2e-token-$(date +%s)}"
admin_user="${GATELET_E2E_ADMIN_USER:-operator}"
admin_password="${GATELET_E2E_ADMIN_PASSWORD:-admin-secret}"
target="gatelet-e2e-target-$$"
tcp_target="gatelet-e2e-tcp-target-$$"
daemon="gatelet-e2e-daemon-$$"
client="gatelet-e2e-client-$$"
tcp_client="gatelet-e2e-tcp-client-$$"

cleanup() {
  docker rm -f "$tcp_client" "$client" "$daemon" "$tcp_target" "$target" >/dev/null 2>&1 || true
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

contains() {
  local haystack="$1"
  local needle="$2"
  case "$haystack" in
    *"$needle"*) return 0 ;;
    *) return 1 ;;
  esac
}

docker build -t "$image" .
docker network create "$network" >/dev/null

docker run -d --name "$target" --network "$network" hashicorp/http-echo:1.0 \
  -listen=:5678 \
  -text='gatelet e2e ok' >/dev/null

docker run -d --name "$tcp_target" --network "$network" \
  -v "$PWD:/src" \
  -w /src \
  golang:1.24-alpine \
  go run ./scripts/e2e-tcp-echo -listen :6789 >/dev/null

docker run -d --name "$daemon" --network "$network" \
  -e GATELET_TOKEN="$token" \
  -e GATELET_ADMIN_USER="$admin_user" \
  -e GATELET_ADMIN_PASSWORD="$admin_password" \
  "$image" \
  --domain e2e.test \
  --http :8080 \
  --control :4443 >/dev/null

wait_for_log "$daemon" "gateletd listening"
wait_for_log "$tcp_target" "tcp echo listening"

docker run -d --name "$client" --network "$network" \
  -e GATELET_TOKEN="$token" \
  --entrypoint gatelet \
  "$image" \
  alex \
  "http://$target:5678" \
  --server "ws://$daemon:8080" \
  --domain e2e.test >/dev/null

docker run -d --name "$tcp_client" --network "$network" \
  -e GATELET_TOKEN="$token" \
  --entrypoint gatelet \
  "$image" \
  pg \
  "$tcp_target:6789" \
  --tcp \
  --remote-port 15432 \
  --server "ws://$daemon:8080" \
  --domain e2e.test >/dev/null

wait_for_log "$daemon" "tunnel connected"
wait_for_log "$daemon" "name=pg"
wait_for_http "http://$daemon:8080/" "alex.e2e.test"

admin_status="$(docker run --rm --network "$network" curlimages/curl:8.8.0 \
  -sS -o /dev/null -w '%{http_code}' -H 'Host: e2e.test' "http://$daemon:8080/admin")"
if [[ "$admin_status" != "401" ]]; then
  echo "unauthenticated admin status = $admin_status, want 401" >&2
  exit 1
fi

admin_body="$(docker run --rm --network "$network" curlimages/curl:8.8.0 \
  -fsS -u "$admin_user:$admin_password" -H 'Host: e2e.test' "http://$daemon:8080/admin")"
if [[ "$admin_body" != *"Gatelet relay"* || "$admin_body" != *"Active tunnels"* ]]; then
  echo "admin dashboard missing expected content" >&2
  exit 1
fi

status_body="$(docker run --rm --network "$network" curlimages/curl:8.8.0 \
  -fsS -u "$admin_user:$admin_password" -H 'Host: e2e.test' "http://$daemon:8080/__gatelet/status")"
if ! contains "$status_body" '"active_tunnels":2' ||
  ! contains "$status_body" '"name":"pg"' ||
  ! contains "$status_body" '"tunnel_type":"tcp"' ||
  ! contains "$status_body" '"remote_port":15432'; then
  echo "status endpoint missing active HTTP/TCP tunnel data: $status_body" >&2
  exit 1
fi

metrics_body="$(docker run --rm --network "$network" curlimages/curl:8.8.0 \
  -fsS -u "$admin_user:$admin_password" -H 'Host: e2e.test' "http://$daemon:8080/metrics")"
if [[ "$metrics_body" != *"gatelet_active_tunnels 2"* ]]; then
  echo "metrics endpoint missing active tunnel count" >&2
  exit 1
fi

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

tcp_body="$(docker run --rm --network "$network" \
  -v "$PWD:/src" \
  -w /src \
  golang:1.24-alpine \
  go run ./scripts/e2e-tcp-client -addr "$daemon:15432")"
if [[ "$tcp_body" != "echo: ping" ]]; then
  echo "unexpected TCP body: $tcp_body" >&2
  exit 1
fi
wait_for_log "$tcp_client" "TCP "

status="$(docker run --rm --network "$network" curlimages/curl:8.8.0 \
  -sS -o /dev/null -w '%{http_code}' -H 'Host: missing.e2e.test' "http://$daemon:8080/")"
if [[ "$status" != "404" ]]; then
  echo "unknown tunnel status = $status, want 404" >&2
  exit 1
fi

action_token="$(printf '%s' "$admin_body" | sed -n 's/.*name="gatelet-admin-action-token" content="\([0-9a-f]*\)".*/\1/p')"
if [[ -z "$action_token" ]]; then
  echo "admin action token not found in dashboard" >&2
  exit 1
fi

docker run --rm --network "$network" curlimages/curl:8.8.0 \
  -fsS -X POST \
  -u "$admin_user:$admin_password" \
  -H 'Host: e2e.test' \
  -H "X-Gatelet-Admin-Token: $action_token" \
  "http://$daemon:8080/admin/tunnels/alex/disconnect" >/dev/null
wait_for_log "$daemon" "tunnel disconnected"

echo "docker e2e passed"
