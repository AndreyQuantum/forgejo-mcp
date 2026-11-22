# Forgejo v17.0.2 API: Actions, runners, commit statuses

Source: sparse checkout of Forgejo v17.0.2 (handlers in `routers/api/v1/repo/action.go`, `routers/api/v1/shared/runners.go`, `routers/api/v1/{admin,org,user}/...`, `routers/api/v1/repo/status.go`, `services/actions/*`, `services/convert/*`, `modules/structs/*`, routes in `routers/api/v1/api.go`).
Not in checkout, so UNVERIFIED (from memory of Gitea/Forgejo): `modules/actions` (log line format, `OpenLogs`, `MaxStoredLineSize`), `modules/setting` (paging defaults), `models/git` (commit status model), `modules/webhook` (event name mapping). Marked `[unverified]`.

## 0. Conventions that apply to everything

- Base: `/api/v1`. Auth: `Authorization: token <t>` (or `Bearer`). Error body (ctx.Error): `{"message": "...", "url": "<swagger url>"}`.
- Scopes: no dedicated "actions" scope. Repo-level routes sit under `tokenRequiresScopes(repository)`: GET needs `read:repository`, POST/PUT/PATCH/DELETE needs `write:repository`. Org routes need `read/write:organization`, `/user/...` needs `read/write:user`, `/admin/...` needs `read/write:admin` + site admin. (Level is derived from HTTP method in `requiredScopeLevel`.)
- Repo-level permission gates (inside the Actions group, which is wrapped by `reqRepoReader(unit.TypeActions)`):
  - Read endpoints (runs, jobs, logs, artifacts, tasks): NO `reqToken`. Anonymous works on public repos; private needs token with repo read + Actions unit enabled (else 404/403).
  - cancel, rerun (run + job), artifact delete, dispatch: `reqToken` + `reqRepoWriter(Actions)`; dispatch also `mustNotBeArchived`.
  - delete run: `reqToken` + `reqAdmin(Actions)` (repo admin).
  - secrets, variables, runners (all of `addActionsRoutes` under a repo): `reqToken` + `reqOwner(Actions)` (repo owner / site admin; a collaborator with write is NOT enough).
  - org: `reqToken` + `reqOrgOwnership()`; user: `reqToken` only (acts on the doer); admin: site admin.
- Pagination (`utils.GetListOptions`): `page` (1-based), `limit`; limit <=0 -> `setting.API.DefaultPagingNum` (default 30 [unverified]); limit > `MaxResponseItems` clamped (default 50 [unverified]; swagger text says "default maximum page size is 50"). Read real values from `GET /settings/api` (`default_paging_num`, `max_response_items`).
- Two pagination styles in this area (important):
  - Header style (`Link` + `X-Total-Count`): artifacts lists, runners lists, secrets/variables lists (count header only), commit statuses list.
  - Body-wrapper style, NO `Link`, NO `X-Total-Count`: `ListActionRuns` -> `{"workflow_runs":[...], "total_count":N}`; `ListActionTasks` -> `{"workflow_runs":[...tasks], "total_count":N}` (key is misleadingly `workflow_runs` for tasks too).
  - Unpaginated arrays: `runs/{id}/jobs`, `runners/jobs` (all levels), combined status paginates statuses but returns count only in header.
- Ordering: runs and tasks `id DESC` (newest first); run jobs `id ASC`; artifacts `id DESC`.

## 1. ID model (run_id vs index vs job id vs task id)

| Thing | JSON field | Meaning |
|---|---|---|
| Run global id | `ActionRun.id` | `action_run.id`, global PK. This is `{run_id}` in ALL API paths. |
| Run number | `ActionRun.index_in_repo`; `ActionTask.run_number`; `DispatchWorkflowRun.run_number` | Per-repo counter, what the UI shows (`/{o}/{r}/actions/runs/{index}`). NOT usable in API paths. Resolve via `GET .../actions/runs?run_number=N` -> `.workflow_runs[0].id`. |
| Job id | `ActionRunJob.id` | `action_run_job.id`, global. `{job_id}` in `/actions/jobs/{job_id}[/logs|/rerun]`. Jobs of a run are listed by `runs/{run_id}/jobs`. |
| Job attempt | `ActionRunJob.attempt` | Count of attempts incl. current (0 = never picked up). Reruns bump it. `?attempt=N` on job logs selects history. |
| Task id | `ActionRunJob.task_id`, `ActionTask.id` | `action_task.id`, one per (job, attempt) as picked by a runner. `job.task_id` always points to the LATEST attempt. 0 = not started (or reset by rerun). |
| Handle | `ActionRunJob.handle` | opaque id of one attempt of a job; not used in any path. |
| UI job index | (not in API) | Web URL `/runs/{runIndex}/jobs/{jobIndex}` uses the 0-based POSITION of the job in the run, not the job id. API `ActionRunJob.html_url` already contains it. |

Consequences:
- Typical flow: dispatch (returns run `id`) or list runs -> `run_id`; `runs/{run_id}/jobs` -> job ids; `jobs/{job_id}` -> steps; `jobs/{job_id}/logs`.
- `ActionTask` (from `/actions/tasks`) has NO run id and NO job id; only `id` (task), `run_number`, `name` (job name), `workflow_id`, `head_sha`, `head_branch`. Weak for navigation; prefer `/runs`.
- All run/job/artifact handlers verify `run.RepoID == repo.ID` and return 404 on mismatch, so ids cannot be probed across repos.

## 2. Status values

- Run / job / task / step status strings (`actions_model.Status`): `unknown`, `waiting`, `running`, `success`, `failure`, `cancelled`, `skipped`, `blocked`.
  - Done (final): success, failure, cancelled, skipped. Pending: unknown, waiting, running, blocked.
  - `blocked` = waiting on `needs` or on approval (fork PR / untrusted user, see `need_approval`).
  - There is NO `queued`/`pending`/`completed`/`in_progress` in this API. A GitHub-style filter value like `in_progress` returns 400 `unknown status: in_progress` (the swagger description is copied from GitHub and misleading).
- Runner status: `offline` (no contact > 1 min), `idle` (online but last_active > 10 s ago), `active` (picked a task in the last 10 s; this is NOT "currently busy", a long job reads `idle` after 10 s). Constants `RunnerOfflineTime=1m`, `RunnerIdleTime=10s`. So runner `active` is a weak signal; to see load use `runners/jobs` (waiting+running jobs).
- Commit status states: `pending`, `success`, `error`, `failure`, `warning`, `skipped`.
  - Combined state = worst by priority error(0) < failure < warning < pending < success < skipped(5); `error` is rewritten to `failure`. Empty state `""` when there are no statuses.
- Artifact status is internal (1 pending, 2 confirmed, 3 error, 4 expired, 5 pending deletion, 6 deleted); the API only exposes `expired` bool.

## 3. Runs

### GET /repos/{owner}/{repo}/actions/runs  (operationId ListActionRuns)
Scope read:repository, no token needed for public.
Query:
- `page`, `limit`.
- `status` (repeatable, array): one of the 8 strings above; invalid -> 400.
- `event` (repeatable): matched with SQL `IN` against the RAW stored `event` column. The response field `event` is `run.Event.Event()` (normalised), so a value shown in the response (e.g. `pull_request`) might not match rows stored as sub-events such as `pull_request_sync` [unverified, mapping in modules/webhook]. Safe values: `push`, `workflow_dispatch`, `schedule`. Test `pull_request` before relying on it.
- `run_number` (int64): exact per-repo index (`index` column). 0/absent = no filter.
- `head_sha`: exact `commit_sha` (full 40-char; no prefix match).
- `ref`: exact match on stored full ref, e.g. `refs/heads/main`, `refs/pull/12/head`, `refs/tags/v1`. A short `main` matches nothing.
- `workflow_id`: workflow FILE NAME, e.g. `ci.yml` (not the display name, not a numeric id).
- No filter for actor/trigger_user, no `created` date range, no branch short-name, no `pr`. (Web UI has `actor` + numeric `status`; API lacks `actor`.)
Response 200: `{"workflow_runs":[ActionRun...], "total_count":int}`; no Link header.
ActionRun fields: `id, title, repository{FULL repo object, ~60 fields}, workflow_id, index_in_repo, trigger_user{FULL user object}, ScheduleID (no json tag, capitalised key), prettyref, is_ref_deleted, commit_sha, is_fork_pull_request, need_approval, approved_by, event, event_payload (huge JSON string), trigger_event, status, started, stopped, created, updated, duration (Go time.Duration = integer NANOSECONDS), html_url`.
Gotchas:
- Each entry embeds the whole `repository` and `trigger_user` objects plus `event_payload`; a 30-run page is easily 50-100 KB. This is THE biggest token sink in this area; MUST project.
- `prettyref` is the short branch/tag name, or `#N` for pull refs (`refs/pull/N/head`).
- `started`/`stopped` are optional (absent/null when not started).
- `ListActionRuns` is also the only way to find the run for a PR or sha: `head_sha=<sha>` (for PRs the run's commit is the PR head sha; PR event runs have `ref=refs/pull/N/head`).

### GET /repos/{owner}/{repo}/actions/runs/{run_id}  (ActionRun)
`run_id` int64 = global id. Same heavy ActionRun object. 404 if not in this repo.

### DELETE /repos/{owner}/{repo}/actions/runs/{run_id}  (DeleteActionRun)
Repo admin. 204. Run must be done (success/failure/cancelled/skipped). Gotcha: if not done, `services.DeleteRun` returns a plain error and the handler maps it to 500 (not 400). Also marks its artifacts deleted, removes jobs.

### POST /repos/{owner}/{repo}/actions/runs/{run_id}/cancel  (CancelActionRun)
Write on Actions. 204 always (also when the run was already finished: silent no-op). Errors other than 404 -> 500. No body.

### POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun  (RerunActionRun)
Write on Actions. 204. Reruns all jobs. 400 for: workflow still running, workflow invalid (syntax), workflow disabled. Does NOT return the (new) run: rerun reuses the same run id, bumps job attempts. Poll `runs/{id}` / jobs afterwards. No `failed_only` variant (use job rerun per failed job).

### POST /repos/{owner}/{repo}/actions/jobs/{job_id}/rerun  (repoRerunActionJob)
Write on Actions. 204. Reruns that job and every job depending on it (via needs). 400: job still running / invalid / disabled workflow. Job must be done.

### POST /repos/{owner}/{repo}/actions/workflows/{workflowfilename}/dispatches  (DispatchWorkflow)
Write on Actions, not archived. JSON body `DispatchWorkflowOption`:
- `ref` (string, required): branch/tag/sha; expanded with `gitRepo.ExpandRef`, so short names (`main`) work. Unknown ref -> 400 `reference '...' does not exist`.
- `inputs` (map[string]string): values are strings only, even for boolean/number inputs (`"true"`). Missing inputs fall back to `default`; a `required` input without value/default -> 400 `input required for '<description or key>'`. Inputs not declared in `on.workflow_dispatch.inputs` are silently ignored. Count limited by `setting.Actions.LimitDispatchInputs` (else 500 `too many inputs`).
- `return_run_info` (bool, default false).
Response: `return_run_info=false` -> 204, empty body, no way to learn the run id. `return_run_info=true` -> 201 `{"id": <run_id>, "run_number": <index>, "jobs": [<job keys from workflow yaml>]}`. `jobs` are the YAML job ids (map keys, unordered), NOT action_run_job ids; matrix expansion not reflected. ALWAYS send `return_run_info:true` from the MCP.
Gotchas:
- `{workflowfilename}` is matched by file basename (`ci.yml`) within the workflow dir of the commit at `ref` (`.forgejo/workflows`, `.gitea/workflows` or `.github/workflows`). Unknown file -> 404 `workflow not found`.
- The handler does NOT check that the workflow declares `on: workflow_dispatch` (code only reads inputs if `WorkflowDispatchConfig()` != nil). It will create a run of any workflow file.
- Dispatch with `concurrency: cancel-in-progress` cancels previous runs in the group.
- Dispatched run: `event=workflow_dispatch`, `trigger_event=workflow_dispatch`, trigger_user = token owner, status starts `waiting`.
- Web UI equivalent (`POST /{repo}/actions/manual`) sends `inputs[key]` form fields; same service.

### GET /actions/run  (getActionsRun, miscellaneous)
Only for the automatic job token (`${{ forgejo.token }}`); any other token -> 403 `must use an automatic actions token`. Returns ActionRun of the calling job. Useless for an MCP acting as a user; skip.

## 4. Jobs, steps, logs

### GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs  (ListActionRunJobs)
Unpaginated array of ActionRunJob, ordered by id. Fields: `id, run_id, attempt, handle, repo_id, owner_id, name, html_url, needs[], runs_on[], task_id, status`. NO started/stopped/duration, NO steps here. Each element triggers `job.LoadAttributes` + `HTMLURL` (loads all sibling jobs) server-side, so large matrices are slow but fine.

### GET /repos/{owner}/{repo}/actions/jobs/{job_id}  (repoGetActionJob)
Same ActionRunJob plus `steps: [{number, name, status, started?, stopped?}]` when the job has a task. `number` 0 = "Set up job", last = "Complete job", real steps 1..N in YAML order (this is the `step` value for logs). Steps omitted entirely if never picked up (task_id 0). Step `started/stopped` are optional timestamps (compute durations). Only the latest attempt's steps.

### GET /repos/{owner}/{repo}/actions/jobs/{job_id}/logs  (repoGetActionJobLogs)
Best-designed endpoint in the area; this is what makes token-cheap log reading possible.
Query:
- `attempt` (int64, 1-based, matches `ActionRunJob.attempt`/task attempt): default = latest (via `job.task_id`). Unknown attempt -> 404 (via GetTaskByJobAttempt not-exist).
- `step` (int): step `number` from the job endpoint; returns only that step's byte slice. Out of range -> 404 `step index out of range`.
- `q` (string): plain substring filter (NO regex) applied to line CONTENT (after timestamp). `qi=true` for case-insensitive. Output lines keep the raw stored form (timestamp prefix + content).
- `format=text|ndjson`: ndjson = one `{"time":"<RFC3339Nano UTC>","content":"..."}` per line, `Content-Type: application/x-ndjson`.
Response headers/format:
- Default (`q` empty, text): served by `http.ServeContent` -> `Content-Type: text/plain; charset=utf-8`, `Accept-Ranges: bytes`, `Content-Length`, `Last-Modified`, supports `Range: bytes=a-b`, suffix `Range: bytes=-N` (tail!), `If-Modified-Since`, `HEAD`, and returns 206 for partial. Range applies over the step slice when `step` is set. `Content-Disposition` filename `job-{jobid}-attempt-{n}[-step-{k}].log`.
- With `q` or `format=ndjson`: streamed, no Content-Length, no Range support (full filtered body always returned, scanned linewise server side); `Content-Disposition: inline`.
- Stored line format [unverified, modules/actions]: `<RFC3339-ish UTC timestamp with 7-9 fractional digits>Z <content>\n`, i.e. ~30 chars of overhead per line. Strip it in the MCP (or use ndjson to get `time` separately; text mode + local strip is cheaper). Content can contain ANSI escapes and `::group::`-style markers; strip ANSI for token savings.
- Whole log is NOT size-capped by the handler; a verbose job can be many MB. Always use Range suffix / step / q.
Errors 404: unknown job, job in another repo, `job has not been executed yet` (task_id 0), `logs have expired` (`task.LogExpired`, cleaned by retention), bad step. 401/403 for unreadable repo.
While the job is running the log is partial (`Last-Modified` = task.Updated); polling a running job: use `Range: bytes=<offset>-` with the previous total size (from `Content-Length`/`Content-Range`) to fetch only the new part (works only on the plain path; `[unverified]` whether archived `.log.zst` logs are seekable cheaply, `OpenLogs` returns `io.ReadSeekCloser` so it must emulate seek).
Tail recipe: `GET ...?step=N` with `Range: bytes=-8000`; failing-lines recipe: `?q=error&qi=true&format=text`.

### GET /repos/{owner}/{repo}/actions/runs/{run_id}/logs  (repoGetActionRunLogs)
`Content-Type: application/zip`, `Content-Disposition: attachment; filename="run-{id}-logs.zip"`. One entry per job: `{sanitised job name}-{job id}-attempt-{N}.log` of the LATEST attempt. Marker entries `.MISSING` (not executed / expired / open failed) and `.PARTIAL` (read failed). Streamed, no Range, no filters, can be large. MCP should NOT expose this directly; implement run-level log tools by iterating jobs and calling per-job logs (or unzip server-side in the MCP and tail each entry).

### GET /repos/{owner}/{repo}/actions/tasks  (ListActionTasks)
Query `page, limit, status[]` (same 8 values). Response `{"workflow_runs":[ActionTask], "total_count"}`. ActionTask: `id (task), name (job name), head_branch (prettyref), head_sha, run_number, event (trigger_event), display_title, status, workflow_id, url, created_at, updated_at, run_started_at`. Newest first. One row per job-attempt picked by a runner, so noisier than runs; no run_id/job_id. Useful only as "recent job executions" overview; mostly redundant with runs.

## 5. Artifacts

### GET /repos/{owner}/{repo}/actions/artifacts and GET .../runs/{run_id}/artifacts
Query `name` (exact artifact name), `page`, `limit`. Headers `Link`, `X-Total-Count`. Items `ActionArtifact`: `id, name, size_in_bytes, archive_download_url, expired, run_id, created_at, updated_at, expires_at`. Artifacts are AGGREGATED per (run_id, name); `id` is `MIN(id)` of the group. Only confirmed and expired artifacts listed. Compact: `{id,name,size,run_id,expired,expires_at}`; drop `archive_download_url` (derivable: `/repos/{o}/{r}/actions/artifacts/{id}/zip`).
### GET .../artifacts/{artifact_id}
Single ActionArtifact. 404 if id is not the canonical (MIN) id of its group or other repo.
### GET .../artifacts/{artifact_id}/zip
`application/zip` stream via `ServeArtifact`; 404 if any chunk not confirmed. Binary; the MCP should save to a local file path rather than return content (return path+size).
### DELETE .../artifacts/{artifact_id}
Write on Actions. 204. Only marks for deletion (`SetArtifactNeedDelete` for run+name); space reclaimed asynchronously.

## 6. Secrets and variables (repo / org / user levels identical shape)

Routes (`addActionsRoutes`), prefix `/repos/{owner}/{repo}`, `/orgs/{org}`, `/user`:
- `GET   {prefix}/actions/secrets` (page, limit; `X-Total-Count`) -> `[{name, created_at}]` (values never returned).
- `PUT   {prefix}/actions/secrets/{secretname}` body `{"data": "..."}` required. 201 created, 204 updated. 400 invalid name. Line endings normalised to LF (send base64 if exact bytes matter).
- `DELETE {prefix}/actions/secrets/{secretname}` 204; 404 if missing.
- `GET    {prefix}/actions/variables` (page, limit, `X-Total-Count`) -> `[{owner_id, repo_id, name, data}]`.
- `GET    {prefix}/actions/variables/{variablename}` -> one.
- `POST   {prefix}/actions/variables/{variablename}` body `{"value": "..."}` required: create. 409 if exists. Swagger lists 201/204; code returns 204.
- `PUT    {prefix}/actions/variables/{variablename}` body `{"name": "<new name, optional>", "value": "..."}`; value required (binding Required, so empty value is rejected). 404 if missing, 204 on success. Names are uppercased by Forgejo.
- `DELETE {prefix}/actions/variables/{variablename}` 204.
Gate: repo owner (`reqOwner(Actions)`), org owner, or the user themself. Scope write:repository/organization/user accordingly.

## 7. Runners (repo / org / user / admin)

Same handler set (`shared/runners.go`); prefixes `/repos/{owner}/{repo}/actions/runners`, `/orgs/{org}/actions/runners`, `/user/actions/runners`, `/admin/actions/runners`. Gates as in section 0 (admin = site admin).

- `GET {prefix}` list. Query `page, limit, visible` (bool). Headers `Link`, `X-Total-Count`. Items `ActionRunner`: `id, uuid, owner_id, repo_id, name, status(offline|idle|active), version, labels[], description, ephemeral`.
  - SCOPE GOTCHA: `visible=false` (default) returns only runners owned directly at that level (repo: `repo_id=repo`; org/user: `owner_id=owner`; admin: ONLY global runners, `repo_id=0 AND owner_id=0`). `visible=true` widens: repo -> repo + owner's + global; org/user -> owner's + global; admin -> EVERYTHING on the instance. The admin swagger summary ("all runners no matter ...") is only true with `visible=true`.
  - No server-side filter for name/labels/status (no `IsOnline`/`Filter` exposed). Filter client side.
  - Compact: `{id,name,status,labels,ver:version,eph:ephemeral,scope:(repo_id?'repo':owner_id?'owner':'global')}`; drop `uuid`, `description` unless asked.
- `GET {prefix}/{runner_id}` one runner; 404 if not visible from that scope.
- `POST {prefix}` register. Body `RegisterRunnerOptions`: `name` (required, not unique), `description`, `ephemeral` (bool; runner deleted after one job). 201 `{"id": int, "uuid": "<random v4>", "token": "<40 hex chars>"}`. The token is shown ONLY here (stored salted+hashed). No labels are set at registration; labels are reported by the runner at `declare` time (`labels` in the runner config / `.runner`). To bring a runner up manually, the `.runner` file needs `id, uuid, token, name, address(instance URL), labels` [unverified against runner source]; note the UUID is NOT derived from the token in this API path (the older `forgejo-cli actions register --secret` path does that). Treat the token as a secret in MCP output (mask in logs, return once).
- `DELETE {prefix}/{runner_id}` 204. 404 `No permission to delete this runner` when the runner exists but is not owned at that level (e.g. deleting a global runner via repo path) or does not exist. Deleting a running runner leaves its task to time out.
- `GET {prefix}/registration-token` -> `{"token": "..."}`. DEPRECATED since Forgejo 15 (use POST register or the web UI); still present in v17. GET with side effect: returns the latest active token, creates a new one if none/inactive. Also duplicated at `/admin/runners/registration-token` and `/admin/actions/runners/registration-token`.
- `GET {prefix}/jobs` (`labels` comma-separated). operationIds `repoSearchRunJobs`, `orgSearchRunJobs`, `userSearchRunJobs`, `adminGetActionRunJobs` (plus deprecated alias `/admin/runners/jobs` = `adminSearchRunJobs`). Returns unpaginated array of ActionRunJob in status `waiting` or `running` only (`status` param not accepted), scoped to repo/owner/instance. Label filter: empty -> all; job matches if runner labels cover its `runs_on` (`ItRunsOn`). Intended for runner autoscalers ("is there queued work for labels X?"). Great for a "queue" tool: count `waiting` vs `running`, group by `runs_on`.
  - Quirk: the jobs are loaded with `FindTaskOptions` (RepoID/OwnerID/Status) so scope filtering works on action_run_job `repo_id/owner_id`; the response omits repo name; map `repo_id` -> name locally if needed.
- `/user/actions/runners*` acts on the doer's personal runners; `/orgs/{org}/...` needs org owner.

## 8. Commit statuses

### GET /repos/{owner}/{repo}/statuses/{sha}  (repoListStatuses)
### GET /repos/{owner}/{repo}/commits/{ref}/statuses  (repoListStatusesByRef)
Read:repository (Code unit reader). `{sha}` full or short sha; `{ref}` may be branch/tag/sha (resolved via `ResolveRefOrSha`, requires git repo). Query: `sort` (`oldest|recentupdate|leastupdate|leastindex|highestindex`), `state` (`pending|success|error|failure|warning`; no `skipped` listed in swagger), `page`, `limit`. Headers `Link`, `X-Total-Count`. Items: `{id (per-sha index, NOT global), status (state; JSON key is "status"), target_url, description, url, context, creator{FULL user}, created_at, updated_at (equals created_at, bug)}`. Returns EVERY historical status per context (not deduplicated); to get current state use the combined endpoint.

### GET /repos/{owner}/{repo}/commits/{ref}/status  (repoGetCombinedStatusByRef)
Latest status per context. Response `CombinedStatus`: `{state, sha, total_count (= number of statuses on THIS page), statuses[...], repository{FULL repo}, commit_url, url}`. Paginates (`page`, `limit`), `X-Total-Count` header. No statuses -> `{}`-like empty object (`state:""`, total_count 0, repository null). Compact: `{state, sha, n, checks:[{ctx, status, desc, target_url}]}` and drop `repository`, `creator`, `url`.
Forgejo Actions creates statuses with context `"<workflow name> / <job name> (<event>)"` and description like "Successful in 1m3s"/"Failing after 12s"/"Waiting to run"/"Has started running"; `target_url` = `<run link>/jobs/<jobIndex>` (the UI URL). So combined status is the cheapest "is CI green for this commit/branch/PR head" check; it also includes external statuses.

### POST /repos/{owner}/{repo}/statuses/{sha}  (repoCreateStatus)
Write on Code unit (`reqRepoWriter(Code)`), `write:repository`. Body `{state, target_url, description, context}`; `sha` must be a real commit (404 `sha` otherwise; a full sha is safest, branch names are NOT resolved here). Returns 201 CommitStatus. State valid values as above (+ `skipped`). Posting a status with the same context replaces it as "latest".

## 9. What the web UI routes offer that the API lacks

Web routes (`routers/web/web.go` ~1629-1668, handlers in `routers/web/repo/actions/`) need a browser session + CSRF; API tokens do not work on them. Missing from the API:
- List workflow files of the default branch with enabled/disabled state, `on:` triggers, dispatch inputs schema, runner-label availability warning (`actions.List`). API has no `GET .../actions/workflows`; to know available workflows/inputs you must read the YAML via the contents/raw API (`.forgejo/workflows/*.yml`, `.gitea/workflows`, `.github/workflows`) and parse `on.workflow_dispatch.inputs`.
- Enable/disable a workflow file (`POST /actions/disable|enable?workflow=`; stored in repo Actions unit config). No API.
- Approve a run that `need_approval` (fork PRs) (`ApproveRun` exists in service; no API route in this tree; web via trust settings `action-user-trust`). API exposes only the `need_approval` / `approved_by` fields; run stays `blocked`.
- Prioritize / deprioritize run (`POST /runs/{index}/prioritize|deprioritize`). No API.
- Run view JSON (`ViewPost`): log cursors for incremental per-step log streaming (`logCursors` -> `{step, cursor, lines[{index,message,timestamp}]}`), step durations/summaries, `estimatedOutcome`, `canCancel/canRerun/canApprove` flags, pre-execution errors/warnings (`PreExecutionError`, e.g. invalid workflow YAML) and `allAttempts` with `status_diagnostics`. The API exposes none of: pre-execution error text, estimated outcome, attempts list per job (only `attempt` number; but each attempt's logs are fetchable with `?attempt=N`), diagnostics why a job is `waiting` (no matching runner).
- Filter runs by actor + workflow in the list (`?actor=<id>&workflow=<file>&status=<n>`): API has workflow_id and status but not actor.
- `GET /actions/workflows/{name}/runs/latest` (redirect to latest run of a workflow, `ViewLatestWorkflowRun`) and `/runs/latest`; badge SVG. API substitute: `runs?workflow_id=ci.yml&limit=1`.
- Run via index in URL (`/runs/{index}`); API needs the id (see section 1).
- Web log download per attempt (`/runs/{r}/jobs/{j}/attempt/{a}/logs`) = full log, attachment; API logs endpoint is a superset.
- Web artifact download by NAME (`/runs/{r}/artifacts/{name_or_id}`) and delete by name; API by id only.
- No long-poll/SSE/webhook in the API; waiting for CI must be done by polling (MCP-side wait loop). Webhooks for `action_run_*` events exist as repo hooks but out of scope.

## 10. Token-sink summary (where projection pays)

1. `runs` list/get: each item embeds full repository + trigger_user + event_payload (tens of KB). Project to ~12 short fields (about 100-150 bytes/run).
2. `statuses` / `combined status`: embed `creator` (user object) and `repository`. Project.
3. Job logs: unbounded size and ~30-char timestamp prefix per line + ANSI codes. Tail by Range, filter by `q`, per-step slice, strip prefix/ANSI, dedupe repeated lines, cap output bytes with an explicit "truncated, N bytes more" hint.
4. Pagination bodies: runs/tasks need `total_count` from the body, others from `X-Total-Count`; normalise to `{items, total, page, has_more}`.
5. Runner lists on admin: remember `visible=true`.

## 11. Recommended MCP tools (Actions area)

Design rules: every list tool takes `repo` as `owner/name` string (default repo from config/git remote optional), `limit` default 10-20, returns compact JSON arrays with short keys and a trailing `{"total":N,"more":bool}`; ids always `run` = global run_id unless param is named `number`. Time as `YYYY-MM-DDTHH:MM:SSZ`; durations in seconds.

Compact shapes:
- run: `{id, n, wf, title, st, ev, ref, sha(8), by, t0, secs, url}` (`n`=index_in_repo, `wf`=workflow_id, `st`=status, `by`=trigger_user.login, `secs`=duration/1e9, `url`=html_url; add `need_approval:true` only when set).
- job: `{id, name, st, att, needs?, runs_on?}`; with `steps:[{n,name,st,secs}]` for single-job get.
- runner: `{id, name, st, labels, ver, eph, scope}`.
- status check: `{ctx, st, desc, url}`.

Tools:
1. `actions_runs` — list runs. params: `repo`, `status?` (one or many of the 8), `workflow?` (file name), `ref?` (accepts short `main` -> prefix `refs/heads/`, `#12` -> `refs/pull/12/head`, `v1` tag), `sha?`, `event?`, `number?` (run_number), `limit?`, `page?`. Out: array of run + total (from `total_count`).
2. `actions_run` — get one run + its jobs in one call. params: `repo`, `run` (id) OR `number` (resolved via run_number filter), `steps?` (bool: also fetch each non-successful job's steps). Out: run + `jobs:[job]` (+ steps of failed jobs). Combines GET run + GET jobs (+ GET job).
3. `actions_job` — get job with steps. params: `repo`, `job`. Out: job + steps with secs.
4. `actions_log` — job log reader. params: `repo`, `job`, `step?` (number or "failed" -> pick first step with status failure, requires the job call), `tail?` (lines, default 80), `grep?` (substring, optional `ignore_case`), `attempt?`, `max_bytes?` (default 6000), `since_offset?` (byte offset for incremental follow). Impl: no grep -> `GET ...logs?step=N` with `Range: bytes=-<max_bytes*2>` then drop partial first line, strip timestamps/ANSI, keep last `tail` lines; grep -> `q=`,`qi=`, then cap + `context` not available (server has no context lines; say so). Return `{text, truncated, total_bytes, next_offset}` where `total_bytes` from `Content-Range`/`Content-Length`.
5. `actions_failure` — "why did CI fail" one-shot. params: `repo`, one of `run` | `number` | `ref` | `sha` | `pr`. Flow: resolve run (latest failing for sha/ref via runs?status=failure&head_sha=), list jobs, for each failed job fetch steps, find first failed step, tail its log (last ~40 lines, stripped). Out: `{run, failed:[{job, step, tail}]}`. Replaces 4-6 round trips.
6. `actions_wait` — block (poll server-side every 3-5 s, backoff) until run reaches a done status or `timeout` (default 600 s, max cap), returning final compact run + job summary (+ failure tails if failed). Params: `repo`, `run` | `sha`/`ref` (picks newest run), `timeout?`, `interval?`. API has no push mechanism, so the loop lives in the MCP; keep poll responses internal, only return the final result (huge token saving).
7. `actions_dispatch` — params: `repo`, `workflow` (file name), `ref`, `inputs?` (map of strings), `wait?` (bool, chains to wait). Always send `return_run_info:true`; out `{run, n, jobs}`. Surface 400 messages verbatim (`input required for ...`).
8. `actions_run_op` — `op` in `cancel|rerun|delete`, params `repo`, `run`; plus `actions_job_rerun` (`repo`, `job`). Out: `{ok:true, op}` (they return 204). For delete require done status (do a pre-check GET to turn the 500 into a clear error). After rerun, optionally poll once and return the run's new status.
9. `actions_artifacts` — list artifacts; params `repo`, `run?`, `name?`, `limit?`; plus `actions_artifact_get` (download to `dest` path; returns path+size) and `actions_artifact_delete` (`id`).
10. `actions_queue` — pending/running work: params `scope` (`repo|org|user|admin`), `repo?/org?`, `labels?`. Calls `.../actions/runners/jobs`. Out: `{waiting:N, running:M, by_label:{"docker":2,...}, jobs:[compact job + repo_id]}`; truncate jobs to 20.
11. `runners_list` — params `scope` (`repo|org|user|admin`), `owner/repo` or `org`, `visible?` (default true), `status?` (client side filter), `label?` (client side). Out: compact runners + total. Notes in tool description: admin needs `visible=true`.
12. `runner_get` (same scope args + `id`), `runner_register` (`scope`, `name`, `description?`, `ephemeral?`; returns `{id,uuid,token}` once and warns to store it; never log), `runner_delete` (`scope`, `id`), `runner_reg_token` (deprecated GET; fall back only if register is not wanted).
13. `actions_vars` — `op` in `list|get|set|delete` for variables, params `scope` (`repo|org|user`), target, `name?`, `value?`. `set` does GET then POST (create) or PUT (update) to hide the 409/404 split. `actions_secrets` — `op` in `list|set|delete` (no get; values are write-only); `set` uses PUT `{data}`. Never echo secret `data`.
14. `commit_status` — params `repo`, `ref` (branch/tag/sha/`pr:N`), `detail?` (bool). Calls combined status; out `{state, sha, n, checks:[{ctx,st,desc,url}]}` (checks listed only if not all success, unless `detail`). Optionally merges in run summary (`runs?head_sha=`) when `detail`.
15. `commit_status_set` — params `repo`, `sha` (full), `state`, `context`, `description?`, `target_url?`; returns `{ok, id}`. `commit_statuses` (history) only behind `detail`; rarely needed.
16. (gap-fill, optional) `workflows_list` — no API; implement by listing `.forgejo/workflows`, `.gitea/workflows`, `.github/workflows` via the contents API at a ref and parsing `on:` and `workflow_dispatch.inputs` (name, description, type, required, default, options). Needed so Claude can build correct `inputs` before dispatch. Cache per (repo, ref sha).

Tool-definition size: group related ops behind `op` enums (items 8, 13, 9, 12) and keep descriptions to one line each plus enumerated params; items 1-7, 10, 14 are the high-use ones and should be the always-visible core; runners/vars/secrets/artifacts can be one `actions_admin`-style tool with `kind` + `op` if the definition budget matters.

Error handling hints for the MCP layer:
- 404 on `/logs` with message `job has not been executed yet` / `logs have expired` -> return as normal text results, not errors.
- 204 responses have no body; map to `{ok:true}`.
- Cancel is idempotent no-op on finished runs (do not claim "cancelled" unless a follow-up GET shows `cancelled`).
- Do not follow `archive_download_url`/`html_url` redirects; build URLs from `base + path`.
- Registration token / runner token / secret values: never include in logs or tool echo beyond the one required return.
