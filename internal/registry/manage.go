package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

// TargetToolsDir resolves a write destination without creating it.
func (r *Registry) TargetToolsDir(scope contract.Scope) (string, error) {
	if scope == contract.ScopeGlobal {
		return r.GlobalToolsDir()
	}
	if scope != contract.ScopeRepo {
		return "", &contract.ToolError{Code: contract.ErrInvalidArgument, Message: "target scope must be repo or global"}
	}
	if dir, ok, err := r.RepoToolsDir(); err != nil || ok {
		return dir, err
	}
	dir, err := filepath.Abs(r.effectiveWorkDir())
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Join(dir, ".knurl", "tools"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", &contract.ToolError{Code: contract.ErrInvalidArgument, Message: "repo proposal requires a repository root"}
}

// Discover returns compact metadata, with repository names taking precedence.
func (r *Registry) Discover(req contract.DiscoverRequest) (contract.DiscoverResponse, error) {
	scope := req.Scope
	if scope == "" || scope == contract.ScopeAll {
		scope = contract.ScopeAuto
	}
	entries, err := r.List(scope)
	out := contract.DiscoverResponse{Tools: []contract.ToolSummary{}}
	if err != nil {
		return out, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 100 {
		return out, &contract.ToolError{Code: contract.ErrInvalidArgument, Message: "limit must be 1..100"}
	}
	for _, e := range entries {
		hay := strings.ToLower(e.Name + " " + e.Manifest.Description + " " + strings.Join(e.Manifest.Tags, " "))
		if !strings.Contains(hay, strings.ToLower(req.Query)) {
			continue
		}
		match := true
		for _, tag := range req.Tags {
			if !slices.Contains(e.Manifest.Tags, tag) {
				match = false
			}
		}
		if match {
			out.Tools = append(out.Tools, e.Summary)
		}
		if len(out.Tools) == limit {
			break
		}
	}
	return out, nil
}

// Inspect returns the full manifest without source code.
func (r *Registry) Inspect(req contract.InspectRequest) (contract.InspectResponse, error) {
	var b *Bundle
	var err error
	if req.Version > 0 {
		b, err = r.LoadVersion(req.Name, req.Version, req.Scope)
	} else {
		b, err = r.Resolve(req.Name, req.Scope)
	}
	if err != nil {
		return contract.InspectResponse{}, err
	}
	return contract.InspectResponse{Name: b.Name, Version: b.Version, Scope: b.Scope, ActiveVersion: b.ActiveVersion, AvailableVersions: b.AvailableVersions, Elevated: b.Manifest.IsElevated(), Manifest: b.Manifest}, nil
}

// AtomicJSON syncs a temporary file before replacing metadata within root.
func AtomicJSON(root *os.Root, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temp := fmt.Sprintf(".metadata-%d", time.Now().UnixNano())
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temp) }()
	if _, err = f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return root.Rename(temp, name)
}

// Lock serializes mutations across independent processes. Existing locks fail closed.
func Lock(root *os.Root) (func(), error) {
	if err := root.Mkdir(".write-lock", 0o700); err != nil {
		return nil, fmt.Errorf("registry busy (remove stale .write-lock only after verifying no writer): %w", err)
	}
	return func() { _ = root.Remove(".write-lock") }, nil
}

// Promote installs immutable bytes and updates active.json, exclusively for CLI approval.
func (r *Registry) Promote(m contract.Manifest, files map[string][]byte, base int, audit contract.ApprovalRecord, scope contract.Scope) error {
	if err := ValidateToolName(m.Name); err != nil {
		return err
	}
	dir, err := r.TargetToolsDir(scope)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	unlock, err := Lock(root)
	if err != nil {
		return err
	}
	defer unlock()
	b, loadErr := r.Resolve(m.Name, scope)
	current := 0
	latest := 0
	if loadErr == nil {
		current = b.Version
		for _, v := range b.AvailableVersions {
			latest = max(latest, v)
		}
	} else {
		var te *contract.ToolError
		if !errors.As(loadErr, &te) || te.Code != contract.ErrToolNotFound {
			return loadErr
		}
	}
	if current != base || m.Version != latest+1 {
		return &contract.ToolError{Code: contract.ErrInvalidManifest, Message: "stale base or target version; re-propose against active version"}
	}
	if err = root.MkdirAll(m.Name, 0o700); err != nil {
		return err
	}
	tool, err := root.OpenRoot(m.Name)
	if err != nil {
		return err
	}
	defer func() { _ = tool.Close() }()
	temp := fmt.Sprintf(".install-%d", time.Now().UnixNano())
	if err = tool.Mkdir(temp, 0o700); err != nil {
		return err
	}
	defer func() { _ = tool.RemoveAll(temp) }()
	stage, err := tool.OpenRoot(temp)
	if err != nil {
		return err
	}
	for _, name := range []string{"manifest.json", "tool.js", "tests.json"} {
		data, ok := files[name]
		if !ok {
			_ = stage.Close()
			return fmt.Errorf("missing %s", name)
		}
		f, e := stage.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if e != nil {
			_ = stage.Close()
			return e
		}
		_, e = f.Write(data)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			_ = stage.Close()
			return e
		}
	}
	err = AtomicJSON(stage, "approval.json", audit)
	_ = stage.Close()
	if err != nil {
		return err
	}
	version := fmt.Sprintf("v%d", m.Version)
	if _, err = tool.Stat(version); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("version %s already exists or cannot be checked", version)
	}
	if err = tool.Rename(temp, version); err != nil {
		return err
	}
	return AtomicJSON(tool, "active.json", contract.ActiveVersionPointer{ActiveVersion: m.Version, PreviousVersion: current, UpdatedAt: time.Now().UTC()})
}

// Rollback atomically switches to an installed approved version, preserving history.
func (r *Registry) Rollback(name string, to int, scope contract.Scope) (int, int, error) {
	b, err := r.Resolve(name, scope)
	if err != nil {
		return 0, 0, err
	}
	dir, err := r.TargetToolsDir(b.Scope)
	if err != nil {
		return 0, 0, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = root.Close() }()
	unlock, err := Lock(root)
	if err != nil {
		return 0, 0, err
	}
	defer unlock()
	b, err = r.Resolve(name, b.Scope)
	if err != nil {
		return 0, 0, err
	}
	tool, err := root.OpenRoot(name)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tool.Close() }()
	if to == 0 {
		data, e := tool.ReadFile("active.json")
		if e != nil {
			return 0, 0, e
		}
		var p contract.ActiveVersionPointer
		if e = json.Unmarshal(data, &p); e != nil {
			return 0, 0, e
		}
		to = p.PreviousVersion
	}
	if to < 1 || to == b.Version {
		return 0, 0, &contract.ToolError{Code: contract.ErrVersionNotFound, Message: "no previous version"}
	}
	target, err := r.LoadVersion(name, to, b.Scope)
	if err != nil {
		return 0, 0, err
	}
	if target.Approval == nil {
		return 0, 0, &contract.ToolError{Code: contract.ErrInvalidManifest, Message: "rollback target lacks approval record"}
	}
	err = AtomicJSON(tool, "active.json", contract.ActiveVersionPointer{ActiveVersion: to, PreviousVersion: b.Version, UpdatedAt: time.Now().UTC()})
	return b.Version, to, err
}
