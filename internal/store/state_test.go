package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHistoryLivesOutsideTheWorkspace(t *testing.T) {
	state := t.TempDir()
	t.Setenv(StateDirEnv, state)
	w, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, w, 3)

	// The whole point: nothing a repository can pick up.
	if _, err := os.Stat(filepath.Join(w.Root, "history")); !os.IsNotExist(err) {
		t.Error("a history directory was created inside the workspace")
	}
	dir, err := w.historyDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, state) {
		t.Errorf("history landed at %s, want it under the state directory %s", dir, state)
	}
	if got := logLines(t, w, 1); got != 3 {
		t.Errorf("state log holds %d entries, want 3", got)
	}
}

func TestWorkspaceIDIsStableAndIgnored(t *testing.T) {
	t.Setenv(StateDirEnv, t.TempDir())
	parent := t.TempDir()
	first, err := Init(parent)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, first, 1)

	// Re-opening the same workspace must reach the same log, or history
	// would restart every time the process does.
	reopened, err := Open(first.Root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := first.historyDir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := reopened.historyDir()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("reopening the workspace moved its history from %s to %s", a, b)
	}

	other, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, err := other.historyDir()
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Error("two workspaces share one log directory")
	}

	// The id file has to be covered by a pattern the workspace already
	// ignores, which is why it is named *.local.yaml.
	if !strings.HasSuffix(LocalName, ".local.yaml") {
		t.Errorf("%s is not covered by the *.local.yaml pattern", LocalName)
	}
	if _, err := os.Stat(first.localPath()); err != nil {
		t.Errorf("expected %s to exist after a send: %v", LocalName, err)
	}
	ignore, err := os.ReadFile(filepath.Join(first.Root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ignore), "*.local.yaml") {
		t.Errorf("workspace .gitignore does not cover the id file: %q", ignore)
	}
}

func TestPathsDoNotAssignAnID(t *testing.T) {
	t.Setenv(StateDirEnv, t.TempDir())
	w, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Reporting where the log would go must not be the thing that decides it.
	p, err := w.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "" || p.HistoryDir != "" {
		t.Errorf("a workspace with no sends reported id %q and history %q", p.ID, p.HistoryDir)
	}
	if _, err := os.Stat(w.localPath()); !os.IsNotExist(err) {
		t.Error("Paths wrote the id file")
	}

	appendN(t, w, 1)
	p, err = w.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.HistoryDir == "" {
		t.Error("after a send, Paths should report both the id and the history directory")
	}
	dir, err := w.historyDir()
	if err != nil {
		t.Fatal(err)
	}
	if p.HistoryDir != dir {
		t.Errorf("Paths reports %s, history is written to %s", p.HistoryDir, dir)
	}
}

// seedWorkspaceLog writes a rotated log into the old in-workspace location,
// as a workspace last used before the move would have.
func seedWorkspaceLog(t *testing.T, w *Workspace, name, requestName string) string {
	t.Helper()
	dir := filepath.Join(w.Root, "history")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	line := `{"request":{"name":"` + requestName + `"},"status":200}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil { // #nosec G306 -- deliberately loose, the migration must tighten it
		t.Fatal(err)
	}
	return dir
}

func TestHistoryMigratesRotatedDirIntoState(t *testing.T) {
	w := testWorkspace(t)
	src := seedWorkspaceLog(t, w, "000001.jsonl", "1")

	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ordinals(t, entries); len(got) != 1 || got[0] != 1 {
		t.Errorf("migrated entries are %v, want just 1", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the emptied workspace history directory should have been removed")
	}
	info, err := os.Stat(historyPath(t, w, 1))
	if err != nil {
		t.Fatalf("expected the log in the state directory: %v", err)
	}
	// The workspace copy was group- and world-readable; migrating it must not
	// carry that across, since it holds response bodies.
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("migrated log has mode %o, want 600", mode)
	}
}

func TestHistoryMigrationLeavesSourceOnConflict(t *testing.T) {
	w := testWorkspace(t)
	appendN(t, w, 1) // the state directory now holds a log
	src := seedWorkspaceLog(t, w, "000001.jsonl", "99")

	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	// Merging would reorder or overwrite entries, so the migration declines
	// and leaves the workspace copy where the user can find it.
	if got := ordinals(t, entries); len(got) != 1 || got[0] != 1 {
		t.Errorf("read %v, want only the state directory entry 1", got)
	}
	if _, err := os.Stat(filepath.Join(src, "000001.jsonl")); err != nil {
		t.Errorf("the workspace copy should have been left in place: %v", err)
	}
}

func TestCopyFileWritesATightNewFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil { // #nosec G306 -- the loose mode is the point
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst) // #nosec G304 -- a path built by this test
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "body\n" {
		t.Errorf("copied %q, want the source content", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("copy has mode %o, want 600", mode)
	}
	// The fallback is only ever used to reach a path nothing occupies, so
	// clobbering one would mean something has gone wrong upstream.
	if err := copyFile(src, dst); err == nil {
		t.Error("copyFile overwrote an existing destination")
	}
}

func TestMoveFileTightensTheMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil { // #nosec G306 -- the loose mode is the point
		t.Fatal(err)
	}
	if err := moveFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the source should be gone after a move")
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("moved file has mode %o, want 600", mode)
	}
}

func TestStateDirHonoursTheOverride(t *testing.T) {
	t.Setenv(StateDirEnv, "/somewhere/chosen")
	dir, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/somewhere/chosen" {
		t.Errorf("got %s, want the override used verbatim", dir)
	}
}

func TestStateDirFollowsXDG(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("XDG is not the convention on this platform")
	}
	t.Setenv(StateDirEnv, "")
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	dir, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/xdg/state", appDir); dir != want {
		t.Errorf("got %s, want %s", dir, want)
	}

	t.Setenv("XDG_STATE_HOME", "")
	dir, err = StateDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory to check the fallback against")
	}
	if want := filepath.Join(home, ".local", "state", appDir); dir != want {
		t.Errorf("got %s, want %s", dir, want)
	}
}
