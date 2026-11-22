# Forgejo v17.0.2 API: platform-wide findings

Source: sparse checkout (routers/api/v1/**, routers/api/shared, services/context, services/convert, modules/structs, swagger tmpl).
NOT in the checkout (so marked "unverified/from memory"): models/auth (scope enum), modules/setting (defaults), services/auth (auth methods), models/user|repo|quota.
Base URL: `{ROOT_URL}/api/v1` (swagger basePath = AppSubUrl + /api/v1). Second tiny API: `/api/forgejo/v1`.
Swagger: 512 ops in `templates/swagger/v1_json.tmpl`. Served at `/swagger.v1.json` (and `/api/swagger` HTML) only if `[api] ENABLE_SWAGGER`.

---------------------------------------------------------------------------------------------------

## 1. Authentication

Middleware order (routers/api/shared/middleware.go): securityHeaders -> CORS (optional) -> APIContexter -> checkDeprecatedAuthMethods -> apiAuthentication(authGroup) -> apiAuthorization -> verifyAuthWithOptions -> [per-route: sudo, tokenRequiresScopes, reqToken, ...].

Auth group, tried in order: OAuth2 (JWT/oauth token), HTTPSign, AccessToken (Basic+Bearer permitted), ActionRuntimeToken, ActionTaskToken (Basic+Bearer), AuthorizedIntegration, Basic (username/password; "FIXME should be removed"), ReverseProxy (only if ENABLE_REVERSE_PROXY_AUTHENTICATION_API).

Accepted forms (header):
- `Authorization: token <PAT>` (documented, swagger `AuthorizationHeaderToken`)  <- use this one
- `Authorization: Bearer <PAT | OAuth2 JWT | actions job token>` (case-insensitive scheme; tests use `bearer`)
- `Authorization: Basic base64(user:pass)`; also Basic with username `token` and the PAT as password (used in tests)
- 2FA with Basic password login: header `X-Forgejo-OTP: <totp>` (`X-Gitea-OTP` still accepted/CORS-allowed). PAT/OAuth auth bypass 2FA.
- Query `?token=` / `?access_token=` : DEPRECATED. checkDeprecatedAuthMethods only sets response header `Warning: token and access_token API authentication is deprecated ... Existing queries will continue to work but without authorization`. Treat as NOT authenticating on v17. Never use; also leaks into logs.
- Wrong/invalid credentials: `AuthenticationAttemptedIncorrectCredential` -> 401 `{message}`. No credentials at all -> anonymous request (not an error) and then individual routes demand `reqToken` -> 401 `{"message":"token is required"}` (title `reqToken`).
- `ReqToken` also passes for an actions task token (ActionsTaskID set) even when not "signed".
- Gate checks after authentication (verifyAuthWithOptions), all return 403 JSON `{message}`:
  - inactive + REGISTER_EMAIL_CONFIRM: "This account is not activated."
  - inactive or ProhibitLogin: "This account is prohibited from signing in, please contact your site administrator."
  - MustChangePassword: "You must change your password. Change it at: <AppURL>/user/change_password"
  - user MustHaveTwoFactor and none set: 403 localized "must_enable_2fa"
  - `REQUIRE_SIGNIN_VIEW=true` and anonymous: 403 `"Only signed in user is allowed to call APIs."` for EVERY endpoint including /version.

### Sudo (admin impersonation)
`?sudo=<username>` or header `Sudo: <username>` (query wins if both). Group-level middleware `sudo()` wraps ALL v1 routes. Requires `IsSigned && Doer.IsAdmin`, else 403 `{"message":"Only administrators allowed to sudo."}`; unknown target user -> 404. `ctx.SetDoer(target)`: from then on permissions are evaluated as the target (so admin routes need target to be admin). Token scope check still uses the original token (scope object comes from `ctx.Authentication()`). Sudo is a generic "act as" and avoids minting tokens.

### Token model (v15+ fine-grained tokens)
`AccessToken{id,name,sha1(only on create),token_last_eight,scopes[],created_at,repositories[]|null}`.
- Create: `POST /users/{username}/tokens` body `{name (required, unique per user -> else 400 "access token name has been used already"), scopes:[...] (required non-empty after Normalize, else 400 "access token must have a scope"), repositories:[{owner,name}] (optional; non-null => token limited to those repos, ResourceAllRepos=false; repo must be readable by creator else 400 "repository o/n does not exist")}`. Response 201 with `sha1` = the secret (only time it is shown).
- IMPORTANT: user token create/delete require `reqBasicOrRevProxyAuth` = password login (Basic with real password, +OTP header if 2FA). A PAT cannot create a PAT via `/users/{u}/tokens` (401 "auth method not allowed"). Admin alternative: `POST /admin/users/{username}/tokens` works with an admin PAT (`write:admin`) and returns the secret.
- List: `GET /users/{username}/tokens` (reqSelfOrAdmin; any auth method), paginated, X-Total-Count. Delete: `DELETE /users/{username}/tokens/{token}` where `{token}` = numeric id, or name (422 "multiple matches for token name" if ambiguous). Delete needs password auth too (admin variant: `/admin/users/{u}/tokens/{token}`).
- Repo-restricted token (`SpecificReposAuthorizationReducer`): only listed repos visible; `IsUserSiteAdmin` returns false when `Reducer.AllowAdminOverride()` is false (so admin override does not apply; assume admin endpoints fail 403 for such tokens).
- `public-only` modifier scope: `APIAuthorization` sets publicOnly; `checkTokenPublicOnly` then 403s private repo/issue/org/user/package/notification resources: messages `token scope is limited to public repos|issues|orgs|users|activitypub|notifications|packages`. `GET /orgs` hides limited/private orgs for such tokens.

### Scopes (models/auth not in checkout; reconstructed from api.go + permission tests + swagger example)
Categories: `activitypub, admin, misc, notification, organization, package, issue, repository, user`. Scope strings: `read:<cat>`, `write:<cat>`, plus `all`, `public-only` (comma separated in DB, array in API; `Normalize()` dedups and collapses; unknown -> 400 "invalid access token scope provided: ..."). `write:X` includes read of X; `all` = write on every category (admin included).
Mapping rule (`requiredScopeLevel`): HTTP POST/PUT/PATCH/DELETE -> needs `write:<cat>`; everything else (GET/HEAD/...) -> `read:<cat>` OR `write:<cat>`. When multiple categories listed (`tokenRequiresScopes(a,b)`), token needs at least one of them (error text "token does not have at least one of required scope(s): [...]"). `tokenRequiresScopes` is a no-op when auth is not a scoped token (Basic password, actions token), so scopes only restrict PATs/OAuth.
Failure: 403 `{"message":"token does not have at least one of required scope(s): [read:user write:user]"}` (helpful: message lists acceptable scopes).
Nested scope checks stack (each wrapper is a separate check, all must pass): `/user/repos` and `/user/starred` sit inside the `/user` group (needs user scope) AND carry their own `repository` check, so they need BOTH user and repository scopes; `POST /orgs/{org}/repos` sits under the org group (organization scope). `POST /repos/.../` repo creation additionally runs `CheckRuntimeDeterminedScope` (write:organization for org-owned, write:user for user-owned; the user-owned message text is mis-worded); `DELETE /repos/{o}/{r}` needs write:user or write:organization depending on owner type (`tokenRequiresRepoOwnerScope`).

Endpoint -> scope category map for this area:
| Endpoints | Category |
|---|---|
| `/user/**` (all, incl. /user/actions, /user/hooks, /user/keys, /user/emails, /user/settings, quota, avatar, block), `/users/{u}`, `/users/search`, `/users/{u}/{tokens,keys,gpg_keys,followers,following,starred,subscriptions,heatmap,activities/feeds}` | user |
| `/users/{u}/repos`, `/user/repos`, `/user/starred/**` (GET list also), `/repositories/{id}`, `/topics/search`, `/repos/**` | repository |
| `/user/orgs`, `/users/{u}/orgs`, `/users/{u}/orgs/{org}/permissions` | user OR organization |
| `/orgs`, `/orgs/**`, `/teams/**` | organization |
| `POST /org/{org}/repos` (deprecated) | organization OR repository |
| `/packages/**` | package |
| `/notifications/**`, `/repos/{o}/{r}/notifications` | notification |
| `/admin/**` | admin (+ `reqSiteAdmin`) |
| `/activitypub/**`, `/nodeinfo` | activitypub (only registered when federation enabled) |
| `/version`, `/settings/*`, `/signing-key.*`, `/gitignore/*`, `/licenses*`, `/label/templates*`, `/markdown*`, `/markup`, `/actions/run` | none (misc: no scope) |
| `/repos/**/issues*`, labels, milestones | issue |

### Per-route permission middleware you will hit as 401/403
`reqToken` (401 token is required), `reqSiteAdmin` (403 "user should be the site admin"), `reqSelfOrAdmin` (403 "doer should be the site admin or be same as the contextUser"), `reqOrgOwnership`, `reqOrgMembership`, `reqTeamMembership`, `reqAdmin` (repo), `reqWebhooksEnabled` (403 "webhooks disabled by administrator"), `reqExploreSignIn` (401 "you must be signed in to search for users" when REQUIRE_SIGNIN_VIEW/explore REQUIRE_SIGNIN_VIEW), `reqUsersExploreEnabled` (404 if explore users page disabled -> `/users/search` is 404), `individualPermsChecker` (private/limited users -> 404 for anonymous/other), `doerCanCreateOrganization` (403 "Create organization not allowed").

---------------------------------------------------------------------------------------------------

## 2. Response conventions

### Errors
- Generic (`ctx.Error`): `{"message": "<text>", "url": "<swagger URL from [api]>"}`. `url` is noise: drop it.
- 404 (`ctx.NotFound`): `{"message":"The target couldn't be found.","url":..., "errors":[...]}` (localized `error.not_found`; `errors` = underlying err strings).
- Validation 422 (binding failure): `{"message":"[Field]: <reason>"}` - format is `fmt.Sprintf("%s: %s", errs[0].FieldNames, errs[0].Error())`, i.e. `[Name]: ...`; only the FIRST error. Malformed JSON also lands here.
- 500: message is EMPTY in production for non-admin callers (`setting.IsProd && !admin`); admins and dev get text. Also 500s for things that are really 4xx (see quirks).
- Some handlers return non-standard error shapes: `/users/search` and `/orgs/{o}/teams/search` return `{"ok":false,"error":"..."}` on 500.
- Status code reference: 201 create, 204 no body (delete/put/star/follow/cron run/etc.), 205 with BODY for notification mark-read (`PUT /notifications`, `PATCH /notifications/threads/{id}`; body is JSON list/object, RESET CONTENT but not empty), 301 redirect for renamed repos (GET path rewrite; client re-sending POST/PUT after 301 may downgrade to GET!), 307 for renamed users/orgs, 303-style redirect for `GET /orgs/{org}/members/{u}` (see below), 409 `InviteOrAddTeamMember` already-invited, 413 quota exceeded (`quotaExceeded`).
- Responses come as `application/json;charset=utf-8`; markdown endpoints return `text/html` body (not JSON).
- Request body JSON must be sent with `Content-Type: application/json` (go-chi/binding picks decoder by content type).
- Time: RFC3339 strings. `since/before` query params must be RFC3339 else 422 (`GetQueryBeforeSince`).

### Pagination (services/context/api.go, routers/api/v1/utils/page.go, services/convert/utils.go)
- Query `page` (1-based; 0/missing -> first page) and `limit`.
- `limit` normalization (`ToCorrectPageSize`): `<=0` -> `setting.API.DEFAULT_PAGING_NUM`; `> setting.API.MAX_RESPONSE_ITEMS` -> clamped silently to max. Defaults in stock config (modules/setting, unverified): DEFAULT_PAGING_NUM=30, MAX_RESPONSE_ITEMS=50, DEFAULT_GIT_TREES_PER_PAGE=1000, DEFAULT_MAX_BLOB_SIZE=10485760. Discover real values at runtime: `GET /settings/api` -> `{max_response_items, default_paging_num, default_git_trees_per_page, default_max_blob_size}` (no auth needed). Admin can raise/lower, so always read them.
- `GET /repos/issues/search` is special (repo/issue.go): missing limit -> `setting.UI.IssuePagingNum` (UI value, often 10!), clamp to MAX_RESPONSE_ITEMS. So always pass `limit`.
- Headers: `X-Total-Count` (via `SetTotalCountHeader`, exposed through Access-Control-Expose-Headers) is the universal one; set on essentially every list endpoint in this area. `Link: <url>; rel="next",<url>; rel="last",<..>; rel="first",<..>; rel="prev"` (via `SetLinkHeader`, only on a subset: `/users/search`, repo lists, `/orgs` , `/user/orgs`, orgs/{o}/teams/search, admin users/orgs/emails, packages list, quota lists; NOT on `/orgs/{o}/members`, `/orgs/{o}/teams`, `/teams/{id}/*`, `/user/teams`, `/user/keys`, tokens, cron, admin hooks, unadopted, notifications). Link URLs are built from `setting.AppURL` + request URI (breaks behind proxy with wrong ROOT_URL) -> do NOT follow Link; compute next page yourself: `has_more = page*limit < total`.
- `X-HasMore`, `X-Page`, `X-PerPage`, `X-PageCount`, `X-Total` (compat headers) appear only on `GET /repos/{o}/{r}/commits` and pulls files/commits lists.
- Page beyond the end -> 200 `[]` (no error). Out-of-range/negative page -> treated as page 1.
- Some lists have NO pagination: `/user/emails`, `/admin/users/{u}/emails`, `/admin/quota/*` lists, `/admin/quota/groups/{g}/users`, `/packages/.../files`, `/orgs/{org}/members/...` single checks. `/admin/cron` paginates in-memory (`PaginateSlice`) but returns all with X-Total-Count.
- Post-filtering quirk: `/users/{u}/repos` and `/orgs/{org}/repos` fetch a page by owner then DROP entries the doer cannot access (`permission.HasAccess()`), so a page can contain fewer than `limit` items while X-Total-Count still counts all; do not treat short page as last page. Order is `id ASC`.
- `GET /user/repos` uses `OwnerID=doer, Actor=doer` (SearchRepository; includes repos where the user collaborates / via teams, per Gitea behaviour; unverified in checkout) with `order_by` enum (`name|id|newest|oldest|recentupdate|leastupdate|reversealphabetically|alphabetically|reversesize|size|reversegitsize|gitsize|reverselfssize|lfssize|moststars|feweststars|mostforks|fewestforks`; unknown -> 422 "invalid order_by"); includes descriptions.

### Rate limiting
None in the API code (grep for ratelimit/429/Retry-After: no matches in routers/services/models of the checkout). Any 429 would come from a reverse proxy (nginx/Traefik/Cloudflare); MCP should honor `Retry-After` generically with a small backoff and not hammer.

### Other headers
- Request: `Authorization`, `Sudo`, `X-Forgejo-OTP`/`X-Gitea-OTP`, `Accept: application/json`.
- Response: `X-Total-Count`, `Link`, `Warning` (deprecated query token), `X-Gitea-Warning` (admin create/edit user: email domain conflicts with allow/blocklist; response still 201/200), `X-Forgejo-Object-Type`+`X-Gitea-Object-Type` (raw file/contents), `x-content-type-options: nosniff`, `Cache-Control` no-cache-ish (`SetCacheControlInHeader(0)`), `X-Frame-Options`. Nothing Forgejo-specific is *required* from clients.
- gzip on /api: not found in router code (only web routes use gzipMid); depends on proxy. Send `Accept-Encoding: gzip` anyway (Node fetch does).

---------------------------------------------------------------------------------------------------

## 3. Server version and capability detection

- `GET /api/v1/version` -> `{"version":"17.0.2+gitea-1.22.0"}` (= `setting.AppVer` = `Version` ldflag; Makefile: `FORGEJO_VERSION` from VERSION file / git describe; a `+gitea-1.22.0` compat suffix is appended; dev builds look like `17.0.2-12-abcdef+gitea-1.22.0`). Parse with `^v?(\d+)\.(\d+)\.(\d+)` and ignore the rest; tolerate suffix absent. Auth not needed unless REQUIRE_SIGNIN_VIEW=true (then 403).
- `GET /api/forgejo/v1/version` -> `{"version":"1.0.0"}`: this is the version of the Forgejo-specific API (`ForgejoVersion` var in main.go, default "1.0.0"), NOT the server version. `GET /api/forgejo/v1` -> 204 with `Link: </assets/forgejo/api.v1.yml>; rel="service-desc"` (OpenAPI yaml of that API). Useful only to know the sub-API exists; spec at `/assets/forgejo/api.v1.yml`. No other endpoints (only /version).
- Also: `/api/v1/nodeinfo` (software.name=="forgejo", software.version=AppVer, `openRegistrations`, usage counters if ShareUserStatistics) - ONLY registered when federation is enabled, else 404. `/swagger.v1.json` `info.version`=AppVer (only if swagger enabled). `/api/healthz` (web route, health check JSON). Detect Forgejo vs Gitea: nodeinfo software.name, or presence of `/api/forgejo/v1`.
- Runtime feature probes (all cheap GETs, cache once per session):
  - `GET /settings/api|repository|ui|attachment` (public): `repository` -> `{mirrors_disabled, http_git_disabled, migrations_disabled, stars_disabled, forks_disabled, time_tracking_disabled, lfs_disabled}`; `attachment` -> `{enabled, allowed_types, max_size, max_files}`; `ui` -> `{default_theme, allowed_reactions[], custom_emojis[]}`.
  - Routes conditionally registered (404 when disabled): `/user/quota*`, `/orgs/{o}/quota*`, `/admin/quota/*`, `/admin/users/{u}/quota*` (quota disabled), `/users/{u}/heatmap` (heatmap disabled), `/user/starred*` and `/users/{u}/starred` (DisableStars), `/nodeinfo`, `/activitypub/**`, `/user/activitypub/follow` (federation off), `/swagger` redirect. Hooks: 403 when webhooks disabled.
  - Actions availability for MCP: `/api/actions*` mounted only if Actions enabled; the `/repos/.../actions/*` endpoints are in another report.
- Adapt MCP by `major` version: this report targets v17. Treat unknown endpoints (404 with generic message) as "not available on this server". Scope enum and fine-grained repo tokens exist in this version (older servers: no `repositories` in token create).

---------------------------------------------------------------------------------------------------

## 4. Endpoints

Legend: scope `U`=user, `O`=organization, `R`=repository, `P`=package, `N`=notification, `A`=admin (read:X for GET, write:X for others). Pagination "pg" = page/limit with X-Total-Count; "pgL" = also Link header. Path vars shown as in code. "keep" = recommended compact projection.

### 4.1 Misc / platform (no scope, no auth except noted)
| Endpoint | Notes |
|---|---|
| GET /version | `{version}` (see section 3) |
| GET /settings/{api,attachment,repository,ui} | see section 3; only these 4 |
| GET /nodeinfo | federation only; keep software.version, openRegistrations, usage.users.total |
| GET /signing-key.gpg, /signing-key.ssh | text/plain public instance signing key (default) ; repo-specific variants exist under /repos |
| GET /gitignore/templates ; /gitignore/templates/{name} | list = string[]; item `{name, source}` |
| GET /licenses ; /licenses/{name} | list `[{key,name,url}]`; item adds `implementation, body` (long, avoid) |
| GET /label/templates ; /label/templates/{name} | list string[]; item = array of `{name,color,description,exclusive}` |
| POST /markdown | needs `reqToken`. body `{Text, Mode: markdown|gfm|comment (anything else -> "markdown"), Context (repo path for gfm links, e.g. "owner/repo" or URL), Wiki}` NOTE: struct has no json tags -> keys are `Text`,`Mode`,`Context`,`Wiki` (Go case-insensitive decode, lowercase also works). Response `text/html`. Empty Text -> 200 empty body. |
| POST /markdown/raw | `reqToken`. Body = raw markdown text/plain (any content type), response HTML. |
| POST /markup | `reqToken`. body `{Text, Mode: markdown|comment|gfm|file, Context, Wiki, FilePath (needed for mode=file; ext picks renderer), BranchPath}`; unknown mode -> 422 `Unknown mode: x`. |
| GET /actions/run | ONLY with an automatic actions job token (`Authorization: Bearer ${{ forgejo.token }}`); else 403 "must use an automatic actions token". Returns ActionRun of the running job. |
| GET /swagger | 302 to /api/swagger if swagger enabled |

### 4.2 User endpoints
Current user (all scope U, `reqToken`):
| Endpoint | Key params / body | Response keep |
|---|---|---|
| GET /user | - | `id, login, full_name, email, is_admin, active, visibility, created, followers_count, following_count, starred_repos_count` (drop avatar_url, html_url, language, last_login, login_name, source_id, restricted, prohibit_login, location, pronouns, website, description unless asked). Duplicate `username` field exists (compat) |
| GET/PATCH /user/settings | PATCH body (all optional): `full_name, website, description, location, pronouns, language, theme, diff_view_style, enable_repo_unit_hints, hide_email, hide_pronouns, hide_activity` | whole object small |
| GET/POST/DELETE /user/emails | POST `{emails:[..]}` 201 returns full list; DELETE body `{emails:[..]}` (DELETE with JSON body!) 204/404 | keep `email, primary, verified`. Not paginated |
| GET /user/keys, GET/DELETE /user/keys/{id}, POST /user/keys | list q `fingerprint`; POST `{title, key, read_only}` (required: key,title) | keep `id,title,fingerprint,key_type,read_only,created_at` (drop `key`, `url`, `user`). scope `write:user`; comments say admin:public_key but code uses U |
| GET/POST /user/gpg_keys, GET/DELETE /user/gpg_keys/{id}, GET /user/gpg_key_token, POST /user/gpg_key_verify | POST `{armored_public_key, armored_signature}` | rarely needed; keep id, key_id, emails[].email, can_sign, expires_at |
| GET/POST /user/applications/oauth2, GET/PATCH/DELETE /user/applications/oauth2/{id} | POST/PATCH `{name, redirect_uris[], confidential_client}`; POST returns `client_secret` once | keep `id,name,client_id,redirect_uris` |
| GET /user/repos (R) | page, limit, order_by (enum above) | repo projection (4.6) |
| POST /user/repos (R) | CreateRepoOption (repo report) |  |
| GET /user/starred; GET/PUT/DELETE /user/starred/{owner}/{repo} (R) | list pg; 204 if starred/404 not | repo projection. PUT/DELETE 204; disabled if STARS disabled |
| GET /user/subscriptions | watched repos pg | repo projection |
| GET /user/orgs (U or O) | pg; X-Total-Count + Link | org projection |
| GET /user/teams | pg X-Total-Count only; each team embeds `organization` | `id,name,permission,organization.name` |
| GET /user/followers, /user/following ; GET/PUT/DELETE /user/following/{username} | pg; 204/404 | `login,id` |
| PUT /user/block/{username}, PUT /user/unblock/{username}, GET /user/list_blocked | 204; 422 if target is org | blocked list: `[{block_id,user{...}}]` |
| GET /user/times, GET /user/stopwatches | tracked time: q `since`,`before`; pg | issue-area agent |
| GET/POST /user/hooks, GET/PATCH/DELETE /user/hooks/{id} | needs webhooks enabled; CreateHookOption `{type (forgejo|gitea|gogs|slack|discord|dingtalk|telegram|msteams|feishu|wechatwork|packagist), config{url,content_type,secret?}, events[], branch_filter, authorization_header, active}` | `id,type,active,events,config.url` |
| POST/DELETE /user/avatar | body `{image: base64}` | rarely |
| GET /user/actions/runners, POST (register), GET /user/actions/runners/{id}, DELETE, GET registration-token, GET runners/jobs?labels=, secrets (PUT `{data}`/DELETE), variables (POST/PUT `{value}`, GET/DELETE) | actions agent. Scope U. Note secrets/variables list pg |
| POST /user/activitypub/follow | federation only |
| GET /user/quota, /user/quota/{check,attachments,packages,artifacts} | quota enabled only (see 4.8) |

Public/other users (scope U, mostly need reqToken via the group wrapper; `/users/{username}` and `/users/search` only `reqExploreSignIn`):
| Endpoint | Params | Notes |
|---|---|---|
| GET /users/search | `q` (matches username, full name AND email since SearchByEmail=true), `uid`, `sort` (oldest|newest|alphabetically|reversealphabetically|recentupdate|leastupdate; default alphabetically), page, limit | RESPONSE WRAPPED `{"ok":true,"data":[User...]}` + X-Total-Count + Link. `uid=-1` ghost / `-2` actions user special-cased. Lists only individuals (not orgs). Keep `login,id,full_name,email?,is_admin?` |
| GET /users/{username} | works for orgs too (returns User-shaped org). 404 if invisible | email shown only if signed in AND (not KeepEmailPrivate OR doer is self/admin); `is_admin, login_name, source_id, last_login, language, active, prohibit_login` only for self/admin |
| GET /users/{username}/repos (R) | pg | post-filter quirk; repo projection |
| GET /users/{username}/{followers,following,keys,gpg_keys,starred,subscriptions} | pg; `/following/{target}` 204/404; keys q `fingerprint` | keys public data |
| GET /users/{username}/orgs, GET /users/{username}/orgs/{org}/permissions | `{is_owner,is_admin,can_write,can_read,can_create_repository}` | private orgs only shown to self/admin |
| GET /users/{username}/activities/feeds | `only-performed-by`(bool), `date`(YYYY-MM-DD) pg | activity: `op_type, repo.full_name, ref_name, content, created, act_user.login, comment.body?` |
| GET /users/{username}/heatmap | `[{timestamp,contributions}]` large; heatmap must be enabled |  |
| GET/POST /users/{username}/tokens, DELETE /users/{username}/tokens/{token} | reqSelfOrAdmin; POST/DELETE need password Basic (see 1) | list keep `id,name,scopes,token_last_eight,created_at,repositories[].full_name` |

### 4.3 Organizations and teams (scope O unless noted; all org routes run `orgAssignment` -> 404 if org missing/invisible; renamed org -> 307)
| Endpoint | Perm | Params / body | Notes |
|---|---|---|---|
| GET /orgs | O | pg+Link | visibility: anonymous/`public-only` -> public only; signed -> public+limited; site admin -> all (private incl.). Alphabetical order |
| POST /orgs | doerCanCreateOrganization | `{username*(Username;max40), full_name, email, description, website, location, visibility: public|limited|private (default public), repo_admin_change_team_access}` | 201 Organization; 422 name taken/reserved |
| GET /orgs/{org} | visible | - | `email` blanked for anonymous. keep `id,name,full_name,visibility,description,website,location,email` |
| PATCH /orgs/{org} | org owner | `{full_name,email ("" deletes),description,website,location,visibility,repo_admin_change_team_access}` | 200 Org |
| DELETE /orgs/{org} | owner | - | 204; failures surface as 500 (no 422 mapping in handler) |
| POST /orgs/{org}/rename | owner | `{new_name}` | 204 |
| GET/POST /orgs/{org}/repos | member (POST: any signed doer allowed by service; quota enforced) | list pg + Link (post-filter quirk); POST = CreateRepoOption | repo projection |
| GET /orgs/{org}/members | reqToken | pg X-Total-Count only | caller not a member -> only PUBLIC members listed; member -> all |
| GET /orgs/{org}/members/{u} | reqToken | 204 member / 404; if doer not org member -> 302 redirect to `/orgs/{org}/public_members/{u}` (HTTP client following redirects gets 204/404 from that). 303 per swagger |
| DELETE /orgs/{org}/members/{u} | owner | 204 |
| GET /orgs/{org}/public_members(/{u}) ; PUT/DELETE /orgs/{org}/public_members/{u} | PUT/DELETE: org member (self-service publicize/conceal; 403 otherwise) | 204 |
| GET /orgs/{org}/teams | reqToken+org member | pg X-Total-Count; teams WITHOUT embedded org | |
| GET /orgs/{org}/teams/search | member | q, include_desc (default true when param empty), pg+Link | RESPONSE WRAPPED `{"ok":true,"data":[Team]}`; non-admins only see teams they belong to (UserID filter) |
| POST /orgs/{org}/teams | owner | `{name*(AlphaDashDot,<=255), description, includes_all_repositories, permission: read|write|admin, units[] (deprecated; e.g. repo.code, repo.issues, repo.pulls, repo.releases, repo.wiki, repo.ext_wiki, repo.ext_issues, repo.projects, repo.actions, repo.packages), units_map{unit: none|read|write|admin|owner}, can_create_org_repo}` | permission=admin => all units admin; if permission<admin and units_map given, permission = min of units |
| GET /teams/{id} | reqToken + team member (or site admin) | embeds `organization` | keep `id,name,permission,includes_all_repositories,units_map,can_create_org_repo,description` |
| PATCH /teams/{id} | org owner | EditTeamOption (name REQUIRED per swagger though binding is lenient) | |
| DELETE /teams/{id} | owner | 204 | |
| GET /teams/{id}/members ; GET /teams/{id}/members/{u} | team member | pg X-Total-Count; single returns User or 404 | |
| PUT /teams/{id}/members/{u} | owner | adds directly if user already org member, OTHERWISE creates an email INVITE (needs mailer) ; 409 if invite exists; 204 | |
| DELETE /teams/{id}/members/{u} | owner | 204 | |
| GET /teams/{id}/repos ; GET /teams/{id}/repos/{org}/{repo} | team member | pg; single returns Repository or 404 | |
| PUT/DELETE /teams/{id}/repos/{org}/{repo} | reqAdmin on that repo | 204; 403 if repo not in team's org | |
| GET /teams/{id}/activities/feeds, GET /orgs/{org}/activities/feeds | | `date`, pg | |
| GET /orgs/{org}/labels ; POST ; GET/PATCH/DELETE /orgs/{org}/labels/{id} | list: public; write: owner | list q `sort` (mostissues|leastissues|reversealphabetically); `{name,color,description,exclusive,is_archived?}` | Label: keep `id,name,color,description` |
| GET/POST /orgs/{org}/hooks, GET/PATCH/DELETE /orgs/{org}/hooks/{id} | owner + webhooks enabled | as user hooks | |
| GET/PUT/DELETE secrets, GET/POST/PUT/DELETE variables, runners (`/orgs/{org}/actions/*`) | owner (reqOrgOwnership) | actions agent | |
| POST/DELETE /orgs/{org}/avatar | owner | `{image: base64}` | |
| GET /orgs/{org}/list_blocked, PUT /orgs/{org}/block/{u}, PUT /orgs/{org}/unblock/{u} | owner | 204; 422 if target is an org | |
| GET /orgs/{org}/quota* | owner | see 4.8 | |
| POST /org/{org}/repos (deprecated) | | use /orgs/{org}/repos | |
Quirks: route param is `{teamid}` in code vs `{id}` in swagger (irrelevant for URLs). Org create/edit/delete limited by token scope `write:organization`.

### 4.4 Packages (scope P; `reqPackageAccess`; `checkTokenPublicOnly`)
Package "owner" is user or org name; list returns one entry PER VERSION.
| Endpoint | Params | Notes |
|---|---|---|
| GET /packages/{owner} | `type` (alpine|cargo|chef|composer|conan|conda|container|cran|debian|generic|go|helm|maven|npm|nuget|pub|pypi|rpm|rubygems|swift|vagrant), `q` (name search), page, limit | pg + Link; internal packages excluded; keep `type,name,version,created_at,repository.full_name(if any),html_url` — raw items embed full `owner`, `creator` and `repository` objects (very heavy) |
| GET /packages/{owner}/{type}/{name}/{version} | | same Package object |
| GET /packages/{owner}/{type}/{name}/{version}/files | | NOT paginated; `[{id,Size,name,md5,sha1,sha256,sha512}]` (note `Size` capitalized, no json tag); keep `name,Size,sha256` |
| DELETE /packages/{owner}/{type}/{name}/{version} | write access (reqToken) | 204; deletes that version |
| POST /packages/{owner}/{type}/{name}/-/link/{repo_name} | write access | 201 empty; repo must be owned by `owner` (looked up under same owner); 400/403 service errors |
| POST /packages/{owner}/{type}/{name}/-/unlink | write access | 201 empty |
Registry (non-REST) endpoints live under `/api/packages/{owner}/{type}/...` and `/v2` (container) - only if packages enabled; not part of /api/v1.
Quirk: unknown package -> 404 via PackageAssignmentAPI; link/unlink return 201 (not 204).

### 4.5 Notifications (scope N; reqToken)
| Endpoint | Params | Notes |
|---|---|---|
| GET /notifications | `all`(bool; default false), `status-types` (repeatable: unread|read|pinned; default unread+pinned), `subject-type` (repeatable: issue|pull|repository; "commit" is documented but ignored by the filter), `since`,`before` (RFC3339), page, limit | pg X-Total-Count only. Each thread embeds a FULL Repository object: keep `id, unread, pinned, updated_at, repository.full_name, subject.{title,type,state,html_url}` |
| GET /notifications/new | | `{new: N}` unread count: cheapest poll |
| GET /notifications/threads/{id} ; PATCH ... `?to-status=read|unread|pinned` (default read) | | PATCH returns 205 + body |
| PUT /notifications | `last_read_at`(RFC3339, only threads updated before), `all`, `status-types` (default unread), `to-status` (default read) | 205 + JSON array of changed threads (can be large; ignore body, report count) |
| GET/PUT /repos/{o}/{r}/notifications | same params | scope N (not R) |

### 4.6 Common object shapes (what to keep)
- Repository (user/org/team/starred lists) has ~70 fields. Keep: `full_name, description (trim 120), private, fork, archived, empty, default_branch, stars_count, forks_count, open_issues_count, open_pr_counter, updated_at, html_url? (derivable), ssh_url/clone_url (only on request), has_issues, has_pull_requests, mirror, template, topics?`. Drop: `owner` (login derivable from full_name), permissions, avatar_url, internal_tracker/external_*, wiki_*, allow_* merge flags, languages_url, url, link, size unless asked.
- User: `login, id, full_name, email(if present), is_admin, active` (+ `visibility`, `created`).
- Organization: `name, id, full_name, visibility, description` (drop avatar_url, website unless asked, `username` dup).
- Team: `id, name, permission, includes_all_repositories, can_create_org_repo, units_map`.
- Email: `email, primary, verified` (admin lists add `username, user_id`).
- AccessToken: see 4.2. Hook: `id,type,active,events,branch_filter,config.url` (drop `config` secrets, `authorization_header`, dates).
- Cron: `name, schedule, next, prev, exec_times`.
- All `*_url`, `avatar_url`, `html_url` fields can be dropped from compact default; `html_url` for repos/issues can be reconstructed from base URL.

### 4.7 Admin (scope A, plus `reqSiteAdmin`; repo-restricted tokens cannot override admin)
All under `tokenRequiresScopes(admin), reqToken, reqSiteAdmin`.
| Endpoint | Params / body | Notes |
|---|---|---|
| GET /admin/users | `source_id`(int), `login_name`, `is_2fa_enabled`(bool), `sort` (same enum, default alphabetically), page, limit | pg + Link. NO `q`/keyword filter in handler (code builds SearchUserOptions without Keyword) -> to find by name use `GET /users/search?q=`. Only individuals. Returns User with admin fields (is_admin, active, prohibit_login, last_login, login_name, source_id) |
| POST /admin/users | `{username*, email*, password, full_name, login_name, source_id, must_change_password (default TRUE), send_notify, restricted, visibility, created_at}` | for local users password >= MIN_PASSWORD_LENGTH + complexity + pwned check else 400 `PasswordIsRequired|PasswordComplexity|PasswordPwned`; for external `source_id` no password. Created active. 422 name/email taken. May set `X-Gitea-Warning` header |
| PATCH /admin/users/{username} | `{login_name, source_id (both required together-ish), email, full_name, password, must_change_password, website, location, pronouns, description, active, admin, allow_git_hook, allow_import_local, max_repo_creation(int), prohibit_login, allow_create_organization, restricted, visibility, hide_email}` | 400 "LastAdmin" when demoting last admin |
| DELETE /admin/users/{username} | `purge=true` also deletes repos/orgs/packages/comments | 204. 422 if user owns repos/orgs/packages (without purge), if self, if org target, last admin |
| POST /admin/users/{username}/rename | `{new_name}` | 204; 422 taken/reserved; orgs rejected |
| GET/DELETE /admin/users/{username}/emails | DELETE body `{emails:[..]}` | list unpaginated |
| POST /admin/users/{username}/keys ; DELETE .../keys/{id} | `{title,key,read_only}` | |
| POST /admin/users/{username}/orgs | CreateOrgOption (owner = that user) | GET same path exists in router (`org.ListUserOrgs`) but is NOT in swagger |
| POST /admin/users/{username}/repos | CreateRepoOption (created under that user) | 409 exists |
| GET/POST /admin/users/{username}/tokens ; DELETE .../tokens/{token} | same as user tokens, but works with admin PAT | mint tokens without password |
| GET/POST /admin/users/{username}/quota... | quota only | |
| GET /admin/orgs | pg + Link; all visibilities | |
| GET /admin/emails ; GET /admin/emails/search?q= | pg + Link | `[{email,username,user_id,primary,verified}]` |
| GET /admin/cron ; POST /admin/cron/{task} | list pg (in-memory) | task names (from memory of the cron registry, not verified in checkout) e.g. `update_mirrors`, `repo_health_check`, `check_repo_stats`, `archive_cleanup`, `cleanup_hook_task_table`, `cleanup_packages`, `sync_external_users`, `deleted_branches_cleanup`, `gc_lfs`, `delete_old_actions`, `update_migration_poster_id`, `git_gc_repos`, `resync_all_sshkeys`, `resync_all_hooks`, `reinit_missing_repos`, `delete_missing_repos`, `delete_generated_repository_avatars`, `delete_old_system_notices`, `cleanup_expired_...` (names are from cron registry; always list first). POST runs the task SYNCHRONOUSLY (`task.Run()` inline) -> may block for long; 204 on finish; unknown -> 404 |
| GET/POST /admin/hooks, GET/PATCH/DELETE /admin/hooks/{id} | CreateHookOption | "system" webhooks (fire for all repos); list pg X-Total-Count; paths use `{id}` |
| GET /admin/unadopted | `pattern` per swagger BUT code reads `query` (`ctx.FormString("query")`): use `query`. pg X-Total-Count. Returns `["owner/repo", ...]` | |
| POST/DELETE /admin/unadopted/{owner}/{repo} | adopt (204; 404 if dir missing or repo already exists) / delete files (DESTRUCTIVE) | |
| GET /admin/actions/runners (visible bool), POST register, GET/DELETE /admin/actions/runners/{id}, GET /admin/actions/runners/registration-token, GET /admin/actions/runners/jobs?labels=; legacy duplicates `GET /admin/runners/registration-token`, `GET /admin/runners/jobs` | actions agent | |

### 4.8 Quota (only routes when quota enabled, else 404)
Concepts: rules (name, limit bytes (-1 = unlimited? limit int64), subjects[]), groups (name, rules[]), users in groups. Subject strings (unverified, from Forgejo docs): `none`, `size:all`, `size:repos:all|public|private`, `size:git:all|lfs`, `size:assets:all`, `size:assets:attachments:all|issues|releases`, `size:assets:artifacts`, `size:assets:packages:all`, `size:wiki`.
| Endpoint | Notes |
|---|---|
| GET /user/quota ; GET /orgs/{org}/quota (org owner) | `QuotaInfo {used:{size:{repos:{public,private}, git:{LFS}, assets:{attachments:{issues,releases}, artifacts, packages:{all}}}}, groups:[{name, rules:[{name?,limit,subjects[]}]}]}` ; groups/rules names visible only to admins |
| GET /user/quota/check?subject= ; GET /orgs/{org}/quota/check?subject= | returns bare JSON `true|false` (true = allowed). Bad subject -> 422. `subject` required |
| GET /{user|orgs/{org}}/quota/attachments|packages|artifacts | pg + Link; items `{name,size,api_url,contained_in{api_url,html_url}}` / `{name,type,version,size,html_url}` / `{name,size,html_url}` |
| GET/POST /admin/quota/groups | unpaginated; POST `{name*, rules:[{name*,limit,subjects[]}]}` creates missing rules; 409 exists |
| GET/DELETE /admin/quota/groups/{quotagroup} | |
| PUT/DELETE /admin/quota/groups/{g}/rules/{rule} | 204 (DELETE returns 201 per swagger - odd) |
| GET /admin/quota/groups/{g}/users ; PUT/DELETE /admin/quota/groups/{g}/users/{username} | list unpaginated |
| GET/POST /admin/quota/rules ; GET/PATCH/DELETE /admin/quota/rules/{rule} | PATCH `{limit, subjects[]}` |
| GET /admin/users/{u}/quota ; POST /admin/users/{u}/quota/groups | body `{groups:[names]}` REPLACES the user's groups |
Quota exceeded on writes -> 413 `quotaExceeded` (e.g. repo create).

### 4.9 ActivityPub (only if federation enabled; scope `activitypub`; mostly signed (HTTP signature) server-to-server; not useful for MCP)
GET /activitypub/actor ; POST /activitypub/actor/inbox ; POST /activitypub/actor/outbox(GET in code) ; GET /activitypub/repository-id/{id} ; POST .../repository-id/{id}/inbox|outbox ; GET /activitypub/user-id/{id} ; POST /activitypub/user-id/{id}/inbox ; GET .../user-id/{id}/outbox ; GET .../user-id/{id}/activities/{activity-id} and `/activity`. Everything except `GET /activitypub/actor` needs a valid HTTP signature (`ReqHTTPSignature`). Skip in MCP (maybe only expose via raw api tool).

---------------------------------------------------------------------------------------------------

## 5. Gotchas checklist (verified in code unless marked)
1. Token via `?token=`: unusable on v17 (just a Warning header). Always send `Authorization: token ...`.
2. Creating/deleting tokens needs password Basic auth on `/users/{u}/tokens`; PAT can't. Admin PAT can via `/admin/users/{u}/tokens`.
3. `POST /markdown`, `/markup`, `/markdown/raw` require auth; markdown body keys are capitalized Go field names (no json tags); responses are HTML.
4. Two endpoints wrap lists in `{ok,data}`: `/users/search`, `/orgs/{org}/teams/search`.
5. `X-Total-Count` is dependable; `Link` is partial and absolute-URL-from-ROOT_URL. Use page arithmetic.
6. Limit silently clamped to MAX_RESPONSE_ITEMS (default 50 unverified): MCP must read `/settings/api` once and size pages accordingly; "give me all" tools must loop pages with a hard cap.
7. Short pages on `/users/{u}/repos` and `/orgs/{o}/repos` do not mean last page (post-filter).
8. `GET /admin/users` has no keyword filter; swagger `pattern` of `/admin/unadopted` is really `query`.
9. 205 responses on notification mark-read have bodies; `GET /orgs/{o}/members/{u}` can redirect; renamed repo -> 301 (POST may be replayed as GET by naive clients); renamed user/org -> 307.
10. DELETE endpoints with JSON body: `DELETE /user/emails`, `/admin/users/{u}/emails` (body `{emails:[]}`) - some HTTP clients drop bodies on DELETE; Node fetch supports it.
11. `PUT /teams/{id}/members/{u}` may send an invitation email instead of adding; 409 if already invited.
12. `POST /admin/cron/{task}` runs inline (blocking) -> use a long timeout or fire-and-forget.
13. 500 body is empty for non-admin in prod -> report "server error (no details)" and suggest checking server logs.
14. `GET /admin/users/{u}/orgs` exists but undocumented. `GET /admin/actions/runners/jobs` and `/admin/runners/jobs` duplicates.
15. Admin override is disabled for repo-restricted tokens (`AllowAdminOverride`); a PAT limited to specific repos cannot act as site admin (inferred).
16. Webhooks, quota, stars, heatmap, federation, explore-users each can be disabled instance-wide -> 403/404; surface a clear "feature disabled" message.
17. Per swagger, `X-Total-Count` is declared for most lists but admin quota/groups/rules and some keys lists are not paginated at all.
18. `/user/repos` order_by invalid -> 422; `/users/{u}/repos` has no sort and is ordered by id asc.
19. `sudo` is processed for every route (including unauthenticated ones, where it yields 403 "Only administrators allowed to sudo.").

---------------------------------------------------------------------------------------------------

## 6. Recommended MCP tools (platform area)

Design rules: few tools with an `action`/`kind` enum to keep the tool list schema small (<~1.5k tokens total for this area); every tool takes optional `fields` (override projection) and `limit`/`page`; list outputs are TSV-like lines or compact JSON arrays `{items:[...], total, page, has_more}` without `url`/avatar fields; mutating tools return `{ok:true, id/name}` only; errors normalized to `{status, message}` (drop `url`); enable a generic `forgejo_api` fallback so rare endpoints (activitypub, hooks, avatars, GPG) need no dedicated tool.

Shared client behavior: base URL + token from env; `Authorization: token`; `Accept: application/json`; send `Content-Type: application/json` only with body; handle redirects manually for non-GET; on first call fetch `/version` + `/settings/api` (+ `/user`) and cache; `has_more = page*limit < X-Total-Count` (fallback: returned length == limit when header absent); auto-clamp limit to `max_response_items`; optional `sudo` env/param adds `Sudo:` header; map 401/403 messages to hints ("token lacks scope X: need read:user", parse the bracketed list).

1. `forgejo_info` (no params or `{refresh?}`)
   - GETs /version, /settings/api, /settings/repository, /user (+ optionally /user/quota, /nodeinfo probing).
   - Output: `{server:"17.0.2", major:17, api_limits:{max:50,default:30}, flags:{stars,forks,migrations,mirrors,timetracking,lfs disabled...}, me:{login,id,is_admin}, features:{quota:bool, federation:bool, webhooks:bool}}`.
2. `forgejo_user` `{action: me|get|search|keys|followers|following|starred|orgs|permissions, username?, q?, sort?, page, limit}`
   - search -> unwraps `{ok,data}`; outputs `login id full_name email`.
3. `forgejo_repos_of` `{owner_kind: me|user|org|starred|watched|team, name?, team_id?, order_by?, page, limit}` -> `full_name | private/fork/archived flags | stars | updated | description(80)`.
4. `forgejo_org` `{action: list|get|create|edit|delete|rename|members|public_members|is_member|remove_member|publicize|conceal|labels|activity|block|unblock|list_blocked, org?, username?, body fields...}`
5. `forgejo_team` `{action: list|search|get|create|edit|delete|members|add_member|remove_member|repos|add_repo|remove_repo, org?, team_id?, name?, permission?, units_map?, ...}`
   - warn when add_member may only invite.
6. `forgejo_notifications` `{action: count|list|get|mark_read|mark_thread, all?, status?, subject_type?, since?, before?, repo?, to_status?, thread_id?}`
   - list output: `id unread repo#type title state`; mark_read returns count of changed.
7. `forgejo_packages` `{action: list|get|files|delete|link|unlink, owner, type?, name?, version?, q?, repo?}` -> `type name version created repo`.
8. `forgejo_token` `{action: list|create|delete, username, name?, scopes?, repos?, via: password|admin}`
   - create via admin endpoint by default; returns the secret once; validates scope strings client-side against enum above; delete accepts id or name.
9. `forgejo_admin` `{area: users|orgs|emails|cron|hooks|unadopted|quota, action, ...}`
   - users: list (filters source_id/login_name/is_2fa_enabled/sort) | search (delegates to /users/search) | create | edit | delete(purge) | rename | emails | keys.
   - cron: list | run(task) with long timeout.
   - quota: rules/groups CRUD, set_user_groups, user usage.
   - destructive actions (`delete`, `purge`, unadopted delete) get a `confirm: true` param.
10. `forgejo_render` `{text, mode: markdown|gfm|comment|file, context?, file_path?}` -> HTML (or strip tags? return as-is, trimmed).
11. `forgejo_quota` (if kept separate) `{scope: me|org, org?, action: info|check|attachments|packages|artifacts, subject?}` -> compact usage in MiB.
12. `forgejo_api` fallback `{method, path, query?, body?, pick?: string[] (dot-paths per item), max_items?, all_pages?: bool}`
    - path relative to /api/v1 (or `/forgejo/v1/...`); returns projected JSON; if `all_pages` loops with hard cap (default 5 pages); strips `*_url`, `avatar_url`, `url` keys by default (`raw:true` to disable).
13. `forgejo_endpoints` `{q}`: searches an embedded index of the 512 operations (tag, method, path, operationId, summary, scope) so the model can discover the right `forgejo_api` call without large tool definitions (data = endpoints.tsv plus scope category column from section 1).

Projection spec to hardcode (JSON paths): see 4.6; plus `Team` list -> `id,name,permission`; `Hook` -> `id,type,active,events,config.url`; `Cron` -> `name,schedule,next,prev,exec_times`; `Package` -> `type,name,version,created_at`; `Notification` -> described in 4.5.

Error strategy: `401` -> "unauthenticated or bad token"; `403` with `token does not have at least one of required scope(s): [..]` -> pass through the bracket list; `403` `Only administrators allowed to sudo.`; `404` -> "not found or no access (private resources return 404)"; `422` -> pass `message`; `5xx` empty -> "server error (details hidden in production)". Retry only idempotent GETs, 2 tries with backoff, honor `Retry-After` if a proxy sends 429/503.
