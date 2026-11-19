package contract

import "time"

// DiscoverRequest is the input payload for the knurl_discover MCP meta-tool.
type DiscoverRequest struct {
	Query string   `json:"query,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	Scope Scope    `json:"scope,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

// DiscoverResponse is the output payload for the knurl_discover MCP meta-tool.
type DiscoverResponse struct {
	Tools []ToolSummary `json:"tools"`
}

// ToolSummary provides lightweight discovery metadata without schemas or source.
type ToolSummary struct {
	Name        string   `json:"name"`
	Version     int      `json:"version"`
	Scope       Scope    `json:"scope"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	ReadOnly    bool     `json:"read_only"`
	Elevated    bool     `json:"elevated"`
}

// InspectRequest is the input payload for the knurl_inspect MCP meta-tool.
type InspectRequest struct {
	Name    string `json:"name"`
	Scope   Scope  `json:"scope,omitempty"`
	Version int    `json:"version,omitempty"`
}

// InspectResponse is the output payload for the knurl_inspect MCP meta-tool.
type InspectResponse struct {
	Name              string   `json:"name"`
	Version           int      `json:"version"`
	Scope             Scope    `json:"scope"`
	ActiveVersion     int      `json:"active_version"`
	AvailableVersions []int    `json:"available_versions"`
	Elevated          bool     `json:"elevated"`
	Manifest          Manifest `json:"manifest"`
}

// RunRequest is the input payload for the knurl_run MCP meta-tool.
type RunRequest struct {
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	Scope     Scope          `json:"scope,omitempty"`
	Version   int            `json:"version,omitempty"`
	TimeoutMs int            `json:"timeout_ms,omitempty"`
}

// RunResponse is the output payload for the knurl_run MCP meta-tool.
type RunResponse = ExecutionResult

// ProposeRequest is the input payload for the knurl_propose MCP meta-tool.
type ProposeRequest struct {
	Name     string    `json:"name"`
	Scope    Scope     `json:"scope"`
	Summary  string    `json:"summary"`
	Manifest Manifest  `json:"manifest"`
	Source   string    `json:"source"`
	Tests    TestSuite `json:"tests"`
}

// ProposeResponse is the output payload for the knurl_propose MCP meta-tool.
type ProposeResponse struct {
	ProposalID      string           `json:"proposal_id"`
	Status          ProposalStatus   `json:"status"`
	Tool            string           `json:"tool"`
	TargetVersion   int              `json:"target_version"`
	BaseVersion     int              `json:"base_version"`
	Scope           Scope            `json:"scope"`
	Elevated        bool             `json:"elevated"`
	Validation      ValidationReport `json:"validation"`
	CapabilityDiff  CapabilityDiff   `json:"capability_diff"`
	DryRun          *DryRunPreview   `json:"dry_run,omitempty"`
	ApprovalCommand string           `json:"approval_command"`
}

// StatusRequest is the input payload for the knurl_status MCP meta-tool.
type StatusRequest struct {
	ProposalID string `json:"proposal_id"`
}

// StatusResponse is the output payload for the knurl_status MCP meta-tool.
type StatusResponse struct {
	ProposalID      string         `json:"proposal_id"`
	Status          ProposalStatus `json:"status"`
	Tool            string         `json:"tool"`
	TargetVersion   int            `json:"target_version"`
	BaseVersion     int            `json:"base_version"`
	Scope           Scope          `json:"scope"`
	CreatedAt       time.Time      `json:"created_at"`
	DecidedAt       *time.Time     `json:"decided_at,omitempty"`
	DecisionNote    string         `json:"decision_note,omitempty"`
	ApprovalCommand string         `json:"approval_command,omitempty"`
}
