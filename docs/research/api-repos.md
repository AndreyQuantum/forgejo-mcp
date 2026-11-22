# Forgejo v17.0.2 API: repository content and settings

Source of truth: `routers/api/v1/api.go` (routes), `routers/api/v1/repo/*.go`, `routers/api/v1/utils/*.go`, `modules/structs/*.go`, `services/convert/*.go`, `templates/swagger/v1_json.tmpl`.
Caveat: the sparse checkout has NO `services/repository/**`, `models/**` (mostly), `modules/webhook`, `modules/git`. Behaviour that lives there (ChangeRepoFiles, GetContentsOrList, archiver, webhook event names) is taken from handler call sites + swagger, and marked (swagger) / (unverified).
Base: `{host}/api/v1`. Auth: `Authorization: token <PAT>` (or `Bearer`). All paths below are relative to `/api/v1`.

---------------------------------------------------------------------
## 0. Cross-cutting facts (apply to every endpoint)

### Token scopes
- `tokenRequiresScopes(category)` maps HTTP method to level: GET/HEAD -> `read:<cat>`, POST/PUT/PATCH/DELETE -> `write:<cat>` (`requiredScopeLevel`, api.go:293). Scope is only enforced for tokens with scopes (OAuth/PAT); basic auth bypasses it.
- The entire `/repos/{o}/{r}/**` group (except DELETE repo) needs category `repository`: **read:repository / write:repository**.
- `DELETE /repos/{o}/{r}`: outside that group; needs `tokenRequiresRepoOwnerScope` = **write:user** (user-owned repo) or **write:organization** (org-owned) + `reqOwner` (repo owner / org owner / site admin).
- `POST /orgs/{org}/repos`: write:organization (+ repository). `POST /repos/{o}/{r}/generate`: runtime check write:user or write:organization on top of repository (repo.go:443,448). `POST /user/repos`: write:repository.
- Public-only tokens (`public-only` scope): `checkTokenPublicOnly()` wraps all `/repos/{o}/{r}`; 403 "token scope is limited to public repos" for private repos.
- Permission middleware used below: `reqToken` (must be authenticated), `reqAnyRepoReader`, `reqRepoReader(unit)`, `reqRepoWriter(unit)` (write on unit), `reqAdmin` (repo admin collaborator/owner/site admin), `reqOwner`, `mustNotBeArchived` (423 on archived), `reqSiteAdmin`.

### Pagination (verified)
- Query `page` (1-based) and `limit`. `utils.GetListOptions` -> `ToCorrectPageSize`: limit<=0 -> `setting.API.DefaultPagingNum` (default 30), limit > `MaxResponseItems` is CLAMPED silently (default 50). Real values: `GET /settings/api` returns `{max_response_items, default_paging_num, default_git_trees_per_page, default_max_blob_size}` - MCP should fetch once and cache.
- Response headers: `X-Total-Count` (via `SetTotalCountHeader`), `Link` with `rel="next|last|first|prev"` (`SetLinkHeader`; header OMITTED when only one page). Both are exposed via Access-Control-Expose-Headers.
- Endpoints that return X-Total-Count but NO Link: hooks, collaborators, topics, deploy keys, forks, wiki pages, trees. Endpoints with neither: branch_protections, tag_protections, refs, releases/{id}/assets, languages, topics (uses X-Total-Count only).
- `GET /repos/{o}/{r}/commits` additionally sets `X-Page, X-PerPage, X-Total, X-PageCount, X-HasMore` (use `X-HasMore` as the cheap "more" signal) and its page size is further capped by `setting.Git.CommitsRangeSize` (default 50).
- `GET /repos/{o}/{r}/wiki/pages`: custom paging; `limit<=1` falls back to default (so `limit=1` impossible); X-Total-Count = total entries.
- `GET /repos/{o}/{r}/git/trees/{sha}` uses `page` + `per_page` (NOT `limit`), response has `truncated` + `total_count`.

### Error shape
`{"message": "...", "url": "<swagger url>"}` (`api.APIError`). Status mapping is inconsistent. In production, **500 messages are blanked** (`message: ""`) unless the caller is site admin (api.go `Error`, `InternalServerError`), so a 500 gives no clue. Topics return `{"invalidTopics": [...], "message": "..."}` with 422. 204 = empty body (delete, many PUTs). Validation errors from `binding` give 422 with message.

### Misc gotchas
- Path wildcards (`{filepath}`, `{branch}`, `{tag}`, `{ref}`, `compare/{basehead}`) take `*` so slashes are allowed; URL-encode each segment, keep `/`.
- `DELETE /contents/{filepath}` and `DELETE /releases/...` etc. need a JSON body for contents: HTTP client must send a body with DELETE.
- Binary-ish endpoints (raw, media, archive, diff, patch) return non-JSON. MCP must not feed them blindly into context: cap bytes, detect binary, write archives to disk and return path.
- Many mutating endpoints are async/eventual: fork (202), transfer (202), mirror-sync (200 but queued), webhook test (204 but queued).
- Archived repos: write endpoints decorated with `mustNotBeArchived` return **423**. Mirrors (pull) reject branch/file writes with 403 or 500 (see below).

---------------------------------------------------------------------
## 1. Repository CRUD / search / fork / mirror / transfer

### Repository object (`api.Repository`, convert.ToRepo): fields worth keeping (compact projection)
Keep: `full_name, id, description, private, fork, template, mirror, archived, empty, default_branch, html_url, clone_url, ssh_url, stars_count, forks_count, open_issues_count, open_pr_counter, size, language, updated_at, parent.full_name (if fork), permissions{admin,push,pull}, topics, has_issues/has_pull_requests/has_wiki/has_actions/has_releases (as a short flag set)`.
Drop: `owner` (huge User object, derive from full_name), `url, link, languages_url, original_url, avatar_url, internal_tracker, external_*, wiki_*urls, watchers_count, release_counter, created_at, mirror_updated, repo_transfer`, all `allow_*merge*`/`default_*` (only on a dedicated `settings` view). `parent` is a full nested repo: project to `parent.full_name`.

### 1.1 List / search
| Endpoint | Scope | Params | Notes |
|---|---|---|---|
| `GET /repos/search` | read:repository | `q`, `topic`(bool: q matches topics), `includeDesc`(bool), `uid`, `priority_owner_id`, `team_id`, `starredBy`(user id), `private`(bool default true), `is_private`(bool), `template`(bool), `archived`(bool), `mode` = `source`/`fork`/`mirror`/`collaborative` (else 422), `exclusive`(bool, with uid), `sort` = `alpha,created,updated,size,git_size,lfs_size,id,stars,forks` (unknown -> 422), `order` asc/desc (default asc, only with sort), `page`, `limit` | Response is WRAPPED: `{"ok":true,"data":[Repository...]}` (not a bare array). X-Total-Count + Link. `uid`/`team_id`/`starredBy` take numeric ids, not names (no owner-name filter: to list by owner use `/users/{u}/repos` or `/orgs/{o}/repos`). |
| `GET /user/repos` | read:repository | `page,limit`, `order_by` = `name,id,newest,oldest,recentupdate,leastupdate,reversealphabetically,alphabetically,reversesize,size,reversegitsize,gitsize,reverselfssize,lfssize,moststars,feweststars,mostforks,fewestforks` (unknown -> 422) | Only repos OWNED by the caller (`OwnerID=Doer`), not collaborator/org repos (use `/repos/search?uid=<me>&mode=collaborative` or orgs). Bare array. |
| `GET /users/{username}/repos` | read:repository | `page,limit` | Fixed order `id ASC`. Includes private if caller signed in and has access. |
| `GET /orgs/{org}/repos` | read:repository (+org) | `page,limit` | Same helper (`listUserRepos`), `id ASC`. |
| `GET /repos/{o}/{r}` | read:repository | - | `reqAnyRepoReader`. Full object incl. `permissions`. |
| `GET /repositories/{id}` | read:repository | - | By numeric id. |
| `GET /user/starred`, `/users/{u}/starred`, `/user/subscriptions` | read:repository | page,limit | Lists of repos (watch/star). Star: `PUT/DELETE /user/starred/{o}/{r}` (204). |

### 1.2 Create
- `POST /user/repos` (write:repository), `POST /orgs/{org}/repos` (and deprecated `POST /org/{org}/repos`). Body `CreateRepoOption`:
  `name` (required, AlphaDashDot, <=100), `description` (<=2048), `private` (bool; forced true if `FORCE_PRIVATE`), `issue_labels` (label-set template name), `auto_init` (bool), `template` (bool = make it a template repo), `gitignores` (comma list, names), `license`, `readme` (default `"Default"` when `auto_init` and empty; invalid name -> 400 listing valid), `default_branch`, `trust_model` (default/collaborator/committer/collaboratorcommitter), `object_format_name` (`sha1`/`sha256`).
  Response 201 full Repository. Errors: 409 name exists, 422 reserved/pattern/label-template, 403 not allowed in org, 422 "not allowed creating repository for organization" if the token's user is an org.
  Quirk: `auto_init=false` creates an EMPTY repo (`empty:true`); branches/contents/releases/commit endpoints then fail (see per-endpoint notes).
  The new repo's owner for `/user/repos` is always the token user; no owner param. Admin can create for others with `POST /admin/users/{username}/repos` (out of area).
- `POST /repos/{template_owner}/{template_repo}/generate` (generate from template; needs `template:true`, else 422): body `GenerateRepoOption` `{owner (required in practice), name, default_branch, description, private, git_content, topics, git_hooks, webhooks, avatar, labels, protected_branch}`; at least one of the item flags must be true (422 "must select at least one template item"). 201.
- `POST /repos/migrate` (write:repository): body `MigrateRepoOptions` `{clone_addr (req), repo_name (req), repo_owner or uid, service (git|github|gitea|gitlab|gogs|onedev|gitbucket|codebase|forgejo|pagure|bitbucketdc), auth_username, auth_password, auth_token, mirror, lfs, lfs_endpoint, private, description, wiki, milestones, labels, issues, pull_requests, releases, mirror_interval}`.
  Quirks: synchronous (request blocks for the whole import, can take minutes; client timeout must be large). On failure the half-created repo is deleted. When `mirror:true`, issues/milestones/labels/pull_requests/releases flags are forced to false. 403 if admin disabled migrations/mirrors; 422 for disallowed hosts, local paths, credentials in URL, rate limits, 2FA-protected remote. Credentials go in `auth_*` fields, URL with embedded credentials is rejected (422).
- `POST /repos/{o}/{r}/forks` (write:repository): body `{organization?: string, name?: string}`. Returns **202 Accepted** with the new Repository; 409 if name exists or user over repo limit; 422 unknown org. Fork is created synchronously in handler (`ForkRepositoryAndUpdates`) but 202 means the git content may still be being prepared: poll `GET /repos/{fork}` until `empty:false`. No `default_branch_only` option in this version.
- `GET /repos/{o}/{r}/forks` (`page,limit`; X-Total-Count only).

### 1.3 Edit / delete / archive / convert / transfer
- `PATCH /repos/{o}/{r}` (write:repository, `reqAdmin`): body `EditRepoOption` ALL fields optional pointers (true partial update): `name` (rename, 422 if taken/reserved), `description`, `website`, `private`, `template`, `default_branch` (silently ignored if branch does not exist and repo not empty), `wiki_branch`, `archived` (422 if repo is a mirror), `has_issues`, `internal_tracker{enable_time_tracker, allow_only_contributors_to_track_time, enable_issue_dependencies}`, `external_tracker{external_tracker_url, external_tracker_format, external_tracker_style (numeric|alphanumeric|regexp), external_tracker_regexp_pattern}`, `has_wiki`, `external_wiki{external_wiki_url}`, `globally_editable_wiki`, `has_pull_requests`, `has_projects`, `has_releases`, `has_packages`, `has_actions`, `ignore_whitespace_conflicts`, `allow_merge_commits`, `allow_rebase`, `allow_rebase_explicit`, `allow_squash_merge`, `allow_fast_forward_only_merge`, `allow_manual_merge`, `autodetect_manual_merge`, `allow_rebase_update`, `default_delete_branch_after_merge`, `default_merge_style` (enum `merge,rebase,rebase-merge,squash,fast-forward-only,manually-merged,rebase-update-only`), `default_update_style` (`merge,rebase`), `default_allow_maintainer_edit`, `mirror_interval` (Go duration `8h30m0s`; below `Mirror.MinInterval` -> 422; ignored silently if not a mirror), `enable_prune`.
  Quirks: NOT atomic - handler applies basic props, then units, then archive, then mirror in sequence; a 422 midway leaves earlier changes applied. Setting `has_issues:true` without trackers enables the internal tracker with defaults. Response 200 full Repository.
- `DELETE /repos/{o}/{r}` -> 204. Irreversible. Needs write:user/write:organization (see 0). 403 "Given user is not owner of organization".
- `POST /repos/{o}/{r}/convert` (reqOwner+reqAdmin): mirror -> regular repo; returns Repository.
- `POST /repos/{o}/{r}/transfer` (reqOwner+reqAdmin): body `{new_owner: string, team_ids?: [int]}`. Returns **202** (pending, needs recipient acceptance when new owner is a user and caller is not admin of it) or **201** (done immediately, e.g. into an org the caller owns). 404 unknown owner, 409 transfer already pending, 422 teams for non-org, 403 team not in org.
  `POST .../transfer/accept` -> 202 Repository; `POST .../transfer/reject` -> 200 Repository. Repository field `repo_transfer` shows a pending transfer; `status` pending.
- `POST/DELETE /repos/{o}/{r}/avatar` (`{image: base64}`), `GET /repos/{o}/{r}/languages`, `GET .../editorconfig/{filepath}?ref=`, `GET /repos/{o}/{r}/activities/feeds` - minor.
- Site-admin-only repo flags: `GET/PUT/DELETE /repos/{o}/{r}/flags[/{flag}]` (only if `EnableFlags`, needs admin scope + site admin).

### 1.4 Pull mirrors, push mirrors, sync fork
- `POST /repos/{o}/{r}/mirror-sync` (write; `reqRepoWriter(code)`): triggers pull-mirror update. 200 with empty body. 400 "Repository is not a mirror" / "Mirror feature is disabled". Quirk: request only queues; no status endpoint. Observe via `GET /repos/{o}/{r}` -> `mirror_updated`.
- Push mirrors (reqAdmin; 400 if mirror feature disabled):
  - `GET /push_mirrors` (`page,limit`) -> `[PushMirror]` keep `remote_name, remote_address, interval, sync_on_commit, last_update, last_error, branch_filter`. Link header is computed from the page length (buggy: `SetLinkHeader(len(responsePushMirrors), ...)`), rely on X-Total-Count only.
  - `GET /push_mirrors/{name}` (name = `remote_name` like `remote_mirror_ab12cd34`).
  - `POST /push_mirrors` body `{remote_address, remote_username, remote_password, interval ("8h0m0s"; "0" disable periodic), sync_on_commit, use_ssh, branch_filter}`. Returns **200** (not 201). `use_ssh` is mutually exclusive with username/password (400); with `use_ssh` the response `public_key` must be added to the remote as deploy key. 400 for credentials inside the URL ("The URL contains credentials"), invalid protocol, 401 permission denied.
  - `DELETE /push_mirrors/{name}` -> 204.
  - `POST /repos/{o}/{r}/push_mirrors-sync` (note the hyphen) -> triggers ALL push mirrors of the repo, 200 empty. 500 if any sync fails.
- Fork sync: `GET /sync_fork` and `GET /sync_fork/{branch}` -> `{allowed, fork_commit, base_commit, commits_behind}`; `POST /sync_fork` and `POST /sync_fork/{branch}` (write, not archived) -> 204. 400 "The Repo must be a fork", 400 "You can't sync this branch" (not allowed = diverged).

---------------------------------------------------------------------
## 2. Branches, branch protection, tags, tag protection

### 2.1 Branches (`/repos/{o}/{r}/branches`)
Compact `Branch` projection: `name, commit.id (short 8), commit.message (first line), commit.timestamp, protected, user_can_push, user_can_merge`. Drop `commit.added/removed/modified` (ToPayloadCommit populates file lists -> expensive and large), `commit.url`, `author/committer` unless needed, `required_approvals, enable_status_check, status_check_contexts, effective_branch_protection_name` (put in protection view).
- `GET /branches` (read, `reqRepoReader(code)`): `page,limit`; X-Total-Count + Link. NO name filter/search param (verified: only ListOptions). Empty repo -> 200 `[]`. Ordered by DB `Branch` default order (not by date). Each item triggers a git lookup (`GetBranchCommit`) + protection matching: keep `limit` small. Stale DB rows (branch missing in git) are skipped and the total is decremented.
- `GET /branches/{branch}` -> Branch (404 if absent). Branch name may contain `/`.
- `POST /branches` (write): body `{new_branch_name (req), old_ref_name?, old_branch_name? (deprecated)}`. Base resolution: `old_ref_name` (any ref/sha, via `GetCommit`) else `old_branch_name` (must be a branch) else default branch. 201 Branch. Errors: 404 "Git Repository is empty", 403 if mirror, 409 branch/tag already exists, 404 old branch missing. Gotcha: a bad `old_ref_name` gives **500** (GetCommit error is not mapped to 404), message blanked in prod.
- `PATCH /branches/{branch}` body `{name}` -> 204 rename. 422 "Cannot rename a branch using the same name or rename to a branch that already exists", 404 missing, 403 mirror/empty. Renaming default branch updates default; protection rules by name are not automatically retargeted (unverified).
- `DELETE /branches/{branch}` -> 204. 403 if default branch ("can not delete default branch") or protected ("branch protected"), 403 mirror.
- Note the repo has no "list merged/stale branches" feature in the API.

### 2.2 Branch protections (`reqToken`+`reqAdmin`; read:repository for GET, write for the rest)
- `GET /branch_protections` -> array (no paging), `GET /branch_protections/{name}` (name = **rule_name**, may be a glob like `release/*`), `POST`, `PATCH /{name}`, `DELETE /{name}` (204).
- `BranchProtection` fields: `rule_name` (key; `branch_name` is deprecated alias), `enable_push, enable_push_whitelist, push_whitelist_usernames[], push_whitelist_teams[], push_whitelist_deploy_keys, enable_merge_whitelist, merge_whitelist_usernames[], merge_whitelist_teams[], enable_status_check, status_check_contexts[] (glob patterns), required_approvals, enable_approvals_whitelist, approvals_whitelist_username[] (note singular `username`), approvals_whitelist_teams[], block_on_rejected_reviews, block_on_official_review_requests, block_on_outdated_branch, dismiss_stale_approvals, ignore_stale_approvals, require_signed_commits, protected_file_patterns, unprotected_file_patterns, apply_to_admins, created_at, updated_at`.
  Compact: drop timestamps and empty arrays/false flags (return only non-default fields).
- POST: `rule_name` or deprecated `branch_name`, else 400. 403 (not 409) "Branch protection already exist" when a rule with the same name exists. Create takes plain (non-pointer) fields: omitted booleans default to false. Unknown user/team in whitelists -> 422. Creating a plain-name rule for a non-existent branch is allowed (rule applies later).
- PATCH is a TRUE partial update (pointers/nil-slices keep stored value; sending `[]` replaces the list; `required_approvals<0` ignored). Gotcha: `enable_push:false` also clears `enable_push_whitelist` and `push_whitelist_deploy_keys`; `enable_push:true` + `enable_push_whitelist` toggles whitelist; whitelist user/team arrays only apply when provided. `PATCH` and `POST` are `mustNotBeArchived` (423); DELETE is not.
- 404 on GET/PATCH/DELETE when name unknown (handler returns NotFound; DB error is 500).

### 2.3 Tags (`/repos/{o}/{r}/tags`, `reqRepoReader(code)`)
- `GET /tags` (`page,limit`; X-Total-Count + Link): `Tag{name, message, id (tag object sha), commit{sha,url,created}, zipball_url, tarball_url, archive_download_count}`. Compact: `name, commit.sha, message(first line), id`. Drop URLs/counts. Order: by tag creation, newest first (unverified).
- `GET /tags/{tag}` -> Tag. `GET /git/tags/{sha}` -> `AnnotatedTag` (sha = the tag OBJECT sha = `Tag.id`, not commit sha; 400 if sha is not an annotated tag).
- `POST /tags` (write, not archived): `{tag_name (req), message?, target? (default = default branch; any ref/sha)}`. `message` non-empty -> annotated tag. 201 Tag. 404 target not found, 409 exists, 422 "user not allowed to create protected tag".
- `DELETE /tags/{tag}` -> 204. **409** "a tag attached to a release cannot be deleted directly" (delete the release first via `DELETE /releases/{id}`; whether the git tag survives is unverified), 422 protected tag.
- Tag protections (`reqAdmin`): `GET/POST /tag_protections`, `GET/PATCH/DELETE /tag_protections/{id}` (numeric id). Fields `id, name_pattern (glob or /regex/), whitelist_usernames[], whitelist_teams[]`. POST 400 when `name_pattern` empty; **400 when both whitelists empty** ("both whitelist_usernames and whitelist_teams are empty"); 403 if same pattern exists. No paging.

---------------------------------------------------------------------
## 3. Releases and assets (`reqRepoReader(releases unit)`; writes `reqRepoWriter(releases)`)

`Release` compact projection: `id, tag_name, name, draft, prerelease, target_commitish, published_at, author.login, html_url, body` (body only in single-release view or truncated to N chars in list), `assets[]{id,name,size,download_count,browser_download_url}` (drop uuid, type, created_at). Drop `tarball_url, zipball_url, upload_url, url, hide_archive_links, archive_download_count`.
- `GET /releases` (`draft` bool, `pre-release` bool, `q`, `page`,`limit`): X-Total-Count + Link are of the FILTERED set. Drafts only visible to callers with releases-write; `draft=true` as non-writer returns none. Bare tags without release are excluded (`IncludeTags:false`). Note the param is literally `pre-release` (hyphen).
- `GET /releases/latest` (latest non-draft non-prerelease; 404 if none), `GET /releases/tags/{tag}` (404 if absent; drafts hidden from non-writers), `GET /releases/{id}`.
- `POST /releases`: `{tag_name (req), target_commitish?, name? (defaults to tag_name), body?, draft?, prerelease?, hide_archive_links?}` -> 201. Behaviour (verified): if `tag_name` does not exist, a tag is created at `target_commitish` (default = default branch; 404 if target missing, 422 if tag protected) and the release is created; if a plain TAG already exists it is "promoted" (updated in place; title/note/flags overwritten, `Target` overwritten with the request value, even when empty); if a release for that tag exists -> 409 "ReleaseAlreadyExist" or 409 "Release has no Tag". 422 on empty repo.
- `PATCH /releases/{id}`: fields `tag_name,target_commitish,name,body,draft,prerelease,hide_archive_links`. Gotcha: string fields are only applied when NON-EMPTY (cannot clear `body` or `name` via PATCH). 404 if id is a bare tag.
- `DELETE /releases/{id}` and `DELETE /releases/tags/{tag}` -> 204. 422 if tag protected. (Whether the underlying git tag is removed: `DeleteReleaseByID(..., delTag=...)` called with the tag-delete flag in release.go:217; to be sure, check tag after delete - unverified.)
- Assets: `GET /releases/{id}/assets`, `GET /releases/{id}/assets/{attachment_id}`, `PATCH .../{attachment_id}` (`{name, browser_download_url?}`; `browser_download_url` only for external assets), `DELETE` (204).
  `POST /releases/{id}/assets?name=<filename>`: three input modes (verified in handler): (a) `multipart/form-data` with file field **`attachment`** (name defaults to the filename), (b) NON-multipart: raw request body is the file and `?name=` is REQUIRED (400 "Missing 'name' parameter"), (c) `external_url=<url>` (query or form) to register an external link (mutually exclusive with a file, 400). 201 `Attachment{id,name,size,download_count,created_at,uuid,browser_download_url,type}`. Allowed types limited by `[repository.release] ALLOWED_TYPES` (400 DetectContentType on violation). Draft release assets are invisible to non-writers (404).
  Downloading an asset: use the `browser_download_url` (`/{o}/{r}/releases/download/{tag}/{name}`, a web route, no API scope; private repos need token auth header) - not an `/api/v1` endpoint.

---------------------------------------------------------------------
## 4. Contents API, raw, media, archive (`reqRepoReader(code)`)

### 4.1 Reading
- `GET /contents/{filepath}?ref=<branch|tag|sha>` and `GET /contents?ref=` (root listing). `ref` empty -> default branch. Response (single type): if path is a file -> single `ContentsResponse`; if a directory -> ARRAY of `ContentsResponse` (without content). Shape is polymorphic: client must check `Array.isArray`.
  `ContentsResponse`: `name, path, sha (blob sha; needed for update/delete), last_commit_sha, last_commit_when, type (file|dir|symlink|submodule), size, encoding ("base64" for file), content (base64, only for file), target (symlink), submodule_git_url, url, html_url, git_url, download_url, _links`. Compact: `{path, type, size, sha}` for dir entries; for files `{path,size,sha,content(decoded)}`.
  Quirks: `content` is returned base64 for ANY size (no size cap besides server body limits); no line/byte range; a missing path is 404; wrong `ref` is 404; lack of read rights surfaces as **500** (`ErrUserDoesNotHaveAccessToRepo` mapped to 500). Directory listing is NOT recursive and has no paging. `?ref` may also be a full sha.
- `GET /raw/{filepath}?ref=` returns raw bytes (`ServeBlob`): `Content-Type` sniffed, headers `X-Gitea-Object-Type`/`X-Forgejo-Object-Type`, ETag/Last-Modified (`If-None-Match` supported -> 304, saves tokens on re-reads). Best primary read path for text: pair with a byte cap + `Range` (standard http range not guaranteed -> instead truncate client side while streaming).
  `GET /media/{filepath}?ref=` same, but resolves LFS pointers to the LFS object (302 to storage when `SERVE_DIRECT`).
- For trees: `GET /git/trees/{sha}?recursive=bool&page=&per_page=` (sha may be a branch/tag name or commit/tree sha). Response `{sha,url,tree:[{path,mode,type(blob|tree|commit),size,sha,url}],truncated,page,total_count}`. `per_page` default `default_git_trees_per_page`(1000). With `recursive=true` yields the whole file list (best "ls -R"): compact to `path` + size only, ~1 line per entry. `X-Total-Count` also set. Errors are **400** for everything (including missing sha).
- `GET /git/blobs/{sha}` -> `{content (base64), encoding, url, sha, size}`; `GET /git/blobs?shas=a,b,c` (comma list, URL length limit ~2k) -> list. 400 on error (not 404).
- `GET /git/refs`, `GET /git/refs/{ref}` (ref prefix like `heads/main`, `tags/v1`): Reference `{ref, object{type,sha}}`. **Polymorphic**: returns a single OBJECT when exactly one ref matches the filter exactly, otherwise an ARRAY. 404 if no match. Compact: `{ref, sha, type}`.
- `GET /git/commits/{sha}` and `GET /commits` see section 5. `GET /git/notes/{sha}`, `POST` (`{message}`), `DELETE` - git notes (write needs code write).
- `GET /editorconfig/{filepath}?ref=` returns the effective .editorconfig definition.
- Archive: `GET /archive/{ref}.{zip|tar.gz|bundle}` e.g. `archive/main.zip` or `archive/v1.0.tar.gz` (swagger param `archive`; handler takes `*` and parses extension; unknown ext 400, unknown ref 404). Also GitHub-compatible `GET /{tarball|zipball|bundle}/{ref}`. The handler `Await`s archive generation (request blocks until built; first download of large repo can be slow); sets `Link: <...>; rel="immutable"`; may 302 to object storage. Binary: MCP must stream to disk and return `{path, bytes, commit}`.

### 4.2 Writing files (write:repository; JSON body; base64 content)
Common fields (`FileOptions`): `message` (default auto: "Add/Update/Delete <paths>"), `branch` (default: default branch), `new_branch` (create branch from `branch` and commit there), `force_overwrite_new_branch` (force-push if `new_branch` exists), `author{name,email}`, `committer{name,email}`, `dates{author,committer}` (RFC3339), `signoff` (bool).
- `POST /contents/{filepath}` create: body `{content (base64, required by swagger), ...common}`. 201 `FileResponse{content: ContentsResponse, commit: {sha,url,html_url,author,committer,message,parents,tree}, verification}`. 422 if file exists / invalid path.
- `PUT /contents/{filepath}` update: body `{content, sha (REQUIRED = current BLOB sha of the file), from_path? (rename: moves from_path to {filepath}), ...common}`. 200 `FileResponse`. 409 when `sha` does not match (concurrent edit) - re-read to get new sha, then retry. 422 "repo is empty".
- `DELETE /contents/{filepath}`: JSON body `{sha (required), ...common}` -> 200 `FileDeleteResponse{content:null, commit, verification}` (swagger FIXME: should be 204). 404 file missing, 400 bad sha.
- `POST /contents` multi-file commit: body `{files:[{operation: create|update|delete (required), path (req, <=500), content (base64; create/update), sha (required for update/delete), from_path (rename source)}], ...common}`. One commit for all files. 201 `FilesResponse{files:[ContentsResponse], commit, verification}`. Quirks: all-or-nothing (one commit); `files` entries with bad base64 -> 422 before anything runs; a rename is expressed as `operation:update` + `from_path`. There is no `upload`/`rename` operation (enum fixed to 3). Default commit message groups by operation.
- `POST /diffpatch`: applies a unified diff as ONE commit. Swagger says body `UpdateFileOptions` but the code uses `ApplyDiffPatchFileOptions` = `{content: <plain diff text, NOT base64>, sha (required by binding; value not meaningful), message (default "apply-patch"), branch, new_branch, author, committer, dates, signoff}`. Returns 201 `FileResponse` (swagger claims 200). Gotcha: unlike `/contents`, `branch` is NOT defaulted to the default branch in the handler - always pass it. Best tool for token-cheap multi-hunk edits.
- Error mapping (`handleCreateOrUpdateFileError`): 403 user cannot commit / path protected / push rejected (pre-receive hook, includes protected-branch rejection), 422 invalid filename/path/already exists/sha missing, 409 sha or commit-id mismatch, 404 branch missing, else 500. Writing to a PULL-MIRROR or lacking write access hits `ErrUserDoesNotHaveAccessToRepo` which is not mapped -> **500** (blank message). Archived repo -> 423 via middleware.
- Protected default branch: direct commits are rejected (403 push rejected) unless caller is whitelisted; use `new_branch` then PR.

---------------------------------------------------------------------
## 5. Commits, diffs, compare

### 5.1 `GET /commits` (list; `reqRepoReader(code)`)
Params (verified; ONLY these): `sha` (branch/tag/commit to start from; default HEAD branch), `path` (file or dir filter; with `path` and no `sha` uses default branch; 404 if no commit touches the path), `not` (exclude commits reachable from this ref, i.e. `main..feature` = `sha=feature&not=main`), `stat`, `verification`, `files` (bool each, **default TRUE**), `page` (>=1), `limit` (clamped to `Git.CommitsRangeSize`).
NO author/since/until/message-grep filters. 409 on empty repo (`{"message":"Git Repository is empty."}`).
Cost gotcha: with defaults every commit carries `files[]`, `stats` and `verification` (GPG/SSH check) - pass `stat=false&files=false&verification=false` for light listings (3-10x smaller and much faster).
Commit object: `{sha, url, html_url, commit{message, author{name,email,date}, committer{...}, tree{sha}, verification{...}}, author{User}, committer{User}, parents[{sha}], files[{filename,status}], stats{total,additions,deletions}, created}`.
Compact projection: `{sha:short(10), message: first line, author: commit.author.name, date: commit.author.date, files: count or list of "status filename" (opt-in), +/-: stats}`; skip `author/committer` User objects (large avatar/url fields), `url`, `html_url`, `tree`, `verification` (keep a boolean `verified` only if asked).
Headers: X-Total-Count, Link, plus `X-HasMore`.

### 5.2 Single commit and diff
- `GET /git/commits/{sha}?stat&verification&files` (sha may be a branch/tag/ref too; 422 if the string is not a valid ref pattern; 404 if absent). Same Commit object. Alias route in swagger: `GET /commits/{ref}/status` etc. below.
- `GET /git/commits/{sha}.diff` and `.patch` (`repoDownloadCommitDiffOrPatch`): plain text unified diff / mbox patch. **No size cap, no per-file filter, no paging.** Large commits can be megabytes. MCP must truncate (bytes + lines), and offer a `files`-only mode (`GET /git/commits/{sha}` with `files=true&stat=true`, which gives file list and totals, not per-file counts). `{sha}` here is a commit sha or ref name; 404 NotFound. The route is registered AFTER `/commits/{sha}`; the router distinguishes by the `.diff|.patch` suffix.
- For a per-file diff of one path: no endpoint; fetch whole diff and filter client-side on `diff --git a/<path>` blocks (do this in the MCP: split by file header, return only requested file or `--stat`-like summary). For PR diffs see pull area (`/pulls/{n}.diff`).
- `GET /commits/{ref}/status` (combined CI status), `/commits/{ref}/statuses`, `GET /statuses/{sha}` (list), `POST /statuses/{sha}` (create commit status) - belongs to CI/status area but note path prefix. `GET /commits/{ref}/pull` -> the merged PR that contains commit (404 `PullRequestNotExist`).

### 5.3 Compare
`GET /compare/{base}...{head}` (also `base..head`; a single `x` means default...x). Refs may be branch, tag, sha; for forks use `owner:branch` as head. Params: `verification` (default true), `files` (default true). Response `Compare{total_commits, commits:[Commit], files:[{filename,status}]}`.
Gotchas (verified in handler): (1) always computes `stat` for every commit (cost); (2) `files` is the CONCATENATION of each commit's affected files (duplicates when a file changes in several commits; statuses per commit, not the net diff) -> dedupe client-side; (3) no paging at all: a long branch returns every commit; (4) no diff content. Net diff of a range: no API endpoint (use a PR `.diff` or fetch commit diffs).
Compact: `{total_commits, commits:[{sha8, msg1, author, date}] (cap N), files: unique filenames with merged status}`.

---------------------------------------------------------------------
## 6. Collaborators, teams, topics, webhooks, deploy keys, wiki

### 6.1 Collaborators (`reqToken`; list `reqAnyRepoReader`, mutating `reqAdmin`)
- `GET /collaborators` (page,limit; X-Total-Count only) -> `[User]` (no permission level in list; keep `login, full_name?, id`). 
- `GET /collaborators/{user}` -> 204 yes / 404 no (422 unknown user).
- `GET /collaborators/{user}/permission` -> `{permission (none|read|write|admin|owner), role_name, user}`. 403 unless caller is site admin, repo admin, or asks about self.
- `PUT /collaborators/{user}` body `{permission: read|write|admin}` (optional; default write) -> 204. 403 if user is org-owned repo owner / cannot add, 500 inactive user, 422 unknown user.
- `DELETE /collaborators/{user}` -> 204.
- Teams (org repos): `GET /teams`, `GET/PUT/DELETE /teams/{team}`.
- `GET /assignees`, `GET /reviewers` -> users that can be assigned/review (reqToken + reader).

### 6.2 Topics
- `GET /topics` -> `{"topics":["a","b"]}`. `PUT /topics` body `{topics:[...]}` REPLACES all (204; lowercased; invalid -> 422 `{invalidTopics:[...], message}`; max 25 topics, 35 chars each (setting, unverified)). `PUT /topics/{topic}` add one (204), `DELETE /topics/{topic}` (204). Mutations need repo admin. `GET /topics/search?q=` -> `{topics:[TopicResponse{id,topic_name,repo_count}]}` (global).

### 6.3 Webhooks (`reqToken`+`reqAdmin`+webhooks enabled; read:repository / write:repository)
- `GET /hooks` (`page,limit`; X-Total-Count), `GET /hooks/{id}`, `POST /hooks`, `PATCH /hooks/{id}`, `DELETE /hooks/{id}` (204), `POST /hooks/{id}/tests?ref=` (204).
- `Hook`: `id, type, url? (inside config), config{url,content_type,secret(never returned),...}, events[], branch_filter, active, authorization_header (returned masked?), updated_at, created_at`. Compact: `{id, type, url, events, active, branch_filter, content_type}`.
- Create body: `type` (enum `forgejo, gitea, gogs, slack, discord, dingtalk, telegram, msteams, feishu, wechatwork, packagist`), `config` (map<string,string>, REQUIRED; must include `url` (valid URL) and `content_type` (`json` or `form`); optional `secret`; slack also needs `channel` (+`username`,`icon_url`,`color`)), `events[]`, `branch_filter` (glob), `authorization_header`, `active` (default **false** - an omitted `active` creates an INACTIVE hook!).
  Events (names accepted by the handler; list from code references): `create, delete, fork, push, repository, wiki, release, package, issues (alias group), issue_assign, issue_label, issue_milestone, issue_comment, pull_request (group), pull_request_assign, pull_request_label, pull_request_milestone, pull_request_comment, pull_request_review (group), pull_request_review_request, pull_request_sync, action_run_failure, action_run_success, workflow_run_*/workflow_job_* (blocked, cancelled, failure, running, skipped, success, waiting)`. The special values `issues_only` / `pull_request_only` select just the open/close/edit events. Empty `events` -> `["push"]`.
- **PATCH gotcha (verified in `editHook`)**: it rebuilds the event set from the request every time: omitting `events` RESETS events to `["push"]`; omitting `authorization_header` CLEARS the stored header; omitting `branch_filter` clears it. Only `active` and `config.url/content_type` are truly optional. Edit also does not touch the `action_run_*`/`workflow_*` flags (cannot be changed via PATCH, only set at create). MCP edit tool must GET first and merge (read-modify-write), re-sending `events`, `branch_filter`, `authorization_header`.
- `POST /hooks/{id}/tests`: queues a synthetic PUSH payload for the HEAD of `ref` (default `refs/heads/<default>`). Returns 204 even if delivery fails; 204 and does nothing on an empty repo. There is **no API to list hook deliveries/results** in this version (verified: no `deliveries` route) - result is only visible in web UI (`/settings/hooks/{id}`).
- Server-side git hooks (`/hooks/git`, `/hooks/git/{id}`) exist but need `reqGitHook` = site admin + git hooks enabled; skip.

### 6.4 Deploy keys (`reqToken`+`reqAdmin`)
- `GET /keys?key_id=&fingerprint=&page=&limit=` (X-Total-Count), `GET /keys/{id}`, `POST /keys` body `{title, key (public key text), read_only (default FALSE -> read/write key)}` -> 201 `DeployKey{id,key_id,key,url,title,fingerprint,created_at,read_only,repository}`. 422 on duplicates ("already been added", "used as non-deploy key", "title has been used"), invalid key, SSH disabled. `DELETE /keys/{id}` -> 204 (403 if not this repo's key).
- Compact: `{id, title, fingerprint, read_only, created_at}` (drop `key` body, `repository`, `url`, `key_id`).

### 6.5 Wiki (`mustEnableWiki`; reads open, writes need wiki write + not archived)
- `GET /wiki/pages` (`page`,`limit`) -> `[WikiPageMetaData{title, html_url, sub_url, last_commit{sha,author,committer,message}}]`; compact `{title, sub_url, updated: last_commit.committer.date}`.
- `GET /wiki/page/{pageName}` -> `WikiPage{title, html_url, sub_url, content_base64, commit_count, sidebar, footer, last_commit}`. `sub_url` is the stored (URL-escaped) name; page names with spaces/dashes: pass the `sub_url` value. Compact: decode `content_base64` to text, drop sidebar/footer/html_url.
- `POST /wiki/new` and `PATCH /wiki/page/{pageName}` body `{title, content_base64 (base64), message}` (default message `Add "<title>"` / `Update "<name>"`). 201 / 200 WikiPage. 400 for empty title, reserved name, already exists, bad base64. PATCH with a different `title` renames the page. 403 when wiki write missing.
- `DELETE /wiki/page/{pageName}` -> 204. `GET /wiki/revisions/{pageName}` -> `{commits:[WikiCommit], count}` (`page` only, paging fixed).
- `GET /languages` -> object `{"Go":123456,"JS":2300}` (bytes per language, ordered by size).

---------------------------------------------------------------------
## 7. Notable cross-endpoint gotchas for MCP implementation
1. **Empty repos** (`empty:true`): `/branches` -> `[]`, `/commits` -> 409, `POST /branches` -> 404, `POST /releases` -> 422, `PUT /contents/..` -> 422; `POST /contents/{path}` and `POST /contents` DO work and create the first commit (initial push). Check `GET /repos/{o}/{r}` `empty` first when relevant.
2. Blank 500 messages in production: surface "server error (message hidden)" plus the endpoint, suggest likely causes from this doc (unmapped bad ref, mirror write, read-permission edge).
3. Polymorphic JSON: contents (obj/array), git refs (obj/array), search (`{ok,data}` wrapper) - normalise in the MCP.
4. Partial-update semantics differ: repo PATCH, branch protection PATCH, tag protection PATCH = proper partial; webhook PATCH = replace-ish (reset events/auth header); release PATCH = skips empty strings.
5. All "name in path" lookups (branch protection `{name}`, push mirror `{name}`, wiki `{pageName}`) take the rule/remote/sub_url name, not an id; tag protections, hooks, keys, release assets use numeric ids.
6. Pagination signals: use `X-Total-Count` (present nearly everywhere) and `Link rel=next`; in commits use `X-HasMore`. Never page through everything by default: return the first page + `total` + `next_page`.
7. ETag/If-None-Match works on raw/media; commit/branch lists send `Cache-Control: max-age=0` (no benefit).
8. Rate/size: no server rate limit for tokens by default; mutating content endpoints enforce quota (413 `quotaExceeded`) and `423` for archived repos.
9. DELETE with body (contents), PUT/POST with empty body returning 204/200: do not JSON-parse an empty body.
10. Case/escaping: owner/repo are case-insensitive; branch names with `/` must be path-escaped as `a%2Fb` or left as `a/b` (both work for `*` routes); but `GET /commits?sha=` takes the plain name.

---------------------------------------------------------------------
## 8. Recommended MCP tools (repository area)

Design rules: one tool per intent not per endpoint (a few "action" tools beat 100 tools in the schema); every list tool has `limit` (default 20), `page`, returns `{total, page, next, items}`; every tool returns a compact text/JSON (see projections above); `repo` param is `"owner/name"` string (default repo can be taken from env/config); mutating tools return 1-line confirmation (+ key ids/shas); bodies > N bytes are truncated with `truncated:true,bytes`.

### Repository
- `repo_get(repo)` -> `{full_name, desc, private, fork(parent), archived, mirror, empty, default_branch, size_kb, stars, forks, open_issues, open_prs, topics, perms, urls{clone,ssh,html}}`.
- `repo_list(scope: "mine"|"user"|"org"|"starred"|"search", owner?, q?, mode?, archived?, sort?, order?, page, limit)` -> rows `full_name | private? | archived? | updated | desc(80 chars)`. Maps to `/user/repos`, `/users/{u}/repos`, `/orgs/{o}/repos`, `/repos/search` (unwraps `{ok,data}`).
- `repo_create(name, owner?, description?, private?, auto_init?=true, readme?, license?, gitignores?, default_branch?, template?, from_template?, org?)` -> user/org create or `generate` when `from_template` set. Default `auto_init=true` to avoid empty repos.
- `repo_edit(repo, ...EditRepoOption fields flattened incl. archived, default_branch, name, merge settings)` -> list of changed fields. Warn it is non-atomic.
- `repo_delete(repo, confirm: "owner/name")` - require typed confirmation.
- `repo_fork(repo, org?, name?)`, `repo_transfer(repo, new_owner, team_ids?)` + `repo_transfer_respond(repo, accept: bool)`.
- `repo_migrate(clone_addr, repo_name, owner?, service?, mirror?, private?, auth_token?, include{wiki,issues,pull_requests,releases,labels,milestones,lfs}, interval?)` (long timeout).
- `repo_mirror(repo, action: "pull_sync"|"push_list"|"push_add"|"push_delete"|"push_sync_all"|"sync_fork"|"fork_status", ...)`.
- `repo_topics(repo, set?: string[])` (get when omitted, replace when given).
- `repo_languages(repo)`.

### Branches / tags / protection
- `branch_list(repo, limit, page)` -> `name | sha8 | date | protected | msg1`. (No server-side filter: do a client-side `q` substring filter over a few pages, flagged as such.)
- `branch_get(repo, branch)`.
- `branch_manage(repo, action: "create"|"delete"|"rename", name, from?, new_name?)` (`from` = any ref; map to `old_ref_name`).
- `branch_protection(repo, action: "list"|"get"|"create"|"edit"|"delete", rule?, settings?)` with settings as the BranchProtection fields; list/get output shows only non-default flags.
- `tag_list(repo)`, `tag_manage(repo, action: "create"|"delete"|"get", tag, target?, message?)`, `tag_protection(repo, action, id?, pattern?, users?, teams?)`.

### Releases
- `release_list(repo, draft?, prerelease?, q?, limit, page)` -> `id | tag | name | flags | date | n_assets`.
- `release_get(repo, id|tag|"latest")` -> release + assets, body truncated to 2000 chars unless `full:true`.
- `release_create(repo, tag, name?, body?, target?, draft?, prerelease?)`, `release_edit(repo, id|tag, ...)` (note: cannot clear strings), `release_delete(repo, id|tag)`.
- `release_asset(repo, release, action: "upload"|"list"|"rename"|"delete"|"download", file_path?|url?, name?, asset_id?, dest?)`: upload with raw-body mode (`?name=`) streamed from a local file path (avoid sending bytes through the model); download to a local path via `browser_download_url`.

### Files and content
- `file_read(repo, path, ref?, max_bytes=20000, start_line?, end_line?, grep?)` -> text (via `/raw` with ETag cache or `/contents`), reports `sha` (blob sha) + `truncated`. Directory path -> compact listing `type name size`. Binary -> `{binary:true,size}` only.
- `tree(repo, ref?, path?, recursive?=false, depth?, glob?, limit)` -> from `/git/trees/{ref}?recursive=true` filtered client-side to `path` prefix/glob; output one path per line.
- `file_write(repo, path, content (text) | content_base64, message, branch?, new_branch?, sha?, from_path?)` -> auto-detect create vs update (GET sha first when not provided, or try POST then PUT); returns `commit sha8` + `blob sha`.
- `files_commit(repo, message, branch?, new_branch?, ops:[{op:"create"|"update"|"delete"|"rename", path, content?, from_path?}])` -> multi-file commit via `POST /contents`; the tool fills `sha` for update/delete by one `GET /git/trees?recursive` lookup or per-file `GET /contents` (cache shas). Return commit sha + changed file list.
- `file_delete(repo, path, message, branch?)` (fetches sha).
- `patch_apply(repo, diff, message, branch, new_branch?)` -> `POST /diffpatch` (always send branch; plain-text diff, sha dummy).
- `archive_download(repo, ref, format: zip|tar.gz|bundle, dest)` -> writes file, returns path/bytes.

### Commits and diffs
- `commit_list(repo, ref?, path?, not?, limit=20, page, files?=false, stat?=false)` -> `sha8 date author msg1 [+a/-d]` lines, always sending `stat=false&files=false&verification=false` unless asked; footer `total, has_more`.
- `commit_get(repo, sha|ref, diff?: "none"|"stat"|"patch", path?, max_bytes?)`: `none` = metadata+changed files; `stat` = files + stats; `patch` = `.diff` truncated, optionally filtered to a single file (client-side split on `diff --git`).
- `compare(repo, base, head, detail?: "summary"|"commits"|"files")` -> total commits, unique files with status, first N commit msg1s; warn that it is per-commit aggregated.
- `git_ref(repo, ref?)` for ref/sha resolution (`/git/refs/{ref}`, normalised to array), `blob_get(repo, sha)`.

### Settings / access
- `collaborator(repo, action: "list"|"add"|"remove"|"permission", user?, permission?: read|write|admin)`.
- `webhook(repo, action: "list"|"get"|"create"|"edit"|"delete"|"test", id?, type?, url?, content_type?, secret?, events?, branch_filter?, active?)`. `create` defaults `active=true` (server default false). `edit` is implemented as GET -> merge -> PATCH with full `events`, `branch_filter`, `authorization_header`. `test` returns "queued (204); delivery result is not available via API".
- `deploy_key(repo, action: "list"|"add"|"delete", id?, title?, key?, read_only?=true)` (invert server default to read-only for safety).
- `wiki(repo, action: "list"|"get"|"create"|"edit"|"delete"|"history", page?, content?, message?, new_title?)` - encode/decode base64 inside the tool.

Token budget guidance: expose ~25 tools of this area at ~150 tokens each (short param descriptions, enums for `action`); everything not in this list (git notes, flags, avatar, editorconfig, git hooks, issue templates, pins) is reachable via a generic `forgejo_api(method, path, query?, body?, jq?)` escape hatch with a JSON-path/field projection argument and automatic `X-Total-Count` surfacing.
