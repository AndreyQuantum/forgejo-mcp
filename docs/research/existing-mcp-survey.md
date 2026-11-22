# Survey of existing Forgejo / Gitea MCP servers (2026-11-22)

Method: web search plus README fetches. Dates not shown on GitHub pages are marked "n/d". Tool counts come from READMEs and were not verified in code. Forgejo v17 compatibility is unverified for all of them (none state it explicitly).

| Server | Lang / license | Activity | Actions coverage | PR merge / reviews | Transport | Token efficiency |
|---|---|---|---|---|---|---|
| goern/forgejo-mcp (moved to git.b4mad.industries/agentic-forges/forgejo-mcp; Codeberg and GitHub are read-only mirrors) | Go, GPL-3.0 | Release 3.3.0 on 2026-10-10, 32 releases | list/get/dispatch/cancel/delete runs, list_action_run_jobs, get_action_job_logs (v16+, offset + max_bytes), artifacts, secrets. No runners | merge (merge/rebase/rebase-merge/squash, delete branch, wait-for-checks), full review set, diff | stdio, SSE, streamable HTTP, optional OAuth resource-server, CLI mode (call tools without a server) | JSON; bounded logs; CLI text output; no explicit compact mode |
| gitea.com/gitea/gitea-mcp (official Gitea) | Go, MIT | v1.13.0 on 2026-11-06, commit 2026-11-22 | actions_run_read/write (workflows, runs, jobs, logs, artifacts, dispatch/cancel/rerun/approve), config (secrets, variables). No runners | pull_request_write (create/merge/update branch/reviewers), pull_request_review_write | stdio, stateless HTTP /mcp, OAuth 2.1 | Consolidated method-based tools (48 tools), read-only mode, --scope and --tools filters; result format not documented; Forgejo not mentioned |
| Sqcows/forgejo-mcp (GitHub) | likely TypeScript, MIT | 19 commits, n/d, 18 stars; README count is inconsistent (103 vs 457 tools) | Widest: 54 Actions tools (runs, jobs, plaintext logs, artifacts, secrets, vars, runners at repo/org/user/instance) | 28 tools: merge, is_pr_merged, update branch, cancel auto-merge, reviews | stdio, HTTP /mcp with Bearer and rate limit | Best of the set: compact JSON, trimmed schemas, area toggling (about 45k tokens for all areas), --lazy mode (about 600 tokens, needs a client that reloads the tool list) |
| raohwork/forgejo-mcp (also ronmi on Docker Hub; pkg.go.dev) | Go, MPL-2.0 | 71 commits, n/d, 70 stars | view-only Actions tasks | PR view only, no merge | stdio, SSE, streamable HTTP, per-request token | not described |
| kunde21/forgejo-mcp | Go, MIT | 141 commits, n/d, 5 stars | none | pr_list/create/edit/comments only | stdio | content text plus structured field; minimal |
| dafunn/forgejo-mcp | Python (FastMCP), MIT | 4 commits, 0 stars | CI only: build_status, run_info, run_jobs, job_log(tail), run_log, failed_logs(scan). Needs Forgejo 16+ | none | stdio, streamable HTTP | tail/limit/scan controls; the "failed_logs" shortcut is a good idea |
| TheLazyGeekGuy/forgejo-mcp (fork of kepatrick/forgejo-mcp) | Python plus React, Apache-2.0 | v0.1.0, 0 stars | runs, jobs, logs, artifacts | PRs and reviews, merge not mentioned | streamable HTTP, OAuth 2.1 / Bearer | audit logs, granular permissions, metrics; output format n/d |
| sandraschi/forgejo-mcp | Python (FastMCP), GPL-3.0 | v0.2.0, 0 stars | runners and workflow_runs via one tool | forgejo_prs list/create/merge/diff | local (uvx) | 7 portmanteau tools, profiles, prompt; shallow |
| actionlake.com Forgejo connector | hosted gateway | n/a | claims 485 actions incl. runner management | n/a | hosted | third-party service, not self-hosted |

npm / PyPI: searches found no confirmed packages; the servers are installed as Go binaries, Docker images, or from source. Registry sites (mcpservers.org) list goern/forgejo-mcp only.

## Verdict

No single server covers the full need with confidence.
- Closest on features: goern/forgejo-mcp (maintained, release 3.3.0, merge plus reviews plus run/job/log tools, CLI mode). It has no runner management, uses plain JSON output, and is GPL-3.0 with its home moved to a self-hosted forge (Anubis-protected, hard to reach for automation).
- Widest and most token-aware: Sqcows/forgejo-mcp (runners, compact JSON, lazy mode). It is young, has contradictory docs, was built with an LLM, has no releases, and its quality is unverified.
- Official gitea-mcp is solid and consolidated but built for Gitea; Forgejo v17 behavior (for example the v16+ Actions log API and the diverging Actions endpoints) is untested, and it lacks runners.
- Everything else is narrow.

Recommendation: build our own, scoped to the stated goal of cutting token use, with Forgejo v17 as the target. Existing servers either dump raw JSON, cover only part of the surface, or are of unknown quality. Before committing, spend about 30 minutes smoke-testing goern/forgejo-mcp and Sqcows/forgejo-mcp against the real instance, since either may be adopted or used as a reference.

## Ideas worth borrowing
1. Consolidated, method-based tools (gitea-mcp, sandraschi): few tools with a `method` argument keep the schema size small.
2. Lazy / area toggling (Sqcows): load only needed areas; about 600 tokens at start versus about 45k for everything.
3. Read-only mode plus --scope / --tools filters (gitea-mcp).
4. Bounded logs: offset/max_bytes (goern), tail (dafunn), and a `failed_logs` shortcut that finds the newest failing run and returns only failed-job tails.
5. Compact, non-indented JSON with field projection (return only the fields a model needs, for example number, title, state, mergeable, head sha, check status instead of the full API object).
6. CLI mode sharing the same code path (goern), so skills and scripts can call tools without a server.
7. merge_pull_request with wait-for-checks (goern), and one-call "PR status" that combines PR state, mergeability, and CI status.
8. HTTP transport with per-request Authorization header and loopback binding by default; optional OAuth.
9. Runner management (repo/org/user/instance scopes, registration tokens) is where Sqcows is the only prior art.
10. Multi-profile support for several Forgejo instances (sandraschi).

## Weaknesses to avoid
- Raw API objects in responses (urls, avatars, nested user objects).
- Hundreds of tools in the schema (Sqcows 457 in README).
- Hard dependence on Forgejo 16+ endpoints without a fallback or clear error.
- Unclear licensing mix (GPL-3.0 for goern limits reuse of its code; clean-room or MIT/Apache for our own).
