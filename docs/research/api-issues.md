# Forgejo v17.0.2 API: issues and collaboration

Source: routers/api/v1/{api.go, repo/issue*.go, repo/label.go, repo/milestone.go, notify/*.go, permissions/*}, services/convert/{issue,issue_comment,notification,attachment}.go, modules/structs/**. Verified in handler code; swagger used only for param enums. `models/issues` and `modules/setting` are NOT in the sparse checkout, so a few model-level semantics are marked (unverified).

## 0. Cross-cutting facts

**Auth / scopes.** Issue routes sit under `tokenRequiresScopes(Issue)`, notification routes under `tokenRequiresScopes(Notification)`. Token scope strings: `read:issue`, `write:issue`, `read:notification`, `write:notification` (`all` covers everything; write implies read). Required level is derived from the HTTP method: GET/HEAD = read, POST/PUT/PATCH/DELETE = write. Also `checkTokenPublicOnly()`: a `public-only` token gets 404 on private repos. Anonymous reads are allowed on public repos; every mutation has `reqToken()`.

**Issues and PRs share the index space.** `/issues/{index}` is keyed by `GetIssueByIndex(repoID, index)`, so every issue endpoint also works for a PR index (title/body/state/labels/assignees/milestone/comments/reactions/time/subscriptions/lock/pin/deps/attachments). Distinguish by `pull_request != null` in the response, or filter with `type=issues|pulls`. PR-only things (merge, reviews, review comments, head/base) are in the pulls area. Gate `mustEnableIssuesOrPulls` (needs issues unit OR pulls unit readable) plus `mustEnableLocalIssuesIfIsIssue` (if the Issues unit is off, only PR indexes work, plain issues return 404; note an external tracker also disables these).

**Pagination.** `page` (1-based) and `limit`. `limit<=0` gives `API.DefaultPagingNum` (app.ini default 30), capped at `API.MaxResponseItems` (default 50), via `convert.ToCorrectPageSize`. Real values for a given server: `GET /settings/api` (max_response_items, default_paging_num). (Defaults unverified in the sparse tree.)
- `X-Total-Count` is set on: issues list/search, repo comments list, issue comments list, labels, milestones, notifications, reactions (issue), subscribers, tracked times, stopwatches.
- `Link` header (`rel=next/last/first/prev`) is set ONLY on `GET /repos/{o}/{r}/issues` and `GET /repos/issues/search`. Everywhere else compute `has_more = page*limit < total`.
- NO pagination at all (returns everything): `GET /issues/{i}/comments`, `GET /issues/comments/{id}/reactions`, `GET /issues/{i}/labels`, `GET /issues/{i}/assets`, `GET /issues/pinned`.
- No total header: `/issues/{i}/dependencies`, `/issues/{i}/blocks`, `/issues/{i}/timeline` (sets X-Total-Count = number of items on THIS page after filtering, not the real total).

**Time filters.** `since` / `before`: RFC 3339 only (`2026-11-22T10:00:00Z`); parse error is a 422. They filter by *updated* time (issues) / comment update time (comments). Zero/blank means no filter.

**Status code quirks (do not branch on 200 vs 201 vs 204).**
- `PATCH /issues/{i}` returns **201** with the issue. `POST/DELETE /dependencies` and `/blocks` return **201** with the issue even on DELETE. `POST /deadline` 201. `POST /stopwatch/start|stop` 201 with an EMPTY body. `PUT /subscriptions/{u}` 201 when changed, 200 when already in that state, both empty. `PATCH /issues/{i}/assets/{id}` 201.
- `PUT/PATCH notifications` return **205 Reset Content** with a JSON body (some HTTP clients drop 205 bodies, so do not depend on it; Node `fetch` does hand the body over, but check).
- `GET /issues/comments/{id}` for a non-plain comment (system/review event) returns **204 empty**, and `PATCH` of such a comment returns 204 silently (no edit).
- Lock/unlock when already in that state is **409**. Comment/issue delete: 204. Reactions DELETE: 200 empty.
- Errors are `{"message": "...", "url": "..."}`. Validation failure: 422. Missing/invisible: 404 (also used for "feature disabled" and "no permission to see").
- `convert.toIssue` returns an **empty `{}` issue** (all zero fields) if any sub-load fails; guard for `id==0`. `ToTimelineComment` may return `nil` -> `null` items in the timeline array (filter them).

**Response weight (the token problem).** Every issue embeds: `user` (full User, ~25 fields incl. avatar_url/email/followers_count), `assignee` + `assignees[]` (full Users), `labels[]` (id,name,exclusive,is_archived,color,description,url), `milestone` (full), `repository{id,name,owner,full_name}`, `assets[]`, `url`, `html_url`, `original_author*`, `ref`, `pin_order`, plus the full `body`. A comment embeds a full User plus `html_url`, `pull_request_url`, `issue_url`, `assets[]`. A notification thread embeds a FULL Repository object (50+ fields) per thread. All of these must be projected down.

**Label identifiers.** Label ids are int64; colors are returned WITHOUT `#` (`ToLabel` trims it); input color accepts with or without `#` (`label.NormalizeColor`, 3 or 6 hex).

## 1. Issues

### 1.1 `GET /repos/{owner}/{repo}/issues` (issueListIssues) - scope read:issue
Query:
- `state` open|closed|all (default **open**; any other value -> open).
- `type` issues|pulls (default: both; auto-narrowed to what the viewer can read; 404 if requested type unreadable).
- `q` keyword (issue indexer: title/body/comments; null bytes dropped).
- `labels` comma list. Handler resolves **names** via `GetLabelIDsInRepoByNames` (+ org labels if owner is an org). Special values per code: a single id `0` => "no label" (`NoLabelOnly`); negative ids become *excluded* labels (`ExcludedLabelIDs`), positive are *included* (AND semantics: issue must have all included - unverified in models, indexer `IncludedLabelIDs`). Practical: pass names; a numeric string is treated as name first (verify against server). Unknown names are silently dropped (so a typo widens the result!).
- `milestones` comma list of names, falling back to numeric ids; unknown discarded (typo widens result). A lone resolved id equal to `db.NoConditionID` => no-milestone filter.
- `created_by`, `assigned_by`, `mentioned_by`: usernames (404 if user missing). Note naming: `assigned_by` means *assignee*.
- `since`, `before`: RFC 3339, updated time.
- `sort` relevance|latest(default)|oldest|recentupdate|leastupdate|mostcomment|leastcomment|nearduedate|farduedate.
- `page`, `limit`.
Response: `Issue[]` (full), headers `X-Total-Count`, `Link`. No `milestone`-state/project/`closed_by` filter exists. No `assignee=none` filter exists.
Keep: `number, title, state, user.login, assignees[].login, labels[].name, milestone.title, comments, updated_at, due_date, is_pr (= pull_request!=null), pull_request.merged, pull_request.draft, html_url`. Body only on demand, truncated.

### 1.2 `GET /repos/issues/search` (issueSearchIssues) - read:issue
Cross-repo search over every repo the token can see. Query: `state` (default open), `labels` (ANY-of names, `IncludedAnyLabelIDs`; unknown discarded), `milestones` (names, any-of), `q`, `priority_repo_id`, `type` issues|pulls, `since`, `before`, `assigned`, `created`, `mentioned`, `review_requested`, `reviewed` (booleans relative to the token's user; ignored if unauthenticated; parsed with ParseBool or "on"), `owner` (user/org name; 400 if unknown), `team` (requires `owner`), `page`, `limit`, `sort` (same enum, default latest).
Quirks: there is no `repo` filter (only `owner`, or filter client-side by `repository.full_name`); default page size here is `setting.UI.IssuePagingNum` (default 20), NOT API.DefaultPagingNum; cap is MaxResponseItems. `labels` and `milestones` here are any-of while the per-repo list is all-of. Headers: Link + X-Total-Count. Same compact projection as 1.1 plus `repository.full_name`.

### 1.3 `GET /repos/{o}/{r}/issues/{index}` (issueGetIssue) - read:issue
Full Issue (see weight above). 404 if unreadable for that kind. Keep: `number, title, body, state, is_locked, user.login, assignees[].login, labels[].name, milestone{id,title,state}, comments, created_at, updated_at, closed_at, due_date, ref, pull_request{merged,draft}, assets[]{id,name,size}`.

### 1.4 `POST /repos/{o}/{r}/issues` (issueCreateIssue) - write:issue
Body `CreateIssueOption`: `title` (required), `body`, `ref` (branch/tag ref string), `assignee` (deprecated), `assignees: string[]` (logins), `due_date` (RFC 3339, date part only; stored as 23:59:59), `milestone: int64 ID` (NOT a name), `labels: int64[]` (IDs only, NOT names), `closed: bool`.
Quirks: `reqRepoReader(Issues)` only: a non-writer's `labels`, `milestone`, `assignees`, `due_date` are **silently dropped**. Assignee must be assignable else 422. Blocked-by-user 403. `closed:true` closes right after create (412 if deps open). MCP must resolve label names -> ids (cache `GET /labels`) and milestone title -> id. Returns 201 + Issue. Archived repo: 403 (`mustNotBeArchived`).

### 1.5 `PATCH /repos/{o}/{r}/issues/{index}` (issueEditIssue) - write:issue
Body `EditIssueOption` (all optional): `title` (empty string = unchanged), `body` (pointer; `""` clears), `ref`, `assignee` (deprecated), `assignees: string[]` (**replaces** the set; `[]` clears), `milestone: int64` (`0` removes; ID not name), `state` open|closed, `due_date`, `unset_due_date: bool` (removing needs this flag; `due_date` without it sets; empty `due_date` without flag = 400), `updated_at` (owner/admin only).
Quirks: **labels cannot be changed here** (use 1.9). Permission: the poster of an unlocked issue may edit title/body/ref/state; everything else needs write. `hackEditIssue` silently nils assignee/assignees/deadline for non-writers instead of erroring. State change on a **merged PR returns 412**; closing with open dependencies returns 412 `DependenciesLeft`. Returns **201**. Body edit uses `issue.ContentVersion` (optimistic lock, 400 if raced; retry). On PRs this is how you close/reopen a PR and retitle (WIP prefix semantic is title-based).

### 1.6 `DELETE /repos/{o}/{r}/issues/{index}` - write:issue, repo **admin**
Hard delete, 204.

### 1.7 Subresource endpoints

| Endpoint | Notes |
|---|---|
| `GET /repos/{o}/{r}/issues/pinned` (repoListPinnedIssues) | Issues only (PRs: `/pulls/pinned`). No pagination. |
| `POST/DELETE /issues/{i}/pin`, `PATCH /issues/{i}/pin/{position}` | repo **admin** only, 204. Pin limit (default 3, `GET /repos/{o}/{r}/new_pin_allowed` -> `{issues,pull_requests}` bool). |
| `PUT /issues/{i}/lock` body `{reason}` (**required**, string; typical: off-topic, too heated, resolved, spam but free-form) | write on repo. 204; 409 if already locked. `DELETE` unlocks, 409 if not locked. No archived check. |
| `POST /issues/{i}/deadline` body `{due_date}` (null clears) | 201, `{due_date}`. Writer only (403). Uses server-local TZ for 23:59:59. Redundant with PATCH. |
| `GET /issues/{i}/subscriptions` | Users watching (list of full Users, paged, X-Total-Count). |
| `GET /issues/{i}/subscriptions/check` | `{subscribed, ignored, ...}`; **`ignored = !subscribed`** (not a real ignore state). |
| `PUT/DELETE /issues/{i}/subscriptions/{user}` | self or site-admin only (403 otherwise). 200 no-change / 201 changed, empty body. |
| `GET/POST/DELETE /issues/{i}/reactions` | See 1.10. |
| `GET /issues/{i}/assets`, `POST` (multipart), `GET/PATCH/DELETE /assets/{attachment_id}` | See 1.11. |

### 1.8 Comments
- `GET /repos/{o}/{r}/issues/{index}/comments` (issueGetComments): plain comments only (type Comment; excludes system events and PR review/code comments). Query `since`, `before`. **No page/limit, returns all.** `X-Total-Count` set. Item: `{id, html_url, pull_request_url, issue_url, user, original_author, original_author_id, body, assets[], created_at, updated_at}`. Keep: `id, user.login, body, created_at, updated_at, assets` (omit urls; they are derivable). For long threads the MCP must window client-side (`last_n`, `since`, `from_id`) and truncate bodies.
- `GET /repos/{o}/{r}/issues/comments` (issueGetRepoComments): all comments in the repo (issues+PRs the viewer can read), query `since,before,page,limit`, X-Total-Count. Item has `issue_url`/`pull_request_url`; derive issue number from the URL tail (`.../issues/12`). Good for "what's new" polling.
- `GET /issues/comments/{id}`: 204 for non-comment types. Only repos' own comments (`reqValidCommentID`: comment's issue must belong to this repo, else 404).
- `POST /issues/{index}/comments` body `{body (required), updated_at?}` -> 201 Comment. `reqIssueUnlockedOrCanWrite`: 403 on locked issue without write; `mustNotBeArchived`; blocked-by-user 403. Works on PRs (conversation comment, not a review comment).
- `PATCH /issues/comments/{id}` body `{body (required)}` -> 200 Comment. Allowed for the author or repo writer. Non-content comment types: 204 and no change. Deprecated twin `PATCH /issues/{index}/comments/{id}` ignores `index`.
- `DELETE /issues/comments/{id}` -> 204 (author or writer; returns 204 even if the comment is a non-plain type and nothing was deleted).
- `GET /issues/comments/{id}/assets` etc.: comment attachments, same shape as issue assets (multipart upload `attachment`, `name`; PATCH `{name}`).
- Comment reactions: `GET/POST/DELETE /issues/comments/{id}/reactions` body `{content}`; GET unpaginated.

### 1.9 Labels on an issue
- `GET /issues/{i}/labels` -> Label[] (unpaginated).
- `POST /issues/{i}/labels` body `{labels: [...], updated_at?}`: adds. `labels` is `any[]`: **all strings (names) OR all integers (ids), never mixed** (400). Names are resolved against repo labels and, if the owner is an org, org labels; unknown names are silently ignored (no error!). Returns 200 + the issue's full resulting label list. Exclusive-label scopes (`scope/name` with exclusive scope) swap existing label of the same scope.
- `PUT /issues/{i}/labels`: replaces, same body/resolution; 200 + list. `labels: []` clears via PUT as well.
- `DELETE /issues/{i}/labels/{identifier}`: identifier = name first, then org label name, then numeric id fallback; unknown = 422. Optional JSON body `{updated_at}`. **204** (200+list only if client sends a GitHub-style Accept). 
- `DELETE /issues/{i}/labels` -> clears all, 204.
- All need write on issues/pulls (403 empty body otherwise).
Because names work here, prefer `POST/PUT labels` with names for the "set labels" tool instead of resolving ids; only creation (1.4) needs ids.

### 1.10 Reactions (issue)
`GET /issues/{i}/reactions` (page, limit, X-Total-Count) -> `[{user, content, created_at}]` (keep `user.login, content`). `POST` body `{content}` -> 201 (200 if already exists), `DELETE` body `{content}` -> 200 empty. Allowed set is `[ui].REACTIONS` (default `+1, -1, laugh, hooray, confused, heart, rocket, eyes`; unverified); disallowed -> 403. Locked issues need write. GET gives 403 (not 404) when unreadable.

### 1.11 Attachments (issues and comments)
`GET /issues/{i}/assets` -> `Attachment[]` = `{id, name, size, download_count, created_at, uuid, browser_download_url, type: "attachment"|"external"}`. `POST` multipart: `attachment` (file), optional `name` query, optional `updated_at`; 201; quota enforced (`quotaExceeded` 413); file type allow-list 422; allowed to poster, repo admin, site admin or writer. Upload also rewrites the issue content (`ChangeContent`), bumping updated time. `PATCH` only renames (`{name}`), returns 201. `DELETE` 204. `mustEnableAttachments` gate (404 if disabled). Download: `browser_download_url` (`/attachments/{uuid}` on the web host; needs auth for private repos via token header). The MCP should offer upload by local path and download-to-path, never inline binaries.

### 1.12 Dependencies / blocks
- `GET /issues/{i}/dependencies`: issues that **block** this one (this "depends on" them). `GET /issues/{i}/blocks`: issues blocked **by** this one. Both page/limit but no total header; `blocks` ignores `limit<=1` (treated as default) and paginates in memory. Dependencies disabled for the repo -> 404 on `/dependencies` (blocks list works anyway). Cross-repo entries the viewer cannot read appear as `{title:"HIDDEN"}` (or a stripped record for writers).
- `POST/DELETE /issues/{i}/dependencies` body `IssueMeta {index:int, owner:str, repo:str}` (all required): make **{i} depend on** the body issue. `POST/DELETE /issues/{i}/blocks`: make {i} **block** the body issue. Both: 201 + the *path* issue; 404 when deps disabled, repo archived, or no write on target; cross-repo only if `service.ALLOW_CROSS_REPOSITORY_DEPENDENCIES` (default true) else 400. Direction is confusing: encode explicitly in the tool (`blocked_by` vs `blocks`).

### 1.13 Timeline
`GET /issues/{i}/timeline` (issueGetCommentsAndTimeline): `since, before, page, limit`. Items `TimelineComment`: `id, type, user, body, created_at, updated_at, html_url` + type-specific: `label{name,color}`, `old_milestone/milestone`, `assignee`/`assignee_team` + `removed_assignee`, `old_title/new_title`, `old_ref/new_ref`, `ref_issue` (FULL Issue), `ref_comment`, `ref_action`, `ref_commit_sha`, `review_id`, `resolve_doer`, `dependent_issue` (FULL Issue), `tracked_time`, `project_id`. `type` strings (Gitea comment types): comment, reopen, close, issue_ref, commit_ref, comment_ref, pull_ref, label, milestone, assignees, change_title, delete_branch, start_tracking, stop_tracking, add_time_manual, cancel_tracking, added_deadline, modified_deadline, removed_deadline, add_dependency, remove_dependency, review, lock, unlock, change_target_branch, delete_time_manual, review_request, merge_pull, pull_push, project, project_board, dismiss_review, change_issue_ref, pull_scheduled_merge, pull_cancel_scheduled_merge, pin, unpin (list not verifiable here; models absent). `type=code` items are dropped after pagination, so pages can be short or empty while more exist; also unreadable cross-repo xrefs removed. Total header = page item count. Compact event: `{id, type, user, created_at, body?, label?, old->new?, ref? "owner/repo#N", assignee?}`. For PRs this is the way to see review submissions (type `review`, body = review summary), merges, pushes (`pull_push`).

### 1.14 Time tracking and stopwatch
Requires the repo's timetracker unit enabled (else 400/404). Seconds are the unit.
- `GET /issues/{i}/times` query `user`, `since`, `before`, `page`, `limit`; X-Total-Count. Non-writers only see their own (403 if `user` is somebody else). Item `{id, created, time(sec), user_id, user_name, issue_id, issue(full)}`; keep `id,user_name,time,created`.
- `POST /issues/{i}/times` body `{time: int64 sec (required), created?: RFC 3339, user_name?}` -> 200 TrackedTime. `user_name` honored only for repo admin (other than self) or site admin; otherwise silently the caller.
- `DELETE /issues/{i}/times` resets (deletes) **only the caller's** times, 204. `DELETE /issues/{i}/times/{id}` only owner of entry or site admin (403), 204.
- Repo-wide: `GET /repos/{o}/{r}/times` (read-only list, `user,since,before,page,limit`), `GET /user/times` (caller's, `since/before`), `GET /user/stopwatches` (`StopWatch{created, seconds, duration, issue_index, issue_title, repo_owner_name, repo_name}`, page/limit/total).
- Stopwatch: `POST /issues/{i}/stopwatch/start` (409 if one already running for caller), `POST .../stop` (409 if none; adds tracked time), `DELETE .../stopwatch/delete` (409 if none; discards). Needs write on issues + `CanUseTimetracker`. Empty bodies (201, 201, 204).

## 2. Labels

All repo label routes: read = anyone who can read the repo; write needs token + `reqRepoWriter(Issues, PullRequests)` (write on issues OR pulls). Scope read:issue / write:issue.
- `GET /repos/{o}/{r}/labels`: query `sort` mostissues|leastissues|reversealphabetically (default alphabetical), `page`, `limit`; X-Total-Count. Returns **repo-level labels only**; org labels are NOT included (use `GET /orgs/{org}/labels`, same CRUD under `/orgs/{org}/labels[/{id}]`, org owner required for writes). Keep `id,name,color,description,exclusive,is_archived`.
- `GET /repos/{o}/{r}/labels/{id}`: `{id}` may be numeric id OR the label name (code tries ParseInt, else name). Edit/delete take numeric id only (`ParamsInt64`).
- `POST /labels` body `{name (required), color (required; hex with or without #), description, exclusive, is_archived}` -> 201. Name with `/` scope + `exclusive:true` creates a mutually-exclusive scope.
- `PATCH /labels/{id}` all optional pointers, BUT `is_archived` is **reset to false when omitted** (`SetArchived(form.IsArchived != nil && *form.IsArchived)`): always send the current `is_archived` or fetch first. 200.
- `DELETE /labels/{id}` 204 (also detaches from all issues).

## 3. Milestones

Same permission pattern as labels. `{id}` for GET/PATCH/DELETE accepts numeric id **or the milestone title** (`getMilestoneByIDOrName`: id first, fallback to name). Issue create/edit takes the numeric ID only.
- `GET /milestones`: `state` open|closed|all, `name`, `page`, `limit`, X-Total-Count. Swagger says default open, but code applies a filter only for explicit `open`/`closed`; **omitted state returns ALL milestones**. Always pass state.
- `POST` `{title, description, due_on (RFC 3339), state}` -> 201. Omitted `due_on` stored as 9999-12-31 and rendered as null. Title duplicates allowed? (unverified, model layer).
- `PATCH` `{title (empty=unchanged), description, state open|closed, due_on}`; `due_on` zero value is ignored (cannot clear a deadline via API). 200.
- `DELETE` 204. Issues are detached (not deleted).
- Item: `{id, title, description, state, open_issues, closed_issues, created_at, updated_at, closed_at, due_on}`. Keep: `id,title,state,open_issues,closed_issues,due_on,description?`.

## 4. Notifications (scope read/write:notification)

Per-user inbox; `reqToken`.
- `GET /notifications` (notifyGetList), `GET /repos/{o}/{r}/notifications` (same, repo-scoped). Query: `all` (bool, include read; default false), `status-types` repeated (`unread`, `read`, `pinned`; default **unread+pinned**; ignored when `all=true`), `subject-type` repeated (`issue`, `pull`, `repository`; `commit` is not supported), `since`, `before` (RFC 3339, on updated time), `page`, `limit`. `X-Total-Count`, no Link.
  Item `NotificationThread {id, repository(FULL repo object), subject{title, url, latest_comment_url, html_url, latest_comment_html_url, type: Issue|Pull|Commit|Repository, state: open|closed|merged}, unread, pinned, updated_at, url}`. `subject.url` ends with `/repos/{o}/{r}/issues/{n}`; the issue number is parsed from there. Keep: `id, repo (repository.full_name), type, number, title, state, unread, pinned, updated_at, latest_comment_html_url`. The embedded repo makes each row 2-5 KB raw.
- `GET /notifications/new` -> `{new: int}`: unread count, cheap polling endpoint.
- `GET /notifications/threads/{id}`: one thread (self or site admin; else 403).
- `PATCH /notifications/threads/{id}?to-status=read|unread|pinned` (default read): **query parameter**, no body. Returns **205** + thread.
- `PUT /notifications` and `PUT /repos/{o}/{r}/notifications`: bulk. Query `last_read_at` (RFC 3339; only notifications updated BEFORE it are touched; absent = all), `all`, `status-types` (default **unread** only; the inverse default compared with GET), `to-status` (default read). Returns **205** + list of changed threads (can be large: for "mark all read" the MCP should return only the count). Without `last_read_at`, anything arriving between list and mark is also marked, so pass `last_read_at` = listing time.
- Notifications are only generated for issue/PR activity the user watches or participates in; this is the substitute for polling CI on PRs only for review/comment events (CI status is elsewhere).

## 5. Related lookups needed by tools
- `GET /repos/{o}/{r}/assignees` -> assignable users (validate assignees; keep `login`).
- `GET /repos/{o}/{r}/issue_templates` (Issue templates `{name,title,about,labels,ref,content,body[]fields,file_name}`), `GET /issue_config`: use when creating issues in repos with required templates (blank issues may be disabled in config; API create still works, it does not enforce the template).
- `GET /orgs/{org}/labels` for org-level labels.

## 6. Gotcha summary (for the implementer)
1. Create takes label/milestone **IDs**, label add/replace/remove and list filters take **names**; milestone path accepts id-or-title. Build a cached name->id resolver per repo (TTL ~60s, invalidate on label/milestone mutation), error on unknown names client-side (the server silently ignores unknown names everywhere, which also widens list filters).
2. Edit label resets `is_archived`; edit milestone cannot clear due date; edit issue cannot touch labels.
3. Pagination inconsistent: comments, issue labels, reactions on comments, pinned have none; only issues list/search give `Link`.
4. 201/205/204 mix; empty bodies on stopwatch/subscription/lock; treat any 2xx as success and re-fetch minimal state if needed.
5. Locked issues: non-writers get 403 on comment/react; lock needs a reason.
6. Archived repos: most mutations 403/repoArchivedError (not lock/unlock, label add, dependencies partially).
7. Issue list `labels`/`milestones` unknown value = silently ignored (more results than expected).
8. Search endpoint page size default 20 (UI value), list endpoint 30.
9. Search `assigned/created/mentioned/review_requested/reviewed` are relative to the token's user and silently ignored for anonymous.
10. `ToAPIIssue` failure yields an empty object; timeline may contain `null`.

## 7. Recommended MCP tools (area: issues & collaboration)

Design rules: (a) every tool takes `repo` as `"owner/name"` string with default repo configurable; (b) output is a compact text/JSON, omitted-null fields, logins instead of user objects, short ISO dates (`2026-11-22T10:00Z`) or relative age, bodies truncated to `body_max` (default 600 chars, `full:true` to lift); (c) lists return `{items, total, page, has_more}`, with default `limit` 20; (d) list/get tools mark `kind` as `issue`/`pr` since indexes are shared; (e) mutations return only `{ok, number, url}` plus changed fields, not the full object; (f) names are accepted everywhere and resolved by the server layer (labels, milestone, assignees).

Short descriptions (<= 1 sentence) to keep tool-definition tokens small.

| Tool | Params | Maps to | Compact output |
|---|---|---|---|
| `issue_list` | `repo?`, `state=open`, `type=issues\|pulls\|all`, `q`, `labels[]`, `milestone`, `assignee`, `author`, `mentioned`, `since`, `sort`, `page`, `limit=20` | 1.1 | `[{n, kind, title, state, by, assignees, labels, milestone, comments, updated}]` (+ `merged/draft` for PRs) |
| `issue_search` | `q`, `owner?`, `state`, `type`, `labels[]`, flags `assigned\|created\|mentioned\|review_requested\|reviewed`, `since`, `sort`, `page`, `limit` | 1.2 | same rows + `repo` |
| `issue_get` | `repo?`, `number`, `body_max?`, `full?`, `include=[comments, timeline]` | 1.3 (+1.8/1.13 when asked) | `{n, kind, title, state, by, assignees, labels, milestone, due, locked, body, comments_count, created, updated, closed, attachments[{id,name,size}], pr:{merged,draft}}` |
| `issue_create` | `repo?`, `title`, `body?`, `labels[]` (names), `milestone` (title/id), `assignees[]`, `due`, `ref` | 1.4, resolver | `{number, url}` |
| `issue_update` | `repo?`, `number`, any of `title, body, state, assignees[] (replace), milestone (title/id/null), due (date/null), ref`; plus `add_labels[]`, `remove_labels[]`, `set_labels[]` | 1.5 + 1.9 | `{number, changed:[...], url}` (one call fans out to PATCH + label calls) |
| `issue_comment` | `repo?`, `number`, `body` | 1.8 POST | `{comment_id, url}` |
| `comment_edit` / `comment_delete` | `repo?`, `comment_id`, `body` | 1.8 | `{ok}` |
| `issue_comments` | `repo?`, `number`, `last?` (n newest), `since?`, `from_id?`, `body_max?` | 1.8 GET (+client window) | `[{id, by, at, body}]`; server returns all, MCP slices/truncates |
| `issue_timeline` | `repo?`, `number`, `types?[]` (e.g. `review, merge_pull, label, close`), `page`, `limit=30` | 1.13 | `[{id, type, by, at, summary}]`; drops nulls/code, summary built per type |
| `issue_delete` | `repo?`, `number` | 1.6 | `{ok}` (admin only, flagged destructive) |
| `issue_lock` | `repo?`, `number`, `locked:bool`, `reason?` | 1.7 | `{ok, locked}` (treat 409 as no-op) |
| `issue_link` | `repo?`, `number`, `action=add\|remove`, `relation=blocked_by\|blocks`, `other="owner/repo#N"` | 1.12 | `{ok}` |
| `issue_links` | `repo?`, `number` | 1.12 GETs (2 calls) | `{blocked_by:[{ref,title,state}], blocks:[...]}` |
| `issue_react` | `repo?`, `number` or `comment_id`, `content`, `remove?` | 1.10 | `{ok}` |
| `issue_pin` | `repo?`, `number`, `action=pin\|unpin\|move`, `position?` | 1.7 | `{ok}` |
| `issue_subscribe` | `repo?`, `number`, `subscribe:bool`, `user?=self` | 1.7 | `{subscribed}` |
| `issue_attachment` | `repo?`, `number` or `comment_id`, `action=list\|upload\|delete\|rename\|download`, `path?`, `id?`, `name?` | 1.11 | list `[{id,name,size,url}]`; upload `{id,name}`; download saves to disk and returns path |
| `time_track` | `repo?`, `number`, `action=add\|list\|delete\|reset\|start\|stop\|cancel`, `seconds?`, `id?` | 1.14 | list `[{id,user,seconds,at}]` + total; others `{ok}` |
| `label_list` | `repo?`, `org?`, `sort?`, `page`, `limit=50` | 2 | `[{id,name,color,desc,excl,archived}]` |
| `label_manage` | `repo?`, `action=create\|update\|delete`, `name`, `new_name?`, `color?`, `description?`, `exclusive?`, `archived?` | 2 (reads current `is_archived` before PATCH) | `{id,name}` |
| `milestone_list` | `repo?`, `state=open` (always sent), `name?`, `page`, `limit` | 3 | `[{id,title,state,open,closed,due}]` |
| `milestone_manage` | `repo?`, `action=create\|update\|close\|reopen\|delete`, `title or id`, `description?`, `due?` | 3 | `{id,title,state}` |
| `notifications` | `repo?`, `status=unread\|read\|pinned\|all`, `types?[]`, `since?`, `page`, `limit=20` | 4 GET | `[{id, repo, kind, n, title, state, unread, updated, url}]`; `total` |
| `notifications_count` | none | `/notifications/new` | `{unread}` |
| `notifications_mark` | `thread_id?` or `repo?`/all, `to=read\|unread\|pinned`, `before=now` | 4 PATCH/PUT | `{marked: count}` (never echo list) |
| `assignable_users` | `repo?` | 5 | `["login", ...]` |
| `issue_templates` | `repo?` | 5 | `[{name, title, labels, fields:[id,type,required]}]` |

Implementation notes for the compact projector: derive `n` from `number`; `kind = pull_request ? "pr" : "issue"`; `by = user.login`; dates trimmed to minute precision; drop `url/html_url` except where an action needs it (the web URL is reconstructable as `{base}/{owner}/{repo}/issues/{n}` or `/pulls/{n}`); comment index from `issue_url`; for notifications parse number from `subject.url`. Ship `fields` / `body_max` parameters only on the 3-4 heaviest tools to keep schema tokens low, and use a shared enum-light param naming across tools.
