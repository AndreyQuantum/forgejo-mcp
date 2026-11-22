# forgejo-mcp

A token-efficient [Model Context Protocol](https://modelcontextprotocol.io) server for [Forgejo](https://forgejo.org). It lets an LLM work with pull requests, issues, CI runs and logs, runners, repositories and more over the Forgejo HTTP API, while keeping context usage small: 30 tools with short schemas, compact plain-text output instead of raw JSON, and server-side polling and log processing.

Typical difference for "why did CI fail on my PR?":

| Without | With forgejo-mcp |
|---|---|
| list runs, fetch the run, list jobs, download every job log (tens of KB of timestamps and ANSI codes), then search for the error | one call: `ci_failure pr=12` returns the failed jobs and the tail of the failing step's log, a few dozen lines |
| poll in a loop from the model | `ci_wait pr=12` blocks on the server and returns a verdict |

Everything not covered by a dedicated tool is reachable through `forgejo_endpoints` (search the embedded index of the v17.0.2 API) and `forgejo_api` (raw call with field picking and paging).

Written in Go, one static binary, MIT licensed. Tested against Forgejo 17.0.2; Forgejo 15 and newer is supported on a best-effort basis (actions that need a newer server are reported as such).

## Install

```sh
go install github.com/AndreyQuantum/forgejo-mcp/cmd/forgejo-mcp@latest
```

Or download a binary from the [releases page](https://github.com/AndreyQuantum/forgejo-mcp/releases), or use the container image:

```sh
docker run --rm -i -e FORGEJO_URL=https://forgejo.example.com -e FORGEJO_TOKEN \
  ghcr.io/andreyquantum/forgejo-mcp:latest
```

Check the setup (loads the configuration, calls `/version` and `/user`, prints one summary line):

```sh
FORGEJO_URL=https://forgejo.example.com FORGEJO_TOKEN_FILE=~/.config/forgejo-mcp/token forgejo-mcp --check
```

## Use with an MCP client

### Claude Code

```sh
claude mcp add forgejo \
  -e FORGEJO_URL=https://forgejo.example.com \
  -e FORGEJO_TOKEN_FILE=~/.config/forgejo-mcp/token \
  -- forgejo-mcp
```

Prefer a token file, or the `${FORGEJO_TOKEN}` expansion in a project `.mcp.json`. A literal `-e FORGEJO_TOKEN=...` is stored in plain text in `~/.claude.json`. Add `--scope user` or `--scope project` to choose where the server is registered.

### Claude Desktop

`claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "forgejo": {
      "command": "/path/to/forgejo-mcp",
      "env": {
        "FORGEJO_URL": "https://forgejo.example.com",
        "FORGEJO_TOKEN_FILE": "/home/me/.config/forgejo-mcp/token"
      }
    }
  }
}
```

## Configuration

All settings are environment variables; only `FORGEJO_URL` is required. Flags (`--transport`, `--addr`, `--toolsets`, `--read-only`) override the environment.

| Variable | Meaning | Default |
|---|---|---|
| `FORGEJO_URL` | Instance web root, for example `https://forgejo.example.com` (a trailing `/`, `/api/v1` and `.git` are stripped; sub-path installs work) | required |
| `FORGEJO_TOKEN` | Personal access token | none (anonymous, read-only on public repositories) |
| `FORGEJO_TOKEN_FILE` | File holding the token; used only when `FORGEJO_TOKEN` is empty. UTF-8 and UTF-16 (BOM) files are handled, whitespace is trimmed | none |
| `FORGEJO_REPO` | Default repository `owner/name` | detected from the git remotes of the working directory |
| `FORGEJO_SSH_HOSTS` | Extra hosts (`host` or `host:port`) that mean "this instance" in git remotes | none |
| `FORGEJO_DIR` | Directory where repository detection starts | process working directory |
| `FORGEJO_TOOLSETS` | Names, `all`, or `-name` to subtract | `repos,pulls,issues,actions,runners,settings,users,packages` |
| `FORGEJO_READ_ONLY` | `1`/`true` hides and blocks every mutating action | off |
| `FORGEJO_DENY` | Comma list of `tool.action` or `tool.*` that are hidden and blocked, also inside `forgejo_api` (for example `repo.delete,admin.*`) | none |
| `FORGEJO_TIMEOUT` | Per-request HTTP timeout | `30s` |
| `FORGEJO_MAX_OUTPUT` | Character cap per tool result | `16000` |
| `FORGEJO_DEFAULT_LIMIT` | Default list page size (clamped to the server maximum) | `20` |
| `FORGEJO_WAIT_DEFAULT` | Default `ci_wait` timeout in seconds | `50` |
| `FORGEJO_WAIT_MAX` | Hard cap for the `ci_wait` timeout in seconds (up to 600) | `55` |
| `FORGEJO_SAVE_DIR` | Only root for local file parameters such as `save_to` | `<tmp>/forgejo-mcp` |
| `FORGEJO_SAVE_CWD` | `1` additionally allows the working directory as a root | off |
| `FORGEJO_HTTP_FILES` | `1` enables local file parameters in HTTP mode | off |
| `FORGEJO_SUDO` | User name sent as the `Sudo` header (site admin tokens only) | none |
| `FORGEJO_CA_FILE` | Extra CA bundle | none |
| `FORGEJO_TLS_INSECURE` | `1` skips TLS verification | off |
| `FORGEJO_MCP_BEARER` | HTTP mode: bearer token required on non-loopback listen addresses | none |
| `FORGEJO_DEBUG` | `1` logs method, path, status and duration of requests to stderr (never headers or bodies) | off |

### Token scopes

A token with these scopes covers every tool except `admin`: `read:repository`, `write:repository`, `read:issue`, `write:issue`, `read:notification`, `write:notification`, `read:organization`, `write:organization`, `read:user`, `write:user`, `read:package`. The `admin` tool additionally needs `write:admin` and a site administrator account. A token limited to some repositories works for the repository tools. Grant less when you can: combine a narrow token with read-only mode.

### Toolsets

A tool belongs to exactly one toolset. `meta` is always on; `admin` is off by default.

| Toolset | Tools |
|---|---|
| `meta` | `forgejo_info`, `forgejo_api`, `forgejo_endpoints` |
| `repos` | `repo`, `branch`, `tag`, `release`, `wiki`, `file`, `commit` |
| `pulls` | `pr`, `review` |
| `issues` | `issue`, `comment`, `label`, `milestone`, `attach` |
| `actions` | `run`, `ci_failure`, `ci_wait`, `checks`, `actions_config` |
| `runners` | `runner` |
| `settings` | `hook`, `access` |
| `users` | `user`, `org`, `notify` |
| `packages` | `package` |
| `admin` | `admin` |

The full per-action reference is generated into [docs/TOOLS.md](docs/TOOLS.md) (`make docs`). Every multi-action tool answers `action=help` with its actions and parameters, so the schemas sent to the model stay small.

## Read-only mode and deny list

`FORGEJO_READ_ONLY=1` removes every write and destructive action from the tool schemas and blocks them at call time. `forgejo_api` is classified per call: only `GET`/`HEAD` pass (plus the pure rendering endpoints), and `GET` endpoints with side effects such as runner registration tokens are blocked. `FORGEJO_DENY` removes single actions; the matching endpoints are blocked in `forgejo_api` as well, so it is a real control and not a hint. Destructive actions need `confirm=<exact target>`.

## Output format

Output is compact text, built for reading by a model:

```
# prs 3/3 page 1/1
#12 open | fix login redirect | alice -> main | checks: success | 2026-11-20 14:02
#11 open | docs: update readme | bob -> main | checks: pending | 2026-11-19 09:40
#9 merged | bump deps | alice -> main | 2026-11-18 17:15
```

Lists show `page x/y` and the `next:` page. Long output is truncated with a marker that says how to get the rest. Only `forgejo_api` returns (compact) JSON.

## HTTP mode

```sh
FORGEJO_URL=https://forgejo.example.com FORGEJO_TOKEN_FILE=... forgejo-mcp --transport http --addr 127.0.0.1:8787
```

The endpoint is `/mcp` (streamable HTTP). Security notes:

- The default bind address is loopback. A non-loopback `--addr` requires `FORGEJO_MCP_BEARER`, checked with a constant-time comparison on `Authorization: Bearer`.
- When neither `FORGEJO_TOKEN` nor `FORGEJO_TOKEN_FILE` is set, each request may bring its own Forgejo token in the `X-Forgejo-Token` header (required header when a bearer is configured). Tokens are never shared between requests.
- Local file parameters are disabled in HTTP mode unless `FORGEJO_HTTP_FILES=1`.
- Put TLS in front of it (a reverse proxy) when it leaves the machine.

The container image runs as a non-root user; for HTTP mode use `--transport http --addr 0.0.0.0:8787` together with `FORGEJO_MCP_BEARER`.

## Tool timeouts

Many MCP clients cancel a tool call after 60 seconds. `ci_wait` therefore defaults to 50 s and is capped at 55 s; when time runs out it returns the verdict `timeout` with a `resume:` line, and calling the same tool again continues. To wait longer, raise `FORGEJO_WAIT_MAX` and the client limit (for Claude Code: `MCP_TOOL_TIMEOUT`). Uploads and long migrations are bounded by the same client limit.

## Troubleshooting

- Run `forgejo-mcp --check` to see the effective configuration, server version and the authenticated user.
- `FORGEJO_DEBUG=1` prints one line per request to stderr (the MCP stream on stdout is never touched).
- A `404` for a repository usually means the token has no access to it, not that it does not exist.
- `403` messages name the permission or scope that is missing.
- Startup configuration errors exit with code 2 and never print the token.

## FAQ

**Why one tool with an `action` enum instead of one tool per endpoint?** Every tool schema is sent to the model on each session. Thirty tools with short descriptions cost a fraction of hundreds of single-purpose tools, and `action=help` gives the details only when they are needed.

**How do I call an endpoint that has no tool?** Use `forgejo_endpoints q=<words>` to find the method and path in the embedded index, then `forgejo_api method=... path=...`. Deny rules, toolsets and read-only mode apply to it as well.

## Development

```sh
make build        # static binary
make test         # unit tests
make lint         # golangci-lint (v2)
make docs         # regenerate docs/TOOLS.md
make snapshot     # local GoReleaser snapshot (needs goreleaser and Docker)
make integration  # throwaway Forgejo 17.0.2 in Docker, then the integration tests
```

`make endpoints SWAGGER=<file or url>` regenerates the embedded endpoint index from a swagger file. The design notes are in [docs/DESIGN.md](docs/DESIGN.md).

## License

MIT, see [LICENSE](LICENSE).
