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

// GlobIntersectsAny reports whether reqPattern could match at least one path
// also matched by some pattern in allowedPatterns.
func GlobIntersectsAny(allowedPatterns []string, reqPattern string) bool {
	cleanReq, err := ValidateGlobPattern(reqPattern)
	if err != nil {
		return false
	}
	for _, allowed := range allowedPatterns {
		cleanAllowed, allowedErr := ValidateGlobPattern(allowed)
		if allowedErr != nil {
			continue
		}
		if globPatternsMayIntersect(cleanAllowed, cleanReq) {
			return true
		}
	}
	return false
}

func globPatternsMayIntersect(a, b string) bool {
	if a == "." || b == "." {
		return (a == "." || a == "**") && (b == "." || b == "**")
	}
	aSegs := strings.Split(a, "/")
	bSegs := strings.Split(b, "/")
	m, n := len(aSegs), len(bSegs)

	dp := make([][]bool, m+1)
	for i := range dp {
		dp[i] = make([]bool, n+1)
	}
	dp[0][0] = true
	for i := 1; i <= m; i++ {
		if aSegs[i-1] == "**" {
			dp[i][0] = dp[i-1][0]
		}
	}
	for j := 1; j <= n; j++ {
		if bSegs[j-1] == "**" {
			dp[0][j] = dp[0][j-1]
		}
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			aSeg, bSeg := aSegs[i-1], bSegs[j-1]
			switch {
			case aSeg == "**" && bSeg == "**":
				dp[i][j] = dp[i-1][j] || dp[i][j-1] || dp[i-1][j-1]
			case aSeg == "**":
				dp[i][j] = dp[i-1][j] || dp[i][j-1]
			case bSeg == "**":
				dp[i][j] = dp[i][j-1] || dp[i-1][j]
			default:
				if dp[i-1][j-1] && segmentPatternsMayIntersect(aSeg, bSeg) {
					dp[i][j] = true
				}
			}
		}
	}
	return dp[m][n]
}

type segTokKind uint8

const (
	segTokLit segTokKind = iota
	segTokAny
	segTokStar
	segTokClass
)

type segTok struct {
	kind segTokKind
	ch   rune
	raw  string
}

func tokenizeSegmentPattern(pat string) []segTok {
	toks := make([]segTok, 0, len(pat))
	runes := []rune(pat)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			if len(toks) == 0 || toks[len(toks)-1].kind != segTokStar {
				toks = append(toks, segTok{kind: segTokStar})
			}
		case '?':
			toks = append(toks, segTok{kind: segTokAny})
		case '[':
			end := i + 1
			if end < len(runes) && runes[end] == '^' {
				end++
			}
			if end < len(runes) && runes[end] == ']' {
				end++
			}
			for end < len(runes) && runes[end] != ']' {
				if runes[end] == '\\' && end+1 < len(runes) {
					end += 2
					continue
				}
				end++
			}
			if end < len(runes) {
				toks = append(toks, segTok{kind: segTokClass, raw: string(runes[i : end+1])})
				i = end
			} else {
				toks = append(toks, segTok{kind: segTokLit, ch: '['})
			}
		case '\\':
			if i+1 < len(runes) {
				i++
				toks = append(toks, segTok{kind: segTokLit, ch: runes[i]})
			} else {
				toks = append(toks, segTok{kind: segTokLit, ch: '\\'})
			}
		default:
			toks = append(toks, segTok{kind: segTokLit, ch: runes[i]})
		}
	}
	return toks
}

func segTokensMayMatchSameChar(a, b segTok) bool {
	if a.kind == segTokAny || b.kind == segTokAny {
		return true
	}
	if a.kind == segTokLit && b.kind == segTokLit {
		return a.ch == b.ch
	}
	if a.kind == segTokLit && b.kind == segTokClass {
		ok, err := path.Match(b.raw, string(a.ch))
		return err == nil && ok
	}
	if a.kind == segTokClass && b.kind == segTokLit {
		ok, err := path.Match(a.raw, string(b.ch))
		return err == nil && ok
	}
	// Two character classes: conservatively treat as potentially overlapping.
	return true
}

func segmentPatternsMayIntersect(a, b string) bool {
	t1 := tokenizeSegmentPattern(a)
	t2 := tokenizeSegmentPattern(b)
	m, n := len(t1), len(t2)

	dp := make([][]bool, m+1)
	for i := range dp {
		dp[i] = make([]bool, n+1)
	}
	dp[0][0] = true
	for i := 1; i <= m; i++ {
		if t1[i-1].kind == segTokStar {
			dp[i][0] = dp[i-1][0]
		}
	}
	for j := 1; j <= n; j++ {
		if t2[j-1].kind == segTokStar {
			dp[0][j] = dp[0][j-1]
		}
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			aTok, bTok := t1[i-1], t2[j-1]
			switch {
			case aTok.kind == segTokStar && bTok.kind == segTokStar:
				dp[i][j] = dp[i-1][j] || dp[i][j-1] || dp[i-1][j-1]
			case aTok.kind == segTokStar:
				dp[i][j] = dp[i-1][j] || dp[i][j-1]
			case bTok.kind == segTokStar:
				dp[i][j] = dp[i][j-1] || dp[i-1][j]
			default:
				if dp[i-1][j-1] && segTokensMayMatchSameChar(aTok, bTok) {
					dp[i][j] = true
				}
			}
		}
	}
	return dp[m][n]
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
