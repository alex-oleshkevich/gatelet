# Gatelet

Gatelet exposes a local HTTP service through a stable public subdomain. It is a small ngrok-style tunnel with two binaries:

| Binary | Purpose |
|---|---|
| `gateletd` | Public relay server that accepts tunnel clients and HTTP traffic |
| `gatelet` | Local client that connects to `gateletd` and forwards requests to a local service |

## Architecture

```mermaid
flowchart LR
    Browser[Browser] -->|HTTP Host: demo.example.com| Relay[gateletd]
    Relay -->|yamux stream over TLS control connection| Client[gatelet]
    Client -->|HTTP| Local[localhost:3000]
    Local --> Client --> Relay --> Browser
```

`gatelet` opens an outbound control connection to `gateletd`, sends its protocol and client version, authenticates with a token ID plus challenge-response handshake, and registers a tunnel name such as `demo`. The control connection can use raw TCP/TLS or WebSocket over the HTTP listener at `/__gatelet/control`. When `gateletd` receives an HTTP request for `demo.example.com`, it opens a stream over the existing tunnel connection and forwards the request to the local client.

## Current Scope

Gatelet currently supports HTTP tunneling only. Public HTTP TLS termination, automatic certificates, persistent account storage, rate limits, and raw TCP forwarding are not implemented yet.

## Requirements

- Go 1.24 or newer
- A server reachable from the internet for public use
- A domain with wildcard DNS for public subdomains

## Installation From Source

Clone the repository and build both binaries:

```sh
git clone https://github.com/your-org/gatelet.git
cd gatelet
go build -o bin/gateletd ./cmd/gateletd
go build -o bin/gatelet ./cmd/gatelet
```

Or install them into your Go binary directory:

```sh
go install github.com/your-org/gatelet/cmd/gateletd@latest
go install github.com/your-org/gatelet/cmd/gatelet@latest
```

Make sure your Go binary directory is on `PATH`:

```sh
go env GOPATH
```

The binaries are usually installed into `$(go env GOPATH)/bin`.

## Binary Releases

Tagged releases publish prebuilt `gatelet` and `gateletd` archives for Linux and macOS on amd64 and arm64:

```sh
curl -L https://github.com/your-org/gatelet/releases/latest/download/gatelet_linux_amd64.tar.gz -o gatelet_linux_amd64.tar.gz
tar -xzf gatelet_linux_amd64.tar.gz
```

Release archives include both binaries and `README.md`. Verify downloads with the `checksums.txt` file attached to each GitHub release.

## Arch Linux

On Arch Linux, install the binary package from the AUR:

```sh
yay -S gatelet-bin
```

The package installs both `gatelet` and `gateletd`. TUI copy helpers use the system clipboard; install `wl-clipboard` on Wayland if copy commands do not work.

## Local Smoke Test

Start a local web service:

```sh
python3 -m http.server 3000 --bind 127.0.0.1
```

Start the relay:

```sh
gateletd --domain example.test --http 127.0.0.1:8080 --control 127.0.0.1:4443 --token dev-token
```

Start the tunnel client:

```sh
gatelet demo http://127.0.0.1:3000 --server 127.0.0.1:4443 --token dev-token --control-plaintext
```

Plain client mode prints one line for each completed or failed incoming request:

```text
url https://demo.example.test
target http://127.0.0.1:3000
GET /path?query 200 0B 203.0.113.44
POST /api/items 500 1.4kb 203.0.113.44
```

Use `--log-format jsonl` or `--log-format json` when piping request summaries into tooling. Request records include `method`, `path`, `status`, `request_size`, `remote_ip`, `duration_ms`, `error`, and `error_kind` when forwarding fails.

For an interactive local dashboard, add `--tui`:

```sh
gatelet demo http://127.0.0.1:3000 --server 127.0.0.1:4443 --token dev-token --control-plaintext --tui
```

Send a request through the relay:

```sh
curl -H 'Host: demo.example.test' http://127.0.0.1:8080/
```

The response should come from the local web service.

## Public Server Setup

Run `gateletd` on a public server behind an HTTPS reverse proxy:

```sh
GATELET_TOKEN="$GATELET_TOKEN" gateletd --domain example.com --http :8080
```

The HTTP listener handles both public tunnel requests and WebSocket control upgrades:

```text
GET /__gatelet/control  # WebSocket control endpoint
Host: example.com
```

For token rotation, run the daemon with multiple token IDs. Active tokens are accepted; inactive tokens are rejected but can stay in the config while clients are being migrated:

```sh
GATELET_TOKENS='current=new-token,previous=old-token,retired=oldest-token:inactive' \
  gateletd --domain example.com --http :8080
```

Raw TCP control is still available on `--control`, default `:4443`. Add `--control-tls-cert` and `--control-tls-key` when exposing raw TCP control in production.

Enable the admin dashboard by setting a Basic Auth username and password:

```sh
GATELET_ADMIN_USER=operator \
GATELET_ADMIN_PASSWORD='replace-with-a-long-random-password' \
GATELET_TOKEN="$GATELET_TOKEN" \
  gateletd --domain example.com --http :8080
```

Then open `https://example.com/admin`. When admin credentials are configured, `/admin`, `/__gatelet/status`, and `/metrics` require Basic Auth on the base domain. If admin credentials are not configured, those routes return `404`.

Run `gatelet` on your local machine:

```sh
gatelet demo http://127.0.0.1:3000 --server wss://example.com --token "$GATELET_TOKEN" --token-id current
```

The client also reads `GATELET_SERVER`, `GATELET_TOKEN`, and `GATELET_TOKEN_ID` when `--server`, `--token`, or `--token-id` are omitted. If a `wss://` endpoint or raw TLS control listener uses a private CA or self-signed certificate, pass `--control-ca /path/to/ca.pem`. Use `--control-plaintext` only for trusted local networks or development deployments with raw TCP control and no TLS.

Then open:

```text
http://demo.example.com
```

## Compose Deployment

Use `compose.example.yml` for local Docker Compose testing and keep deployment-specific Uncloud values in an ignored `compose.yml`.

The local `compose.yml` in this repository is set up for:

```text
tun.example.com
```

That means a tunnel named `demo` is served as:

```text
demo.tun.example.com
```

Set a token before starting the service locally:

```sh
export GATELET_TOKEN='replace-with-a-long-random-token'
docker compose -f compose.example.yml up -d --build
```

`compose.example.yml` uses Docker Compose `ports` and publishes the relay on local host ports `8080` and `4443`. For WebSocket control, point clients at `ws://localhost:8080`; the client defaults the path to `/__gatelet/control`. For raw TCP control on `4443`, use `--control-plaintext` unless you configure a control TLS certificate.

For Uncloud, deploy the compose file with `uc` from the host or project where you manage services:

```sh
GATELET_TOKEN='replace-with-a-long-random-token' uc deploy -f compose.yml
```

The ignored `compose.yml` uses Uncloud `x-ports`. In Uncloud, public HTTPS traffic is routed by Caddy for `*.tun.example.com`, and WebSocket control can share the same HTTPS route on the base host:

| Published endpoint | Container port | Purpose |
|---|---|---|
| `*.tun.example.com/https` | `8080` | Public HTTPS tunnel traffic via Caddy |
| `tun.example.com/__gatelet/control` | `8080` | WebSocket client control connection |
| `4443/tcp@host` | `4443` | Optional raw TCP client control connection |

### Caddy and Uncloud Quirks

Gatelet works best behind Uncloud's Caddy-managed HTTPS routing. The daemon listens on plain HTTP inside the container, and Caddy owns public TLS on `80` and `443`.

Use both the base host and wildcard host in the Gatelet service:

```yaml
services:
  gateletd:
    x-ports:
      - "tun.example.com:8080/https"
      - "*.tun.example.com:8080/https"
      - "4443:4443/tcp@host"
```

The base host is required for WebSocket control at `wss://tun.example.com/__gatelet/control`. The wildcard host is required for tunnel traffic such as `https://demo.tun.example.com`. If one of those hosts is missing, clients can fail with TLS errors such as `tls: internal error` before the request reaches `gateletd`.

Do not mix Docker Compose `ports` and Uncloud `x-ports` on the same service. Uncloud rejects a service that specifies both. For local Docker Compose, use `compose.example.yml`; for Uncloud deployment, use an ignored deployment-specific `compose.yml` with `x-ports`.

Do not bind `80:80` or `443:443` from the Gatelet service when Uncloud Caddy is running. Those host ports belong to Caddy. If you need raw TCP control, expose only `4443:4443/tcp@host`; otherwise prefer WebSocket control over `443` and omit the raw control port.

When using Uncloud's shared Caddy service with Cloudflare DNS challenges, the Caddy app needs the Cloudflare-enabled image and a global Caddyfile like:

```caddyfile
{
	acme_dns cloudflare {env.CLOUDFLARE_API_TOKEN}
}
```

Keep `CLOUDFLARE_API_TOKEN` in the Caddy app environment, not in the Gatelet container. The token only needs permission to manage DNS records for certificate issuance.

## DNS Setup

Create wildcard DNS records that point at the public server:

| Record | Type | Value |
|---|---|---|
| `example.com` | `A` or `AAAA` | Public server IP |
| `*.example.com` | `A` or `AAAA` | Public server IP |

Wildcard DNS is required so tunnel names such as `demo.example.com`, `api.example.com`, and `preview.example.com` all reach the relay.

For `tun.example.com` on Cloudflare, create records in the `example.com` zone:

| Type | Name | Content | Proxy status |
|---|---|---|---|
| `A` or `AAAA` | `tun` | Public server IP | Proxied for WebSocket-only control; DNS only if exposing raw `4443` |
| `A` or `AAAA` | `*.tun` | Public server IP | Proxied for HTTPS tunnels; DNS only if bypassing Cloudflare |

If Uncloud gives you a hostname instead of a stable IP address, use `CNAME` records instead:

| Type | Name | Content | Proxy status |
|---|---|---|---|
| `CNAME` | `tun` | Uncloud hostname | Proxied for WebSocket-only control; DNS only if exposing raw `4443` |
| `CNAME` | `*.tun` | Uncloud hostname | Proxied for HTTPS tunnels; DNS only if bypassing Cloudflare |

With WebSocket control, Cloudflare can proxy normal HTTPS/WebSocket traffic on port `443` to your reverse proxy. Raw TCP control on `4443` is not handled by the standard Cloudflare HTTP proxy; it requires DNS-only, Cloudflare Spectrum, Cloudflare Tunnel, or another TCP proxy.

Cloudflare dashboard path:

1. Open the `example.com` zone.
2. Go to **DNS** -> **Records**.
3. Add `tun` and `*.tun` records.
4. Set proxy status to **DNS only**.

Cloudflare API example:

```sh
curl https://api.cloudflare.com/client/v4/zones/$CLOUDFLARE_ZONE_ID/dns_records \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  -d '{
    "type": "A",
    "name": "*.tun.example.com",
    "content": "203.0.113.10",
    "ttl": 1,
    "proxied": false
  }'
```

For CLI management, `flarectl` can manage Cloudflare DNS records:

```sh
go install github.com/cloudflare/cloudflare-go/cmd/flarectl@latest
export CF_API_TOKEN='cloudflare-api-token-with-dns-write'
```

Terraform is a better fit if you want DNS as code. Use the official Cloudflare provider and manage `tun.example.com` plus `*.tun.example.com` as `cloudflare_dns_record` resources.

## Command Reference

### `gateletd`

```sh
GATELET_TOKEN="$GATELET_TOKEN" gateletd --domain example.com --http :8080
```

| Flag | Required | Description |
|---|---|---|
| `--domain` | Yes | Base domain used for tunnel subdomains |
| `--http` | No | Public HTTP listen address, default `:8080` |
| `--control` | No | Tunnel control listen address, default `:4443` |
| `--token` | Alternative | Legacy default authentication token; prefer `GATELET_TOKEN` in production |
| `--tokens` | Alternative | Comma-separated rotation set, for example `current=new,previous=old,retired=older:inactive`; prefer `GATELET_TOKENS` in production |
| `--admin-user` | No | Basic Auth username for `/admin`, `/__gatelet/status`, and `/metrics`; prefer `GATELET_ADMIN_USER` in production |
| `--admin-password` | No | Basic Auth password for `/admin`, `/__gatelet/status`, and `/metrics`; prefer `GATELET_ADMIN_PASSWORD` in production |
| `--reserved-names` | No | Comma-separated extra tunnel names to reject; `admin`, `www`, `api`, and `metrics` are always reserved |
| `--allow-names` | No | Comma-separated tunnel names allowed to connect; empty allows any non-reserved valid name |
| `--log-format` | No | Daemon log format: `text` or `json`; default `text` |
| `--control-tls-cert` | No | PEM certificate chain for TLS on the control listener |
| `--control-tls-key` | No | PEM private key for TLS on the control listener |

### `gatelet`

```sh
gatelet demo http://127.0.0.1:3000 --server wss://example.com --token "$GATELET_TOKEN" --token-id current
```

| Flag | Required | Description |
|---|---|---|
| positional name | Yes | Tunnel name, for example `demo` |
| `--name` | Alternative | Tunnel name if not using the positional form |
| `--server` | Alternative | `gateletd` control address or WebSocket URL, for example `wss://example.com`; WebSocket URLs without a path default to `/__gatelet/control`; prefer `GATELET_SERVER` for repeated local use |
| positional target | Yes | Local HTTP target, with or without `http://` |
| `--to` | Alternative | Compatibility alias for the positional target |
| `--token` | Alternative | Authentication token; prefer `GATELET_TOKEN` in production |
| `--token-id` | No | Token ID sent in the auth handshake; defaults to `default` when omitted |
| `--domain` | No | Public tunnel domain for display, inferred from `--server` when empty |
| `--log-format` | No | Plain-mode output format: `text`, `json`, or `jsonl`; default `text` |
| `--preview-size` | No | Maximum request/response body preview bytes captured for logs and TUI; default `4096` |
| `--control-plaintext` | No | Disable TLS for raw TCP control; ignored for `ws://` and `wss://` URLs |
| `--control-ca` | No | PEM CA bundle used to verify raw TLS or `wss://` control server certificates |
| `--control-server-name` | No | Override the TLS server name used for raw TLS or `wss://` control certificate verification |
| `--control-insecure-skip-verify` | No | Use TLS encryption without certificate verification; explicit insecure opt-in |
| `--tui` | No | Show the Bubble Tea live dashboard instead of plain request logs |

Tunnel names must be lowercase DNS labels: letters, numbers, and interior hyphens only.

In TUI mode, `gatelet` shows the public URL, tunnel connection status, local target health, request history, selected request details, headers, timing, status, errors, and capped text body previews. Target health is `ok` after normal local responses, `degraded` after local `5xx` responses, and `down` when the local target cannot be reached. Press `p` to pause or resume new requests; paused requests wait until resume or timeout. In request detail view, press `b` to open a body-only request/response viewer, `r` to replay the selected request to the local target, `y` to copy it as a curl command, and `e` to save the curl command under the Gatelet capture directory. Press `F` in detail or body view to toggle formatted JSON/plain body text.

List filters split on spaces and AND every term across method, path, status, remote IP, host, and error text. For example, `/ POST /api 500` shows only POST requests under `/api` with status `500`.

`gateletd` writes structured logs for startup, control connections, protocol/client versions, authentication, tunnel lifecycle, incoming requests, tunnel misses, forwards, statuses, durations, byte counts, and forwarding errors. Use `--log-format json` for JSON slog records.

Tunnel lifecycle logs include `connected_at`, `last_seen`, request count, forwarded request bytes, response body bytes, and disconnect reason. Duplicate tunnel registration attempts are rejected with `ERR tunnel name already in use` and logged with the active and duplicate remote addresses.

When admin credentials are configured, `gateletd` exposes Basic Auth protected daemon-only admin endpoints on the base domain, not on tunnel subdomains:

```text
GET /admin              # HTML operations dashboard
GET /__gatelet/status  # JSON uptime, active tunnels, request and byte counters
GET /metrics           # Prometheus text metrics
```

The dashboard shows uptime, active tunnel counts, aggregate traffic, per-tunnel status counters, and a disconnect action for active tunnels. Dashboard actions require Basic Auth and an embedded action token.

Requests to the same paths on a tunnel subdomain, such as `https://demo.example.com/metrics`, are still forwarded to the local target.

The relay sets request timeouts and header limits on its public HTTP server. It also overwrites inbound `X-Forwarded-*` headers before forwarding to the local service so public clients cannot spoof the remote IP, original host, or original protocol.

## Operational Notes

- `gateletd` keeps tunnel registrations in memory. Restarting the relay disconnects active tunnels.
- If a second client registers the same name while the first tunnel is active, `gateletd` rejects the second client and keeps the existing tunnel connected.
- Requests for unknown tunnel names return `404`.
- Reserved tunnel names are rejected during control handshake. `admin`, `www`, `api`, and `metrics` are reserved by default.
- Broken or unavailable tunnels return `502`.
- Hostnames are matched case-insensitively and may include a trailing dot.
- The forwarded request preserves the original public `Host` header.
- Tokens are not sent directly during tunnel registration; the client sends a token ID and proves token knowledge with an HMAC challenge response after the control transport is established.
- `gateletd` can accept multiple token IDs for rotation. Omit `--token-id` for legacy clients that use the implicit `default` token.
- The control protocol currently supports protocol version `1`. Unsupported clients receive `ERR unsupported protocol version`.
- The yamux control session uses periodic ping heartbeats. Dead or unresponsive tunnels are closed and removed from the relay session table.

## Development

CI runs on pushes and pull requests. The workflow runs unit tests, `go vet`, binary builds, `golangci-lint`, and the Docker E2E smoke test.

Run tests:

```sh
go test ./...
```

Run vet:

```sh
go vet ./...
```

Run the Docker E2E smoke test:

```sh
./scripts/e2e-docker.sh
```

The E2E script requires Docker. It builds the image, creates an isolated Docker network, starts `gateletd`, `gatelet`, and a target HTTP service, verifies GET/POST forwarding and unknown-tunnel behavior, then cleans up.

Build both binaries:

```sh
go build -o /tmp/gateletd ./cmd/gateletd
go build -o /tmp/gatelet ./cmd/gatelet
```

## Release Process

Releases are created from semantic version tags. The release workflow accepts tags such as `v0.1.0` and `v0.1.0-rc.1`.

```sh
git tag v0.1.0
git push origin v0.1.0
```

GoReleaser builds `gatelet` and `gateletd`, creates Linux and macOS archives, writes checksums, and publishes the GitHub release. Stable release tags also publish the `gatelet-bin` AUR package from the Linux amd64 archive. Prerelease tags are skipped for AUR because Arch `pkgver` values cannot contain hyphens.

The AUR publish job requires a GitHub Actions secret named `AUR_SSH_PRIVATE_KEY`. The matching public key must be added to the maintainer's AUR account and allowed to push `ssh://aur@aur.archlinux.org/gatelet-bin.git`.

## Limitations

- HTTP only; raw TCP tunnels are not supported.
- Public HTTP TLS is not handled by Gatelet yet. Put a reverse proxy such as Caddy, nginx, or HAProxy in front of `gateletd` for HTTPS.
- Raw TCP control only uses TLS when `gateletd` is started with `--control-tls-cert` and `--control-tls-key`. Clients must pass `--control-plaintext` to connect to a raw plaintext control listener. WebSocket control uses `ws://` for plaintext and `wss://` for TLS through the HTTP listener.
- Token specs are currently configured at daemon startup; live token reload is not implemented yet.
- There is no reservation database for tunnel names.
- The TUI dashboard is local to one active `gatelet` process; there is no daemon-side management API.
