package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

// Registry resolves, inspects, and lists approved tool bundles across the
// two-tier repo-local and global registries or an explicit override directory.
type Registry struct {
	WorkDir     string
	OverrideDir string
	GlobalDir   string
	EnvLookup   func(string) (string, bool)
}

func (r *Registry) lookupEnv() func(string) (string, bool) {
	if r != nil && r.EnvLookup != nil {
		return r.EnvLookup
	}
	return nil
}

func (r *Registry) effectiveWorkDir() string {
	if r == nil {
		return ""
	}
	return r.WorkDir
}

func (r *Registry) effectiveOverrideDir() string {
	if r == nil {
		return ""
	}
	return r.OverrideDir
}

// RepoToolsDir discovers the nearest ./.knurl/tools directory walking upward
// from Registry.WorkDir up to the .git repository boundary.
func (r *Registry) RepoToolsDir() (string, bool, error) {
	return DiscoverRepoToolsDir(r.effectiveWorkDir())
}

// GlobalToolsDir returns the configured global tools directory or resolves it
// from KNURL_HOME / KNURL_CONFIG_DIR / XDG_CONFIG_HOME / HOME.
func (r *Registry) GlobalToolsDir() (string, error) {
	if r != nil && r.GlobalDir != "" {
		absDir, err := filepath.Abs(r.GlobalDir)
		if err != nil {
			return "", fmt.Errorf("resolve global tools directory %q: %w", r.GlobalDir, err)
		}
		return filepath.Clean(absDir), nil
	}
	return ResolveGlobalToolsDir(r.lookupEnv())
}

// Resolve resolves the active version of tool name under the specified scope
// (defaulting empty scope to ScopeAuto).
func (r *Registry) Resolve(name string, scope contract.Scope) (*Bundle, error) {
	return r.resolveWithVersion(name, 0, scope)
}

// LoadVersion loads an explicit version v<N> of tool name under scope.
func (r *Registry) LoadVersion(name string, version int, scope contract.Scope) (*Bundle, error) {
	if version < 1 {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("version must be >= 1, got %d", version),
		}
	}
	return r.resolveWithVersion(name, version, scope)
}

// LoadTarget loads either a direct bundle directory path (when nameOrPath
// contains path separators or equals "."/"..") or a named tool from the registry.
func (r *Registry) LoadTarget(nameOrPath string, scope contract.Scope, version int) (*Bundle, error) {
	if nameOrPath == "" {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: "tool name or path must not be empty",
		}
	}
	if looksLikePath(nameOrPath) {
		if version < 0 {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("version must be >= 1, got %d", version),
			}
		}
		return loadToolFromDir(nameOrPath, "", contract.ScopeOverride, version)
	}
	if version > 0 {
		return r.LoadVersion(nameOrPath, version, scope)
	}
	return r.Resolve(nameOrPath, scope)
}

func (r *Registry) resolveWithVersion(name string, version int, scope contract.Scope) (*Bundle, error) {
	if err := ValidateToolName(name); err != nil {
		return nil, err
	}
	if scope == "" {
		scope = contract.ScopeAuto
	}

	switch scope {
	case contract.ScopeOverride:
		overrideDir := r.effectiveOverrideDir()
		if overrideDir == "" {
			return nil, &contract.ToolError{
				Code:    contract.ErrToolNotFound,
				Message: fmt.Sprintf("tool %q not found (no override tools directory configured)", name),
			}
		}
		return loadToolFromDir(filepath.Join(overrideDir, name), name, contract.ScopeOverride, version)

	case contract.ScopeAuto:
		if overrideDir := r.effectiveOverrideDir(); overrideDir != "" {
			return loadToolFromDir(filepath.Join(overrideDir, name), name, contract.ScopeOverride, version)
		}
		repoDir, hasRepo, repoErr := r.RepoToolsDir()
		if repoErr != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInternalError,
				Message: repoErr.Error(),
			}
		}
		if hasRepo {
			repoToolDir := filepath.Join(repoDir, name)
			if info, statErr := os.Stat(repoToolDir); statErr == nil && info.IsDir() {
				// Fail closed on malformed repo-local bundles instead of silently falling back to global.
				return loadToolFromDir(repoToolDir, name, contract.ScopeRepo, version)
			}
		}
		globalDir, globalErr := r.GlobalToolsDir()
		if globalErr != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInternalError,
				Message: globalErr.Error(),
			}
		}
		return loadToolFromDir(filepath.Join(globalDir, name), name, contract.ScopeGlobal, version)

	case contract.ScopeRepo:
		repoDir, hasRepo, repoErr := r.RepoToolsDir()
		if repoErr != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInternalError,
				Message: repoErr.Error(),
			}
		}
		if !hasRepo {
			return nil, &contract.ToolError{
				Code:    contract.ErrToolNotFound,
				Message: fmt.Sprintf("tool %q not found in repo scope", name),
			}
		}
		return loadToolFromDir(filepath.Join(repoDir, name), name, contract.ScopeRepo, version)

	case contract.ScopeGlobal:
		globalDir, globalErr := r.GlobalToolsDir()
		if globalErr != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInternalError,
				Message: globalErr.Error(),
			}
		}
		return loadToolFromDir(filepath.Join(globalDir, name), name, contract.ScopeGlobal, version)

	default:
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("unsupported registry scope %q", scope),
		}
	}
}

// List enumerates active valid tools matching scope. Missing tier directories
// return an empty slice without error.
func (r *Registry) List(scope contract.Scope) ([]ToolEntry, error) {
	if scope == "" {
		scope = contract.ScopeAuto
	}

	var entries []ToolEntry
	switch scope {
	case contract.ScopeOverride:
		overrideDir := r.effectiveOverrideDir()
		if overrideDir == "" {
			return []ToolEntry{}, nil
		}
		entries = scanToolsTier(overrideDir, contract.ScopeOverride)

	case contract.ScopeRepo:
		repoDir, hasRepo, repoErr := r.RepoToolsDir()
		if repoErr != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInternalError,
				Message: repoErr.Error(),
			}
		}
		if hasRepo {
			entries = scanToolsTier(repoDir, contract.ScopeRepo)
		}

	case contract.ScopeGlobal:
		globalDir, globalErr := r.GlobalToolsDir()
		if globalErr != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInternalError,
				Message: globalErr.Error(),
			}
		}
		entries = scanToolsTier(globalDir, contract.ScopeGlobal)

	case contract.ScopeAuto, contract.ScopeAll:
		if overrideDir := r.effectiveOverrideDir(); overrideDir != "" {
			entries = scanToolsTier(overrideDir, contract.ScopeOverride)
			break
		}
		var repoEntries []ToolEntry
		repoDir, hasRepo, repoErr := r.RepoToolsDir()
		if repoErr != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInternalError,
				Message: repoErr.Error(),
			}
		}
		if hasRepo {
			repoEntries = scanToolsTier(repoDir, contract.ScopeRepo)
		}
		globalDir, globalErr := r.GlobalToolsDir()
		if globalErr != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInternalError,
				Message: globalErr.Error(),
			}
		}
		globalEntries := scanToolsTier(globalDir, contract.ScopeGlobal)

		if scope == contract.ScopeAll {
			entries = make([]ToolEntry, 0, len(repoEntries)+len(globalEntries))
			entries = append(entries, repoEntries...)
			entries = append(entries, globalEntries...)
		} else {
			seen := make(map[string]struct{}, len(repoEntries))
			entries = make([]ToolEntry, 0, len(repoEntries)+len(globalEntries))
			for _, re := range repoEntries {
				seen[re.Name] = struct{}{}
				entries = append(entries, re)
			}
			for _, ge := range globalEntries {
				if _, shadowed := seen[ge.Name]; shadowed {
					continue
				}
				entries = append(entries, ge)
			}
		}

	default:
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("unsupported registry scope %q", scope),
		}
	}

	if entries == nil {
		entries = []ToolEntry{}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return scopeRank(entries[i].Scope) < scopeRank(entries[j].Scope)
	})
	return entries, nil
}

func scanToolsTier(toolsDir string, scope contract.Scope) []ToolEntry {
	dirEntries, err := os.ReadDir(toolsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return nil
	}
	out := make([]ToolEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if !IsValidToolName(name) {
			continue
		}
		toolPath := filepath.Join(toolsDir, name)
		info, statErr := os.Stat(toolPath)
		if statErr != nil || !info.IsDir() {
			continue
		}
		bundle, loadErr := loadToolFromDir(toolPath, name, scope, 0)
		if loadErr != nil {
			continue
		}
		out = append(out, ToolEntry{
			Name:              bundle.Name,
			Version:           bundle.Version,
			ActiveVersion:     bundle.ActiveVersion,
			AvailableVersions: slices.Clone(bundle.AvailableVersions),
			Scope:             bundle.Scope,
			Dir:               bundle.Dir,
			Manifest:          bundle.Manifest,
			Summary: contract.ToolSummary{
				Name:        bundle.Name,
				Version:     bundle.Version,
				Scope:       bundle.Scope,
				Description: bundle.Manifest.Description,
				Tags:        slices.Clone(bundle.Manifest.Tags),
				ReadOnly:    bundle.Manifest.ReadOnly,
				Elevated:    bundle.Manifest.IsElevated(),
			},
		})
	}
	return out
}

func scopeRank(s contract.Scope) int {
	switch s {
	case contract.ScopeRepo:
		return 0
	case contract.ScopeGlobal:
		return 1
	case contract.ScopeOverride:
		return 2
	default:
		return 3
	}
}
