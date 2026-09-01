package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/kalkis/chilli-crisp/internal/httpclient"
	"github.com/kalkis/chilli-crisp/internal/model"
)

const (
	// maxHistoryBody caps the response body stored per history entry.
	maxHistoryBody = 16 * 1024
	// maxHistoryEntries is how many entries a log holds before the next
	// send rolls over to a new one. Concurrent writers may overshoot it by
	// the number of sends in flight, which only means one slightly longer
	// file; nothing depends on the cap being exact.
	maxHistoryEntries = 500
	// maxHistoryFiles is how many logs are kept. Older ones are deleted on
	// rollover, so history costs bounded disk instead of growing forever.
	maxHistoryFiles = 3
)

// historyName matches a rotated log and captures its index.
var historyName = regexp.MustCompile(`^(\d{6})\.jsonl$`)

// HistoryEntry records one sent request and a summary of its response.
// Request is a full snapshot (post-editing, pre-variable-expansion) so an
// entry can be replayed later — and so that a value from a .local.yaml is
// still a {{placeholder}} here rather than the secret itself.
type HistoryEntry struct {
	Time         time.Time     `json:"time"`
	Collection   string        `json:"collection,omitempty"`
	Environment  string        `json:"environment,omitempty"`
	Request      model.Request `json:"request"`
	Status       int           `json:"status"`
	DurationMS   int64         `json:"duration_ms"`
	Size         int64         `json:"size"`
	ResponseBody string        `json:"response_body,omitempty"`
	Error        string        `json:"error,omitempty"`
}

// NewHistoryEntry records the outcome of a send: a response summary on
// success, the error message on failure.
func NewHistoryEntry(collection, env string, req model.Request, resp *httpclient.Response, err error) HistoryEntry {
	e := HistoryEntry{Time: time.Now(), Collection: collection, Environment: env, Request: req}
	if err != nil {
		e.Error = err.Error()
		return e
	}
	e.Status = resp.StatusCode
	e.DurationMS = resp.Duration.Milliseconds()
	e.Size = int64(len(resp.Body))
	e.ResponseBody = string(resp.Body)
	return e
}

// historyFilePath names one rotated log inside dir. The zero-padded index
// sorts lexicographically in the same order it sorts numerically, so the
// highest-numbered file is both the newest and the last one listed.
func historyFilePath(dir string, index int) string {
	return filepath.Join(dir, fmt.Sprintf("%06d.jsonl", index))
}

// migrateHistory brings an older workspace up to the current layout. It runs
// before every read and write: once a workspace is current it costs two
// directory reads, and it is the only chance an older workspace gets to have
// its .gitignore repaired.
func (w *Workspace) migrateHistory(dir string) error {
	if err := w.migrateHistoryDir(dir); err != nil {
		return err
	}
	if err := w.migrateLegacyHistoryFile(dir); err != nil {
		return err
	}
	return w.ensureGitignore()
}

// migrateHistoryDir moves rotated logs out of the workspace and into the
// per-user state directory, where no repository can pick them up.
//
// It refuses to merge: if the destination already holds a log, the workspace
// copy is left exactly where it is. Reaching that state takes a restored
// backup or a branch switch, and the state directory already holds the newer
// logs, so nothing is lost — the directory left behind is itself the notice,
// and it stays covered by the workspace .gitignore.
func (w *Workspace) migrateHistoryDir(dir string) error {
	src := filepath.Join(w.Root, "history")
	names, err := logNames(src)
	if err != nil || len(names) == 0 {
		return err
	}
	existing, err := logNames(dir)
	if err != nil || len(existing) > 0 {
		return err
	}
	for _, name := range names {
		if err := moveFile(filepath.Join(src, name), filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	// Only an emptied directory goes. A stray file left in it fails the
	// remove, which is the right outcome: nothing else in there was ours.
	_ = os.Remove(src)
	return nil
}

// migrateLegacyHistoryFile moves the single pre-rotation log in as the first
// rotated one. The guard means it does nothing once a workspace has rotated
// logs, including ones migrateHistoryDir has only just moved into place.
func (w *Workspace) migrateLegacyHistoryFile(dir string) error {
	legacy := w.legacyHistoryPath()
	if _, err := os.Stat(legacy); err != nil {
		return nil
	}
	first := historyFilePath(dir, 1)
	if _, err := os.Stat(first); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return moveFile(legacy, first)
}

// logNames lists the rotated log file names in dir, ascending. A directory
// that does not exist has none, which is not an error. Names that do not
// match the scheme are ignored, so a stray file dropped in the directory
// cannot disturb the ordering.
func logNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && historyName.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out, nil
}

// historyIndices lists the rotated log indices present in dir, ascending.
func historyIndices(dir string) ([]int, error) {
	names, err := logNames(dir)
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(names))
	for _, name := range names {
		n, err := strconv.Atoi(historyName.FindStringSubmatch(name)[1])
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// AppendHistory appends e to the active log, truncating the stored response
// body to keep the log small and rolling over to a new file at the cap.
//
// The workspace's HistoryMode is applied here rather than by the caller, so
// that it holds for every front end including any added later: a caller may
// choose to record less than the mode allows (run --no-history) but none can
// record more.
//
// Both front ends treat a failure here as non-fatal, which is what lets the
// log live outside the workspace: a home directory that is unset or read-only
// costs the entry, not the send.
func (w *Workspace) AppendHistory(e HistoryEntry) error {
	mode, err := w.HistoryMode()
	if err != nil {
		return err
	}
	if mode == HistoryOff {
		// Nothing is written and nothing is created: no state directory, no
		// workspace id. A workspace that never records leaves nothing behind
		// on the machine, which is the point of being able to turn it off.
		//
		// The .gitignore repair still runs. It is the one part of a history
		// write that touches the workspace rather than the state directory,
		// and an older workspace may still hold logs in the working tree —
		// switching recording off must not be the thing that leaves them
		// exposed.
		return w.ensureGitignore()
	}
	if mode == HistoryMetadata {
		e.ResponseBody = ""
	}
	if len(e.ResponseBody) > maxHistoryBody {
		e.ResponseBody = e.ResponseBody[:maxHistoryBody]
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	dir, err := w.historyDir()
	if err != nil {
		return err
	}
	if err := w.migrateHistory(dir); err != nil {
		return err
	}
	indices, err := historyIndices(dir)
	if err != nil {
		return err
	}
	index := 1
	if len(indices) > 0 {
		index = indices[len(indices)-1]
	}

	// 0600: history holds response bodies, which may contain sensitive data.
	f, err := os.OpenFile(historyFilePath(dir, index), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600) // #nosec G304 -- a path derived from the state directory
	if err != nil {
		return err
	}
	count, err := countLines(f)
	if err != nil {
		_ = f.Close()
		return err
	}
	if count >= maxHistoryEntries {
		if err := f.Close(); err != nil {
			return err
		}
		if f, err = rollHistory(dir, index); err != nil {
			return err
		}
		pruneHistory(dir)
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// rollHistory starts the log after current. The exclusive create is a claim
// on the next index: if another process rolled first, we append to the file
// it made instead, so losing the race costs nothing.
func rollHistory(dir string, current int) (*os.File, error) {
	path := historyFilePath(dir, current+1)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- a path derived from the state directory
	if errors.Is(err, os.ErrExist) {
		return os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- a path derived from the state directory
	}
	return f, err
}

// pruneHistory deletes logs that have fallen outside the retention window.
// It is best-effort: a send must not fail because an old log could not be
// removed.
func pruneHistory(dir string) {
	indices, err := historyIndices(dir)
	if err != nil || len(indices) <= maxHistoryFiles {
		return
	}
	for _, i := range indices[:len(indices)-maxHistoryFiles] {
		_ = os.Remove(historyFilePath(dir, i))
	}
}

// hasWorkspaceLogs reports whether the workspace itself still holds logs from
// an older layout. Only then does a read have anything to migrate out.
func (w *Workspace) hasWorkspaceLogs() bool {
	if names, err := logNames(filepath.Join(w.Root, "history")); err == nil && len(names) > 0 {
		return true
	}
	_, err := os.Stat(w.legacyHistoryPath())
	return err == nil
}

// History returns up to limit entries, newest first. Reading spans logs: when
// the active one holds fewer than limit entries, the rest come from the logs
// before it, so a rollover is invisible to the caller. limit <= 0 means every
// entry still retained. Malformed lines are skipped rather than failing the
// whole read.
//
// HistoryMode does not apply here: it governs what is written, so turning
// recording off stops the log growing rather than hiding what is already in
// it.
func (w *Workspace) History(limit int) ([]HistoryEntry, error) {
	// Reading must not be what decides where the log goes — the rule Paths
	// follows, for the same reason. A workspace with no id has never been
	// written to, so unless it still holds logs from an older layout there is
	// nothing to read and nothing worth creating a state directory for. The
	// .gitignore repair every history call performs still runs.
	if _, ok, err := w.lookupWorkspaceID(); err != nil {
		return nil, err
	} else if !ok && !w.hasWorkspaceLogs() {
		return nil, w.ensureGitignore()
	}

	dir, err := w.historyDir()
	if err != nil {
		return nil, err
	}
	if err := w.migrateHistory(dir); err != nil {
		return nil, err
	}
	indices, err := historyIndices(dir)
	if err != nil {
		return nil, err
	}

	var entries []HistoryEntry
	for i := len(indices) - 1; i >= 0; i-- {
		data, err := os.ReadFile(historyFilePath(dir, indices[i])) // #nosec G304 -- a path derived from the state directory
		if errors.Is(err, os.ErrNotExist) {
			continue // pruned by another process mid-read
		}
		if err != nil {
			return nil, err
		}
		entries = appendReversed(entries, data, limit)
		if limit > 0 && len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

// appendReversed parses one log newest-first onto out, stopping once out
// holds limit entries (limit <= 0 reads all of it). Only the lines it needs
// are unmarshalled, so the cost tracks the limit rather than the file size.
func appendReversed(out []HistoryEntry, data []byte, limit int) []HistoryEntry {
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		if limit > 0 && len(out) >= limit {
			break
		}
		if len(bytes.TrimSpace(lines[i])) == 0 {
			continue
		}
		var e HistoryEntry
		if json.Unmarshal(lines[i], &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// countLines counts a log's entries by counting newlines, so deciding
// whether a file is full costs no JSON parsing. It leaves the read offset at
// EOF, which does not affect writes because the file is opened O_APPEND.
func countLines(f *os.File) (int, error) {
	buf := make([]byte, 64*1024)
	count := 0
	for {
		n, err := f.Read(buf)
		count += bytes.Count(buf[:n], []byte{'\n'})
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return 0, err
		}
	}
}
