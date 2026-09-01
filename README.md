# chilli-crisp 🌶️

[![CI](https://github.com/kalkis/chilli-crisp/actions/workflows/ci.yml/badge.svg)](https://github.com/kalkis/chilli-crisp/actions/workflows/ci.yml)

A fast, minimal, keyboard-driven API client for the terminal.
Single static Go binary for Linux, macOS, and Windows.

- Edit and send HTTP requests: method, URL, query params, headers, body
- Auth: basic, bearer token, API key (header or query), per-request or per-collection
- Environments with `{{variable}}` substitution and gitignored local secret overrides
- Import **OpenAPI 3.x / Swagger 2.0** specs and **Postman Collection v2.x** exports
- Headless collection runner with status assertions (CI-friendly exit codes)
- Request history with replay and copy-as-curl export — kept out of the repo in
  a per-user state directory, and dialled down or off per workspace
- Everything stored as git-friendly YAML in a `.crisp/` directory you can commit
  next to your backend code — or in a standalone workspace found from anywhere

## Install

```sh
go install github.com/kalkis/chilli-crisp@latest
# or from a checkout:
make build
```

## Quick start

Beside the code you are testing:

```sh
cd your-backend-repo
chilli-crisp init                        # creates ./.crisp
chilli-crisp import openapi api.yaml     # or: import postman collection.json
chilli-crisp                             # open the TUI
```

Or, if you test APIs whose code you do not check out — a QA workflow, or several
services at once — make one workspace that is found from anywhere:

```sh
chilli-crisp init --home                 # creates it under your config directory
chilli-crisp import openapi api.yaml     # one collection per API, in one workspace
chilli-crisp                             # open the TUI, from any directory
```

A workspace is a directory, so it can also be its own git repo: commit
`collections/` and `environments/`, clone it wherever you need it, and point
`chilli-crisp -w` at it. Nothing in it can hold response data (see
[Storage layout](#storage-layout)), so it is safe to share with a team that has
no access to the backend.

Define environment variables (e.g. tokens referenced as `{{token}}`):

```yaml
# .crisp/environments/staging.yaml       (committed — no secrets here)
name: staging
vars:
  baseUrl: https://staging.example.com

# .crisp/environments/staging.local.yaml (gitignored — secrets go here)
name: staging
vars:
  token: paste-your-real-token-here
```

## TUI keys

The status bar shows only the keys that do something where the cursor is
standing — `space` appears on a collection or folder row, `D` on a request row,
and the editor's hint changes with the tab. Press `?` for the full reference,
which also covers cursor motion and the modal keys the table below leaves out.

| Key | Action |
| --- | --- |
| `?` | show every key |
| `tab` / `shift+tab` | cycle panes (sidebar → editor → response) |
| `enter` | sidebar: open request, or edit a collection's/folder's variables · editor: edit field |
| `s` | send the current request |
| `esc` | cancel the send in flight (not recorded in history), or clear a sidebar selection |
| `e` | pick environment |
| `W` | switch workspace (or open one by path) |
| `[` `]` or `←` `→` | switch editor/response tabs |
| `u` / `m` | edit URL / cycle method |
| `M` | pick the method from a list (`1`-`8` selects outright) |
| `v` / `r` | Path tab: override a variable for this request / reset it to inherited |
| `a` `d` `space` | add / delete / toggle a param or header row |
| `space` | sidebar: collapse/expand the collection or folder under the cursor |
| `t` | cycle auth type or body type |
| `E` | edit body in `$EDITOR` |
| `y` | show request as curl command |
| `H` | history (enter = send again) |
| `n` / `N` | new request (asks for a name, then a method) / new collection |
| `shift+↑` `shift+↓` (or `K` `J`) | sidebar: extend the selection to several requests |
| `c` / `p` | sidebar: copy the selected requests / paste them into the folder the cursor is in |
| `D` | delete the selected requests, or the folder/collection under the cursor — asks first; `y` deletes |
| `q` | quit |

`D` follows the selection when there is one and the cursor's row otherwise, so
it deletes a whole folder (and its variables) or a whole collection (and its
file) only when nothing is selected. A folder heading swept up in a range never
counts — `D` removes the requests you can see highlighted, never the rest of
the folder they sit in.

Copying is a duplicate, not a system-clipboard operation: `c` fills a buffer
private to the running program, so it never disturbs whatever you have copied
elsewhere. `p` inserts the copies into whichever folder the cursor is in,
renaming one to `name (copy)` only if that collection already answers to its
name, so `crisp run --request name` keeps pointing at the original.

The method list includes **QUERY** ([RFC 10008](https://www.rfc-editor.org/info/rfc10008/)) —
a safe, idempotent method that carries its query in the request body. Choosing
it gives a body-less request a JSON body, since a QUERY without content and a
`Content-Type` is one the server must reject — and a QUERY keeps its method and
content across redirects rather than being downgraded to a GET.

## Headless usage

```sh
chilli-crisp list                                        # show collections & requests
chilli-crisp run my-api --env staging --assert-status 2xx   # exit 1 if any request fails
chilli-crisp run my-api --request "create user" -v          # one request, print body
chilli-crisp run my-api --request admin                     # run every request in a folder
chilli-crisp run my-api --no-history                        # run without writing to the log
chilli-crisp curl my-api "create user" --env staging        # print curl command
chilli-crisp where                                          # workspace, id, log paths, settings
```

`--request` selectors match a request name, a `folder / name` qualified name,
or a bare folder name. When several requests share a name, prefix the HTTP
method to disambiguate: `--request "POST admin / /drinks"`.

## Storage layout

### Finding a workspace

Commands resolve one workspace, taking the first of these that matches:

| | Rule |
| --- | --- |
| 1 | `--workspace` / `-w` — the workspace directory, or a directory containing one |
| 2 | `$CRISP_WORKSPACE` — the same, for a shell profile or a CI job |
| 3 | a `.crisp` in the current directory or any parent |
| 4 | the home workspace, `~/.config/chilli-crisp/workspace` |

Rule 3 comes before rule 4 on purpose: standing in a project that has a `.crisp`
gives you that `.crisp`, however many workspaces you keep elsewhere. Nothing is
created for you — the home workspace counts only once `chilli-crisp init --home`
has made one. `chilli-crisp where` prints which rule matched.

In the TUI, `W` switches without restarting: it lists the workspace you are on,
the home workspace, and any found from the current directory, plus an entry for
typing a path. Switching reloads collections, environments and the per-request
timeout; the environment resets, since an environment belongs to the workspace
that defines it. A request copied with `c` survives the switch, so `p` pastes it
into the workspace you land in.

### What is stored, and where

A workspace holds only what a team shares. Everything personal to one machine
lives in a per-user state directory instead.

```
.crisp/                            # in your repo — commit it
  collections/my-api.yaml          # requests
  environments/staging.yaml        # shared variables
  environments/staging.local.yaml  # secrets (gitignored)
  workspace.local.yaml             # this checkout's workspace id (gitignored)
  config.yaml                      # optional settings

~/.local/state/chilli-crisp/       # per-user — never inside a repo
  workspaces/<id>/history/000003.jsonl
```

Response bodies do not go in your working tree. A `.gitignore` is a convention,
not a boundary: it does not cover a Docker build context, a sync client
watching the repo folder, or a source tarball — and `git clean -xdf` deletes
what it does cover. So the log lives outside the workspace, keyed by an id kept
in the gitignored `workspace.local.yaml`; two clones of one repo keep separate
logs.

| Platform | State directory |
| --- | --- |
| Linux, BSD | `$XDG_STATE_HOME/chilli-crisp`, else `~/.local/state/chilli-crisp` |
| macOS | `~/Library/Application Support/chilli-crisp` |
| Windows | `%LocalAppData%\chilli-crisp` |

`CRISP_STATE_DIR` overrides all of them — set it where there is no writable
home directory, as in most CI containers. A workspace that still has a
`.crisp/history/` from an older version migrates it out the next time history is
read or written.

A request's URL is a template, so a path parameter is just a variable:

```yaml
name: TODO Sample API
vars:
  baseUrl: http://localhost:3000
  id: "1"                      # the shared default
folders:
  - name: admin                # only appears once a folder has variables
    vars:
      baseUrl: http://localhost:3001
requests:
  - name: getTodo
    method: GET
    url: '{{baseUrl}}/todos/{{id}}'
    vars:
      id: "10"                 # this request only; every other one still uses 1
```

The editor's **Path** tab lists the variables a request's URL references, with
the value each resolves to and the layer it came from; `v` overrides one for
that request alone and `r` hands it back. `enter` on a collection or folder in
the sidebar edits the shared defaults. A request `vars` entry applies to the
whole template, not just the URL — headers, query values, and the body see it
too.

`config.yaml` is optional. It sets collection display order, the per-request
timeout, and how much of a send is recorded — all of which the TUI and `run`
share so they cannot drift apart:

```yaml
collections: [my-api, internal]   # display order; unlisted ones follow
timeout: 45s                      # per-request timeout (default 30s)
history: metadata                 # full (default) | metadata | off
```

`run --timeout 5s` overrides the configured value for that invocation. Ctrl-C
during a headless run stops it at the next request rather than killing it
mid-flight; the aborted request is reported but, like an `esc` in the TUI, is
not written to history.

### How much is recorded

`history` decides what goes in the log. It is committed with the collections,
so a workspace pointed at production records the same amount for everyone
rather than for whoever remembered a flag.

| `history:` | Recorded |
| --- | --- |
| `full` (default) | request, status, timing, and the response body (first 16 KB) |
| `metadata` | everything above except the response body |
| `off` | nothing — and nothing is created either: no state directory, no workspace id |

`on`/`true`/`yes` and `no`/`false` are accepted as the obvious spellings of
`full` and `off`. Anything else is an error rather than a fallback to the
default, so a typo cannot quietly restore full recording in a workspace you
believed was quiet.

`run --no-history` skips the log for one invocation — a CI job can print its
results without leaving them on the runner. It only ever records less than
`config.yaml` allows, never more.

Two things the setting does not do. It does not hide entries already written:
turning recording off stops the log growing, and `H` still shows what is in it
(the overlay title says which level is in force). And `metadata` still keeps the
transport error text for a request that failed to send, which can quote the
expanded URL — it means "no response payloads", not "nothing from the wire at
all". Use `off` if that matters.

History rotates: sends append to the highest-numbered log, roll into a new one every 500 entries, and the oldest is dropped once three logs exist (~1,500 sends retained). Reads span logs, so the history view stays full across a rollover. A failure to write history never fails a send, so an unwritable state directory costs the log entry and nothing else.

Collections and environments are addressed by name or by file stem (`run my-api-2`). Names are slugged to file names, so a second collection whose name slugs onto a taken stem gets the next free one (`my-api-2.yaml`) rather than overwriting it; when a name matches more than one, commands list the candidates and ask for the file stem instead.

Variable resolution order, most specific first: a request's own `vars` → environment (with `.local.yaml` overrides winning) → the request's folder `vars` → collection `vars`. Variable values may reference other variables (e.g. `baseUrl: "{{scheme}}://api.example.com"`). Unresolved `{{variables}}` fail the request with a clear error instead of sending garbage; so do circular definitions.

## Building for all platforms

```sh
make release   # dist/ binaries for linux/darwin/windows × amd64/arm64
```

## CI

Every push and pull request to `main` runs the [CI workflow](.github/workflows/ci.yml)
(status shown in the badge above):

- **Build & test** on Linux, macOS, and Windows, plus `go vet` and a gofmt check
- **Cross-compile** the full six-target release matrix
- **SAST**: [gosec](https://github.com/securego/gosec) (security rules) and
  [staticcheck](https://staticcheck.dev); findings that are intentional for an
  HTTP client (reading user files, launching `$EDITOR`) carry annotated
  `#nosec` justifications in the code
- **Dependency vulnerabilities**: [govulncheck](https://go.dev/blog/vuln)
  fails the build only for vulnerabilities reachable from this code
- **Secret scanning**: [gitleaks](https://github.com/gitleaks/gitleaks) over
  the full git history
- [Dependabot](.github/dependabot.yml) keeps Go modules and actions updated weekly

## Notes on imports

- **OpenAPI**: one request per operation; path templates (`/pets/{petId}`) become `{{petId}}` variables seeded from schema examples; JSON request bodies get a sample payload generated from the schema; security schemes map to collection auth with `{{token}}`-style placeholders you fill in via an environment. If a security scheme carries an `x-sample-token` extension (or `x-sample-apikey` / `x-sample-username` / `x-sample-password`), that value seeds the matching collection variable so demo APIs import ready to send.
- **Postman**: folders are preserved (requests keep a `folder`, shown as sub-groups in the sidebar and `list`); folder-level auth is inherited by requests that don't set their own, copied onto each request; v2.0 and v2.1 auth formats are supported; multipart file uploads are not (text fields are kept).
