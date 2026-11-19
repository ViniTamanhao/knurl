package contract

const (
	// DefaultTimeoutMs is the default execution timeout applied when a manifest
	// omits timeout_ms.
	DefaultTimeoutMs = 30000

	// MaxTimeoutMs is the hard upper bound for tool execution timeouts.
	MaxTimeoutMs = 120000
)

// Manifest defines the typed metadata, I/O JSON Schemas, capabilities, and
// secret bindings for a knurl tool bundle.
type Manifest struct {
	SchemaVersion string         `json:"schema_version"`
	Name          string         `json:"name"`
	Version       int            `json:"version"`
	Description   string         `json:"description"`
	Tags          []string       `json:"tags,omitempty"`
	ReadOnly      bool           `json:"read_only"`
	TimeoutMs     int            `json:"timeout_ms,omitempty"`
	InputSchema   map[string]any `json:"input_schema"`
	OutputSchema  map[string]any `json:"output_schema"`
	Capabilities  Capabilities   `json:"capabilities"`
	Secrets       []SecretRef    `json:"secrets,omitempty"`
}

// Capabilities declares the host-enforced permissions required by a tool.
type Capabilities struct {
	FS   *FSCapability   `json:"fs,omitempty"`
	Git  *GitCapability  `json:"git,omitempty"`
	HTTP *HTTPCapability `json:"http,omitempty"`
	Exec *ExecCapability `json:"exec,omitempty"`
}

// FSCapability declares allowed relative filesystem glob patterns for reads.
type FSCapability struct {
	Read []string `json:"read"`
}

// GitCapability declares read-only Git repository inspection access.
type GitCapability struct {
	Read bool `json:"read"`
}

// HTTPCapability declares outbound domain-allowlisted HTTP access.
type HTTPCapability struct {
	Allow []HTTPRule `json:"allow"`
}

// HTTPRule authorizes HTTP requests to one domain or single-label wildcard
// subdomain with explicit methods, optional path prefixes, and bound secrets.
type HTTPRule struct {
	Domain       string   `json:"domain"`
	Methods      []string `json:"methods"`
	PathPrefixes []string `json:"path_prefixes,omitempty"`
	Secrets      []string `json:"secrets,omitempty"`
	AllowHTTP    bool     `json:"allow_http,omitempty"`
}

// ExecCapability declares constrained external command templates.
type ExecCapability struct {
	Templates []ExecTemplate `json:"templates"`
}

// ExecTemplate defines a single shell-free external command template with
// literal first argument and RE2-validated parameter placeholders.
type ExecTemplate struct {
	ID          string                   `json:"id"`
	Program     string                   `json:"program"`
	Args        []string                 `json:"args"`
	ArgsSchema  map[string]ExecArgSchema `json:"args_schema,omitempty"`
	Env         map[string]string        `json:"env,omitempty"`
	ReadOnly    bool                     `json:"read_only"`
	Description string                   `json:"description"`
}

// ExecArgSchema defines RE2 regex, length, and enum constraints for a template
// `${arg:<key>}` placeholder.
type ExecArgSchema struct {
	Pattern     string   `json:"pattern"`
	MaxLength   int      `json:"max_length,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Description string   `json:"description,omitempty"`
}

// SecretRef declares a named secret resolved at the Go host boundary.
type SecretRef struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// IsElevated reports whether the tool requests any capability beyond local
// read-only workspace inspection (non-read-only execution, external exec
// templates, outbound HTTP rules, or host secrets).
func (m Manifest) IsElevated() bool {
	if !m.ReadOnly {
		return true
	}
	if len(m.Secrets) > 0 {
		return true
	}
	if m.Capabilities.HTTP != nil && len(m.Capabilities.HTTP.Allow) > 0 {
		return true
	}
	if m.Capabilities.Exec != nil && len(m.Capabilities.Exec.Templates) > 0 {
		return true
	}
	return false
}

// EffectiveTimeoutMs returns the execution timeout clamped to v0 bounds.
func (m Manifest) EffectiveTimeoutMs() int {
	if m.TimeoutMs <= 0 {
		return DefaultTimeoutMs
	}
	if m.TimeoutMs > MaxTimeoutMs {
		return MaxTimeoutMs
	}
	return m.TimeoutMs
}
