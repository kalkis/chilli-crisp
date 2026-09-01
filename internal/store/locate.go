package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDirEnv names the environment variable that overrides the per-user
// configuration directory. It is the counterpart of StateDirEnv, and what a
// test sets so it can never read or create the developer's own home
// workspace.
const ConfigDirEnv = "CRISP_CONFIG_DIR"

// WorkspaceEnv names a workspace path, for a shell profile, a direnv block or
// a CI job that would otherwise repeat --workspace on every invocation.
const WorkspaceEnv = "CRISP_WORKSPACE"

// HomeWorkspaceName is the home workspace's directory inside ConfigDir. It is
// deliberately not called .crisp, so Discover can never stumble onto it while
// walking up from some directory beneath it.
const HomeWorkspaceName = "workspace"

// Source says which rule resolved a workspace. It exists so that `where` can
// report it: with four rules in play, which one fired is the first thing
// anyone chasing a wrong workspace needs to know.
type Source string

const (
	FromFlag      Source = "--workspace"
	FromEnv       Source = "$" + WorkspaceEnv
	FromDiscovery Source = "the current directory"
	FromHome      Source = "the home workspace"
)

// ConfigDir is the per-user configuration directory. Unlike StateDir this
// needs no platform branching: os.UserConfigDir already answers correctly
// everywhere (XDG_CONFIG_HOME or ~/.config, ~/Library/Application Support,
// %AppData%).
func ConfigDir() (string, error) {
	if dir := os.Getenv(ConfigDirEnv); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating the configuration directory: %w (set %s to choose one)", err, ConfigDirEnv)
	}
	return filepath.Join(base, appDir), nil
}

// HomeWorkspacePath is the workspace belonging to the user rather than to any
// project — the answer for anyone testing an API whose code they do not check
// out.
func HomeWorkspacePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, HomeWorkspaceName), nil
}

// Resolve finds the workspace a command should act on, trying in order: an
// explicit path from --workspace, then CRISP_WORKSPACE, then a .crisp at or
// above the current directory, then the home workspace. First match wins.
//
// Discovery deliberately outranks the home workspace. Standing in a project
// that has a .crisp and getting that .crisp is the least surprising rule
// there is, and an explicit path covers anyone who wants otherwise.
//
// Nothing here creates a workspace: the home workspace counts only once
// init --home has made one, so no command writes into the configuration
// directory unasked.
func Resolve(explicit string) (*Workspace, Source, error) {
	if explicit != "" {
		w, err := Open(explicit)
		if err != nil {
			return nil, "", err
		}
		return w, FromFlag, nil
	}
	if env := os.Getenv(WorkspaceEnv); env != "" {
		w, err := Open(env)
		if err != nil {
			return nil, "", err
		}
		return w, FromEnv, nil
	}
	if w, ok := Discover("."); ok {
		return w, FromDiscovery, nil
	}
	home, err := HomeWorkspacePath()
	if err != nil {
		return nil, "", err
	}
	if isWorkspace(home) {
		w, err := Open(home)
		if err != nil {
			return nil, "", err
		}
		return w, FromHome, nil
	}
	return nil, "", fmt.Errorf(
		"no workspace found: nothing given with --workspace or %s, none in this directory "+
			"or any parent, and no home workspace at %s\n"+
			"Run 'chilli-crisp init' to create one here, or 'chilli-crisp init --home' for "+
			"one you can use from anywhere.",
		WorkspaceEnv, home)
}
