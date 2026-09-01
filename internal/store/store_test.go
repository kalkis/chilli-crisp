package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// isolate points the per-user directories at temporary ones, so a test can
// never read or write the developer's real state directory or home workspace,
// and a CRISP_WORKSPACE set in their shell cannot change a result.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv(StateDirEnv, t.TempDir())
	t.Setenv(ConfigDirEnv, t.TempDir())
	t.Setenv(WorkspaceEnv, "")
}

func testWorkspace(t *testing.T) *Workspace {
	t.Helper()
	isolate(t)
	w, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// writeConfig replaces a workspace's config.yaml.
func writeConfig(t *testing.T, w *Workspace, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(w.Root, ConfigName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// homeWorkspace creates the home workspace inside the isolated config dir.
func homeWorkspace(t *testing.T) *Workspace {
	t.Helper()
	path, err := HomeWorkspacePath()
	if err != nil {
		t.Fatal(err)
	}
	w, err := InitAt(path)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// initAt makes a workspace under a fresh temporary parent and returns both.
func initAt(t *testing.T) (parent string, w *Workspace) {
	t.Helper()
	parent = t.TempDir()
	w, err := Init(parent)
	if err != nil {
		t.Fatal(err)
	}
	return parent, w
}

// samePath compares two paths after resolving symlinks, since a temporary
// directory is reached through one on macOS.
func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	return resolve(a) == resolve(b)
}

func TestInitCreatesLayout(t *testing.T) {
	parent := t.TempDir()
	w, err := Init(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"collections", "environments", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(w.Root, p)); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
	// Init on an existing workspace must not fail or clobber.
	if _, err := Init(parent); err != nil {
		t.Errorf("re-init failed: %v", err)
	}
}

func TestDiscoverWalksUp(t *testing.T) {
	parent := t.TempDir()
	if _, err := Init(parent); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(parent, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	w, ok := Discover(nested)
	if !ok {
		t.Fatal("expected to discover workspace from nested dir")
	}
	if w.Root != filepath.Join(parent, DirName) {
		t.Errorf("got root %s", w.Root)
	}
	if _, ok := Discover(t.TempDir()); ok {
		t.Error("should not discover a workspace in an unrelated dir")
	}
}

func TestCollectionRoundTrip(t *testing.T) {
	w := testWorkspace(t)
	c := &model.Collection{
		Name: "My API",
		Vars: map[string]string{"baseUrl": "http://localhost:8080"},
		Auth: &model.Auth{Type: model.AuthBearer, Token: "{{token}}"},
		Requests: []model.Request{{
			Name:    "create user",
			Method:  "POST",
			URL:     "{{baseUrl}}/v1/users",
			Headers: []model.Param{{Name: "Content-Type", Value: "application/json"}},
			Query:   []model.Param{{Name: "verbose", Value: "true", Disabled: true}},
			Body:    &model.Body{Type: model.BodyJSON, Content: `{"name":"Ada"}`},
		}},
	}
	if err := w.SaveCollection(c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Root, "collections", "my-api.yaml")); err != nil {
		t.Fatalf("expected slugged filename: %v", err)
	}
	got, err := w.Collection("My API")
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests[0].URL != c.Requests[0].URL || got.Auth.Token != "{{token}}" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if !got.Requests[0].Query[0].Disabled {
		t.Error("disabled flag lost in round trip")
	}
}

func TestSaveCollectionSuffixesOnCollision(t *testing.T) {
	w := testWorkspace(t)
	first := &model.Collection{Name: "my api"}
	second := &model.Collection{Name: "My API"}
	if err := w.SaveCollection(first); err != nil {
		t.Fatal(err)
	}
	if err := w.SaveCollection(second); err != nil {
		t.Fatal(err)
	}
	if stem(first.Path) != "my-api" || stem(second.Path) != "my-api-2" {
		t.Fatalf("expected my-api and my-api-2, got %s and %s", stem(first.Path), stem(second.Path))
	}
	cols, err := w.Collections()
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 {
		t.Fatalf("both collections should survive, got %d", len(cols))
	}
	byStem := map[string]string{}
	for _, c := range cols {
		byStem[stem(c.Path)] = c.Name
	}
	if byStem["my-api"] != "my api" || byStem["my-api-2"] != "My API" {
		t.Errorf("names clobbered: %v", byStem)
	}
}

// Creating a new collection must never wipe an existing one, even when the
// names are identical — only an explicit import refreshes in place.
func TestSaveCollectionNeverOverwritesExisting(t *testing.T) {
	w := testWorkspace(t)
	if err := w.SaveCollection(&model.Collection{
		Name:     "My API",
		Requests: []model.Request{{Name: "keep me", Method: "GET", URL: "http://x"}},
	}); err != nil {
		t.Fatal(err)
	}
	fresh := &model.Collection{Name: "My API"}
	if err := w.SaveCollection(fresh); err != nil {
		t.Fatal(err)
	}
	if stem(fresh.Path) != "my-api-2" {
		t.Errorf("a new same-named collection should get its own file, got %s", stem(fresh.Path))
	}
	cols, err := w.Collections()
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 {
		t.Fatalf("expected both collections, got %d", len(cols))
	}
	original, err := w.Collection("my-api")
	if err != nil {
		t.Fatal(err)
	}
	if len(original.Requests) != 1 || original.Requests[0].Name != "keep me" {
		t.Errorf("original collection was clobbered: %+v", original.Requests)
	}
}

func TestImportCollectionRefreshesSameName(t *testing.T) {
	w := testWorkspace(t)
	if err := w.SaveCollection(&model.Collection{
		Name:     "My API",
		Requests: []model.Request{{Name: "old", Method: "GET", URL: "http://x"}},
	}); err != nil {
		t.Fatal(err)
	}
	// A re-import arrives as a fresh value with no Path; it must replace the
	// same-named collection rather than create a second file.
	fresh := &model.Collection{
		Name:     "My API",
		Requests: []model.Request{{Name: "new", Method: "GET", URL: "http://x"}},
	}
	if err := w.ImportCollection(fresh); err != nil {
		t.Fatal(err)
	}
	if stem(fresh.Path) != "my-api" {
		t.Errorf("re-import should reuse my-api.yaml, got %s", stem(fresh.Path))
	}
	cols, err := w.Collections()
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 {
		t.Fatalf("re-import should not add a file, got %d collections", len(cols))
	}
	if cols[0].Requests[0].Name != "new" {
		t.Errorf("refresh did not take effect: %+v", cols[0].Requests)
	}

	// A differently named import still gets its own file.
	other := &model.Collection{Name: "my api"}
	if err := w.ImportCollection(other); err != nil {
		t.Fatal(err)
	}
	if stem(other.Path) != "my-api-2" {
		t.Errorf("a differently named import should not clobber, got %s", stem(other.Path))
	}
}

func TestSaveCollectionWritesBackToSourceFile(t *testing.T) {
	w := testWorkspace(t)
	oddPath := filepath.Join(w.Root, "collections", "staging-new.yaml")
	if err := os.WriteFile(oddPath, []byte("name: staging\nrequests: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := w.Collection("staging")
	if err != nil {
		t.Fatal(err)
	}
	c.Requests = append(c.Requests, model.Request{Name: "r", Method: "GET", URL: "http://x"})
	if err := w.SaveCollection(c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Root, "collections", "staging.yaml")); err == nil {
		t.Error("save should not orphan the source file by writing to the slug path")
	}
	data, err := os.ReadFile(oddPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: r") {
		t.Errorf("edit did not land in the source file:\n%s", data)
	}
}

func TestCollectionAmbiguousName(t *testing.T) {
	w := testWorkspace(t)
	if err := w.SaveCollection(&model.Collection{Name: "my api"}); err != nil {
		t.Fatal(err)
	}
	if err := w.SaveCollection(&model.Collection{Name: "My API"}); err != nil {
		t.Fatal(err)
	}
	_, err := w.Collection("My API")
	if err == nil {
		t.Fatal("expected an ambiguity error")
	}
	if !strings.Contains(err.Error(), "my-api-2") || !strings.Contains(err.Error(), "matches 2 collections") {
		t.Errorf("error should list candidate files, got: %v", err)
	}
	got, err := w.Collection("my-api-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "My API" {
		t.Errorf("stem lookup returned the wrong collection: %q", got.Name)
	}
	// The unsuffixed stem must stay addressable too, even though both names
	// slug to it.
	got, err = w.Collection("my-api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "my api" {
		t.Errorf("stem lookup returned the wrong collection: %q", got.Name)
	}
}

func TestEnvironmentAmbiguousName(t *testing.T) {
	w := testWorkspace(t)
	if err := w.SaveEnvironment(&model.Environment{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	if err := w.SaveEnvironment(&model.Environment{Name: "Staging"}); err != nil {
		t.Fatal(err)
	}
	// "Staging" is not a file stem, and both names slug alike.
	if _, err := w.Environment("Staging"); err == nil {
		t.Error("expected an ambiguity error for a name matching both")
	}
	// Each file stem still addresses exactly one environment.
	for stemName, want := range map[string]string{"staging": "staging", "staging-2": "Staging"} {
		got, err := w.Environment(stemName)
		if err != nil {
			t.Fatalf("%s: %v", stemName, err)
		}
		if got.Name != want {
			t.Errorf("%s resolved to %q, want %q", stemName, got.Name, want)
		}
	}
}

func TestCollectionsAssignIDs(t *testing.T) {
	w := testWorkspace(t)
	if err := w.SaveCollection(&model.Collection{
		Name:     "api",
		Requests: []model.Request{{Name: "a", Method: "GET"}, {Name: "b", Method: "GET"}},
	}); err != nil {
		t.Fatal(err)
	}
	cols, err := w.Collections()
	if err != nil {
		t.Fatal(err)
	}
	c := cols[0]
	if c.ID == 0 || c.Requests[0].ID == 0 || c.Requests[1].ID == 0 {
		t.Fatalf("loading should assign runtime IDs: %+v", c)
	}
	if c.Requests[0].ID == c.Requests[1].ID {
		t.Error("request IDs must be unique")
	}
	// IDs are runtime-only and must not reach the file.
	data, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "id:") {
		t.Errorf("IDs leaked into the saved file:\n%s", data)
	}
}

func TestCollectionsFollowConfigOrder(t *testing.T) {
	w := testWorkspace(t)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := w.SaveCollection(&model.Collection{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	// Default is filename order.
	cols, err := w.Collections()
	if err != nil {
		t.Fatal(err)
	}
	if cols[0].Name != "alpha" || cols[2].Name != "gamma" {
		t.Fatalf("expected filename order, got %s, %s, %s", cols[0].Name, cols[1].Name, cols[2].Name)
	}

	cfg := "collections:\n  - gamma\n  - alpha\n"
	if err := os.WriteFile(filepath.Join(w.Root, ConfigName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cols, err = w.Collections()
	if err != nil {
		t.Fatal(err)
	}
	// Listed collections lead in config order; unlisted ones follow.
	got := []string{cols[0].Name, cols[1].Name, cols[2].Name}
	want := []string{"gamma", "alpha", "beta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("config order not honoured: got %v, want %v", got, want)
		}
	}
}

func TestConfigTimeout(t *testing.T) {
	cases := []struct {
		name string
		cfg  string // config.yaml contents; "" writes no file at all
		want time.Duration
	}{
		{"no config file", "", DefaultTimeout},
		{"config without a timeout", "collections:\n  - alpha\n", DefaultTimeout},
		{"duration string", "timeout: 45s\n", 45 * time.Second},
		{"minutes", "timeout: 2m\n", 2 * time.Minute},
		// Zero and negative both mean "unset": one sentinel, no way to
		// accidentally ask for an instantly-expiring request.
		{"zero", "timeout: 0s\n", DefaultTimeout},
		{"negative", "timeout: -5s\n", DefaultTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := testWorkspace(t)
			if c.cfg != "" {
				if err := os.WriteFile(filepath.Join(w.Root, ConfigName), []byte(c.cfg), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := w.RequestTimeout()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("RequestTimeout() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestConfigHistoryMode(t *testing.T) {
	cases := []struct {
		name string
		cfg  string // config.yaml contents; "" writes no file at all
		want HistoryMode
	}{
		{"no config file", "", HistoryFull},
		{"config without a history key", "timeout: 45s\n", HistoryFull},
		{"full", "history: full\n", HistoryFull},
		{"metadata", "history: metadata\n", HistoryMetadata},
		{"off", "history: off\n", HistoryOff},
		{"case and spacing are ignored", "history: \" OFF \"\n", HistoryOff},
		// The field reads like a switch, so the switch spellings have to mean
		// what they look like. YAML resolves off and no as strings; false is
		// a bool node that still decodes into the string.
		{"false", "history: false\n", HistoryOff},
		{"no", "history: no\n", HistoryOff},
		{"true", "history: true\n", HistoryFull},
		{"on", "history: on\n", HistoryFull},
		// An empty value is the same as leaving the key out, matching the way
		// a zero timeout means the default.
		{"present but empty", "history:\n", HistoryFull},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := testWorkspace(t)
			if c.cfg != "" {
				writeConfig(t, w, c.cfg)
			}
			got, err := w.HistoryMode()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("HistoryMode() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestConfigHistoryModeRejectsAnUnknownValue pins the one behaviour this
// setting cannot get wrong. A typo that fell back to the default would
// silently restore full recording in a workspace whose owner believed they
// had turned it off, so it has to fail loudly and say what is allowed.
func TestConfigHistoryModeRejectsAnUnknownValue(t *testing.T) {
	w := testWorkspace(t)
	writeConfig(t, w, "history: of\n")

	_, err := w.HistoryMode()
	if err == nil {
		t.Fatal("an unrecognised history value must be an error")
	}
	for _, want := range []string{"of", "full", "metadata", "off"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	// Config is read for collection ordering too, so the whole workspace is
	// unreadable until it is fixed. That is the intended loudness.
	if _, err := w.Collections(); err == nil {
		t.Error("a broken config should fail every read of it, not just the history one")
	}
}

func TestEnvironmentLocalOverride(t *testing.T) {
	w := testWorkspace(t)
	if err := w.SaveEnvironment(&model.Environment{
		Name: "staging",
		Vars: map[string]string{"baseUrl": "https://staging.example.com", "token": "placeholder"},
	}); err != nil {
		t.Fatal(err)
	}
	local := "name: staging\nvars:\n  token: real-secret\n"
	if err := os.WriteFile(filepath.Join(w.Root, "environments", "staging.local.yaml"), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := w.Environment("staging")
	if err != nil {
		t.Fatal(err)
	}
	all := e.AllVars()
	if all["token"] != "real-secret" {
		t.Errorf("local override should win in AllVars, got %q", all["token"])
	}
	if all["baseUrl"] != "https://staging.example.com" {
		t.Errorf("non-overridden vars should survive, got %q", all["baseUrl"])
	}
	if e.Vars["token"] != "placeholder" {
		t.Errorf("shared Vars must keep the shareable value, got %q", e.Vars["token"])
	}
	if e.LocalVars["token"] != "real-secret" {
		t.Errorf("LocalVars should hold the override, got %q", e.LocalVars["token"])
	}
	envs, err := w.Environments()
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 {
		t.Errorf(".local.yaml must not appear as its own environment, got %d", len(envs))
	}
}

func TestSaveEnvironmentNeverWritesLocalVars(t *testing.T) {
	w := testWorkspace(t)
	if err := w.SaveEnvironment(&model.Environment{
		Name: "staging",
		Vars: map[string]string{"token": "placeholder"},
	}); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(w.Root, "environments", "staging.local.yaml")
	local := "name: staging\nvars:\n  token: real-secret\n"
	if err := os.WriteFile(localPath, []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}

	// The trap: load-modify-save must not copy the local secret into the
	// shareable file.
	e, err := w.Environment("staging")
	if err != nil {
		t.Fatal(err)
	}
	e.Vars["baseUrl"] = "https://staging.example.com"
	if err := w.SaveEnvironment(e); err != nil {
		t.Fatal(err)
	}

	shared, err := os.ReadFile(filepath.Join(w.Root, "environments", "staging.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(shared)
	if !strings.Contains(got, "placeholder") || !strings.Contains(got, "baseUrl") {
		t.Errorf("shared file lost shareable vars:\n%s", got)
	}
	if strings.Contains(got, "real-secret") {
		t.Errorf("local secret leaked into the shareable file:\n%s", got)
	}
	localAfter, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(localAfter) != local {
		t.Errorf(".local.yaml changed: %q", localAfter)
	}
}

func TestVarsRoundTripAndStayOutOfFilesThatHaveNone(t *testing.T) {
	w := testWorkspace(t)
	c := &model.Collection{
		Name:    "vars api",
		Vars:    map[string]string{"baseUrl": "http://localhost:3000", "id": "1"},
		Folders: []model.Folder{{Name: "admin", Vars: map[string]string{"id": "7"}}},
		Requests: []model.Request{
			{Name: "getTodo", Folder: "admin", Method: "GET", URL: "{{baseUrl}}/todos/{{id}}", Vars: map[string]string{"id": "10"}},
			{Name: "health", Method: "GET", URL: "{{baseUrl}}/health"},
		},
	}
	if err := w.SaveCollection(c); err != nil {
		t.Fatal(err)
	}
	got, err := w.Collection("vars api")
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Requests[0].Vars["id"]; v != "10" {
		t.Errorf("request vars lost in round trip: %v", got.Requests[0].Vars)
	}
	if v := got.FolderVars("admin"); v["id"] != "7" {
		t.Errorf("folder vars lost in round trip: %v", v)
	}
	if got.Requests[1].Vars != nil {
		t.Errorf("a request without vars should load without a map, got %v", got.Requests[1].Vars)
	}

	// Neither key may appear for a collection that declares none, so existing
	// workspaces are untouched by the upgrade.
	plain := &model.Collection{Name: "plain api", Requests: []model.Request{{Name: "ping", Method: "GET", URL: "http://x"}}}
	if err := w.SaveCollection(plain); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(w.Root, "collections", "plain-api.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"folders:", "vars:"} {
		if strings.Contains(string(data), key) {
			t.Errorf("%q should be omitted from a collection that has none:\n%s", key, data)
		}
	}
}

func TestDeleteCollectionRemovesTheFile(t *testing.T) {
	w := testWorkspace(t)
	col := &model.Collection{Name: "my api", Requests: []model.Request{{Name: "a", Method: "GET"}}}
	if err := w.SaveCollection(col); err != nil {
		t.Fatal(err)
	}
	path := col.Path

	if err := w.DeleteCollection(col); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file should be gone, stat err = %v", err)
	}
	if _, err := w.Collection("my api"); err == nil {
		t.Error("a deleted collection must no longer resolve")
	}
}

// TestDeleteCollectionIgnoresAnUnsavedOne: a collection that was never written
// has no file to remove, which is nothing to do rather than an error.
func TestDeleteCollectionIgnoresAnUnsavedOne(t *testing.T) {
	w := testWorkspace(t)
	if err := w.DeleteCollection(&model.Collection{Name: "never saved"}); err != nil {
		t.Errorf("deleting an unsaved collection should be a no-op, got %v", err)
	}
}

// TestDeleteCollectionRefusesAPathOutsideTheWorkspace guards the one
// irreversible filesystem operation in the package. Path is only ever set from
// the workspace root, so this should be unreachable — which is the point.
func TestDeleteCollectionRefusesAPathOutsideTheWorkspace(t *testing.T) {
	w := testWorkspace(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.yaml")
	if err := os.WriteFile(outside, []byte("name: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := w.DeleteCollection(&model.Collection{Name: "x", Path: outside})

	if err == nil {
		t.Fatal("a path outside the collections directory must be refused")
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Errorf("the outside file must be untouched, stat err = %v", statErr)
	}
}

func TestResolvePrefersTheFlag(t *testing.T) {
	isolate(t)
	home := homeWorkspace(t)
	parent, project := initAt(t)
	t.Chdir(parent)

	w, src, err := Resolve(project.Root)
	if err != nil {
		t.Fatal(err)
	}
	if src != FromFlag {
		t.Errorf("resolved via %q, want %q", src, FromFlag)
	}
	if samePath(t, w.Root, home.Root) {
		t.Error("an explicit path lost to the home workspace")
	}
	if !samePath(t, w.Root, project.Root) {
		t.Errorf("opened %s, want %s", w.Root, project.Root)
	}
}

func TestResolveUsesTheEnvironment(t *testing.T) {
	isolate(t)
	homeWorkspace(t)
	// A project under the cursor, and an environment variable pointing
	// elsewhere: the variable is the more deliberate statement, so it wins.
	parent, discovered := initAt(t)
	_, named := initAt(t)
	t.Chdir(parent)
	t.Setenv(WorkspaceEnv, named.Root)

	w, src, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if src != FromEnv {
		t.Errorf("resolved via %q, want %q", src, FromEnv)
	}
	if samePath(t, w.Root, discovered.Root) {
		t.Error("the environment variable lost to discovery")
	}
	if !samePath(t, w.Root, named.Root) {
		t.Errorf("opened %s, want %s", w.Root, named.Root)
	}
}

func TestResolveDiscoveryBeatsTheHomeWorkspace(t *testing.T) {
	isolate(t)
	home := homeWorkspace(t)
	parent, project := initAt(t)
	nested := filepath.Join(parent, "a", "b")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	// Standing in a project that has a .crisp must give that .crisp, however
	// many workspaces the user has elsewhere.
	w, src, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if src != FromDiscovery {
		t.Errorf("resolved via %q, want %q", src, FromDiscovery)
	}
	if samePath(t, w.Root, home.Root) {
		t.Error("the home workspace outranked one found from the current directory")
	}
	if !samePath(t, w.Root, project.Root) {
		t.Errorf("opened %s, want %s", w.Root, project.Root)
	}
}

func TestResolveFallsBackToTheHomeWorkspace(t *testing.T) {
	isolate(t)
	home := homeWorkspace(t)
	// Nowhere in particular, which is where a QA user without a checkout of
	// the backend stands.
	t.Chdir(t.TempDir())

	w, src, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if src != FromHome {
		t.Errorf("resolved via %q, want %q", src, FromHome)
	}
	if !samePath(t, w.Root, home.Root) {
		t.Errorf("opened %s, want the home workspace %s", w.Root, home.Root)
	}
}

func TestResolveExhaustedNamesBothRoutes(t *testing.T) {
	isolate(t)
	t.Chdir(t.TempDir()) // no home workspace was created

	_, _, err := Resolve("")
	if err == nil {
		t.Fatal("expected an error when nothing resolves")
	}
	// The message is the only guidance a first-time user gets, so it has to
	// name both ways out rather than just the one for a project.
	for _, want := range []string{"chilli-crisp init", "--home", WorkspaceEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

func TestResolveDoesNotCreateAHomeWorkspace(t *testing.T) {
	isolate(t)
	t.Chdir(t.TempDir())

	if _, _, err := Resolve(""); err == nil {
		t.Fatal("expected an error when nothing resolves")
	}
	path, err := HomeWorkspacePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a failed resolve created the home workspace; only init --home may")
	}
}

func TestOpenAcceptsEitherDirectoryForm(t *testing.T) {
	parent, w := initAt(t)

	byRoot, err := Open(w.Root)
	if err != nil {
		t.Fatal(err)
	}
	// The path init is given must work with --workspace too, which is the
	// asymmetry this removes.
	byParent, err := Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	if byRoot.Root != w.Root || byParent.Root != w.Root {
		t.Errorf("got %s and %s, want both to be %s", byRoot.Root, byParent.Root, w.Root)
	}
}

func TestOpenRejectsADirectoryThatIsNotAWorkspace(t *testing.T) {
	// A mistyped flag or a stale environment variable must fail here rather
	// than quietly turning some unrelated directory into a workspace.
	_, err := Open(t.TempDir())
	if err == nil {
		t.Fatal("expected an unrelated directory to be refused")
	}
	if !strings.Contains(err.Error(), "init") {
		t.Errorf("the error should say how to make one: %v", err)
	}
}

func TestInitAtMakesAWorkspaceOpenAccepts(t *testing.T) {
	root := filepath.Join(t.TempDir(), HomeWorkspaceName)
	w, err := InitAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if w.Root != root {
		t.Errorf("InitAt made %s, want exactly %s", w.Root, root)
	}
	// Not named .crisp, so it is accepted on its contents alone.
	opened, err := Open(root)
	if err != nil {
		t.Fatalf("Open refused a workspace InitAt just made: %v", err)
	}
	if opened.Root != root {
		t.Errorf("opened %s, want %s", opened.Root, root)
	}
	if _, err := InitAt(root); err != nil {
		t.Errorf("InitAt is not idempotent: %v", err)
	}
}
