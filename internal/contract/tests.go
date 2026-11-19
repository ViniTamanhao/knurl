package contract

// TestSuite defines the deterministic mock-backed fixture suite stored in
// tests.json for a tool bundle.
type TestSuite struct {
	SchemaVersion string     `json:"schema_version"`
	Cases         []TestCase `json:"cases"`
}

// TestCase defines a single mock-backed contract test case for a tool.
type TestCase struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Input       map[string]any  `json:"input"`
	Mocks       *TestMocks      `json:"mocks,omitempty"`
	Expect      TestExpectation `json:"expect"`
}

// TestMocks supplies deterministic host responses for tests.
type TestMocks struct {
	FS      *FSMock    `json:"fs,omitempty"`
	Git     *GitMock   `json:"git,omitempty"`
	HTTP    []HTTPMock `json:"http,omitempty"`
	Exec    []ExecMock `json:"exec,omitempty"`
	Secrets []string   `json:"secrets,omitempty"`
}

// FSMock maps workspace-relative file paths to mock UTF-8 contents.
type FSMock struct {
	Files map[string]string `json:"files"`
}

// GitMock supplies mock outputs or a simulated error for knurl.git.* calls.
type GitMock struct {
	Error  ErrorCode   `json:"error,omitempty"`
	Branch *GitBranch  `json:"branch,omitempty"`
	Status *GitStatus  `json:"status,omitempty"`
	Log    []GitCommit `json:"log,omitempty"`
	Diff   *GitDiff    `json:"diff,omitempty"`
}

// GitBranch represents the result of knurl.git.branch().
type GitBranch struct {
	Current  string `json:"current"`
	Detached bool   `json:"detached"`
	Head     string `json:"head"`
	Upstream string `json:"upstream,omitempty"`
}

// GitStatus represents the result of knurl.git.status().
type GitStatus struct {
	Clean     bool            `json:"clean"`
	Staged    []GitFileChange `json:"staged"`
	Unstaged  []GitFileChange `json:"unstaged"`
	Untracked []string        `json:"untracked"`
}

// GitFileChange describes a single changed file in git status.
type GitFileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// GitCommit represents a commit entry returned by knurl.git.log().
type GitCommit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`
	Author    string `json:"author"`
	Date      string `json:"date"`
	Subject   string `json:"subject"`
}

// GitDiff represents the result of knurl.git.diff().
type GitDiff struct {
	Files     []GitDiffFile `json:"files"`
	Patch     string        `json:"patch,omitempty"`
	Truncated bool          `json:"truncated"`
}

// GitDiffFile summarizes per-file line churn in a diff.
type GitDiffFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
}

// HTTPMock defines a deterministic response or error for a matching HTTP call.
type HTTPMock struct {
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	RequireHeaders map[string]string `json:"require_headers,omitempty"`
	Result         *HTTPMockResult   `json:"result,omitempty"`
	Error          ErrorCode         `json:"error,omitempty"`
}

// HTTPMockResult holds a mocked HTTP status, headers, and body.
type HTTPMockResult struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// ExecMock defines a deterministic response or error for a template invocation.
type ExecMock struct {
	TemplateID string            `json:"template_id"`
	Params     map[string]string `json:"params,omitempty"`
	Result     *ExecMockResult   `json:"result,omitempty"`
	Error      ErrorCode         `json:"error,omitempty"`
}

// ExecMockResult holds a mocked command exit status and streams.
type ExecMockResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr,omitempty"`
}

// TestExpectation asserts either successful structured output or an error code.
type TestExpectation struct {
	OK            bool           `json:"ok"`
	Output        map[string]any `json:"output,omitempty"`
	OutputSubset  map[string]any `json:"output_subset,omitempty"`
	ErrorCode     ErrorCode      `json:"error_code,omitempty"`
	ErrorContains string         `json:"error_contains,omitempty"`
}
