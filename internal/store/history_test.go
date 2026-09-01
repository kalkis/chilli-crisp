package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// appendN writes n entries whose request names are the ordinals 1..n, so a
// read can assert exactly which entries came back and in what order.
func appendN(t *testing.T, w *Workspace, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		e := HistoryEntry{
			Time:    time.Now(),
			Request: model.Request{Name: strconv.Itoa(i), Method: "GET", URL: "http://x"},
			Status:  200,
		}
		if err := w.AppendHistory(e); err != nil {
			t.Fatal(err)
		}
	}
}

// ordinals reads back the request names as the integers appendN wrote.
func ordinals(t *testing.T, entries []HistoryEntry) []int {
	t.Helper()
	out := make([]int, len(entries))
	for i, e := range entries {
		n, err := strconv.Atoi(e.Request.Name)
		if err != nil {
			t.Fatalf("entry %d has a non-ordinal name %q", i, e.Request.Name)
		}
		out[i] = n
	}
	return out
}

// historyPath names one of a workspace's rotated logs, wherever the state
// directory put them.
func historyPath(t *testing.T, w *Workspace, index int) string {
	t.Helper()
	dir, err := w.historyDir()
	if err != nil {
		t.Fatal(err)
	}
	return historyFilePath(dir, index)
}

// historyIndicesOf lists the rotated logs a workspace currently has.
func historyIndicesOf(t *testing.T, w *Workspace) []int {
	t.Helper()
	dir, err := w.historyDir()
	if err != nil {
		t.Fatal(err)
	}
	indices, err := historyIndices(dir)
	if err != nil {
		t.Fatal(err)
	}
	return indices
}

// logLines counts the entries physically stored in one rotated log.
func logLines(t *testing.T, w *Workspace, index int) int {
	t.Helper()
	data, err := os.ReadFile(historyPath(t, w, index))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func TestHistoryAppendAndRead(t *testing.T) {
	w := testWorkspace(t)
	for i, status := range []int{200, 404, 500} {
		err := w.AppendHistory(HistoryEntry{
			Time:         time.Now().Add(time.Duration(i) * time.Second),
			Request:      model.Request{Name: "r", Method: "GET", URL: "http://x"},
			Status:       status,
			ResponseBody: "ok",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := w.History(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Status != 500 || entries[1].Status != 404 {
		t.Errorf("expected newest first, got %d then %d", entries[0].Status, entries[1].Status)
	}
	all, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("limit 0 should return all, got %d", len(all))
	}
}

func TestHistoryTruncatesBody(t *testing.T) {
	w := testWorkspace(t)
	big := make([]byte, maxHistoryBody*2)
	for i := range big {
		big[i] = 'x'
	}
	if err := w.AppendHistory(HistoryEntry{ResponseBody: string(big)}); err != nil {
		t.Fatal(err)
	}
	entries, err := w.History(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries[0].ResponseBody) != maxHistoryBody {
		t.Errorf("body should be truncated to %d, got %d", maxHistoryBody, len(entries[0].ResponseBody))
	}
}

func TestHistoryRotatesAtCap(t *testing.T) {
	w := testWorkspace(t)
	appendN(t, w, maxHistoryEntries+5)

	indices := historyIndicesOf(t, w)
	if len(indices) != 2 || indices[0] != 1 || indices[1] != 2 {
		t.Fatalf("expected logs 1 and 2, got %v", indices)
	}
	if got := logLines(t, w, 1); got != maxHistoryEntries {
		t.Errorf("first log holds %d entries, want the cap of %d", got, maxHistoryEntries)
	}
	if got := logLines(t, w, 2); got != 5 {
		t.Errorf("second log holds %d entries, want 5", got)
	}
}

func TestHistoryReadSpansFiles(t *testing.T) {
	w := testWorkspace(t)
	total := maxHistoryEntries + 5
	appendN(t, w, total)

	entries, err := w.History(total)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != total {
		t.Fatalf("got %d entries across the rollover, want %d", len(entries), total)
	}
	// Newest first and contiguous: no entry dropped or repeated at the seam
	// between the two logs.
	got := ordinals(t, entries)
	for i, n := range got {
		if want := total - i; n != want {
			t.Fatalf("entry %d is ordinal %d, want %d (seam is at index %d)", i, n, want, 5)
		}
	}
}

func TestHistoryFillsFromPreviousFile(t *testing.T) {
	w := testWorkspace(t)
	// Five entries past the cap, so the active log is short and the rest of
	// a 200-entry read has to come from the log before it.
	total := maxHistoryEntries + 5
	appendN(t, w, total)

	entries, err := w.History(200)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 200 {
		t.Fatalf("got %d entries, want a full 200 spilled from the previous log", len(entries))
	}
	got := ordinals(t, entries)
	if got[0] != total {
		t.Errorf("newest entry is %d, want %d", got[0], total)
	}
	if got[len(got)-1] != total-199 {
		t.Errorf("oldest entry is %d, want %d", got[len(got)-1], total-199)
	}
}

func TestHistoryPrunesOldFiles(t *testing.T) {
	w := testWorkspace(t)
	appendN(t, w, maxHistoryEntries*(maxHistoryFiles+1)+1)

	indices := historyIndicesOf(t, w)
	if len(indices) != maxHistoryFiles {
		t.Fatalf("kept %v, want exactly %d logs", indices, maxHistoryFiles)
	}
	// The survivors are the newest, and contiguous.
	newest := maxHistoryFiles + 2
	for i, idx := range indices {
		if want := newest - maxHistoryFiles + 1 + i; idx != want {
			t.Errorf("log %d is %d, want %d", i, idx, want)
		}
	}
	if _, err := os.Stat(historyPath(t, w, 1)); !os.IsNotExist(err) {
		t.Error("the oldest log should have been deleted")
	}
}

func TestHistoryMigratesLegacyFile(t *testing.T) {
	w := testWorkspace(t)
	// A workspace written before rotation: one flat file, and a .gitignore
	// naming it literally.
	legacy := w.legacyHistoryPath()
	var lines string
	for i := 1; i <= 3; i++ {
		lines += fmt.Sprintf(`{"request":{"name":"%d"},"status":200}`+"\n", i)
	}
	if err := os.WriteFile(legacy, []byte(lines), 0o644); err != nil { // #nosec G306 -- deliberately loose, the migration must tighten it
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Root, ".gitignore"), []byte("*.local.yaml\nhistory.jsonl\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ordinals(t, entries); len(got) != 3 || got[0] != 3 || got[2] != 1 {
		t.Errorf("migrated entries are %v, want 3 2 1", got)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("the legacy file should have been moved, not copied")
	}
	info, err := os.Stat(historyPath(t, w, 1))
	if err != nil {
		t.Fatalf("expected the legacy log to become the first rotated log: %v", err)
	}
	// The legacy file was group- and world-readable; migrating it must not
	// carry that across, since it holds response bodies.
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("migrated log has mode %o, want 600", mode)
	}

	ignore, err := os.ReadFile(filepath.Join(w.Root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"history/", "*.local.yaml"} {
		if !strings.Contains(string(ignore), want) {
			t.Errorf("gitignore lost or never gained %q: %q", want, ignore)
		}
	}
}

func TestEnsureGitignoreAppendsMissingLine(t *testing.T) {
	root := t.TempDir()
	w := &Workspace{Root: root}
	// An older workspace, plus a pattern its owner added by hand. Neither
	// may be lost, and the trailing newline is missing on purpose.
	before := "*.local.yaml\nhistory.jsonl\nscratch/"
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.ensureGitignore(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"*.local.yaml", "history.jsonl", "scratch/", "history/"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q after repair: %q", want, data)
		}
	}
	if strings.Contains(string(data), "scratch/history/") {
		t.Errorf("appended without a newline separator: %q", data)
	}

	// Repairing twice must not duplicate the line.
	if err := w.ensureGitignore(); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(again), "history/\n") != 1 {
		t.Errorf("ensureGitignore is not idempotent: %q", again)
	}
}

func TestHistorySkipsMalformedLines(t *testing.T) {
	w := testWorkspace(t)
	appendN(t, w, 2)
	f, err := os.OpenFile(historyPath(t, w, 1), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not json at all\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	appendN(t, w, 1)

	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want the 3 well-formed ones", len(entries))
	}
}

func TestHistoryConcurrentAppend(t *testing.T) {
	w := testWorkspace(t)
	const writers, each = 8, 100

	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range each {
				e := HistoryEntry{
					Time:    time.Now(),
					Request: model.Request{Name: fmt.Sprintf("%d-%d", i, j), Method: "GET"},
				}
				if err := w.AppendHistory(e); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	// Enough writes to force a rollover, so this also covers racing on the
	// claim of the next log index.
	if len(entries) != writers*each {
		t.Errorf("got %d entries, want every one of the %d appends", len(entries), writers*each)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.Request.Name] {
			t.Fatalf("entry %q was written twice", e.Request.Name)
		}
		seen[e.Request.Name] = true
	}
}

// entry builds a history entry with a response body, the thing the recording
// level decides the fate of.
func entry(name, body string) HistoryEntry {
	return HistoryEntry{
		Time:         time.Now(),
		Collection:   "smoke",
		Request:      model.Request{Name: name, Method: "GET", URL: "http://x"},
		Status:       200,
		DurationMS:   12,
		Size:         int64(len(body)),
		ResponseBody: body,
	}
}

// TestHistoryOffWritesNothingAndCreatesNothing covers the strong half of the
// promise: a workspace configured to record nothing does not quietly leave a
// state directory, a workspace id, or an empty log behind.
func TestHistoryOffWritesNothingAndCreatesNothing(t *testing.T) {
	w := testWorkspace(t)
	writeConfig(t, w, "history: off\n")

	if err := w.AppendHistory(entry("one", "the secret")); err != nil {
		t.Fatal(err)
	}

	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("recording is off; got %d entries", len(entries))
	}
	p, err := w.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "" {
		t.Errorf("a workspace that records nothing should not be assigned an id, got %q", p.ID)
	}
	if _, err := os.Stat(filepath.Join(w.Root, LocalName)); !os.IsNotExist(err) {
		t.Errorf("%s should not have been written, stat err = %v", LocalName, err)
	}
	base, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "workspaces")); !os.IsNotExist(err) {
		t.Errorf("no state directory should have been created, stat err = %v", err)
	}
}

// TestHistoryOffStillRepairsTheGitignore covers the part of a history write
// that touches the workspace rather than the state directory. An older
// workspace may still hold logs in the working tree, and turning recording
// off must not be the thing that leaves them uncovered.
func TestHistoryOffStillRepairsTheGitignore(t *testing.T) {
	w := testWorkspace(t)
	writeConfig(t, w, "history: off\n")
	if err := os.Remove(filepath.Join(w.Root, ".gitignore")); err != nil {
		t.Fatal(err)
	}

	if err := w.AppendHistory(entry("one", "body")); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(w.Root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range gitignoreLines {
		if !strings.Contains(string(data), want) {
			t.Errorf(".gitignore is missing %q:\n%s", want, data)
		}
	}
}

// TestHistoryOffLeavesEarlierEntriesReadable pins that the setting governs
// what is written, not what can be read: switching recording off stops the
// log growing rather than hiding the log you already have.
func TestHistoryOffLeavesEarlierEntriesReadable(t *testing.T) {
	w := testWorkspace(t)
	if err := w.AppendHistory(entry("before", "body")); err != nil {
		t.Fatal(err)
	}

	writeConfig(t, w, "history: off\n")
	if err := w.AppendHistory(entry("after", "body")); err != nil {
		t.Fatal(err)
	}

	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Request.Name != "before" {
		t.Fatalf("expected only the entry written before the switch, got %+v", entries)
	}
}

// TestHistoryMetadataDropsTheResponseBody checks the middle level against the
// file itself, not just the parsed entry: the claim is that the body never
// reaches the disk, so that is what the assertion has to be about.
func TestHistoryMetadataDropsTheResponseBody(t *testing.T) {
	w := testWorkspace(t)
	writeConfig(t, w, "history: metadata\n")

	const body = "sk-live-000000000000"
	if err := w.AppendHistory(entry("one", body)); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(historyPath(t, w, 1))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), body) {
		t.Errorf("the response body reached the log:\n%s", raw)
	}

	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	got := entries[0]
	if got.ResponseBody != "" {
		t.Errorf("ResponseBody = %q, want empty", got.ResponseBody)
	}
	// Everything that is not payload is still recorded, or the level would be
	// indistinguishable from off.
	if got.Status != 200 || got.DurationMS != 12 || got.Size != int64(len(body)) || got.Request.Name != "one" {
		t.Errorf("metadata should survive the body being dropped, got %+v", got)
	}
}

// TestReadingHistoryDoesNotAssignAnID pins for the read path what
// TestPathsDoNotAssignAnID pins for `where`: browsing an empty log must not
// be the thing that decides where the log will be kept, or a workspace that
// has never sent a request acquires a state directory just for being looked
// at.
func TestReadingHistoryDoesNotAssignAnID(t *testing.T) {
	w := testWorkspace(t)

	entries, err := w.History(200)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a workspace that has never sent has no history, got %d entries", len(entries))
	}
	p, err := w.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "" {
		t.Errorf("reading assigned the id %q", p.ID)
	}

	// A send still assigns one, so the read path is skipping the claim rather
	// than breaking it.
	if err := w.AppendHistory(entry("one", "body")); err != nil {
		t.Fatal(err)
	}
	if p, err := w.Paths(); err != nil {
		t.Fatal(err)
	} else if p.ID == "" {
		t.Error("a send must still assign an id")
	}
}
