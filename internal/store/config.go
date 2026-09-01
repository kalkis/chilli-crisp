package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// ConfigName is the optional workspace settings file.
const ConfigName = "config.yaml"

// DefaultTimeout is the per-request timeout used when the workspace config
// does not set one.
const DefaultTimeout = 30 * time.Second

// Config holds workspace-wide settings. It is committed alongside the
// collections, so everything in it must be meaningful to a teammate.
type Config struct {
	// Collections lists collection file stems in display order. Collections
	// not listed here follow, in filename order.
	Collections []string `yaml:"collections,omitempty"`

	// Timeout is the per-request timeout, written the way Go durations read
	// ("45s", "2m"). Absent or zero means DefaultTimeout. Read it through
	// Workspace.RequestTimeout rather than directly.
	//
	// Config is only ever read, never marshalled back: yaml.Marshal would
	// write a Duration as raw nanoseconds, so anything adding a SaveConfig
	// needs a string field or a custom marshaller here.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// History is how much of a send is recorded in the request log. Absent
	// means HistoryFull. Read it through Workspace.HistoryMode rather than
	// directly.
	History HistoryMode `yaml:"history,omitempty"`
}

// HistoryMode says how much of a send the request log keeps. The three levels
// are ordered by what they retain, so there is no combination to get wrong:
// metadata is full without the response body, and off writes nothing at all.
//
// It lives in the committed config because it is a property of the API being
// tested, not of the person testing it — a workspace pointed at production
// records nothing for the whole team, rather than for whoever remembered a
// flag.
type HistoryMode string

const (
	// HistoryFull records the request, the response summary and a truncated
	// response body. It is the default.
	HistoryFull HistoryMode = "full"
	// HistoryMetadata records everything except the response body: status,
	// duration, size, and the transport error if there was one. That error
	// is a message from net/http and can quote the expanded URL, so this is
	// "no response payloads", not "no strings from the wire at all".
	HistoryMetadata HistoryMode = "metadata"
	// HistoryOff records nothing, and creates nothing: see AppendHistory.
	HistoryOff HistoryMode = "off"
)

// historyModes maps every accepted spelling to a mode. The switch spellings
// are here because the field reads like a switch and YAML makes them tempting
// to write; full, metadata and off are the documented three.
var historyModes = map[string]HistoryMode{
	"full": HistoryFull, "on": HistoryFull, "true": HistoryFull, "yes": HistoryFull,
	"metadata": HistoryMetadata,
	"off":      HistoryOff, "false": HistoryOff, "no": HistoryOff,
}

// UnmarshalYAML resolves one of the accepted spellings, and rejects anything
// else. An unrecognised value is an error rather than a fallback to the
// default: this setting exists to keep response data out of the log, so a
// typo must fail loudly instead of quietly restoring full recording.
//
// A bare "history:" with no value decodes to the empty string, which
// Workspace.HistoryMode reads as unset — the same as omitting the key.
func (h *HistoryMode) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return fmt.Errorf("history: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	mode, ok := historyModes[strings.ToLower(strings.TrimSpace(raw))]
	if !ok {
		return fmt.Errorf("history: %q is not one of full, metadata, off", raw)
	}
	*h = mode
	return nil
}

// Config reads the workspace settings. A missing file is an empty config,
// not an error.
func (w *Workspace) Config() (*Config, error) {
	cfg, err := readYAML[Config](filepath.Join(w.Root, ConfigName))
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// RequestTimeout is the per-request timeout for this workspace: the value in
// config.yaml, or DefaultTimeout when it is unset or nonsensical. A zero
// timeout deliberately means "the default" rather than "no timeout at all",
// so that one sentinel is unambiguous; write 24h for effectively none.
func (w *Workspace) RequestTimeout() (time.Duration, error) {
	cfg, err := w.Config()
	if err != nil {
		return 0, err
	}
	if cfg.Timeout <= 0 {
		return DefaultTimeout, nil
	}
	return cfg.Timeout, nil
}

// HistoryMode is how much of a send this workspace records: the value in
// config.yaml, or HistoryFull when it is unset. Every front end reads the
// setting through here for the same reason both read RequestTimeout through
// one accessor — the TUI and a headless run must not be able to disagree
// about what is being written down.
func (w *Workspace) HistoryMode() (HistoryMode, error) {
	cfg, err := w.Config()
	if err != nil {
		return "", err
	}
	if cfg.History == "" {
		return HistoryFull, nil
	}
	return cfg.History, nil
}

// orderCollections sorts cols to match the configured stem order. Anything
// unlisted keeps its existing (filename) order, after the listed ones.
func orderCollections(cols []*model.Collection, order []string) []*model.Collection {
	if len(order) == 0 || len(cols) == 0 {
		return cols
	}
	remaining := make([]*model.Collection, len(cols))
	copy(remaining, cols)

	var out []*model.Collection
	for _, want := range order {
		for i, c := range remaining {
			if c != nil && stem(c.Path) == want {
				out = append(out, c)
				remaining[i] = nil
				break
			}
		}
	}
	for _, c := range remaining {
		if c != nil {
			out = append(out, c)
		}
	}
	return out
}
