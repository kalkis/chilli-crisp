// Package model defines the core domain types shared by the store,
// importers, HTTP engine, and UI.
package model

// Collection is a named group of requests, typically one per API under test.
type Collection struct {
	Name     string            `yaml:"name" json:"name"`
	Vars     map[string]string `yaml:"vars,omitempty" json:"vars,omitempty"`
	Folders  []Folder          `yaml:"folders,omitempty" json:"folders,omitempty"`
	Auth     *Auth             `yaml:"auth,omitempty" json:"auth,omitempty"`
	Requests []Request         `yaml:"requests" json:"requests"`
	// Path is the file this was loaded from, set by the store. Never
	// serialized; empty until saved.
	Path string `yaml:"-" json:"-"`
	// ID identifies this collection at runtime. Never serialized.
	ID ID `yaml:"-" json:"-"`
}

// Folder carries settings shared by the requests whose Folder field names
// it. A folder is otherwise implied by those requests alone, so an entry
// exists only once it has something to say — nothing writes an empty one.
type Folder struct {
	Name string            `yaml:"name" json:"name"`
	Vars map[string]string `yaml:"vars,omitempty" json:"vars,omitempty"`
}

// FolderVars returns the variables declared for a folder, or nil when the
// folder has no entry. The empty name (a request outside any folder) never
// matches.
func (c *Collection) FolderVars(name string) map[string]string {
	if name == "" {
		return nil
	}
	for i := range c.Folders {
		if c.Folders[i].Name == name {
			return c.Folders[i].Vars
		}
	}
	return nil
}

// EnsureFolder returns the entry for name, appending one if it is missing.
// It returns nil for the empty name, which addresses no folder.
func (c *Collection) EnsureFolder(name string) *Folder {
	if name == "" {
		return nil
	}
	for i := range c.Folders {
		if c.Folders[i].Name == name {
			return &c.Folders[i]
		}
	}
	c.Folders = append(c.Folders, Folder{Name: name})
	return &c.Folders[len(c.Folders)-1]
}

// PruneFolders drops entries for folders that no request names any more.
//
// A folder exists because its requests do, so once the last one goes the entry
// is unreachable: there is no sidebar row for an empty folder, and the
// variables modal opens from that row. Left behind it would be invisible state
// in a committed file that EnsureFolder could silently revive.
func (c *Collection) PruneFolders() {
	if len(c.Folders) == 0 {
		return
	}
	used := make(map[string]bool, len(c.Folders))
	for i := range c.Requests {
		used[c.Requests[i].Folder] = true
	}
	kept := c.Folders[:0]
	for _, f := range c.Folders {
		if used[f.Name] {
			kept = append(kept, f)
		}
	}
	// Length zero rather than the empty slice: Folders is omitempty, and a
	// non-nil empty slice still marshals as "folders: []".
	if len(kept) == 0 {
		c.Folders = nil
		return
	}
	c.Folders = kept
}

// EnsureIDs assigns an ID to the collection and to any request that lacks
// one. Safe to call repeatedly; existing IDs are kept.
func (c *Collection) EnsureIDs() {
	if c.ID == 0 {
		c.ID = NewID()
	}
	for i := range c.Requests {
		if c.Requests[i].ID == 0 {
			c.Requests[i].ID = NewID()
		}
	}
}

// FindRequest returns the index of the request with the given ID, or -1.
func (c *Collection) FindRequest(id ID) int {
	if id == 0 {
		return -1
	}
	for i := range c.Requests {
		if c.Requests[i].ID == id {
			return i
		}
	}
	return -1
}

// Append adds r to the collection with a fresh ID and returns that ID.
func (c *Collection) Append(r Request) ID {
	r.ID = NewID()
	c.Requests = append(c.Requests, r)
	return r.ID
}

// InsertAt inserts r at index i with a fresh ID and returns that ID. i is
// clamped to the slice, so an out-of-range index appends rather than panics.
//
// This is what duplicating a request uses, rather than Append: a folder is a
// contiguous run of requests naming it, not a container, so a copy has to land
// beside its neighbours. Appended to the end it would start a second run, and
// both the sidebar and 'crisp list' would draw the folder twice.
func (c *Collection) InsertAt(i int, r Request) ID {
	r.ID = NewID()
	i = min(max(i, 0), len(c.Requests))
	c.Requests = append(c.Requests, Request{})
	copy(c.Requests[i+1:], c.Requests[i:])
	c.Requests[i] = r
	return r.ID
}

// Request describes a single HTTP request template. URL, header values,
// query values, and body may contain {{variable}} placeholders.
type Request struct {
	// ID identifies this request at runtime. Never serialized.
	ID     ID     `yaml:"-" json:"-"`
	Name   string `yaml:"name" json:"name"`
	Folder string `yaml:"folder,omitempty" json:"folder,omitempty"`
	Method string `yaml:"method" json:"method"`
	URL    string `yaml:"url" json:"url"`
	// Vars override any variable for this request alone, beating every
	// wider layer. They apply to the whole template, not just the URL.
	Vars    map[string]string `yaml:"vars,omitempty" json:"vars,omitempty"`
	Headers []Param           `yaml:"headers,omitempty" json:"headers,omitempty"`
	Query   []Param           `yaml:"query,omitempty" json:"query,omitempty"`
	Auth    *Auth             `yaml:"auth,omitempty" json:"auth,omitempty"`
	Body    *Body             `yaml:"body,omitempty" json:"body,omitempty"`
}

// Clone deep-copies the request so the copy can be read (or kept, e.g. in
// history) while the original is still being edited. The ID is preserved:
// the copy stands for the same logical request. Code that duplicates a
// request into a new one must assign a fresh ID.
func (r Request) Clone() Request {
	if r.Vars != nil {
		cp := make(map[string]string, len(r.Vars))
		for k, v := range r.Vars {
			cp[k] = v
		}
		r.Vars = cp
	}
	r.Headers = append([]Param(nil), r.Headers...)
	r.Query = append([]Param(nil), r.Query...)
	if r.Auth != nil {
		auth := *r.Auth
		r.Auth = &auth
	}
	if r.Body != nil {
		body := *r.Body
		body.Form = append([]Param(nil), body.Form...)
		r.Body = &body
	}
	return r
}

// QualifiedName is the request name prefixed with its folder, when it has
// one — the unambiguous form used in listings and lookups.
func (r *Request) QualifiedName() string {
	if r.Folder == "" {
		return r.Name
	}
	return r.Folder + " / " + r.Name
}

// Param is a name/value pair used for headers, query parameters, and form
// fields. Disabled pairs are kept in the file but not sent.
type Param struct {
	Name     string `yaml:"name" json:"name"`
	Value    string `yaml:"value" json:"value"`
	Disabled bool   `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

// Body payload types.
const (
	BodyNone = "none"
	BodyJSON = "json"
	BodyText = "text"
	BodyForm = "form"
)

// Body is the request payload. Form holds url-encoded fields when Type is
// BodyForm; otherwise Content holds the raw payload.
type Body struct {
	Type    string  `yaml:"type" json:"type"`
	Content string  `yaml:"content,omitempty" json:"content,omitempty"`
	Form    []Param `yaml:"form,omitempty" json:"form,omitempty"`
}

// Auth schemes.
const (
	AuthNone   = "none"
	AuthBasic  = "basic"
	AuthBearer = "bearer"
	AuthAPIKey = "apikey"
)

// Auth locations for API keys.
const (
	APIKeyInHeader = "header"
	APIKeyInQuery  = "query"
)

// Auth configures how a request authenticates. Only the fields for the
// selected Type are used.
type Auth struct {
	Type     string `yaml:"type" json:"type"`
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	Token    string `yaml:"token,omitempty" json:"token,omitempty"`
	Key      string `yaml:"key,omitempty" json:"key,omitempty"`
	Value    string `yaml:"value,omitempty" json:"value,omitempty"`
	In       string `yaml:"in,omitempty" json:"in,omitempty"`
}

// Environment is a named set of variables (e.g. dev, staging, prod).
type Environment struct {
	Name string            `yaml:"name" json:"name"`
	Vars map[string]string `yaml:"vars,omitempty" json:"vars,omitempty"`
	// LocalVars holds the gitignored overrides loaded from the environment's
	// .local.yaml file. Never serialized, so saving an Environment can never
	// leak them into the shareable file.
	LocalVars map[string]string `yaml:"-" json:"-"`
	// Path is the file this was loaded from, set by the store. Never
	// serialized; empty until saved.
	Path string `yaml:"-" json:"-"`
}

// AllVars merges Vars and LocalVars (local wins) into a fresh map for
// variable resolution.
func (e *Environment) AllVars() map[string]string {
	merged := make(map[string]string, len(e.Vars)+len(e.LocalVars))
	for k, v := range e.Vars {
		merged[k] = v
	}
	for k, v := range e.LocalVars {
		merged[k] = v
	}
	return merged
}

// EffectiveAuth returns the auth to apply to r: its own if set, otherwise
// the collection default. May return nil.
func (r *Request) EffectiveAuth(c *Collection) *Auth {
	if r.Auth != nil {
		return r.Auth
	}
	if c != nil {
		return c.Auth
	}
	return nil
}
