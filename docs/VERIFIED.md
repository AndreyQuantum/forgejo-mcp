# Verified on Forgejo 17.0.2

Results of the release-candidate run against a live `codeberg.org/forgejo/forgejo:17.0.2` instance
(`go test ./test/integration/... -count=1` with `FORGEJO_TEST_RUNNER=1`, forgejo-runner
`code.forgejo.org/forgejo/runner:13.2.2` as a host-labelled container). Result: 65 tests pass,
1 is skipped (`TestRecord`, fixture recording is opt-in), 0 fail. Windows host.

"Open" means the run did not isolate the question; the code keeps its defensive behaviour for it.

## Section 15 items

| # | Item | Result | Evidence | Code or spec change |
|---|---|---|---|---|
| 1 | Image tags, `.runner` schema, label precedence | Verified (tags, schema); precedence open | Instance runs as `forgejo:17.0.2`; runner image `code.forgejo.org/forgejo/runner:13.2.2`. The `.runner` line printed by `runner register` was accepted by the daemon and the runner came online (`TestActionsE2E`). | none. Whether `.runner` or `config.yml` labels win was not isolated: the e2e writes the same label in both. |
| 2 | Job-log line format, suffix Range, `.log.zst` | Partly verified | `TestActionsE2E`: `run log` with `tail`, `grep`, `step=failed` and `offset` follow all return the expected lines. Suffix-range behaviour on logs smaller than the range and Range on archived `.log.zst` logs were not isolated. | none |
| 3 | 409 body, paging limits, files cap, order | Partly verified | `default_paging_num` 30, `max_response_items` 50 (limits probe, `forgejo_info`). Merge conflict maps to the conflict error (`TestPullsMergeConflict`). Files totals cap, `/commits` order, closed PRs on `pulls/{base}/{head}` and fork-PR status location are open. | none |
| 4 | `event=pull_request` filter and `refs/pull/N/head` normalisation | Open | The e2e covers push runs and dispatch runs only. | none |
| 5 | Release delete vs git tag, create on existing tag | Verified | `TestContentRelease`: deleting a release keeps the git tag; a second release for the same tag answers 409 ("Release has no Tag"). | none |
| 6 | Draft prefix | Verified | `TestPullsLifecycle`: `WIP:` is accepted and the PR shows as `draft`. | none |
| 7 | Minimum-major table | Partly verified | Run list, job, log, cancel, rerun, rerun_job, delete and dispatch all work on 17. Behaviour on 15 and 16 cannot be tested here. `ci_failure.run` and `ci_wait.run` use the runs list and job logs, so they were added to `minMajorTable` with the same 16. | `internal/tools/minver.go`: two rows added, comment updated. |
| 8 | Generic package upload path | Verified | `PUT /api/packages/{user}/generic/{name}/{version}/{file}` works in the package tests (plain HTTP, outside `/api/v1`). | none |
| 9 | Registry `server.json`, OCI label, goreleaser key | Open | Needs the registry validator and a release build, not part of this run. | none |
| 10 | go-sdk v1.8.0 behaviour | Verified | The raw `Server.AddTool` accepts the hand-built object schema and `tools/list` returns 29 default tools (30 with `admin`) over stdio and over the in-process session used by the harness. | none |
| 11 | Timeline event names for scheduled merges | Open | The auto-merge cancel path is exercised (`TestPullsAutomergeCancelNothingScheduled`), the event names are not. | none |
| 12 | Push-mirror interval | Partly verified | `pushmirror_add` with `interval=0` is accepted as well-formed (`TestReposPushMirror`); the 10m minimum and the `sync_on_commit` combination are open. | none |
| 13 | `.runner` JSON printed by `runner register` | Verified | Same run as item 1: the printed file is read by forgejo-runner 13.2.2 without changes. | none |
| 14 | `os.Root` on Windows | Partly verified | The whole suite, including the file upload and download tests, passed on a Windows host. Junctions and case-insensitive roots were not tested. | none |
| 15 | `.forgejo` vs `.gitea` workflows | Partly verified | `run workflows` lists `ci.yml` and `manual.yml` with their inputs (`TestActionsE2E`). Precedence when both directories exist, and that `.github/workflows` never runs, are open. | none |
| 16 | MinIO/S3 `ServeDirect` redirects | Open | No object-storage backend in the test instance. The same-host and cross-host redirect rules are covered by unit tests. | none |

## Other findings of the run

- **Webhook authorization header cannot be cleared.** `PATCH` with `authorization_header: ""` answers 200 and keeps the old value (verified on 17.0.2). `hook edit` now refuses an empty value locally with `invalid: the authorization header cannot be cleared via API on this server; delete and recreate the hook`. Spec: section 8.8. Tests: `TestSettingsHookRepoLifecycle` and `TestHookEditRejectsEmptyAuthorizationHeader`.
- **Annotated tag, verbose.** `tag get verbose=true` prints an indented `annotation:` block instead of a `message:` line; the integration test expects that form.
- **forgejo_api and secrets.** `authorization_header` and `secret` values are replaced by `(hidden)` unless `raw=true`.
- **forgejo_api policy.** An endpoint is denied only when no enabled action declares it, so turning a toolset off no longer blocks shared endpoints such as `GET /repos/{owner}/{repo}`.
- **Endpoint index.** `swagger_sha256` is the hash of the `paths` object only. The live `/swagger.v1.json` and the checked-in file differ in `basePath` and would otherwise always look stale.

## Tool-schema token budget

Measured with `tools/list` over stdio (compact JSON, bytes):

| Set | Tools | `tools/list` bytes | About tokens (bytes / 4) |
|---|---|---|---|
| Default toolsets | 29 | 51,804 | 12,950 |
| `FORGEJO_TOOLSETS=all` | 30 | 55,529 | 13,880 |

- Server instructions: 551 characters.
- Widest tools: `repo` 4,259 B, `pr` 3,935 B, `issue` 3,914 B, `admin` 3,724 B, `file` 3,016 B.
- Limits in `internal/tools/budget.go`: 75,000 B total, 6,000 B per tool.

## Output size against the raw API JSON

Measured in `TestActionsE2E` against the live instance (`SIZE` lines in `go test -v`). The test repository is small,
so real repositories with long logs and many PRs save more.

| Call | Tool output | Raw API JSON | Ratio |
|---|---|---|---|
| `pr list state=all` (1 PR) | 89 B | 6,490 B | 1.4% |
| `pr get` | 233 B | 6,488 B | 3.6% |
| `run get` | 238 B | 565 B (jobs only) | 42% |
| `run log tail=2` | 164 B | 462 B (full log) | 36% |
| `ci_failure` | 294 B | 10,003 B (runs, jobs and log: the calls an agent needs without it) | 2.9% |
