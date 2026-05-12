# qbittorrent-mcp

MCP server wrapping the [qBittorrent](https://www.qbittorrent.org) WebUI v2 API.

Designed to run as a sidecar to the qBittorrent container, reaching the daemon over loopback. qBittorrent must have **"Bypass authentication for clients on localhost"** enabled in WebUI settings — the MCP server performs no login.

## Tools

| Tool | Description |
| --- | --- |
| _(none yet)_ | Scaffolding only. Add tools in [`internal/mcp/tools.go`](internal/mcp/tools.go); see `../dmhy-mcp/internal/mcp/tools.go` for the same pattern fully worked out. |

## Build & run

```sh
go build ./cmd/qbit-mcp
./qbit-mcp --transport=stdio
./qbit-mcp --transport=http --addr=:8080
```

HTTP transport exposes the MCP endpoint at `/mcp` and a k8s liveness probe at `/healthz`.

## Container

```sh
docker build -t qbit-mcp .
docker run --rm --network host qbit-mcp           # sidecar-style: shares loopback with qBittorrent
docker run --rm -i qbit-mcp --transport=stdio     # stdio
```

## Devserver (hot reload + MCP inspector)

```sh
make devserver-build
QBITTORRENT_URL=http://host.docker.internal:8080 make devserver-run
```

The container runs [air](https://github.com/air-verse/air) (rebuilds on `.go` save) alongside [@modelcontextprotocol/inspector](https://github.com/modelcontextprotocol/inspector). On startup it prints a prefilled inspector URL to copy into a browser.

## Configuration

| Flag | Env | Default |
| --- | --- | --- |
| `--transport` | `QBITTORRENT_TRANSPORT` | `stdio` |
| `--addr` | `QBITTORRENT_ADDR` | `:8080` |
| `--qb-url` | `QBITTORRENT_URL` | `http://localhost:8080` |
| `--qb-timeout` | `QBITTORRENT_TIMEOUT` | `15s` |
| `--log-level` | `QBITTORRENT_LOG_LEVEL` | `info` |

## Errors

Tool errors are returned as `IsError: true` with a JSON body:

```json
{ "code": "upstream_forbidden", "message": "...", "retriable": false }
```

Codes: `invalid_argument`, `upstream_unavailable`, `upstream_forbidden`, `upstream_not_found`, `internal`.

`upstream_forbidden` signals the loopback-auth-bypass assumption was wrong — re-check qBittorrent's WebUI settings.

The qBittorrent WebUI v2 calls go through [`github.com/autobrr/go-qbittorrent`](https://github.com/autobrr/go-qbittorrent); errors from the SDK are translated to the codes above at the MCP tool boundary in `internal/mcp/errors.go`.
