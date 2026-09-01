// Package store persists a workspace: collections, environments and settings
// as files inside a .crisp directory that can live in a project repo and be
// committed to git. Request history is deliberately not part of that — it
// holds response bodies, so it lives in the per-user state directory instead
// (see state.go).
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// DirName is the workspace directory created inside a project.
const DirName = ".crisp"

// gitignoreLines are the patterns a workspace must ignore. The *.local.yaml
// entry covers both environment secret overrides and the per-user
// workspace.local.yaml. The history directory no longer exists in a current
// workspace, but the pattern stays: it keeps an older workspace covered until
// its logs are migrated out, and covers anything a halted migration leaves
// behind.
var gitignoreLines = []string{"*.local.yaml", "history/"}

// Workspace is an open workspace rooted at a .crisp directory.
type Workspace struct {
	Root string
}

// Init creates a workspace under parent (parent/.crisp) with its
// subdirectories and a .gitignore for secrets. It is a no-op for pieces that
// already exist.
func Init(parent string) (*Workspace, error) {
	return InitAt(filepath.Join(parent, DirName))
}

// InitAt creates a workspace at exactly root. Most workspaces are a .crisp
// inside a project, which is what Init makes; the home workspace is not one,
// so the exact path has to be addressable.
func InitAt(root string) (*Workspace, error) {
	for _, dir := range []string{root, filepath.Join(root, "collections"), filepath.Join(root, "environments")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}
	w := &Workspace{Root: root}
	if err := w.ensureGitignore(); err != nil {
		return nil, err
	}
	return w, nil
}

// ensureGitignore appends any required pattern the workspace .gitignore is
// missing, creating the file if it does not exist. It only ever appends:
// a workspace may carry patterns its owner added, and an older workspace
// predates the history directory and so must be repaired in place rather
// than overwritten.
func (w *Workspace) ensureGitignore() error {
	path := filepath.Join(w.Root, ".gitignore")
	data, err := os.ReadFile(path) // #nosec G304 -- a path derived from the workspace root
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	existing := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		existing[strings.TrimSpace(line)] = true
	}
	add := ""
	for _, want := range gitignoreLines {
		if !existing[want] {
			add += want + "\n"
		}
	}
	if add == "" {
		return nil
	}
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		add = "\n" + add
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- a path derived from the workspace root
	if err != nil {
		return err
	}
	if _, err := f.WriteString(add); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// workspaceMarkers are the things Init leaves in a workspace. A clone may
// carry only some of them, because git does not track an empty directory, so
// any one is enough to identify a workspace.
var workspaceMarkers = []string{"collections", "environments", ConfigName, ".gitignore"}

// Open returns the workspace at dir, accepting either the workspace directory
// itself or a directory containing one — so the path that works with init
// works with --workspace too.
//
// It refuses a directory carrying no sign of being a workspace. Nothing stops
// this package writing collections into an arbitrary directory, which is
// exactly why a mistyped --workspace or a stale CRISP_WORKSPACE has to fail
// here rather than quietly scattering files somewhere nobody is looking.
func Open(dir string) (*Workspace, error) {
	info, err := os.Stat(dir) // #nosec G703 -- the workspace path is named by the user running the command, in their own shell; opening it is the command's purpose
	if err != nil {
		return nil, fmt.Errorf("workspace %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace %s: not a directory", dir)
	}
	// Being handed a project directory is the same gesture as running init in
	// it, so a .crisp inside wins over the directory itself.
	if nested := filepath.Join(dir, DirName); isWorkspace(nested) {
		return &Workspace{Root: nested}, nil
	}
	if !isWorkspace(dir) {
		return nil, fmt.Errorf("%s is not a chilli-crisp workspace; run 'chilli-crisp init' to create one", dir)
	}
	return &Workspace{Root: dir}, nil
}

// isWorkspace reports whether dir is a directory that looks like a workspace.
func isWorkspace(dir string) bool {
	info, err := os.Stat(dir) // #nosec G703 -- a user-named workspace path, checked read-only
	if err != nil || !info.IsDir() {
		return false
	}
	// A directory named .crisp says what it is, which keeps Open in step with
	// Discover: a workspace found by name is a workspace even when empty.
	if filepath.Base(dir) == DirName {
		return true
	}
	for _, marker := range workspaceMarkers {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil { // #nosec G703 -- a fixed marker name under a user-named workspace path
			return true
		}
	}
	return false
}

// Discover walks up from start looking for a .crisp directory, mirroring
// how git finds its repository root.
func Discover(start string) (*Workspace, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, false
	}
	for {
		candidate := filepath.Join(dir, DirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return &Workspace{Root: candidate}, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, false
		}
		dir = parent
	}
}

func (w *Workspace) collectionsDir() string  { return filepath.Join(w.Root, "collections") }
func (w *Workspace) environmentsDir() string { return filepath.Join(w.Root, "environments") }

// legacyHistoryPath is the single pre-rotation log file, migrated on first use.
func (w *Workspace) legacyHistoryPath() string { return filepath.Join(w.Root, "history.jsonl") }

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

// Slug converts a display name to a safe file stem ("My API" -> "my-api").
func Slug(name string) string {
	s := slugStrip.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "unnamed"
	}
	return s
}

// stem is a workspace file's name without its .yaml extension, the
// unambiguous way to address one collection or environment.
func stem(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".yaml")
}

// resolveSavePath picks the file a not-yet-saved value should be written to:
// <slug>.yaml, or the next free stem when that is taken. It never returns a
// path that already exists, so saving something new cannot overwrite
// anything — names that slug alike get their own files.
func resolveSavePath(dir, name string) string {
	slug := Slug(name)
	for i := 1; ; i++ {
		candidate := filepath.Join(dir, slug+".yaml")
		if i > 1 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s-%d.yaml", slug, i))
		}
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}

// Collections loads every collection in the workspace, in the order given by
// the workspace config and otherwise by filename. Each loaded collection and
// request is given a runtime ID.
func (w *Workspace) Collections() ([]*model.Collection, error) {
	files, err := sortedYAMLFiles(w.collectionsDir())
	if err != nil {
		return nil, err
	}
	var cols []*model.Collection
	for _, f := range files {
		c, err := readYAML[model.Collection](f)
		if err != nil {
			return nil, fmt.Errorf("collection %s: %w", filepath.Base(f), err)
		}
		c.Path = f
		c.EnsureIDs()
		cols = append(cols, c)
	}
	cfg, err := w.Config()
	if err != nil {
		return nil, err
	}
	return orderCollections(cols, cfg.Collections), nil
}

// Collection loads one collection by name or by file stem. Names that slug
// alike are reported as ambiguous instead of resolving to an arbitrary one.
func (w *Workspace) Collection(name string) (*model.Collection, error) {
	cols, err := w.Collections()
	if err != nil {
		return nil, err
	}
	// A file stem is a literal, unique address, so it wins outright; names
	// are matched loosely by slug and may be ambiguous.
	for _, c := range cols {
		if stem(c.Path) == strings.TrimSpace(name) {
			return c, nil
		}
	}
	want := Slug(name)
	var hits []*model.Collection
	for _, c := range cols {
		if Slug(c.Name) == want {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("collection %q not found", name)
	case 1:
		return hits[0], nil
	default:
		var names []string
		for _, c := range hits {
			names = append(names, fmt.Sprintf("  %s (%s)", c.Name, stem(c.Path)))
		}
		return nil, fmt.Errorf("%q matches %d collections; use the file name in brackets instead:\n%s",
			name, len(hits), strings.Join(names, "\n"))
	}
}

// SaveCollection writes c to the file it was loaded from, or to a free
// collections/<slug>.yaml for a collection that has never been saved. A new
// collection never overwrites an existing file, even if their names slug
// alike.
func (w *Workspace) SaveCollection(c *model.Collection) error {
	if c.Path == "" {
		c.Path = resolveSavePath(w.collectionsDir(), c.Name)
	}
	return writeYAML(c.Path, c)
}

// DeleteCollection removes a collection's file.
//
// A collection that was never saved has no Path and is simply forgotten, which
// is not an error. A Path outside the collections directory is refused: it can
// only be set by Collections or resolveSavePath, both of which build it from
// the workspace root, so the guard should never fire — but this is the one
// irreversible filesystem operation in the package and it should not be
// possible to aim it elsewhere.
func (w *Workspace) DeleteCollection(c *model.Collection) error {
	if c.Path == "" {
		return nil
	}
	dir := w.collectionsDir()
	abs, err := filepath.Abs(c.Path)
	if err != nil {
		return err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if filepath.Dir(abs) != absDir {
		return fmt.Errorf("refusing to delete %s: outside %s", c.Path, dir)
	}
	return os.Remove(abs) // #nosec G304 -- a path derived from the workspace root, checked above
}

// ImportCollection saves c, replacing an existing collection that has the
// very same name instead of creating a second file — re-importing a spec
// refreshes the collection it came from. Any other name gets its own file,
// as with SaveCollection.
func (w *Workspace) ImportCollection(c *model.Collection) error {
	if c.Path == "" {
		cols, err := w.Collections()
		if err != nil {
			return err
		}
		for _, existing := range cols {
			if existing.Name == c.Name {
				c.Path = existing.Path
				break
			}
		}
	}
	return w.SaveCollection(c)
}

// Environments loads every environment. Overrides from <name>.local.yaml are
// kept separately in LocalVars — resolve variables via AllVars, which merges
// them with local values winning. Local-only files are skipped as standalone
// environments.
func (w *Workspace) Environments() ([]*model.Environment, error) {
	files, err := sortedYAMLFiles(w.environmentsDir())
	if err != nil {
		return nil, err
	}
	var envs []*model.Environment
	for _, f := range files {
		if strings.HasSuffix(f, ".local.yaml") {
			continue
		}
		e, err := readYAML[model.Environment](f)
		if err != nil {
			return nil, fmt.Errorf("environment %s: %w", filepath.Base(f), err)
		}
		localPath := strings.TrimSuffix(f, ".yaml") + ".local.yaml"
		if _, statErr := os.Stat(localPath); statErr == nil {
			local, err := readYAML[model.Environment](localPath)
			if err != nil {
				return nil, fmt.Errorf("environment %s: %w", filepath.Base(localPath), err)
			}
			e.LocalVars = local.Vars
		}
		e.Path = f
		envs = append(envs, e)
	}
	return envs, nil
}

// Environment loads one environment by name or by file stem. Names that slug
// alike are reported as ambiguous instead of resolving to an arbitrary one.
func (w *Workspace) Environment(name string) (*model.Environment, error) {
	envs, err := w.Environments()
	if err != nil {
		return nil, err
	}
	for _, e := range envs {
		if stem(e.Path) == strings.TrimSpace(name) {
			return e, nil
		}
	}
	want := Slug(name)
	var hits []*model.Environment
	for _, e := range envs {
		if Slug(e.Name) == want {
			hits = append(hits, e)
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("environment %q not found", name)
	case 1:
		return hits[0], nil
	default:
		var names []string
		for _, e := range hits {
			names = append(names, fmt.Sprintf("  %s (%s)", e.Name, stem(e.Path)))
		}
		return nil, fmt.Errorf("%q matches %d environments; use the file name in brackets instead:\n%s",
			name, len(hits), strings.Join(names, "\n"))
	}
}

// SaveEnvironment writes e to environments/<slug>.yaml, the shareable file.
// Only the shared Vars are written: LocalVars is never serialized, so
// secrets loaded from a .local.yaml override cannot end up in the committed
// file.
func (w *Workspace) SaveEnvironment(e *model.Environment) error {
	if e.Path == "" {
		e.Path = resolveSavePath(w.environmentsDir(), e.Name)
	}
	return writeYAML(e.Path, e)
}

func sortedYAMLFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".yml") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	return files, nil
}

func readYAML[T any](path string) (*T, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- reading workspace files chosen by the user is this tool's purpose
	if err != nil {
		return nil, err
	}
	var v T
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
