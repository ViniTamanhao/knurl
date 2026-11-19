package host

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

// ValidateRelativePath verifies that raw is a safe workspace-relative path
// with no leading slash, backslashes, NUL bytes, or '..' traversal segments.
func ValidateRelativePath(raw string) (string, error) {
	if raw == "" {
		return "", &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "path must not be empty",
		}
	}
	if strings.ContainsRune(raw, 0) {
		return "", &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: "path must not contain NUL bytes",
		}
	}
	if strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, '\\') {
		return "", &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("path %q must be relative with '/' separators", raw),
		}
	}
	if slices.Contains(strings.Split(raw, "/"), "..") {
		return "", &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("path %q must not contain '..' traversal segments", raw),
		}
	}
	cleaned := path.Clean(raw)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("path %q escapes workspace root", raw),
		}
	}
	return cleaned, nil
}

// ValidateGlobPattern validates that a glob pattern is relative, has no '..'
// segments, and uses valid path.Match segment syntax.
func ValidateGlobPattern(pattern string) (string, error) {
	if pattern == "" {
		return "", &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "glob pattern must not be empty",
		}
	}
	if strings.ContainsRune(pattern, 0) {
		return "", &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: "glob pattern must not contain NUL bytes",
		}
	}
	if strings.HasPrefix(pattern, "/") || strings.ContainsRune(pattern, '\\') {
		return "", &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("glob pattern %q must be relative with '/' separators", pattern),
		}
	}
	segs := strings.Split(pattern, "/")
	for _, seg := range segs {
		if seg == ".." {
			return "", &contract.ToolError{
				Code:    contract.ErrCapabilityDenied,
				Message: fmt.Sprintf("glob pattern %q must not contain '..' segments", pattern),
			}
		}
		if seg == "" || seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return "", &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("invalid glob pattern %q: %v", pattern, err),
			}
		}
	}
	cleaned := path.Clean(pattern)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("glob pattern %q escapes workspace root", pattern),
		}
	}
	return cleaned, nil
}

// MatchGlob reports whether relPath matches pattern, supporting '**' zero-or-more
// directory segments as well as standard '*', '?', and '[...]' segment patterns.
func MatchGlob(pattern, relPath string) (bool, error) {
	cleanPat, err := ValidateGlobPattern(pattern)
	if err != nil {
		return false, err
	}
	if relPath == "." {
		return cleanPat == "**" || cleanPat == ".", nil
	}
	patSegs := strings.Split(cleanPat, "/")
	pathSegs := strings.Split(relPath, "/")

	m, n := len(patSegs), len(pathSegs)
	dp := make([][]bool, m+1)
	for i := range dp {
		dp[i] = make([]bool, n+1)
	}
	dp[0][0] = true
	for i := 1; i <= m; i++ {
		if patSegs[i-1] == "**" {
			dp[i][0] = dp[i-1][0]
		}
	}

	for i := 1; i <= m; i++ {
		pSeg := patSegs[i-1]
		for j := 1; j <= n; j++ {
			if pSeg == "**" {
				dp[i][j] = dp[i-1][j] || dp[i][j-1]
				continue
			}
			if dp[i-1][j-1] {
				matched, matchErr := path.Match(pSeg, pathSegs[j-1])
				if matchErr != nil {
					return false, &contract.ToolError{
						Code:    contract.ErrInvalidArgument,
						Message: fmt.Sprintf("invalid glob pattern %q: %v", pattern, matchErr),
					}
				}
				dp[i][j] = matched
			}
		}
	}
	return dp[m][n], nil
}

// MatchesAny reports whether relPath matches at least one pattern in patterns.
func MatchesAny(patterns []string, relPath string) bool {
	for _, pat := range patterns {
		matched, err := MatchGlob(pat, relPath)
		if err == nil && matched {
			return true
		}
	}
	return false
}

// DirMayMatchIgnore reports whether every descendant under dirRel is guaranteed
// to match at least one ignore pattern (allowing safe fs.SkipDir pruning).
func DirMayMatchIgnore(ignorePatterns []string, dirRel string) bool {
	if dirRel == "" || dirRel == "." {
		return false
	}
	for _, pat := range ignorePatterns {
		cleanPat, err := ValidateGlobPattern(pat)
		if err != nil {
			continue
		}
		if cleanPat == "**" {
			return true
		}
		if prefix, ok := strings.CutSuffix(cleanPat, "/**"); ok && prefix != "" {
			matched, matchErr := MatchGlob(prefix, dirRel)
			if matchErr == nil && matched {
				return true
			}
		}
	}
	return false
}
