# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

chilli-crisp is a keyboard-driven terminal API client (a Postman alternative) — a
single static Go binary. It stores everything as git-friendly YAML in a `.crisp/`
workspace directory that lives next to the backend code under test and gets
committed with it. See `README.md` for user-facing docs.

## Commands

```sh
make build                   # -> ./chilli-crisp (CGO_ENABLED=0, -trimpath)
make release                 # dist/ binaries, 6 targets
go test ./...                # or: go test -race ./...   (use -race, the TUI sends on goroutines)
go test ./internal/store/ -run TestHistoryRotatesAtCap -v   # a single test
go vet ./...
gofmt -l .                   # must print nothing; CI fails on any output
gosec ./...                  # must report 0 issues
```

CI additionally runs `staticcheck` and `govulncheck` (installed `@latest`) and
`gitleaks` over the full history. **`staticcheck` does not run locally** — the
installed binary fails with "export data version 4 is greater than maximum
supported version 2", a pre-existing toolchain mismatch you did not introduce;
let CI cover it.

Cross-compilation is part of CI, so check anything platform-specific (e.g. a
`syscall` reference) with `GOOS=windows go build ./...` and
`GOOS=darwin go build ./...`.

## Architecture

Dependency direction is strictly one way, in layers (verify with
`go list -f '{{join .Imports "\n"}}' ./internal/...`):

```
model, vars                     leaves; import nothing from this project
httpclient   -> model, vars     variable expansion, auth, the actual send
curlgen      -> httpclient      renders a Resolved as a curl command
importer/*   -> model           openapi, postman
store        -> httpclient, model
runner       -> store, httpclient, vars, model
tui          -> store, httpclient, curlgen, vars, model
cli          -> all of the above
```

`internal/cli` is the only place that wires a workspace to the runner or the
TUI; `main.go` just calls `cli.Execute()`.

### The send pipeline is shared, deliberately

`model.Request` is a **template** — URL, header values, query values and body may
all contain `{{variable}}` placeholders. It becomes a real request in one place:
`httpclient.Resolve(req, col, resolver)` → `httpclient.Resolved`. `Resolve`
expands variables (`internal/vars`), merges enabled query params into any query
string already in the URL, applies the body and its default `Content-Type`, and
applies auth (request auth beats collection auth; configured auth overrides a
manually set header of the same name). Both `SendResolved` and `curlgen.Command`
consume `Resolved`, which is what keeps "copy as curl" honest — it renders the
request that would actually be sent. Anything new that needs a concrete request
should take a `Resolved`, not re-derive one.

Variable precedence is fixed in exactly one place, `httpclient.ResolverFor`:

    request vars -> environment -> folder vars -> collection vars

The environment layer has `.local.yaml` overrides already merged in by
`Environment.AllVars`. `Request.Vars` applies to the whole template (URL,
headers, query values, body), which is what makes a path parameter editable
without touching the shared default; `Collection.Folders` holds an entry only
for folders that have variables — a folder is otherwise implied by its requests
alone. Every front end calls `ResolverFor` rather than `vars.NewResolver`, and
it must be built **per request** (the runner does this inside its loop), or the
two narrowest layers silently vanish. Expansion is recursive with a depth cap,
so circular definitions error instead of hanging, and an undefined variable
fails the request rather than sending a literal `{{name}}`.

### Runtime IDs, not slice indices

`model.ID` comes from an atomic counter (`internal/model/id.go`) and is
`yaml:"-" json:"-"` — **no file may ever reference an ID**, because the next run
hands out different ones. `EnsureIDs()` assigns them on load.

The TUI selects, tracks and edits by ID (see `sel`, `sbItem`, `editor.request()`),
so a request survives its collection being reordered or an earlier sibling being
deleted. When adding UI state that points at a request, key it by ID.

The same `yaml:"-"` runtime-metadata pattern carries `Collection.Path`,
`Environment.Path`, and `Environment.LocalVars`. `LocalVars` is load-bearing:
because it is never serialized, `SaveEnvironment` physically cannot write a
secret from a `.local.yaml` back into the shared, committed file.

### Secrets discipline: the workspace / state split

Treat this as a correctness requirement. A **workspace** holds only what a team
shares and commits. Anything that can carry response data or is personal to one
machine lives in the **per-user state directory** (`internal/store/state.go`),
which no repository can reach — response bodies are not gitignored inside the
working tree; they are not in the working tree at all.

`Workspace.ensureGitignore` covers `*.local.yaml` — both environment secret
overrides and the per-user `workspace.local.yaml` — plus `history/`, which only
an unmigrated workspace still has. It **only ever appends** a missing pattern,
never rewrites the file: users add their own lines, and older workspaces on disk
must be repairable in place.

New file kinds go one of two ways. Response data or per-machine state belongs in
the state directory. Anything genuinely shared that can still hold a credential
goes in the workspace, gets a pattern in `gitignoreLines`, and the code path that
first creates one must run `ensureGitignore` before it. Naming such a file
`*.local.yaml` means it is covered the day it is written, which is why the
workspace id file is called `workspace.local.yaml` and not `id`.

`HistoryEntry.Request` is a snapshot taken **pre-expansion**, so a value from a
`.local.yaml` is still a `{{placeholder}}` in the log rather than the secret. Do
not "improve" this by storing a `Resolved`.

### How much is recorded: `history` in config.yaml

`Config.History` settles what goes in the log: `full` (the default) keeps the
response body, `metadata` drops it, `off` writes nothing. Four things about it
are load-bearing:

- **The level is applied inside `AppendHistory`, never by a caller**, so it
  holds for any front end added later: a caller may record *less* than the
  level allows (`run --no-history`) but none can record more. Read it through
  `Workspace.HistoryMode`, the way both front ends read `RequestTimeout`
  through one accessor.
- **`off` creates nothing**, not merely writes nothing: it returns before
  `historyDir()`, so no state directory and no workspace id are minted. It
  still calls `ensureGitignore` — an older workspace may still hold logs in
  the working tree, and switching recording off must not be what leaves them
  exposed.
- **`History` never assigns an id either.** A read with no id and no logs left
  in the workspace returns early, for the reason `Paths` does: browsing an
  empty log must not be what decides where the log will go. `hasWorkspaceLogs`
  is the exception that keeps lazy migration working on a read.
- **An unrecognised value is an error**, not a fallback, in
  `HistoryMode.UnmarshalYAML`: a typo must not quietly restore full recording.
  `Config` is read for collection ordering too, so a bad value fails every
  read of the workspace — the intended loudness.

The level does not touch `Error`: a transport error is a `net/http` message
that can quote the expanded URL, so `metadata` means "no response payloads",
not "nothing from the wire". `off` is the answer where that matters — say so
rather than silently redacting.

`HistoryMode` is a string type, so a future `SaveConfig` is no worse off than
`Timeout` already makes it.

### The per-user state directory

`store.StateDir()` resolves it: `$CRISP_STATE_DIR` outright, else
`%LocalAppData%\chilli-crisp` on Windows, `~/Library/Application Support/…` on
darwin, else `$XDG_STATE_HOME` or `~/.local/state`. It branches on
`runtime.GOOS`, not build tags, so the six-target matrix stays clean. There is
no `os.UserStateDir` in the stdlib, and `os.UserCacheDir` is the wrong home:
cache directories get swept by cleaners, which would eat a log the user thinks
they still have.

Each workspace gets `<state>/workspaces/<id>/`, keyed by 8 random bytes in the
gitignored `.crisp/workspace.local.yaml`. The id is assigned lazily on first
history use, never by `Init`, so two clones of one repo keep separate logs —
which is the point. Assignment is a **claim**, the same idiom as rolling a log:
`O_CREATE|O_EXCL`, and losing the race means reading the winner's id rather
than failing. `Workspace.Paths()` is the read-only view for `crisp where`; it
must never assign an id, or reporting where the log would go becomes the thing
that decides it.

Anything moving between the workspace and the state directory goes through
`moveFile`, which falls back to copy-and-unlink on **any** rename failure — it
deliberately does not test for `EXDEV`, since a repo and `$HOME` are routinely
on different mounts (every repo under `/mnt/c` on WSL) and checking the errno
would only add a platform-specific reference.

Tests must set `t.Setenv(store.StateDirEnv, t.TempDir())` before any send —
`store`'s `testWorkspace` and `runner`'s do it, as does `tui`'s `testModel`.
Without it a test writes into the developer's real state directory.

### Finding a workspace

`store.Resolve` is the single place workspace lookup is decided, the way
`httpclient.ResolverFor` is for variable precedence. The order is `--workspace`
-> `$CRISP_WORKSPACE` -> a `.crisp` at or above the cwd (`Discover`) -> the
home workspace at `<ConfigDir>/workspace`. Anything that needs a workspace goes
through it; `cli.openWorkspace` is a one-line wrapper, and only `where` keeps
the returned `Source`.

Three properties are load-bearing:

- **Discovery outranks the home workspace.** Standing in a project that has a
  `.crisp` must give that `.crisp`; an explicit path covers the other case.
- **Resolve never creates anything.** The home workspace counts only once
  `init --home` has made one, so no command writes into the configuration
  directory unasked. `TestResolveDoesNotCreateAHomeWorkspace` pins this.
- **`Open` validates.** It accepts the workspace directory or a directory
  containing a `.crisp`, and refuses a directory with none of
  `workspaceMarkers` and no `.crisp` name — without that check a mistyped flag
  or a stale `CRISP_WORKSPACE` would quietly scatter collections into an
  unrelated directory. `isWorkspace` treats a directory *named* `.crisp` as one
  regardless of contents, which keeps `Open` in step with `Discover`.

The TUI switches workspace in place (`W` -> `modeWorkspace`). Two rules there
are load-bearing:

- **`loadWorkspace` reads everything fallible before it changes anything**, so a
  workspace that will not load leaves the model exactly where it was. It pairs
  with `resetWorkspaceState`, the single list of what a workspace owns — add a
  per-workspace field to `appModel` and it belongs in that reset. Deliberately
  *not* reset: the keymap, the clipboard (pasting across workspaces is the
  point of it being process-local), and `sending`/`cancel`, so a send in
  flight keeps its abort. Replacing `m.editor` means rebinding
  `m.editor.envVars`, or the Path tab silently shows no environment values.
- **The client is rebuilt from the new workspace's timeout.** `startSend` copies
  `m.client` and `m.ws` into locals before handing them to the goroutine, which
  is what makes both safe to replace mid-flight — and `responseMsg` carries that
  workspace, so a send started before a switch still writes its history entry to
  the workspace it belonged to.

`ConfigDir` needs no `runtime.GOOS` branching — `os.UserConfigDir` is already
right on all three platforms. On macOS the two resolve to the same base, so
`workspace/` (the home workspace) and `workspaces/<id>/` (per-workspace state)
sit side by side there.

Tests: `isolate(t)` in `store_test.go` sets `StateDirEnv`, `ConfigDirEnv` **and**
blanks `WorkspaceEnv`. Any test that resolves must call it, or it reads the
developer's real home workspace, or their own `CRISP_WORKSPACE` decides the
result. Use `t.Chdir` to drive the discovery rule.

### History is a rotated JSONL log

`<state>/workspaces/<id>/history/%06d.jsonl`; the highest index is the active
log. Appends roll to a new index at 500 entries and prune to the newest 3. The
directory is resolved **once** at the top of `AppendHistory` and `History` and
threaded down, so the one fallible step surfaces in one place and the internals
(`historyFilePath`, `historyIndices`, `rollHistory`, `pruneHistory`) stay total
functions over a `dir`. Details that matter if you touch
`internal/store/history.go`:

- Rolling claims the next index with `O_CREATE|O_EXCL`; losing that race just
  means appending to the winner's file, so concurrent processes (a `run` while
  the TUI is open) are safe.
- `History(limit)` walks logs newest-first and unmarshals **backwards** per file,
  so read cost tracks `limit`, not file size, and a read stays full right after
  a rollover by spilling into the previous log.
- Migration is lazy (on any history read or write, not on `list`) and also
  repairs the `.gitignore`. A read of a workspace that has neither an id nor
  logs of its own skips straight to that repair. Two migrations run in order:
  `.crisp/history/*.jsonl` moves out to the state directory, then the
  pre-rotation `.crisp/history.jsonl` becomes `000001.jsonl` — guarded by
  "only if `000001` is absent", which correctly no-ops when the first
  migration just supplied one.
- The move out **refuses to merge**. If the state directory already holds a log,
  the workspace copy is left exactly where it is: reaching that needs a restored
  backup or a branch switch, the state directory already has the newer logs, and
  the directory left behind is itself the notice. It stays gitignored.
- A failed history write must stay non-fatal at both call sites
  (`runner.go`, `tui.go`) — that is what lets the log live outside the
  workspace: an unset or read-only home directory costs the entry rather than
  the send. `CRISP_STATE_DIR` is the documented fix.

### Addressing: slug vs stem

Collections and environments are addressed by display name or by file stem.
`Slug()` maps a name to a file stem; `stem()` is the literal, unique address and
wins outright in lookups. Names that slug alike produce an *ambiguity error*
listing candidates rather than silently resolving to one — preserve that.
`SaveCollection` never overwrites an existing file (it picks `<slug>-2.yaml`);
`ImportCollection` deliberately does replace a collection with the exact same
name, so re-importing a spec refreshes it.

`store.MatchRequests` resolves `--request` selectors: a name, a `folder / name`
qualified name, a bare folder name, or any of those with an HTTP method prefix.
The whole selector is tried before the method-prefix split, so a request actually
named "post deploy" is not mistaken for a filter.

### Timeouts and cancellation

The per-request timeout lives in the committed `.crisp/config.yaml` as
`timeout: 45s`; `Workspace.RequestTimeout()` clamps zero/negative to
`store.DefaultTimeout` (30s). Both the TUI and the runner read it, so they cannot
drift apart. `run --timeout` overrides and defaults to `0` (= unset) so cobra
prints no default that would contradict the config.

The timeout stays on the `http.Client`; the context carries **only**
cancellation. That separation is what makes `errors.Is(err, context.Canceled)` a
trustworthy "the user aborted this" signal, distinct from a request that ran out
of time. The rule both front ends follow: **an aborted send is not a result**, so
`esc` in the TUI and Ctrl-C in a headless run write no history entry, while a
real timeout still does.

`cli.Execute` wires `signal.NotifyContext` + `ExecuteContext`, and the runner
checks `ctx.Err()` between requests. This does not disturb the TUI: Bubble Tea
uses raw mode, so `^C` arrives there as a key press and never becomes a signal.

Note `httpclient.New` leaves `Transport` nil, so every client shares the
process-global `http.DefaultTransport` — constructing a client is cheap and does
not affect connection reuse. It does set `CheckRedirect` (`preserveQuery`),
because `net/http` rewrites 301 and 302 to a bodiless GET for every method but
GET and HEAD, and RFC 10008 requires a QUERY to keep its method and content
across a redirect — otherwise we would send a request the user never built and
report it as the result. Supplying the hook means owning the redirect cap that
`net/http` provides for free, hence `maxRedirects`. Any new `http.Client` in
this codebase must carry it too.

QUERY (RFC 10008) is a normal method everywhere else: it lives in `methods`
(`internal/tui/editor.go`, appended so `m` still cycles GET -> POST in one
press), `store.httpMethods`, `shortMethod`, and `methodColors`. The one rule it
adds is that its content and `Content-Type` are mandatory, which
`tui.applyMethod` handles by seeding a JSON body when the method is set to
QUERY on a body-less request; the send pipeline itself knows nothing about it.

### The keymap is the single source of truth

`internal/tui/keys.go` holds `keyMap`/`defaultKeys`: every key the TUI binds,
with its help text. Handlers dispatch with `key.Matches(msg, km.X)` rather than
comparing `msg.String()`, and the status bar and all six modal footers render
from the same bindings through `helpLine`. Adding a key means adding a binding —
`TestEveryBindingHasHelp` fails a binding with no help text, because one would
silently vanish from both the hint and the `?` reference.

`refreshKeys` enables exactly the bindings the cursor can act on and runs at the
top of `handleKey` **as well as** `render`. Running it before dispatch is what
makes a hidden key inert in the same frame it is hidden, which is why
`OpenVars` and `OpenRequest` can share `enter` without ambiguity: only one of
the pair is ever live (the header pair `Fold`/`OpenVars` and the request-row
keys never both answer one keystroke). The bar shows only context keys, so `?`
is pinned past the truncation point in `statusBar` — it is the route to every
key the bar is not showing. `Help` is disabled while a field is open, because
`?` is printable and has to reach the text input.

`appModel.keys` is a value copy of `defaultKeys` (a zero `keyMap` has every
binding disabled, so the TUI would ignore all input); `newAppModel` is the
single construction path that guarantees it. The `?` overlay renders from
`defaultKeys`, never `m.keys`, so a key the cursor has disabled still appears.

`helpLine` wraps `help.ShortHelpView` in this package's `truncate`. That is not
belt-and-braces: bubbles v2.2.0's `shouldAddItem` stops dropping items once its
own ellipsis no longer fits, and then appends every remaining one, so the
library alone does not bound the width.

### Copy/paste, and why a folder is not a container

`c` copies the sidebar selection into `appModel.clip` and `p` pastes it into
the folder the cursor is in (`internal/tui/clipboard.go`). Four things about it
are load-bearing:

- **`Collection.Append` is wrong for a paste.** A folder is a *contiguous run*
  of requests naming it, not a container — `sidebar.rebuild` opens a header
  every time `Request.Folder` differs from the previous request's, and
  `cli.go`'s `list` has the same loop. A request added at the end of the slice
  starts a second run, so the folder is drawn twice and `foldKey`
  (`colID/folderName`) gives both halves one shared fold state. Use
  `Collection.InsertAt` and put the request inside the run.
- **`Request.Clone` is called on the way in *and* on the way out.** Cloning
  only on copy would let two pastes of one buffer share a `Headers` array.
- **The clipboard is process-local on purpose.** Nothing in this TUI touches
  the system clipboard (`y` renders curl into the response pane rather than
  copying it), so a copy here cannot clobber the user's own. Keep it that way;
  it is a documented property, not an omission.
- **`store.UniqueRequestName` tests names with `MatchRequests`, not `==`.**
  `--request` resolves by slug and treats several hits as an ambiguity, so a
  copy keeping its name would make the *original* unaddressable. A name that
  merely slugs alike, or collides with a folder, is just as taken.

The selection itself is an anchor `model.ID` on `sidebar` plus the cursor, not
a set of marked rows — same reason the cursor is restored by row identity, so a
rebuild cannot strand it. `refreshKeys` enables `Copy` from
`sidebar.selectedCount()` and `Delete` through `deleteLabel()` rather than from
the cursor's row, because extending a range past the end of a folder parks the
cursor on the next header while the highlighted requests behind it are still
what those keys act on.

### Deleting a container, and the escalation it must not do

`D` deletes the selection when there is one, and the folder or collection under
the cursor when there is not (`openDeleteConfirm`). That order is the whole
safety property, not a convenience:

**A header inside a range must never count.** Extending a range across a folder
boundary sweeps up the header between the two runs. If it counted, `D` would
take the entire folder — including requests below the range that were never
highlighted and may be off screen — escalating a destructive action past what
the user can see. `sidebar.selected()` therefore skips headers and must go
on doing so; headers are actionable only with no range open. A range that
covers a header *and* all of its requests still empties the folder, and
`PruneFolders` drops the entry, so both routes reach the same file anyway.

`refreshKeys` rebuilds `Delete`'s help from `deleteLabel()`, which mirrors that
same branching — the enable flag and the hint text are one value, so the bar
cannot offer "delete folder" where `D` would actually delete a request.

Two more things carry weight:

- **`Collection.PruneFolders` runs on every request delete**, not just folder
  deletes. A folder entry that outlives its requests is unreachable (the vars
  modal opens from a folder header, and there is no header without requests)
  and `EnsureFolder` would silently revive it.
- **`config.yaml` is never rewritten** when a collection is deleted. A stale
  stem in `Collections:` is inert — `orderCollections` skips stems it cannot
  find — and rewriting is actively unsafe: `Config.Timeout` is a
  `time.Duration`, which `yaml.Marshal` would write back as raw nanoseconds.
  Anything wanting a `SaveConfig` needs a string field there first.

`store.DeleteCollection` is the only irreversible filesystem operation in the
codebase. It refuses a `Path` outside the collections directory; that guard
should be unreachable, which is the reason to keep it.

`ctrl+c`/`ctrl+v` are deliberately *not* used: `ctrl+c` is the quit key (four
hardcoded `msg.String()` sites), and `ctrl+v` is claimed by terminals that bind
it to paste, which with bracketed paste on arrives as `tea.PasteMsg` anyway.
The range keys bind both `shift+up`/`shift+down` and `K`/`J` because a shifted
letter stringifies as the capital, never `shift+k`, and not every terminal
emits `CSI 1;2A`.

## Conventions and traps

- **Bubble Tea v2 / Bubbles v2 / Lip Gloss v2** on `charm.land/*` vanity import
  paths (not `github.com/charmbracelet/*`). Key messages are `tea.KeyPressMsg`,
  the root returns `View() tea.View`, and the space key stringifies as `"space"`.
- **lipgloss v2 `Style.Width`/`Height` include the border** (v1 excluded it). A
  bordered pane's content area is `paneSize - 2`; see `appModel.layout()`.
- **gofmt 1.27 rewrites quotes in comments** (golang.org/issue/76975). Avoid a
  `''` pair and avoid double-backtick pairs inside Go comments, or `gofmt -l`
  will flag files you did not mean to change.
- **`internal/tui`'s `truncate` measures display cells**, via `reflow/truncate`
  plus `reflow/ansi` (aliased `trunc`/`reflowansi`). The fit check and the cut
  must use the *same* measurer, and reflow reserves the tail width
  unconditionally — so a string that already fits has to be returned untouched.
  Never truncate a rendered (ANSI-bearing) line by rune count.
- **`#nosec` annotations must carry a justification** after `--`; gosec runs in
  CI and must stay at 0 issues. The existing ones cover reading user-chosen
  files, launching `$EDITOR`, and paths derived from the workspace root.
- **Testing the TUI**: unit-test the model directly (build an `appModel`, feed it
  `tea.KeyPressMsg{Code: tea.KeyEscape}`, assert on state) — see
  `internal/tui/tui_test.go`. For end-to-end checks drive the real binary under
  tmux (`tmux new-session -d -x 130 -y 36 ...`, `send-keys`, `capture-pane -p`);
  remember the overlay draws inside a box, so rows are prefixed with `│`, and the
  status bar is not the last line of the pane.
