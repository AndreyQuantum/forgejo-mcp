# Forgejo v17.0.2 API: pull requests (research for token-lean MCP)

Source files read: `routers/api/v1/api.go` (routes ~L1020-1062), `routers/api/v1/repo/pull.go`, `pull_review.go`, `issue_pin.go`, `status.go`, `commits.go`, `services/convert/{pull,pull_review,status,convert}.go`, `modules/structs/{pull,pull_review,commit_status,repo_commit}.go`, `services/forms/repo_form.go`.
NOT in the sparse checkout (items marked [unverified] come from memory of Forgejo/Gitea, not from code here): `models/issues/**`, `services/pull/**`, `models/*` error types, `modules/setting`.
Base: `{origin}/api/v1`. Auth header: `Authorization: token <PAT>`. Placeholders: `{o}` owner, `{r}` repo, `{n}` PR index (the per-repo number, NOT the DB `id`).

## 0. Cross-cutting

**Middleware for the whole `/repos/{o}/{r}/pulls` group:** `mustAllowPulls()` (404 if PR unit disabled / no read on PR unit), `reqRepoReader(TypeCode)`, `ReferencesGitRepo()`. The whole `/repos/{o}/{r}` tree sits in `tokenRequiresScopes(Repository)`, so:
- GET -> `read:repository`; POST/PATCH/PUT/DELETE -> `write:repository` (`requiredScopeLevel`, api.go L295). No `issue` scope needed for PR endpoints (PR conversation comments under `/issues/{n}/comments` need `write:issue`).
- `reqToken()` on every write; reads work anonymously on public repos.
- `mustNotBeArchived()` only on create, merge, cancel-automerge -> 423 on archived repo (edit, update, reviews do not have it).
- `EnforceQuotaAPI(git all)` on merge and update -> 413.
- `checkTokenPublicOnly()` applies to public-only tokens.

**Errors.** `ctx.Error(status, title, obj)` emits `{"message": <obj text>, "url": "<swagger url>"}`; **`title` is only logged**. So `ctx.Error(405,"PR already merged","")` yields an EMPTY message. 500 message is blanked in prod for non-admins. `ctx.NotFound()` -> 404 with generic message. `bind(...)` failures -> 422 `{message,url}`. MCP should surface status + `message`, and map empty messages to a canned reason per code (see merge table).

**Pagination.** `utils.GetListOptions`: `page` (1-based), `limit`; `ToCorrectPageSize`: `limit<=0 -> setting.API.DefaultPagingNum`, `>MaxResponseItems -> MaxResponseItems` (defaults 30 / 50 [unverified]; instance configurable; read `GET /api/v1/settings/api` once). Headers: `X-Total-Count` + `Link` (rel next/last/first/prev; only when >1 page) on list PRs, commits, files; commits/files additionally `X-Page`, `X-PerPage`, `X-PageCount`, `X-HasMore`. Reviews list: **only `X-Total-Count`, no `Link`**. Review comments, pinned PRs, requested reviewers: no pagination.

**PR JSON bloat (the biggest token cost).** `convert.ToAPIPullRequest` embeds the FULL `Repository` object (owner, permissions, urls, ~60 fields) in both `base.repo` and `head.repo`, plus full `User` objects (avatar_url, email, language, ...) for `user`, `assignee(s)`, `requested_reviewers`, `merged_by`, and full `Team` objects. One PR is ~4-6 KB; a list of 30 is ~150 KB. The MCP MUST project; there is no `fields`/`expand` parameter anywhere. Every PR in a list triggers git operations (branch lookup, shortstat), so big `limit` is slow. **List items can be `null`** when conversion failed (`ToAPIPullRequest` returns nil on any load/git error): filter nulls.

### Compact PR projection (recommended)
```
{ n: number, title, state: open|closed, draft, merged (bool), mergeable (bool),
  author: user.login, base: base.ref, head: head.label, base_sha, head_sha,
  head_repo: head.repo.full_name  (only if != base repo -> fork PR),
  labels: [label.name], milestone: milestone.title, assignees: [login],
  reviewers: [requested_reviewers[].login + team names],
  comments, review_comments, additions, deletions, changed_files,
  created_at, updated_at, closed_at?, merged_at?, merged_by?, merge_commit_sha?, due_date?,
  html_url, body (get only; truncate) }
```
Drop: `id`, `url`, `diff_url`, `patch_url`, `merge_base`, `flow` (0 github-flow, 1 AGit), `pin_order`, `allow_maintainer_edit`, `is_locked` (keep only if true), full repo objects, avatars.
Field-name quirks: `number` (not index), `user` = author, `merged` = bool, `head.label`/`base.label` = BRANCH NAME (not `owner:branch` like GitHub), `head.ref` = branch name if branch exists else `refs/pull/{n}/head` (deleted head branch), `head.sha` falls back to the `refs/pull/{n}/head` commit when branch gone, `state` is `open|closed` (merged PRs are `closed` + `merged:true`). `additions/deletions/changed_files` are only computed for github-flow PRs (AGit flow: 0).

## 1. List / get / lookup

### GET `/repos/{o}/{r}/pulls` (repoListPullRequests), read:repository
Query: `state` open|closed|all (default open; `closed` includes merged); `sort` oldest|recentupdate|recentclose|leastupdate|mostcomment|leastcomment|priority; `milestone` int64 id; `labels` repeated **label IDs only** (non-numeric -> **500**, not 400); `poster` username (unknown -> 400); `base`, `head` exact branch names (`head` = branch only, no `owner:`); `page`, `limit`.
Returns array (nullable items); `X-Total-Count`, `Link`.
Gotchas: no text search `q`, no `assignee`, `review_requested` or `since` filters. For those use the issues search `GET /repos/issues/search?type=pulls&...` (other area) or filter client-side. Repo-scoped only.

### GET `/repos/{o}/{r}/pulls/{n}` (repoGetPullRequest)
200 PullRequest; 404 (no detail). Contains `mergeable`, `merged`, stats.

### GET `/repos/{o}/{r}/pulls/{base}/{head}` (repoGetPullRequestByBaseHead)
Route is `/{base}/*`: `base` is ONE path segment (a base like `release/1.0` cannot be addressed), `head` is the rest (may contain slashes): `branch`, `owner:branch` or `owner/repo:branch`. Uses `GetPullRequestByBaseHeadInfo` (one match; closed/merged inclusion [unverified]). 404 if none. MCP: prefer `pr_list(base=,head=,state=all)` for branch lookup.

### GET `/repos/{o}/{r}/pulls/pinned` (repoListPinnedPullRequests)
Array of PullRequest (bloated shape), no pagination. Pin/unpin is NOT under /pulls: `POST/DELETE /repos/{o}/{r}/issues/{n}/pin` (repo admin), `PATCH .../issues/{n}/pin/{position}`, `GET /repos/{o}/{r}/new_pin_allowed`. Low value; expose at most as `pinned=true` on list.

### GET `/repos/{o}/{r}/commits/{ref}/pull` (repoGetCommitPullRequest, commits.go L331)
Returns the PR whose **merge commit** equals `{ref}` (`GetPullRequestByMergedCommit`); merged PRs only, 404 otherwise. Not a "PRs containing commit" search.

### GET `/repos/{o}/{r}/pulls/{n}/merge` (repoPullRequestIsMerged)
204 if merged, 404 if not merged OR missing. Handler: `if pr.HasMerged { ctx.Status(204) }; ctx.NotFound()` (no `return`; first status written wins so 204 works). Cannot distinguish "not merged" from "no such PR": use GET pull (`merged`). No separate MCP tool.

## 2. Create / edit

### POST `/repos/{o}/{r}/pulls` (repoCreatePullRequest), write:repository, reqToken, not archived
Body JSON: `head` (REQUIRED; `branch` or `owner:branch` for forks; more than one colon -> 404), `base` (REQUIRED; branch/tag/commit in base repo), `title` (REQUIRED), `body`, `assignee` (string), `assignees` ([]string), `milestone` (int64 id, 0 none), `labels` ([]int64 **IDs**; org-owned repos also accept org-label IDs), `due_date` (RFC3339).
No `reviewers`, no `draft` field: **draft = title prefix** `WIP:` / `[WIP]` (configurable [unverified]); response `draft` = `pr.IsWorkInProgress`. Reviewers via a second call (section 6). `allow_maintainer_edit` can only be set by PATCH.
Responses: 201 PullRequest; 409 PR already exists for head/base (`ErrPullRequestAlreadyExists`; message carries ids); 422 `head == base` (string compare) or unknown assignee or assignee has no repo access; 404 base/head ref not found, head owner unknown, no fork relation, or no read permission (message says which); 403 blocked by user; 400 UserDoesNotHaveAccessToRepo; 413 quota; 423 archived.
Gotchas: unknown label IDs silently dropped; API does not obviously reject a PR with no diff [unverified]. MCP should accept label NAMES and resolve via `GET /repos/{o}/{r}/labels` (+ org labels), cached per repo.

### PATCH `/repos/{o}/{r}/pulls/{n}` (repoEditPullRequest), write:repository
Allowed if poster OR write on PR unit, else 403 (empty body). All optional: `title` (non-empty to change; WIP prefix toggling = draft), `body` (*string; `""` clears; optimistic-lock conflict -> 400), `base` (retarget; 404 branch missing; 409 PR exists / merged; 422 closed), `assignee`/`assignees` (write perm only; replaces), `milestone` (int64; **0 = no change, cannot be unset**), `labels` ([]int64 **replaces all**; `[]` clears; omit keeps; write perm only), `state` ("open"|"closed"; any other string is treated as open; merged PR -> **412**; open dependencies -> 412), `due_date` (date only; time forced to 23:59:59), `unset_due_date` bool, `allow_maintainer_edit` bool (403 if not permitted).
Response is **201** (not 200) with full PullRequest (re-fetched).
Gotchas: `unset_due_date:false` with no `due_date` dereferences nil `form.Deadline` (`.IsZero()`), likely 500: send only `unset_due_date:true`. Fields are applied sequentially without a transaction: failure midway leaves earlier fields applied. Poster without write silently has labels/assignees/milestone ignored. No reviewers field; no comment on close.

## 3. Merge

### POST `/repos/{o}/{r}/pulls/{n}/merge` (repoMergePullRequest), write:repository, not archived, quota
Body (`MergePullRequestForm`; **mixed key casing**, swagger `MergePullRequestOption`):
- `Do` REQUIRED: `merge | rebase | rebase-merge | squash | fast-forward-only | manually-merged`. Missing/invalid -> 422 (binding `Required;In(...)`; the handler's "default to merge" is unreachable). Style must be enabled in repo settings (`allow_merge_commits`, `allow_rebase`, `allow_rebase_explicit`, `allow_squash_merge`, `allow_fast_forward_only_merge`, `allow_manual_merge` on `GET /repos/{o}/{r}`) else 405 "<style> is not an allowed merge style for this repository".
- `MergeTitleField` (commit title; empty -> `GetDefaultMergeMessage`), `MergeMessageField` (body, appended after blank line). PascalCase, send exactly.
- `MergeCommitID` (only `manually-merged`: sha already in base branch; wrong -> 409 "Wrong commit ID").
- `head_commit_id` (expected head sha; mismatch -> 409 "head out of date").
- `force_merge` (bool; bypass protection checks for users permitted to).
- `merge_when_checks_succeed` (auto merge). Handler: `ScheduleAutoMerge(...)`; `scheduled=true` -> **201, empty body**; `scheduled=false` (checks already green) falls through to an immediate merge -> 200. Already scheduled -> **409**. Merge check type "auto" tolerates pending required checks.
- `delete_branch_after_merge` (bool; also passed into the scheduled auto merge).
Success: **200 empty body** (merged) or **201 empty body** (scheduled). No merge sha returned: re-GET the PR (`merge_commit_sha`, `merged`).
Handler order: `CheckPullMergeable` -> manually-merged shortcut -> auto-merge scheduling -> `pull_service.Merge` -> optional branch delete.

**Error table (verified in handler unless noted):**
| code | cause | message |
|---|---|---|
| 404 | PR missing OR `ErrIsClosed` (closed-unmerged PR gives a plain 404!) | generic |
| 405 | `ErrUserNotAllowedToMerge` | "User not allowed to merge PR" |
| 405 | `ErrHasMerged` | **empty message** |
| 405 | `ErrIsWorkInProgress` | "Work in progress PRs cannot be merged" |
| 405 | `ErrNotMergeableState` (conflict check still running) | "Please try again later" -> retry |
| 405 | `ErrDisallowedToMerge` (branch protection: approvals, required status checks, pending review requests, outdated branch, official change request) | err text e.g. "Does not have enough approvals yet" |
| 405 | `ErrWontSign` | "Protected branch X requires signed commits but this merge would not be signed" |
| 405 | invalid / disallowed style | "<style> is not an allowed merge style for this repository" |
| 409 | `ErrMergeConflicts`, `ErrRebaseConflicts`, `ErrMergeUnrelatedHistories`, `ErrPullRequestHasMerged` | body = JSON of the Go error struct, NOT `{message}` (likely `{"Style","StdOut","StdErr","Err"}` [unverified]); detect missing `message` |
| 409 | push out of date | "merge push out of date" |
| 409 | `head_commit_id` mismatch | "head out of date" |
| 409 | push rejected by hook | "PushRejected with remote message: ..." or "PushRejected without remote error message" |
| 409 | auto merge already scheduled | error text |
| 413 | quota | quotaExceeded |
| 422 | binding (bad `Do`) | validation message |
| 423 | archived | repoArchivedError |
| 403 | AFTER successful merge: head branch is default / protected / no permission to delete | "the head branch is the default branch" / "...is protected" / "insufficient permission to delete head branch" |
| 500 | other (e.g. manually-merged failure) | blank in prod |

**Gotcha: 403/500 from `delete_branch_after_merge` arrive AFTER the merge happened.** MCP must re-GET the PR and report "merged, branch not deleted".
Race: right after push/PR creation the conflict check is async; merge returns 405 "Please try again later" until it settles. MCP retries ~3x with 2-8 s backoff.

### DELETE `/repos/{o}/{r}/pulls/{n}/merge` (repoCancelScheduledAutoMerge)
204; 403 not allowed; 404 PR missing OR **nothing scheduled** (`db.IsErrNotExist`); 423 archived. **No GET exposes whether auto merge is scheduled**: PR JSON has no `auto_merge` field. Detect via timeline comment (`GET /repos/{o}/{r}/issues/{n}/timeline`, type `pull_scheduled_merge` [unverified type name]) or remember the 201 from merge. Server merges automatically when the last required status turns success.

### POST `/repos/{o}/{r}/pulls/{n}/update` (repoUpdatePullRequest): "update branch"
Query `style`: `merge` (default) | `rebase` (anything other than the literal "rebase" is merge). Merges base into head with fixed message `Merge branch '<base>' into <head>`. 200 empty; also 200 when already up to date. 403 (empty) if user cannot update (`IsUserAllowedToUpdate`; rebase checked separately). 409 "merge failed because of conflict" / "rebase failed because of conflict". **422 with empty message** if PR merged or closed (nil err passed). 413 quota. No archived check.

## 4. Mergeability, conflicts, CI status

- `mergeable` (bool) = `pr.Mergeable(ctx)` [impl unverified; roughly "status not conflict/error/manually-merged", so state `checking` yields true]. **No `mergeable_state`, no conflicted file list, no merge-status enum** in the v17 `PullRequest` struct. Rule: open PR with `mergeable:false` = conflicts (or error); `true` = no known conflict, may still be blocked by protection/CI/reviews/draft. Conflicting files only appear in a failed merge/update stderr/stdout (409): parse `CONFLICT (content): Merge conflict in <file>`.
- `merged:true` + `merge_commit_sha`, `merged_by`, `merged_at` for completed PRs.
- **Combined CI status of PR head:** take `head.sha` from the PR, then `GET /repos/{o}/{r}/commits/{sha}/status` (repoGetCombinedStatusByRef; `{ref}` may be sha/branch/tag via `ResolveRefOrSha`; params `page`,`limit`). Use the BASE repo (Actions `pull_request` runs post statuses in base repo; fork-PR location [unverified]). Response `{state, sha, total_count, statuses[], repository (FULL repo object, drop), commit_url, url}`; each status `{id, status, target_url, description, context, creator (full user, drop), created_at, updated_at}`. States `pending|success|error|failure|warning|skipped`; combined `error` folded into `failure`; worst wins (`NoBetterThan`). Quirks: **no statuses at all -> empty object (`state:""`, `total_count:0`), not "pending"**; `limit` paginates latest-status-per-context, `total_count` = page length, `X-Total-Count` = real count; 400 if ref empty.
  Compact: `{state, sha8, total, ok, failing:[{context,status,target_url,description}], pending:[context]}`; hand `target_url` (Actions run/job URL) to the Actions tools for log tailing.
  History: `GET /repos/{o}/{r}/commits/{ref}/statuses` (`sort` oldest|recentupdate|leastupdate|leastindex|highestindex, `state`, page/limit, Link + X-Total-Count) lists ALL statuses (noisy; debugging only).
- Protection requirements are not in the PR JSON; to explain a 405 read `GET /repos/{o}/{r}/branch_protections/{branch}` (`status_check_contexts`, `required_approvals`, ...).
- "Can I merge" summary = `mergeable` + CI state + latest review per reviewer (APPROVED/REQUEST_CHANGES, not dismissed, `official`) + `requested_reviewers` + `draft`.

## 5. Content: files, commits, diff

### GET `/pulls/{n}/files` (repoGetPullRequestFiles)
Query `page`, `limit`, `skip-to` (start list at that filename), `whitespace` ignore-all|ignore-change|ignore-eol|show-all. Array `ChangedFile {filename, previous_filename? (renames), status added|deleted|renamed|copied|changed|unchanged, additions, deletions, changes, html_url, contents_url, raw_url}`. **No patch hunks.** Headers `X-Total-Count`, `Link`, `X-Page/PerPage/PageCount/HasMore`. Range merge-base..`refs/pull/N/head` (merged PR: stored MergeBase). Compact `{path, status, +a, -d, from?}`; drop the 3 URLs. Quirk: `GetDiffNameStatus(..., listOptions.PageSize)` receives the page size as a limit; totals may be capped on huge PRs [verify]; `skip-to` can continue.

### GET `/pulls/{n}/commits` (repoGetPullRequestCommits)
Query `page`, `limit`, `verification` (default TRUE), `files` (default TRUE). Array of `Commit {sha, html_url, commit{message, author{name,email,date}, committer, tree, verification}, author, committer, parents, files, stats}` (stats always computed = extra cost). Always send `verification=false&files=false`. Compact `{sha7, subject, author, date}`. Paging done in memory after computing the whole commit list. Order [unverified].

### GET `/pulls/{n}.diff` and `.patch` (repoDownloadPullDiffOrPatch)
Route `.{diffType:diff|patch}`; `text/plain`; `.patch` = per-commit mbox patches, `.diff` = single unified diff; `binary=true` includes binary data. **No pagination, no file selection, no size cap.** MCP: fetch, split on `^diff --git `, filter by `path` globs, truncate by `max_bytes` with trailer "N files / M bytes omitted". Prefer `.diff`. Default tool mode should use `/files` (cheap) instead.

## 6. Reviews and review requests

All `/reviews/{id}/*` handlers use `prepareSingleReview` (review must belong to PR else 404; **PENDING reviews visible only to author or site admin**, others get 404).

### GET `/pulls/{n}/reviews` (repoListPullReviews)
`page`, `limit`; only `X-Total-Count` (no Link). Others' pending reviews are filtered AFTER counting, so the count can exceed the items. Item `PullReview {id, user(full), team, state, body, commit_id, stale, official, dismissed, comments_count, submitted_at, updated_at, html_url, pull_request_url}`. `state`: APPROVED | REQUEST_CHANGES | COMMENT | PENDING | REQUEST_REVIEW (review *requests* appear as REQUEST_REVIEW rows). Compact `{id, user|team, state, body(trunc), commit_id(7), stale, official, dismissed, n_comments, submitted_at}`; split REQUEST_REVIEW rows out.

### GET `/pulls/{n}/reviews/{id}`; GET `.../reviews/{id}/comments`; GET `.../comments/{comment}`
Comments (no pagination): `{id, body, user, resolver, pull_request_review_id, created_at, updated_at, path, commit_id, original_commit_id, diff_hunk, position, original_position, extra_lines_count, html_url, pull_request_url}`. `position` = new-file line, `original_position` = old-file line (one non-zero; handler maps `Line<0` to old). Resolved = `resolver != null`. No thread/reply linkage, no resolve endpoint in /pulls. **No endpoint lists all review comments of a PR**: loop over reviews (N+1). Compact `{id, path, line (new) | old:line, body, user, resolved, diff_hunk? (last 3 lines)}`.

### POST `/pulls/{n}/reviews` (repoCreatePullReview), reqToken
Body `{event, body, commit_id, comments:[{path, body, old_position, new_position, extra_lines_count}]}`.
- `event`: `APPROVED` (body optional; **422 if you are the PR author**), `REQUEST_CHANGES` (body REQUIRED; 422 own PR), `COMMENT` (needs body or inline comment, else 422), anything else incl. omitted/`PENDING` -> **pending draft review** needing submit. `REQUEST_REVIEW` is not an input.
- `commit_id` default = current `refs/pull/N/head` sha.
- inline: `new_position` (new file line) or `old_position` (old file line; when >0 it wins); `extra_lines_count` >= 0 for ranges (422 invalid).
Response **200** PullReview. Inline comments are created one by one BEFORE the review is submitted: failure midway leaves orphan pending comments (500). `official` only if reviewer is eligible.

### POST `/pulls/{n}/reviews/{id}` (repoSubmitPullReview)
Body `{event, body}`; only PENDING reviews (else 422 "only a pending review can be submitted"); same event rules; staying pending -> 422 "review stay pending". Commit = current head. 200 PullReview.

### POST `.../reviews/{id}/comments` (repoCreatePullReviewComment)
Body `{path, body, new_position|old_position, extra_lines_count}`; 200 PullReviewComment.

### DELETE `.../reviews/{id}` (204; author or site admin else 403); DELETE `.../reviews/{id}/comments/{comment}` (204; delegates to `deleteIssueComment`).
### POST `.../reviews/{id}/dismissals` `{message, priors}` and `/undismissals`: **repo admin only** (403 "Must be repo admin"); only APPROVED/REQUEST_CHANGES reviews (403 otherwise); closed PR 403; 200 PullReview.

### POST/DELETE `/pulls/{n}/requested_reviewers`
Body `{reviewers:["login or email"], team_reviewers:["team"]}` (teams only if owner is an org). POST -> **201** `PullReview[]` (state REQUEST_REVIEW); DELETE -> 204. 404 unknown user/team (validated before any change); 422 `ErrNotValidReviewRequest` (author, already requested, no access ...); 403 PR closed or token reducer prevents write. `reqToken` only; real permission in `IsValidReviewRequest`.

## 7. Related, outside /pulls
- PR conversation comments: `GET/POST /repos/{o}/{r}/issues/{n}/comments`; edit/delete `/issues/comments/{id}`; timeline `/issues/{n}/timeline`.
- Labels on a PR: `/issues/{n}/labels`.
- Cross-repo PR search (author, assignee, review_requested, mentioned, text): `GET /repos/issues/search?type=pulls`.
- Delete head branch: `DELETE /repos/{o}/{r}/branches/{branch}`.

## 8. Recommended MCP tools (pull requests)
Rules: `repo` as `"owner/name"` (config default); `n` for index; short terse JSON; list `limit` default 20; always `total` (from `X-Total-Count`) and `next` page when `Link` has rel=next; drop every `*_url` except the PR `html_url`; users -> login; shas -> 8 chars unless `full=true`. Cache per session: whoami, label name->id, milestone title->id, default branch, paging limits.

1. **`pr_list`** `{repo, state?: open|closed|merged|all = open, author?, base?, head?, labels?: [names], milestone?, sort?, limit?=20, page?}` -> `{total, next?, items:[{n,title,state,draft,author,base,head,mergeable,labels,updated}]}`. `merged` = `state=closed` filtered by `merged`. Nulls dropped.
2. **`pr_get`** `{repo, n | head(+base), include?: [body, files, commits, ci, reviews], body_max?=1500}` -> compact PR + optional sections in one call; `ci` via head sha; `reviews` = latest state per reviewer. Adds a derived `blocked_by` list (conflicts, ci:<state>, draft, changes_requested:[...], pending_reviewers) replacing 3-4 round trips.
3. **`pr_create`** `{repo, head, base? (default branch), title, body?, draft?, labels?: [names], assignees?, milestone? (title), reviewers?, due?}` -> `{n, html_url}`; also requests reviewers; 409 -> `{error:"exists", n:<existing via list head/base>}`.
4. **`pr_edit`** `{repo, n, title?, body?, base?, state?, draft? (toggles WIP prefix after reading title), labels? (replace), assignees?, milestone?, due?|unset_due?, allow_maintainer_edit?}` -> compact PR line (treat 201 as success).
5. **`pr_merge`** `{repo, n, method?=merge|rebase|rebase-merge|squash|fast-forward-only|manually-merged, title?, message?, when_green?, delete_branch?, head_sha?, force?, merge_commit_id?}` -> `{result: merged|scheduled|error, merge_commit?, reason?}`. 200 -> re-GET for `merge_commit_sha`; 201 -> scheduled; 405 "try again later" -> retry 3x; map table in section 3; 409 conflict -> "conflict" + <=10 stderr lines; after `delete_branch` 403/500 re-GET PR and report "merged, branch not deleted". Optional `auto:"if_green"` (merge now if CI success, else `when_green`).
6. **`pr_cancel_automerge`** `{repo, n}` (or `method:"cancel"` on pr_merge); 404 -> "none scheduled".
7. **`pr_update_branch`** `{repo, n, style?}` -> `updated|up_to_date|conflict`.
8. **`pr_status`** `{repo, n, wait_s?, until?: done}` -> `{verdict: green|failed|pending|timeout, ci:{state, failing:[{context,url}], pending:[...], ok}, mergeable, merged}`. Server-side polling inside the MCP (5-10 s loop, hard cap) replaces `forgejo-watch-pr.sh` and avoids repeated model polls.
9. **`pr_diff`** `{repo, n, mode?: files|diff|commits = files, path?: [globs], max_bytes?=20000, whitespace?}` -> files: compact list (merge pages up to a cap); diff: fetch `.diff` once, per-file split, filter, truncate with "N files / M bytes omitted; pass path= to see more"; commits: `{sha7, subject, author, date}` with `verification=false&files=false`.
10. **`pr_reviews`** `{repo, n, with_comments?=true, only_unresolved?, review_id?}` -> `[{id, user, state, body, stale, dismissed, comments:[{id,path,line,body,user,resolved}]}]`, `requested:[logins]` separated; N+1 inside the MCP; hide REQUEST_REVIEW rows and others' pending.
11. **`pr_review`** `{repo, n, action: approve|request_changes|comment|submit|dismiss|undismiss|delete, body?, comments?: [{path, line, side?: new|old, body, extra_lines?}], review_id?, message?, priors?}` -> `{id, state}`; pre-check approve/request_changes against own PR (422).
12. **`pr_reviewers`** `{repo, n, add?, remove?, teams_add?, teams_remove?}` -> `{requested:[...]}`.

Token-saving notes: never return repo/user objects; `pr_get include=` collapses sequences; keep tool descriptions short with inline enums; explain mergeability via `blocked_by`; truncate bodies/diffs with explicit "omitted" counters.

## 9. To verify on a live v17.0.2 instance (code absent from checkout)
1. Exact JSON of 409 merge conflict bodies (`ErrMergeConflicts` serialization).
2. `Mergeable()` semantics in `checking` state and for AGit flow.
3. Whether `/files` totals are capped by page size; `/commits` order.
4. Whether `pulls/{base}/{head}` returns closed/merged PRs.
5. Fork-PR combined-status location (base vs head repo).
6. Actual `DefaultPagingNum`/`MaxResponseItems` (`GET /api/v1/settings/api`).
7. Whether creating a PR with no diff is rejected by `NewPullRequest`.
8. Timeline comment type name for scheduled auto merge.
