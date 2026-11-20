package approval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/registry"
	"github.com/ViniTamanhao/knurl/internal/runtime"
)

// Store owns the user-local staging area; proposals never modify active tools.
type Store struct {
	Registry   *registry.Registry
	Supervisor *runtime.Supervisor
	WorkDir    string
}

var proposalPattern = regexp.MustCompile(`^prop_[0-9a-f]{32}$`)

func failure(code contract.ErrorCode, message string) error {
	return &contract.ToolError{Code: code, Message: message}
}
func (s *Store) staging() (string, error) {
	dir, err := s.Registry.GlobalToolsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(dir), "staging"), nil
}
func digest(files map[string][]byte) string {
	h := sha256.New()
	for _, name := range []string{"manifest.json", "tool.js", "tests.json"} {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", name, len(files[name]))
		_, _ = h.Write(files[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}
func bundleFiles(req contract.ProposeRequest) (map[string][]byte, error) {
	m, err := json.MarshalIndent(req.Manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	t, err := json.MarshalIndent(req.Tests, "", "  ")
	return map[string][]byte{"manifest.json": m, "tool.js": []byte(req.Source), "tests.json": t}, err
}

func compile(source string) error {
	if _, err := goja.Compile("tool.js", source, false); err != nil {
		return failure(contract.ErrCompileError, err.Error())
	}
	program, err := parser.ParseFile(nil, "tool.js", source, 0)
	if err != nil {
		return failure(contract.ErrCompileError, err.Error())
	}
	for _, statement := range program.Body {
		if decl, ok := statement.(*ast.FunctionDeclaration); ok && decl.Function.Name != nil && decl.Function.Name.Name.String() == "main" {
			if decl.Function.Async {
				return failure(contract.ErrCompileError, "main must be synchronous")
			}
			return nil
		}
	}
	return failure(contract.ErrCompileError, "tool.js must declare function main(input)")
}

// Propose validates a bundle before persisting one pending proposal.
func (s *Store) Propose(ctx context.Context, req contract.ProposeRequest) (contract.ProposeResponse, error) {
	var out contract.ProposeResponse
	if err := registry.ValidateToolName(req.Name); err != nil {
		return out, err
	}
	if req.Name != req.Manifest.Name {
		return out, failure(contract.ErrInvalidManifest, "request name differs from manifest.name")
	}
	if utf8.RuneCountInString(req.Summary) < 5 || utf8.RuneCountInString(req.Summary) > 200 {
		return out, failure(contract.ErrInvalidArgument, "summary must contain 5..200 characters")
	}
	target, err := s.Registry.TargetToolsDir(req.Scope)
	if err != nil {
		return out, err
	}
	files, err := bundleFiles(req)
	if err != nil {
		return out, err
	}
	for _, data := range files {
		if len(data) > 1024*1024 {
			return out, failure(contract.ErrLimitExceeded, "bundle file exceeds 1 MiB")
		}
	}
	m, err := contract.ValidateManifestJSON(files["manifest.json"])
	if err != nil {
		return out, err
	}
	tests, err := contract.ValidateTestsJSON(files["tests.json"], m)
	if err != nil {
		return out, err
	}
	base := 0
	latest := 0
	var previous contract.Manifest
	var oldSource string
	active, err := s.Registry.Resolve(req.Name, req.Scope)
	if err == nil {
		base = active.Version
		previous = active.Manifest
		oldSource = active.Source
		for _, v := range active.AvailableVersions {
			latest = max(latest, v)
		}
	} else {
		var te *contract.ToolError
		if !errors.As(err, &te) || te.Code != contract.ErrToolNotFound {
			return out, err
		}
	}
	requestedBase := m.Version - 1
	if req.BaseVersion != nil {
		requestedBase = *req.BaseVersion
	}
	if requestedBase != base || m.Version != latest+1 {
		return out, failure(contract.ErrInvalidManifest, "stale base_version or target version; inspect active tool and re-propose")
	}
	if err = compile(req.Source); err != nil {
		return out, err
	}
	report := contract.ValidationReport{SchemaValid: true, Compiled: true, TestsTotal: len(tests.Cases), Failures: []string{}}
	sup := s.Supervisor
	if sup == nil {
		sup = &runtime.Supervisor{}
	}
	for _, tc := range tests.Cases {
		resp := sup.ExecuteFixture(ctx, m, req.Source, tc)
		passed := matches(tc.Expect, resp)
		result := contract.FixtureResult{Name: tc.Name, Passed: passed, Error: resp.Error}
		if !passed {
			result.Output = resp.Output
		}
		report.Tests = append(report.Tests, result)
		if passed {
			report.TestsPassed++
		} else {
			report.Failures = append(report.Failures, tc.Name+": fixture expectation mismatch")
		}
	}
	out.Validation = report
	if report.TestsPassed != report.TestsTotal {
		return out, &contract.ToolError{Code: contract.ErrTestFailed, Message: "fixture tests failed", Details: map[string]any{"validation": report}}
	}
	preview := &contract.DryRunPreview{}
	if m.IsElevated() {
		preview.SkippedReason = "elevated tool: exec, external HTTP, secrets, or non-read-only declaration"
	} else {
		candidates := []map[string]any{}
		for _, tc := range tests.Cases {
			if tc.Expect.OK {
				candidates = append(candidates, tc.Input)
			}
		}
		defaults := map[string]any{}
		if props, ok := m.InputSchema["properties"].(map[string]any); ok {
			for k, v := range props {
				if p, ok := v.(map[string]any); ok {
					if d, ok := p["default"]; ok {
						defaults[k] = d
					}
				}
			}
		}
		candidates = append(candidates, defaults, map[string]any{})
		var input map[string]any
		for _, candidate := range candidates {
			if contract.ValidateInput(m, candidate) == nil {
				input = candidate
				break
			}
		}
		if input == nil {
			preview.SkippedReason = "no schema-valid sample or default input"
		} else {
			res := sup.ExecuteBundle(ctx, m, req.Source, s.WorkDir, input, 0, req.Scope)
			preview.Executed = true
			preview.OK = res.OK
			preview.DurationMs = res.DurationMs
			preview.OutputPreview = res.Output
			preview.Error = res.Error
		}
	}
	idBytes := make([]byte, 16)
	if _, err = rand.Read(idBytes); err != nil {
		return out, err
	}
	id := "prop_" + hex.EncodeToString(idBytes)
	record := contract.ProposalRecord{ProposalID: id, ToolName: req.Name, TargetVersion: m.Version, BaseVersion: base, Scope: req.Scope, Summary: req.Summary, Status: contract.ProposalPending, BundleSHA256: digest(files), Elevated: m.IsElevated(), Validation: report, CapabilityDiff: Diff(previous, m), DryRun: preview, CreatedAt: time.Now().UTC(), Kind: "create"}
	if base > 0 {
		record.Kind = "repair"
		record.SourceDiff = SourceDiff(oldSource, req.Source)
	}
	if req.Scope == contract.ScopeRepo {
		record.TargetRepoRoot = filepath.Dir(filepath.Dir(target))
	}
	metadata, err := json.Marshal(record)
	if err != nil {
		return out, err
	}
	if len(metadata) > 1024*1024 {
		return out, failure(contract.ErrLimitExceeded, "proposal report exceeds 1 MiB")
	}
	dir, err := s.staging()
	if err != nil {
		return out, err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return out, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return out, err
	}
	defer func() { _ = root.Close() }()
	temp := ".pending-" + id
	if err = root.Mkdir(temp, 0o700); err != nil {
		return out, err
	}
	defer func() { _ = root.RemoveAll(temp) }()
	child, err := root.OpenRoot(temp)
	if err != nil {
		return out, err
	}
	for name, data := range files {
		if err = child.WriteFile(name, data, 0o600); err != nil {
			_ = child.Close()
			return out, err
		}
	}
	err = registry.AtomicJSON(child, "proposal.json", record)
	_ = child.Close()
	if err != nil {
		return out, err
	}
	if err = root.Rename(temp, id); err != nil {
		return out, err
	}
	return contract.ProposeResponse{SourceDiff: record.SourceDiff, ProposalID: id, Status: record.Status, Tool: req.Name, TargetVersion: m.Version, BaseVersion: base, Scope: req.Scope, Elevated: record.Elevated, Validation: report, CapabilityDiff: record.CapabilityDiff, DryRun: preview, ApprovalCommand: "knurl approve " + id}, nil
}

func matches(expect contract.TestExpectation, resp contract.WorkerResponse) bool {
	if expect.OK != resp.OK {
		return false
	}
	if !expect.OK {
		return resp.Error != nil && (expect.ErrorCode == "" || expect.ErrorCode == resp.Error.Code) && strings.Contains(resp.Error.Message, expect.ErrorContains)
	}
	if expect.Output != nil && !jsonEqual(expect.Output, resp.Output) {
		return false
	}
	return subset(expect.OutputSubset, resp.Output)
}
func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func subset(want, got map[string]any) bool {
	for k, v := range want {
		actual, ok := got[k]
		if !ok {
			return false
		}
		if nested, ok := v.(map[string]any); ok {
			other, ok := actual.(map[string]any)
			if !ok || !subset(nested, other) {
				return false
			}
		} else if !jsonEqual(v, actual) {
			return false
		}
	}
	return true
}

func readBounded(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if len(data) > 1024*1024 {
		return nil, failure(contract.ErrLimitExceeded, "staged file exceeds 1 MiB")
	}
	return data, err
}
func (s *Store) open(id string) (*os.Root, error) {
	if !proposalPattern.MatchString(id) {
		return nil, failure(contract.ErrProposalNotFound, "invalid proposal ID")
	}
	dir, err := s.staging()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, failure(contract.ErrProposalNotFound, "proposal not found")
	}
	defer func() { _ = root.Close() }()
	child, err := root.OpenRoot(id)
	if err != nil {
		return nil, failure(contract.ErrProposalNotFound, "proposal not found")
	}
	return child, nil
}
func readRecord(root *os.Root) (contract.ProposalRecord, error) {
	var p contract.ProposalRecord
	data, err := readBounded(root, "proposal.json")
	if err == nil {
		err = json.Unmarshal(data, &p)
	}
	return p, err
}

// Read returns staged metadata with bounded confined reads.
func (s *Store) Read(id string) (contract.ProposalRecord, error) {
	root, err := s.open(id)
	if err != nil {
		return contract.ProposalRecord{}, err
	}
	defer func() { _ = root.Close() }()
	p, err := readRecord(root)
	if err == nil && p.ProposalID != id {
		return p, failure(contract.ErrInvalidManifest, "proposal ID mismatch")
	}
	return p, err
}

// List returns proposals sorted newest first.
func (s *Store) List(all bool) ([]contract.ProposalRecord, error) {
	out := []contract.ProposalRecord{}
	dir, err := s.staging()
	if err != nil {
		return out, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	for _, entry := range entries {
		if !proposalPattern.MatchString(entry.Name()) {
			continue
		}
		p, e := s.Read(entry.Name())
		if e != nil {
			continue // Corrupt records remain directly inspectable, never approval candidates.
		}
		if all || p.Status == contract.ProposalPending {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b contract.ProposalRecord) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ProposalID, a.ProposalID)
	})
	return out, nil
}

// Last selects the latest pending proposal.
func (s *Store) Last() (string, error) {
	list, err := s.List(false)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", failure(contract.ErrProposalNotFound, "no pending proposals")
	}
	return list[0].ProposalID, nil
}

// Review verifies the staged digest and returns exact source bytes for human review.
func (s *Store) Review(id string) (contract.ProposalRecord, map[string][]byte, error) {
	root, err := s.open(id)
	if err != nil {
		return contract.ProposalRecord{}, nil, err
	}
	defer func() { _ = root.Close() }()
	return verified(root, id)
}
func verified(root *os.Root, id string) (contract.ProposalRecord, map[string][]byte, error) {
	p, err := readRecord(root)
	if err != nil {
		return p, nil, err
	}
	if p.ProposalID != id || p.Status != contract.ProposalPending {
		return p, nil, failure(contract.ErrInvalidArgument, "proposal is not pending or ID mismatches")
	}
	files := map[string][]byte{}
	for _, name := range []string{"manifest.json", "tool.js", "tests.json"} {
		data, e := readBounded(root, name)
		if e != nil {
			return p, nil, e
		}
		files[name] = data
	}
	if digest(files) != p.BundleSHA256 {
		return p, nil, failure(contract.ErrInvalidManifest, "staged bundle digest mismatch; re-propose")
	}
	return p, files, nil
}

// Approve verifies reviewed bytes and promotes them; called only by human CLI.
func (s *Store) Approve(id, reviewedDigest string) (contract.ProposalRecord, error) {
	root, err := s.open(id)
	if err != nil {
		return contract.ProposalRecord{}, err
	}
	defer func() { _ = root.Close() }()
	unlock, err := registry.Lock(root)
	if err != nil {
		return contract.ProposalRecord{}, err
	}
	defer unlock()
	p, files, err := verified(root, id)
	if err != nil {
		return p, err
	}
	if p.BundleSHA256 != reviewedDigest {
		return p, failure(contract.ErrInvalidManifest, "proposal changed since review")
	}
	m, err := contract.ValidateManifestJSON(files["manifest.json"])
	if err != nil {
		return p, err
	}
	if m.Name != p.ToolName || m.Version != p.TargetVersion {
		return p, failure(contract.ErrInvalidManifest, "proposal metadata mismatch")
	}
	if _, err = contract.ValidateTestsJSON(files["tests.json"], m); err != nil {
		return p, err
	}
	if err = compile(string(files["tool.js"])); err != nil {
		return p, err
	}
	reg := *s.Registry
	if p.Scope == contract.ScopeRepo {
		if !filepath.IsAbs(p.TargetRepoRoot) {
			return p, failure(contract.ErrInvalidManifest, "invalid target repo root")
		}
		reg.WorkDir = p.TargetRepoRoot
	}
	reg.OverrideDir = ""
	now := time.Now().UTC()
	audit := contract.ApprovalRecord{ProposalID: id, ApprovedAt: now, BundleSHA256: p.BundleSHA256, Summary: p.Summary, CapabilityDiff: p.CapabilityDiff}
	if err = reg.Promote(m, files, p.BaseVersion, audit, p.Scope); err != nil {
		return p, err
	}
	p.Status = contract.ProposalApproved
	p.PromotedVersion = m.Version
	p.DecidedAt = &now
	p.DecisionNote = "approved via CLI"
	err = registry.AtomicJSON(root, "proposal.json", p)
	return p, err
}

// Reject records a human rejection without touching active registry state.
func (s *Store) Reject(id, reason string) (contract.ProposalRecord, error) {
	root, err := s.open(id)
	if err != nil {
		return contract.ProposalRecord{}, err
	}
	defer func() { _ = root.Close() }()
	unlock, err := registry.Lock(root)
	if err != nil {
		return contract.ProposalRecord{}, err
	}
	defer unlock()
	p, err := readRecord(root)
	if err != nil {
		return p, err
	}
	if p.Status != contract.ProposalPending {
		return p, failure(contract.ErrInvalidArgument, "proposal is not pending")
	}
	now := time.Now().UTC()
	p.Status = contract.ProposalRejected
	p.DecidedAt = &now
	p.DecisionNote = reason
	return p, registry.AtomicJSON(root, "proposal.json", p)
}

// Status returns the frozen MCP status envelope.
func (s *Store) Status(id string) (contract.StatusResponse, error) {
	p, err := s.Read(id)
	if err != nil {
		return contract.StatusResponse{}, err
	}
	out := contract.StatusResponse{ProposalID: p.ProposalID, Status: p.Status, Tool: p.ToolName, TargetVersion: p.TargetVersion, BaseVersion: p.BaseVersion, Scope: p.Scope, CreatedAt: p.CreatedAt, DecidedAt: p.DecidedAt, DecisionNote: p.DecisionNote, PromotedVersion: p.PromotedVersion}
	if p.Status == contract.ProposalPending {
		out.ApprovalCommand = "knurl approve " + p.ProposalID
	}
	return out, nil
}
