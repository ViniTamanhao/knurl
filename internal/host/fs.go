package host

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

const (
	// DefaultFSReadMaxBytes is the default knurl.fs.read byte cap (256 KiB).
	DefaultFSReadMaxBytes = 262144
	// MaxFSReadBytes is the hard maximum knurl.fs.read byte cap (1 MiB).
	MaxFSReadBytes = 1048576
	// DefaultGlobMaxResults is the default knurl.fs.glob result limit.
	DefaultGlobMaxResults = 500
	// MaxGlobMaxResults is the hard maximum knurl.fs.glob result limit.
	MaxGlobMaxResults = 5000

	maxSymlinkHops = 40
)

// FSReadOptions configures knurl.fs.read.
type FSReadOptions struct {
	MaxBytes int
	HasMax   bool
	Truncate bool
}

// FSGlobOptions configures knurl.fs.glob.
type FSGlobOptions struct {
	MaxResults    int
	HasMax        bool
	IncludeHidden bool
	Ignore        []string
}

// FSStatResult matches the knurl.fs.stat JS return contract.
type FSStatResult struct {
	Exists  bool   `json:"exists"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

func (h *Host) allowedFSReadPatterns() ([]string, error) {
	if h.manifest.Capabilities.FS == nil || len(h.manifest.Capabilities.FS.Read) == 0 {
		return nil, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: "capabilities.fs.read is not granted",
		}
	}
	return h.manifest.Capabilities.FS.Read, nil
}

// FSRead reads a UTF-8 text file within the os.Root-confined workspace.
func (h *Host) FSRead(rawPath string, opts FSReadOptions) (string, error) {
	patterns, err := h.allowedFSReadPatterns()
	if err != nil {
		return "", err
	}

	maxBytes := DefaultFSReadMaxBytes
	if opts.HasMax {
		if opts.MaxBytes <= 0 {
			return "", &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("max_bytes must be positive, got %d", opts.MaxBytes),
			}
		}
		if opts.MaxBytes > MaxFSReadBytes {
			return "", &contract.ToolError{
				Code:    contract.ErrLimitExceeded,
				Message: fmt.Sprintf("max_bytes %d exceeds maximum %d", opts.MaxBytes, MaxFSReadBytes),
			}
		}
		maxBytes = opts.MaxBytes
	}

	relPath, err := ValidateRelativePath(rawPath)
	if err != nil {
		return "", err
	}
	if !MatchesAny(patterns, relPath) {
		return "", &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("path %q is outside capabilities.fs.read", rawPath),
		}
	}

	resolvedRel, _, err := resolveWithinRoot(h.root, relPath)
	if err != nil {
		return "", err
	}
	if !MatchesAny(patterns, resolvedRel) {
		return "", &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("resolved path %q is outside capabilities.fs.read", resolvedRel),
		}
	}

	file, openErr := h.root.Open(resolvedRel)
	if openErr != nil {
		if errors.Is(openErr, fs.ErrNotExist) {
			return "", &contract.ToolError{
				Code:    contract.ErrNotFound,
				Message: fmt.Sprintf("file %q does not exist", rawPath),
			}
		}
		return "", &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("open %q failed: %v", rawPath, openErr),
		}
	}
	defer func() { _ = file.Close() }()

	info, statErr := file.Stat()
	if statErr != nil {
		return "", &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("stat %q failed: %v", rawPath, statErr),
		}
	}
	if info.IsDir() {
		return "", &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("path %q is a directory", rawPath),
		}
	}

	data, readErr := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if readErr != nil {
		return "", &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("read %q failed: %v", rawPath, readErr),
		}
	}
	if len(data) > maxBytes {
		if !opts.Truncate {
			return "", &contract.ToolError{
				Code:    contract.ErrLimitExceeded,
				Message: fmt.Sprintf("file %q exceeds max_bytes (%d)", rawPath, maxBytes),
			}
		}
		data = trimTrailingIncompleteUTF8(data[:maxBytes])
	}
	return string(data), nil
}

// FSStat inspects a path within the os.Root-confined workspace.
func (h *Host) FSStat(rawPath string) (FSStatResult, error) {
	patterns, err := h.allowedFSReadPatterns()
	if err != nil {
		return FSStatResult{}, err
	}

	relPath, err := ValidateRelativePath(rawPath)
	if err != nil {
		return FSStatResult{}, err
	}
	if !MatchesAny(patterns, relPath) {
		return FSStatResult{}, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("path %q is outside capabilities.fs.read", rawPath),
		}
	}

	resolvedRel, _, err := resolveWithinRoot(h.root, relPath)
	if err != nil {
		return FSStatResult{}, err
	}
	if !MatchesAny(patterns, resolvedRel) {
		return FSStatResult{}, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("resolved path %q is outside capabilities.fs.read", resolvedRel),
		}
	}

	info, statErr := h.root.Stat(resolvedRel)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return FSStatResult{Exists: false}, nil
		}
		return FSStatResult{}, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("stat %q failed: %v", rawPath, statErr),
		}
	}

	return FSStatResult{
		Exists:  true,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

// FSGlob returns sorted slash-normalized matching file paths confined to os.Root
// and capabilities.fs.read.
func (h *Host) FSGlob(rawPattern string, opts FSGlobOptions) ([]string, error) {
	patterns, err := h.allowedFSReadPatterns()
	if err != nil {
		return nil, err
	}

	cleanPattern, err := ValidateGlobPattern(rawPattern)
	if err != nil {
		return nil, err
	}
	if !GlobIntersectsAny(patterns, cleanPattern) {
		return nil, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("glob pattern %q is outside capabilities.fs.read", rawPattern),
		}
	}

	if literalPrefix, restSegs := splitLiteralGlobPrefix(cleanPattern); literalPrefix != "" {
		resolvedPrefix, _, resErr := resolveWithinRoot(h.root, literalPrefix)
		if resErr != nil {
			return nil, resErr
		}
		if resolvedPrefix != literalPrefix {
			rewritten := rewriteGlobPattern(resolvedPrefix, restSegs)
			if !GlobIntersectsAny(patterns, rewritten) {
				return nil, &contract.ToolError{
					Code:    contract.ErrCapabilityDenied,
					Message: fmt.Sprintf("resolved glob target %q is outside capabilities.fs.read", rewritten),
				}
			}
		}
	}

	maxResults := DefaultGlobMaxResults
	if opts.HasMax {
		if opts.MaxResults <= 0 {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("max_results must be positive, got %d", opts.MaxResults),
			}
		}
		if opts.MaxResults > MaxGlobMaxResults {
			return nil, &contract.ToolError{
				Code:    contract.ErrLimitExceeded,
				Message: fmt.Sprintf("max_results %d exceeds maximum %d", opts.MaxResults, MaxGlobMaxResults),
			}
		}
		maxResults = opts.MaxResults
	}

	cleanIgnores := make([]string, 0, len(opts.Ignore))
	for _, ig := range opts.Ignore {
		cleanIg, igErr := ValidateGlobPattern(ig)
		if igErr != nil {
			return nil, igErr
		}
		cleanIgnores = append(cleanIgnores, cleanIg)
	}

	results := make([]string, 0)
	walkErr := fs.WalkDir(h.root.FS(), ".", func(rel string, d fs.DirEntry, entryErr error) error {
		if entryErr != nil {
			//nolint:nilerr // continue walking past unreadable entries during glob traversal.
			return nil
		}
		if rel == "." {
			return nil
		}
		base := d.Name()
		if !opts.IncludeHidden && strings.HasPrefix(base, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if DirMayMatchIgnore(cleanIgnores, rel) {
				return fs.SkipDir
			}
			return nil
		}
		if !h.shouldIncludeGlobFile(rel, d, patterns, cleanIgnores, cleanPattern) {
			return nil
		}
		results = append(results, rel)
		if len(results) >= maxResults {
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("glob walk failed: %v", walkErr),
		}
	}

	sort.Strings(results)
	return results, nil
}

func (h *Host) shouldIncludeGlobFile(
	rel string,
	d fs.DirEntry,
	patterns []string,
	cleanIgnores []string,
	cleanPattern string,
) bool {
	if d.Type()&fs.ModeSymlink != 0 {
		resolvedRel, exists, resErr := resolveWithinRoot(h.root, rel)
		if resErr != nil || !exists || !MatchesAny(patterns, resolvedRel) {
			return false
		}
		info, statErr := h.root.Stat(rel)
		if statErr != nil || info.IsDir() {
			return false
		}
	}
	if MatchesAny(cleanIgnores, rel) || !MatchesAny(patterns, rel) {
		return false
	}
	matched, matchErr := MatchGlob(cleanPattern, rel)
	return matchErr == nil && matched
}

// resolveWithinRoot walks relPath component-by-component via root.Lstat and
// root.Readlink so symlinks escaping root or pointing outside capabilities.fs.read
// are rejected before opening.
func resolveWithinRoot(root *os.Root, relPath string) (string, bool, error) {
	if relPath == "." {
		if _, err := root.Stat("."); err != nil {
			return ".", false, err
		}
		return ".", true, nil
	}

	queue := strings.Split(relPath, "/")
	resolved := make([]string, 0, len(queue))
	hops := 0

	for len(queue) > 0 {
		seg := queue[0]
		queue = queue[1:]
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." {
			if len(resolved) == 0 {
				return "", false, &contract.ToolError{
					Code:    contract.ErrCapabilityDenied,
					Message: fmt.Sprintf("symlink traversal in %q escapes workspace root", relPath),
				}
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}

		candidate := seg
		if len(resolved) > 0 {
			candidate = strings.Join(append(slicesClone(resolved), seg), "/")
		}
		info, err := root.Lstat(candidate)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				remaining := append(slicesClone(resolved), seg)
				for _, rem := range queue {
					if rem == "" || rem == "." {
						continue
					}
					if rem == ".." {
						if len(remaining) == 0 {
							return "", false, &contract.ToolError{
								Code:    contract.ErrCapabilityDenied,
								Message: fmt.Sprintf("symlink traversal in %q escapes workspace root", relPath),
							}
						}
						remaining = remaining[:len(remaining)-1]
						continue
					}
					remaining = append(remaining, rem)
				}
				if len(remaining) == 0 {
					return ".", false, nil
				}
				return strings.Join(remaining, "/"), false, nil
			}
			return "", false, &contract.ToolError{
				Code:    contract.ErrCapabilityDenied,
				Message: fmt.Sprintf("lstat %q failed: %v", candidate, err),
			}
		}

		if info.Mode()&os.ModeSymlink != 0 {
			hops++
			if hops > maxSymlinkHops {
				return "", false, &contract.ToolError{
					Code:    contract.ErrCapabilityDenied,
					Message: fmt.Sprintf("too many levels of symbolic links resolving %q", relPath),
				}
			}
			target, readErr := root.Readlink(candidate)
			if readErr != nil {
				return "", false, &contract.ToolError{
					Code:    contract.ErrCapabilityDenied,
					Message: fmt.Sprintf("readlink %q failed: %v", candidate, readErr),
				}
			}
			if target == "" || filepath.IsAbs(target) || strings.HasPrefix(target, "/") || strings.ContainsRune(target, '\\') {
				return "", false, &contract.ToolError{
					Code:    contract.ErrCapabilityDenied,
					Message: fmt.Sprintf("symlink %q points outside workspace root", candidate),
				}
			}
			targetSegs := strings.Split(target, "/")
			nextQueue := make([]string, 0, len(targetSegs)+len(queue))
			nextQueue = append(nextQueue, targetSegs...)
			nextQueue = append(nextQueue, queue...)
			queue = nextQueue
			continue
		}

		resolved = append(resolved, seg)
	}

	if len(resolved) == 0 {
		return ".", true, nil
	}
	return strings.Join(resolved, "/"), true, nil
}

func slicesClone(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func splitLiteralGlobPrefix(cleanPattern string) (string, []string) {
	if cleanPattern == "" || cleanPattern == "." {
		return "", nil
	}
	segs := strings.Split(cleanPattern, "/")
	var literal []string
	var idx int
	for idx = 0; idx < len(segs); idx++ {
		seg := segs[idx]
		if seg == "" || seg == "." || seg == "**" || strings.ContainsAny(seg, `*?[\`) {
			break
		}
		literal = append(literal, seg)
	}
	if len(literal) == 0 {
		return "", segs
	}
	return strings.Join(literal, "/"), segs[idx:]
}

func rewriteGlobPattern(resolvedPrefix string, restSegs []string) string {
	if len(restSegs) == 0 {
		return resolvedPrefix
	}
	if resolvedPrefix == "." || resolvedPrefix == "" {
		return strings.Join(restSegs, "/")
	}
	return resolvedPrefix + "/" + strings.Join(restSegs, "/")
}

func trimTrailingIncompleteUTF8(b []byte) []byte {
	// Only trim up to 3 trailing bytes if they form a truncated multi-byte rune at EOF.
	for cut := 0; cut < 3 && len(b) > 0; cut++ {
		if utf8.Valid(b) {
			return b
		}
		last := b[len(b)-1]
		if utf8.RuneStart(last) && last >= utf8.RuneSelf {
			return b[:len(b)-1]
		}
		if (last & 0xC0) != 0x80 {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}
