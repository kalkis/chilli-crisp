package model

import "sync/atomic"

// ID identifies a collection or request for the lifetime of the process, so
// the UI can track one across reordering, sorting, and deletion. It is
// assigned on load and never serialized: no file may reference an ID, because
// the next run will hand out different ones.
type ID uint64

var lastID atomic.Uint64

// NewID returns an ID unique within this process. The zero ID means
// "not assigned yet".
func NewID() ID { return ID(lastID.Add(1)) }
