package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"
)

// StateDirEnv names the environment variable that overrides the per-user
// state directory outright. It is the answer wherever the home directory is
// unset or read-only, which is most CI containers.
const StateDirEnv = "CRISP_STATE_DIR"

// appDir is this program's directory name under whichever base the platform
// designates for per-user state.
const appDir = "chilli-crisp"

// LocalName is the per-user file inside a workspace. The *.local.yaml entry
// in gitignoreLines already covers it, so it is ignored from the moment it is
// written and no clone inherits one.
const LocalName = "workspace.local.yaml"

// workspaceLocal is the per-user side of a workspace: state that belongs to
// one person on one machine. It is a struct rather than a bare id so that
// later per-user settings have an obvious home.
type workspaceLocal struct {
	ID string `yaml:"id"`
}

// Paths reports where a workspace's files live.
type Paths struct {
	Root       string
	ID         string // empty until the workspace has been sent from
	HistoryDir string // empty when ID is
}

// StateDir is the per-user directory holding everything that must never enter
// a repository: today, the request logs.
//
// Response bodies live here rather than in the workspace because a .gitignore
// is a convention and not a boundary — it does not cover a Docker build
// context, a sync client watching the repo folder, or a source tarball, and
// git clean -xdf deletes what it does cover.
//
// There is no os.UserStateDir in the standard library, and os.UserCacheDir is
// the wrong home despite being free and portable: cache directories are swept
// by cleaners, which would quietly eat a log the user believes they still
// have.
func StateDir() (string, error) {
	if dir := os.Getenv(StateDirEnv); dir != "" {
		return dir, nil
	}
	switch runtime.GOOS {
	case "windows":
		if base := os.Getenv("LocalAppData"); base != "" {
			return filepath.Join(base, appDir), nil
		}
		return homeJoin("AppData", "Local", appDir)
	case "darwin":
		return homeJoin("Library", "Application Support", appDir)
	default:
		if base := os.Getenv("XDG_STATE_HOME"); base != "" {
			return filepath.Join(base, appDir), nil
		}
		return homeJoin(".local", "state", appDir)
	}
}

// homeJoin builds a path under the user's home directory, naming the escape
// hatch when there is no home to build under.
func homeJoin(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the state directory: %w (set %s to choose one)", err, StateDirEnv)
	}
	return filepath.Join(append([]string{home}, parts...)...), nil
}

// stateWorkspaceDir is where one workspace's state lives under base.
func stateWorkspaceDir(base, id string) string {
	return filepath.Join(base, "workspaces", id)
}

// localPath names the workspace's per-user file.
func (w *Workspace) localPath() string { return filepath.Join(w.Root, LocalName) }

// lookupWorkspaceID reads the workspace's id without assigning one. The bool
// reports whether it has an id yet, which is what lets a reporting command
// print the state paths without writing anything.
func (w *Workspace) lookupWorkspaceID() (string, bool, error) {
	data, err := os.ReadFile(w.localPath()) // #nosec G304 -- a path derived from the workspace root
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var local workspaceLocal
	if err := yaml.Unmarshal(data, &local); err != nil {
		return "", false, fmt.Errorf("%s: %w", LocalName, err)
	}
	return local.ID, local.ID != "", nil
}

// workspaceID returns the workspace's id, assigning one on first use.
//
// The id lives in a gitignored file rather than in the committed config.yaml
// so that two clones of one repository keep separate logs: a log records what
// you sent from this checkout. Assignment is a claim in the same sense that
// rolling a log is one — two senders can race here on a workspace's very
// first send, and losing that race means reading the winner's id rather than
// failing.
func (w *Workspace) workspaceID() (string, error) {
	id, ok, err := w.lookupWorkspaceID()
	if err != nil {
		return "", err
	}
	if ok {
		return id, nil
	}
	if id, err = newWorkspaceID(); err != nil {
		return "", err
	}
	data, err := yaml.Marshal(workspaceLocal{ID: id})
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(w.localPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- a path derived from the workspace root
	if errors.Is(err, os.ErrExist) {
		// The file was already there: either another sender claimed the id
		// between the read above and this write, or it exists with a blank
		// id. Re-reading settles the first; a blank still needs filling in.
		existing, ok, err := w.lookupWorkspaceID()
		if err != nil {
			return "", err
		}
		if ok {
			return existing, nil
		}
		return id, os.WriteFile(w.localPath(), data, 0o600)
	}
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", err
	}
	return id, f.Close()
}

// newWorkspaceID mints an id from the cryptographic source. These name
// directories under a root shared by every workspace the user has, so they
// must not collide.
func newWorkspaceID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Paths reports the workspace root, the id keying its per-user state, and the
// directory holding its logs. ID and HistoryDir come back empty for a
// workspace that has never been sent from, because reporting where the log
// would go must not be the thing that decides it.
func (w *Workspace) Paths() (Paths, error) {
	p := Paths{Root: w.Root}
	id, ok, err := w.lookupWorkspaceID()
	if err != nil || !ok {
		return p, err
	}
	base, err := StateDir()
	if err != nil {
		return p, err
	}
	p.ID = id
	p.HistoryDir = filepath.Join(stateWorkspaceDir(base, id), "history")
	return p, nil
}

// historyDir is the directory holding this workspace's rotated logs, created
// on demand. 0700: everything below it is response data.
func (w *Workspace) historyDir() (string, error) {
	base, err := StateDir()
	if err != nil {
		return "", err
	}
	id, err := w.workspaceID()
	if err != nil {
		return "", err
	}
	dir := stateWorkspaceDir(base, id)
	hist := filepath.Join(dir, "history")
	if err := os.MkdirAll(hist, 0o700); err != nil {
		return "", err
	}
	w.writeStateMeta(dir)
	return hist, nil
}

// stateMeta records which workspace a state directory belongs to. Nothing
// reads it yet: it is what makes a listing of the state root legible by hand,
// and what a later prune would need in order to tell a live workspace from
// one whose directory has since been deleted.
type stateMeta struct {
	Workspace string `yaml:"workspace"`
	Updated   string `yaml:"updated"`
}

// writeStateMeta refreshes the marker when it is missing or names a different
// path, so a moved workspace corrects its own record. Best effort throughout:
// a send must not fail because a descriptive file could not be written.
func (w *Workspace) writeStateMeta(dir string) {
	root, err := filepath.Abs(w.Root)
	if err != nil {
		return
	}
	path := filepath.Join(dir, "meta.yaml")
	if data, err := os.ReadFile(path); err == nil { // #nosec G304 -- a path derived from the state directory
		var existing stateMeta
		if yaml.Unmarshal(data, &existing) == nil && existing.Workspace == root {
			return
		}
	}
	data, err := yaml.Marshal(stateMeta{Workspace: root, Updated: time.Now().Format(time.RFC3339)})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// moveFile moves src to dst, falling back to a copy when a rename cannot
// cross between them.
//
// It does not test the rename error for EXDEV. A workspace in a repository
// and a state directory under the home directory are routinely on different
// filesystems — every repo under /mnt/c on WSL is one — and every rename
// failure is answered the same way here, so inspecting the errno would buy
// nothing but a platform-specific reference in a package that cross-compiles
// to six targets. A copy that fails reports its own error.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		// A rename carries the source mode across, so tighten it: these files
		// hold response bodies whatever they were before.
		return os.Chmod(dst, 0o600)
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// copyFile writes src to a new dst, refusing to overwrite an existing one and
// leaving no half-written file behind on failure.
func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- a path derived from the workspace root
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- a path derived from the state directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}
