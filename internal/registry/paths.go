package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

var toolNameRegexp = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// IsValidToolName reports whether name matches the strict kebab-case tool
// naming specification (2-64 chars, lowercase alphanumeric segments).
func IsValidToolName(name string) bool {
	if len(name) < 2 || len(name) > 64 {
		return false
	}
	return toolNameRegexp.MatchString(name)
}

// ValidateToolName rejects invalid, empty, or path-traversal tool names
// fail-closed before touching the filesystem.
func ValidateToolName(name string) error {
	if !IsValidToolName(name) {
		return &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: fmt.Sprintf("tool %q not found (invalid tool name)", name),
		}
	}
	return nil
}

// DiscoverRepoToolsDir walks upward from workDir looking for .knurl/tools.
// Discovery stops immediately after checking a directory containing .git
// (whether directory or Git worktree/submodule file) or upon reaching the
// filesystem root.
func DiscoverRepoToolsDir(workDir string) (string, bool, error) {
	start := workDir
	if start == "" {
		var wdErr error
		start, wdErr = os.Getwd()
		if wdErr != nil {
			return "", false, fmt.Errorf("resolve working directory: %w", wdErr)
		}
	}
	absStart, absErr := filepath.Abs(start)
	if absErr != nil {
		return "", false, fmt.Errorf("resolve working directory %q: %w", start, absErr)
	}
	if canonical, evalErr := filepath.EvalSymlinks(absStart); evalErr == nil {
		absStart = canonical
	}

	dir := absStart
	for {
		candidate := filepath.Join(dir, ".knurl", "tools")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			if canonical, evalErr := filepath.EvalSymlinks(candidate); evalErr == nil {
				return canonical, true, nil
			}
			return filepath.Clean(candidate), true, nil
		}

		// Stop at repository root boundary (.git may be a directory or a file in worktrees).
		if _, gitErr := os.Stat(filepath.Join(dir, ".git")); gitErr == nil {
			return "", false, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

// ResolveGlobalToolsDir returns the global active tools directory according to
// environment precedence: KNURL_HOME > KNURL_CONFIG_DIR > XDG_CONFIG_HOME > HOME.
func ResolveGlobalToolsDir(envLookup func(string) (string, bool)) (string, error) {
	useOSFallback := envLookup == nil
	lookup := envLookup
	if lookup == nil {
		lookup = os.LookupEnv
	}

	if v, ok := lookup("KNURL_HOME"); ok && strings.TrimSpace(v) != "" {
		return filepath.Clean(filepath.Join(v, "tools")), nil
	}
	if v, ok := lookup("KNURL_CONFIG_DIR"); ok && strings.TrimSpace(v) != "" {
		return filepath.Clean(filepath.Join(v, "tools")), nil
	}
	if v, ok := lookup("XDG_CONFIG_HOME"); ok && strings.TrimSpace(v) != "" {
		return filepath.Clean(filepath.Join(v, "knurl", "tools")), nil
	}
	if v, ok := lookup("HOME"); ok && strings.TrimSpace(v) != "" {
		return filepath.Clean(filepath.Join(v, ".config", "knurl", "tools")), nil
	}
	if useOSFallback {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("resolve home directory for global tools: %w", homeErr)
		}
		return filepath.Clean(filepath.Join(home, ".config", "knurl", "tools")), nil
	}
	return "", fmt.Errorf("cannot resolve global tools directory: KNURL_HOME, KNURL_CONFIG_DIR, XDG_CONFIG_HOME, and HOME are unset")
}

func looksLikePath(v string) bool {
	return v == "." || v == ".." || strings.ContainsAny(v, `/\`)
}
