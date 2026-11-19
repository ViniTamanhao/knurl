package contract

import (
	"fmt"
	"time"
)

// ErrorCode enumerates the canonical machine-readable errors produced by knurl.
type ErrorCode string

const (
	// ErrToolNotFound indicates the requested tool does not exist in scope.
	ErrToolNotFound ErrorCode = "TOOL_NOT_FOUND"
	// ErrVersionNotFound indicates the requested tool version does not exist.
	ErrVersionNotFound ErrorCode = "VERSION_NOT_FOUND"
	// ErrProposalNotFound indicates the requested proposal_id was not found.
	ErrProposalNotFound ErrorCode = "PROPOSAL_NOT_FOUND"
	// ErrInvalidInput indicates tool input failed manifest.input_schema.
	ErrInvalidInput ErrorCode = "INVALID_INPUT"
	// ErrInvalidOutput indicates tool output failed manifest.output_schema.
	ErrInvalidOutput ErrorCode = "INVALID_OUTPUT"
	// ErrInvalidManifest indicates manifest.json failed schema or semantic checks.
	ErrInvalidManifest ErrorCode = "INVALID_MANIFEST"
	// ErrInvalidTests indicates tests.json failed schema or semantic checks.
	ErrInvalidTests ErrorCode = "INVALID_TESTS"
	// ErrCompileError indicates tool.js failed compilation or lacked main(input).
	ErrCompileError ErrorCode = "COMPILE_ERROR"
	// ErrTestFailed indicates one or more fixture tests failed.
	ErrTestFailed ErrorCode = "TEST_FAILED"
	// ErrCapabilityDenied indicates a script exceeded its declared capabilities.
	ErrCapabilityDenied ErrorCode = "CAPABILITY_DENIED"
	// ErrNotFound indicates an allowed fs.read target did not exist.
	ErrNotFound ErrorCode = "NOT_FOUND"
	// ErrNotAGitRepo indicates knurl.git.* was called outside a Git repository.
	ErrNotAGitRepo ErrorCode = "NOT_A_GIT_REPO"
	// ErrInvalidArgument indicates a knurl.exec argument failed args_schema.
	ErrInvalidArgument ErrorCode = "INVALID_ARGUMENT"
	// ErrSecretMissing indicates a referenced secret is unavailable on the host.
	ErrSecretMissing ErrorCode = "SECRET_MISSING"
	// ErrTimeout indicates execution exceeded timeout_ms.
	ErrTimeout ErrorCode = "TIMEOUT"
	// ErrLimitExceeded indicates a byte, count, or IPC payload limit was hit.
	ErrLimitExceeded ErrorCode = "LIMIT_EXCEEDED"
	// ErrToolError indicates tool.js threw an unhandled JavaScript exception.
	ErrToolError ErrorCode = "TOOL_ERROR"
	// ErrExecFailed indicates spawning an approved exec binary failed.
	ErrExecFailed ErrorCode = "EXEC_FAILED"
	// ErrHTTPError indicates an outbound HTTP failure or blocked target/redirect.
	ErrHTTPError ErrorCode = "HTTP_ERROR"
	// ErrWorkerCrashed indicates the _worker subprocess died unexpectedly.
	ErrWorkerCrashed ErrorCode = "WORKER_CRASHED"
	// ErrInternalError indicates an unexpected host error.
	ErrInternalError ErrorCode = "INTERNAL_ERROR"
)

// ToolError is the structured error returned across worker IPC, CLI, and MCP.
type ToolError struct {
	Code    ErrorCode      `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the standard error interface.
func (e *ToolError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Scope identifies which registry tier is targeted or matched.
type Scope string

const (
	// ScopeRepo targets or matches the repository-local ./.knurl/tools tier.
	ScopeRepo Scope = "repo"
	// ScopeGlobal targets or matches the global ~/.config/knurl/tools tier.
	ScopeGlobal Scope = "global"
	// ScopeAuto checks repo-local first and falls back to global.
	ScopeAuto Scope = "auto"
	// ScopeAll searches across both repo-local and global tiers.
	ScopeAll Scope = "all"
)

// ExecutionRequest describes a host request to execute an approved tool.
type ExecutionRequest struct {
	Tool      string         `json:"tool"`
	Version   int            `json:"version,omitempty"`
	Scope     Scope          `json:"scope,omitempty"`
	WorkDir   string         `json:"work_dir"`
	Input     map[string]any `json:"input"`
	TimeoutMs int            `json:"timeout_ms,omitempty"`
}

// ExecutionResult is the canonical structured envelope returned by CLI and MCP runs.
type ExecutionResult struct {
	OK          bool           `json:"ok"`
	Tool        string         `json:"tool"`
	Version     int            `json:"version"`
	Scope       Scope          `json:"scope"`
	DurationMs  int64          `json:"duration_ms"`
	Output      map[string]any `json:"output,omitempty"`
	Error       *ToolError     `json:"error,omitempty"`
	Diagnostics []string       `json:"diagnostics,omitempty"`
}

// WorkerRequest is sent as a single framed JSON message from parent to `knurl _worker`.
type WorkerRequest struct {
	Manifest  Manifest          `json:"manifest"`
	Source    string            `json:"source"`
	WorkDir   string            `json:"work_dir"`
	Input     map[string]any    `json:"input"`
	TimeoutMs int               `json:"timeout_ms"`
	Secrets   map[string]string `json:"secrets,omitempty"`
	Mocks     *TestMocks        `json:"mocks,omitempty"`
}

// WorkerResponse is returned as a single framed JSON message from `knurl _worker`.
type WorkerResponse struct {
	OK          bool           `json:"ok"`
	Output      map[string]any `json:"output,omitempty"`
	Error       *ToolError     `json:"error,omitempty"`
	Diagnostics []string       `json:"diagnostics,omitempty"`
	DurationMs  int64          `json:"duration_ms"`
}

// ActiveVersionPointer is persisted atomically in tools/<name>/active.json.
type ActiveVersionPointer struct {
	ActiveVersion   int       `json:"active_version"`
	PreviousVersion int       `json:"previous_version,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ApprovalRecord is persisted immutably in tools/<name>/v<N>/approval.json.
type ApprovalRecord struct {
	ProposalID     string         `json:"proposal_id"`
	ApprovedAt     time.Time      `json:"approved_at"`
	BundleSHA256   string         `json:"bundle_sha256"`
	Summary        string         `json:"summary"`
	CapabilityDiff CapabilityDiff `json:"capability_diff"`
}

// ProposalStatus tracks the lifecycle state of a staged proposal.
type ProposalStatus string

const (
	// ProposalPending indicates the proposal passed validation and awaits approval.
	ProposalPending ProposalStatus = "pending"
	// ProposalApproved indicates the proposal was promoted into the active registry.
	ProposalApproved ProposalStatus = "approved"
	// ProposalRejected indicates the proposal failed validation or was rejected by a human.
	ProposalRejected ProposalStatus = "rejected"
	// ProposalSuperseded indicates a newer proposal replaced this pending proposal.
	ProposalSuperseded ProposalStatus = "superseded"
)

// ProposalRecord is persisted in ~/.config/knurl/staging/<proposal-id>/proposal.json.
type ProposalRecord struct {
	ProposalID     string           `json:"proposal_id"`
	ToolName       string           `json:"tool_name"`
	TargetVersion  int              `json:"target_version"`
	BaseVersion    int              `json:"base_version"`
	Scope          Scope            `json:"scope"`
	Summary        string           `json:"summary"`
	Status         ProposalStatus   `json:"status"`
	BundleSHA256   string           `json:"bundle_sha256"`
	Elevated       bool             `json:"elevated"`
	Validation     ValidationReport `json:"validation"`
	CapabilityDiff CapabilityDiff   `json:"capability_diff"`
	DryRun         *DryRunPreview   `json:"dry_run,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	DecidedAt      *time.Time       `json:"decided_at,omitempty"`
	DecisionNote   string           `json:"decision_note,omitempty"`
}

// CapabilityDiff summarizes capability changes between an active version and a proposal.
type CapabilityDiff struct {
	ElevatedBefore bool               `json:"elevated_before"`
	ElevatedAfter  bool               `json:"elevated_after"`
	HasEscalation  bool               `json:"has_escalation"`
	Changes        []CapabilityChange `json:"changes"`
}

// CapabilityChange describes a single capability delta during proposal review.
type CapabilityChange struct {
	Kind       string `json:"kind"`
	Capability string `json:"capability"`
	Detail     string `json:"detail"`
}

// ValidationReport summarizes schema, compilation, and test outcomes during propose.
type ValidationReport struct {
	SchemaValid bool     `json:"schema_valid"`
	Compiled    bool     `json:"compiled"`
	TestsPassed int      `json:"tests_passed"`
	TestsTotal  int      `json:"tests_total"`
	Failures    []string `json:"failures"`
}

// DryRunPreview captures the optional read-only working-directory dry run during propose.
type DryRunPreview struct {
	Executed      bool           `json:"executed"`
	SkippedReason string         `json:"skipped_reason,omitempty"`
	DurationMs    int64          `json:"duration_ms,omitempty"`
	OK            bool           `json:"ok"`
	OutputPreview map[string]any `json:"output_preview,omitempty"`
	Error         *ToolError     `json:"error,omitempty"`
}
