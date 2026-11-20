package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

const (
	// DefaultGitLogLimit is the default commit count returned by knurl.git.log.
	DefaultGitLogLimit = 20
	// MaxGitLogLimit is the maximum commit count allowed by knurl.git.log.
	MaxGitLogLimit = 100
	// MaxGitDiffPatchBytes is the maximum patch text retained by knurl.git.diff.
	MaxGitDiffPatchBytes = 262144

	maxGitRefLength = 256
)

var hardenedGitGlobalArgs = []string{
	"--no-pager",
	"-c", "core.fsmonitor=false",
	"-c", "core.quotepath=false",
	"-c", "protocol.ext.allow=never",
}

// GitLogOptions configures knurl.git.log.
type GitLogOptions struct {
	Limit    int
	HasLimit bool
	Ref      string
	Path     string
}

// GitDiffOptions configures knurl.git.diff.
type GitDiffOptions struct {
	Base     string
	Head     string
	Staged   bool
	StatOnly bool
	Path     string
}

func (h *Host) ensureGitRepo(ctx context.Context) error {
	if h.manifest.Capabilities.Git == nil || !h.manifest.Capabilities.Git.Read {
		return &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: "capabilities.git.read is not granted",
		}
	}
	if h.mocks != nil {
		if h.mocks.Git == nil {
			return mockMissing("git")
		}
		if h.mocks.Git.Error != "" {
			return &contract.ToolError{Code: h.mocks.Git.Error, Message: "mock git error"}
		}
		return nil
	}
	out, code, err := h.runGitRaw(ctx, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return err
	}
	if code != 0 || strings.TrimSpace(string(out)) != "true" {
		return &contract.ToolError{
			Code:    contract.ErrNotAGitRepo,
			Message: "working directory is not inside a git worktree",
		}
	}
	return nil
}

func (h *Host) buildGitCmd(ctx context.Context, args ...string) *exec.Cmd {
	baseEnv := SanitizedBaseEnv(h.envLookup)
	baseEnv = append(
		baseEnv,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"LC_ALL=C",
	)

	fullArgs := make([]string, 0, len(hardenedGitGlobalArgs)+len(args))
	fullArgs = append(fullArgs, hardenedGitGlobalArgs...)
	fullArgs = append(fullArgs, args...)

	//nolint:gosec // G204: fixed git binary invoked with host-validated read-only flags.
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Dir = h.workDir
	cmd.Env = baseEnv
	cmd.Stdin = bytes.NewReader(nil)
	ConfigureProcessGroup(cmd)
	return cmd
}

func (h *Host) runGitRaw(ctx context.Context, args ...string) ([]byte, int, error) {
	cmd := h.buildGitCmd(ctx, args...)

	stdout := NewLimitedBuffer(MaxExecStreamBytes)
	stderr := NewLimitedBuffer(MaxExecStreamBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), 0, nil
	}
	if ctx.Err() != nil {
		return nil, -1, &contract.ToolError{
			Code:    contract.ErrTimeout,
			Message: "git command timed out",
		}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), exitErr.ExitCode(), nil
	}
	return nil, -1, &contract.ToolError{
		Code:    contract.ErrExecFailed,
		Message: h.redactor.String(fmt.Sprintf("git execution failed: %v", err)),
	}
}

func (h *Host) runGitBounded(ctx context.Context, maxBytes int, args ...string) ([]byte, bool, int, string, error) {
	cmd := h.buildGitCmd(ctx, args...)

	stdout := NewLimitedBuffer(maxBytes)
	stderr := NewLimitedBuffer(MaxExecStreamBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), stdout.Truncated(), 0, h.redactor.String(stderr.String()), nil
	}
	if ctx.Err() != nil {
		return nil, false, -1, "", &contract.ToolError{
			Code:    contract.ErrTimeout,
			Message: "git command timed out",
		}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stdout.Truncated(), exitErr.ExitCode(), h.redactor.String(stderr.String()), nil
	}
	return nil, false, -1, "", &contract.ToolError{
		Code:    contract.ErrExecFailed,
		Message: h.redactor.String(fmt.Sprintf("git execution failed: %v", err)),
	}
}

func validateGitRef(ref string) error {
	if ref == "" {
		return nil
	}
	if len(ref) > maxGitRefLength {
		return &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("invalid git ref %q: exceeds %d byte limit", ref, maxGitRefLength),
		}
	}
	if strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, ":") {
		return &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("invalid git ref %q: must not begin with '-' or ':'", ref),
		}
	}
	if strings.ContainsRune(ref, '\\') {
		return &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("invalid git ref %q: must not contain backslashes", ref),
		}
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if c <= 0x20 || c == 0x7f {
			return &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("invalid git ref %q: contains whitespace or control characters", ref),
			}
		}
	}
	return nil
}

func validateGitPath(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if strings.HasPrefix(raw, "-") || strings.HasPrefix(raw, ":") {
		return "", &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("invalid git path %q: must not begin with '-' or ':'", raw),
		}
	}
	return ValidateRelativePath(raw)
}

// GitBranch returns current branch, detached state, HEAD commit SHA, and upstream tracking branch.
func (h *Host) GitBranch(ctx context.Context) (contract.GitBranch, error) {
	if err := h.ensureGitRepo(ctx); err != nil {
		return contract.GitBranch{}, err
	}

	var res contract.GitBranch
	if h.mocks != nil {
		if h.mocks.Git.Branch == nil {
			return res, mockMissing("git.branch")
		}
		return *h.mocks.Git.Branch, nil
	}

	branchOut, branchCode, err := h.runGitRaw(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return contract.GitBranch{}, err
	}
	if branchCode == 0 {
		res.Current = strings.TrimSpace(string(branchOut))
	} else {
		res.Detached = true
	}

	headOut, headCode, err := h.runGitRaw(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return contract.GitBranch{}, err
	}
	if headCode == 0 {
		res.Head = strings.TrimSpace(string(headOut))
		// Unborn HEAD is not considered detached when symbolic-ref succeeded.
	}

	if !res.Detached && res.Head != "" {
		upOut, upCode, upErr := h.runGitRaw(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
		if upErr != nil {
			return contract.GitBranch{}, upErr
		}
		if upCode == 0 {
			res.Upstream = strings.TrimSpace(string(upOut))
		}
	}

	return res, nil
}

// GitStatus returns structured staged, unstaged, and untracked repository changes.
func (h *Host) GitStatus(ctx context.Context) (contract.GitStatus, error) {
	if err := h.ensureGitRepo(ctx); err != nil {
		return contract.GitStatus{}, err
	}
	if h.mocks != nil {
		if h.mocks.Git.Status == nil {
			return contract.GitStatus{}, mockMissing("git.status")
		}
		return *h.mocks.Git.Status, nil
	}

	out, code, err := h.runGitRaw(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return contract.GitStatus{}, err
	}
	if code != 0 {
		return contract.GitStatus{}, &contract.ToolError{
			Code:    contract.ErrExecFailed,
			Message: fmt.Sprintf("git status failed with exit code %d", code),
		}
	}

	status := contract.GitStatus{
		Staged:    make([]contract.GitFileChange, 0),
		Unstaged:  make([]contract.GitFileChange, 0),
		Untracked: make([]string, 0),
	}

	tokens := bytes.Split(out, []byte{0})
	for i := 0; i < len(tokens); i++ {
		rec := string(tokens[i])
		if len(rec) < 4 {
			continue
		}
		x := rec[0]
		y := rec[1]
		filePath := rec[3:]

		if x == '?' && y == '?' {
			status.Untracked = append(status.Untracked, filePath)
			continue
		}

		if x != ' ' && x != '?' {
			status.Staged = append(status.Staged, contract.GitFileChange{
				Path:   filePath,
				Status: normalizeGitStatusCode(x, y),
			})
		}
		if y != ' ' && y != '?' {
			status.Unstaged = append(status.Unstaged, contract.GitFileChange{
				Path:   filePath,
				Status: normalizeGitStatusCode(y, x),
			})
		}

		// Porcelain v1 -z emits an extra NUL-delimited original path for renames/copies.
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			i++
		}
	}

	status.Clean = len(status.Staged) == 0 && len(status.Unstaged) == 0 && len(status.Untracked) == 0
	return status, nil
}

func normalizeGitStatusCode(primary, other byte) string {
	if primary == 'U' || other == 'U' || (primary == 'A' && other == 'A') || (primary == 'D' && other == 'D') {
		return "U"
	}
	switch primary {
	case 'M', 'A', 'D', 'R', 'C':
		return string(primary)
	case 'T':
		return "M"
	default:
		return "M"
	}
}

// GitLog returns recent commits formatted with RFC 3339 UTC timestamps.
func (h *Host) GitLog(ctx context.Context, opts GitLogOptions) ([]contract.GitCommit, error) {
	if err := h.ensureGitRepo(ctx); err != nil {
		return nil, err
	}

	limit := DefaultGitLogLimit
	if opts.HasLimit {
		if opts.Limit <= 0 {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("git log limit must be positive, got %d", opts.Limit),
			}
		}
		if opts.Limit > MaxGitLogLimit {
			return nil, &contract.ToolError{
				Code:    contract.ErrLimitExceeded,
				Message: fmt.Sprintf("git log limit %d exceeds maximum %d", opts.Limit, MaxGitLogLimit),
			}
		}
		limit = opts.Limit
	}

	if err := validateGitRef(opts.Ref); err != nil {
		return nil, err
	}
	cleanPath, pathErr := validateGitPath(opts.Path)
	if pathErr != nil {
		return nil, pathErr
	}

	if h.mocks != nil {
		return h.mocks.Git.Log[:min(limit, len(h.mocks.Git.Log))], nil
	}
	if opts.Ref == "" {
		_, headCode, headErr := h.runGitRaw(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
		if headErr != nil {
			return nil, headErr
		}
		if headCode != 0 {
			return make([]contract.GitCommit, 0), nil
		}
	}

	args := []string{
		"log",
		"-n", strconv.Itoa(limit),
		"--format=%H%x1f%h%x1f%an%x1f%cI%x1f%s%x1e",
	}
	if opts.Ref != "" {
		args = append(args, opts.Ref)
	}
	args = append(args, "--")
	if cleanPath != "" {
		args = append(args, cleanPath)
	}

	out, _, code, stderr, err := h.runGitBounded(ctx, MaxExecStreamBytes, args...)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: h.redactor.String(fmt.Sprintf("git log failed: %s", strings.TrimSpace(stderr))),
		}
	}

	records := bytes.Split(out, []byte{0x1e})
	commits := make([]contract.GitCommit, 0, len(records))
	for _, rec := range records {
		trimmed := strings.TrimSpace(string(rec))
		if trimmed == "" {
			continue
		}
		fields := strings.SplitN(trimmed, "\x1f", 5)
		if len(fields) != 5 {
			continue
		}
		dateUTC := fields[3]
		if parsed, parseErr := time.Parse(time.RFC3339, fields[3]); parseErr == nil {
			dateUTC = parsed.UTC().Format(time.RFC3339)
		}
		commits = append(commits, contract.GitCommit{
			Hash:      fields[0],
			ShortHash: fields[1],
			Author:    h.redactor.String(fields[2]),
			Date:      dateUTC,
			Subject:   h.redactor.String(fields[4]),
		})
	}
	return commits, nil
}

// GitDiff returns per-file diff statistics and optional bounded unified patch output.
func (h *Host) GitDiff(ctx context.Context, opts GitDiffOptions) (contract.GitDiff, error) {
	if err := h.ensureGitRepo(ctx); err != nil {
		return contract.GitDiff{}, err
	}
	if opts.Staged && (opts.Base != "" || opts.Head != "") {
		return contract.GitDiff{}, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "git diff cannot combine staged with base/head refs",
		}
	}
	if opts.Head != "" && opts.Base == "" {
		return contract.GitDiff{}, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "git diff head requires base",
		}
	}
	if err := validateGitRef(opts.Base); err != nil {
		return contract.GitDiff{}, err
	}
	if err := validateGitRef(opts.Head); err != nil {
		return contract.GitDiff{}, err
	}
	cleanPath, pathErr := validateGitPath(opts.Path)
	if pathErr != nil {
		return contract.GitDiff{}, pathErr
	}

	if h.mocks != nil {
		if h.mocks.Git.Diff == nil {
			return contract.GitDiff{}, mockMissing("git.diff")
		}
		return *h.mocks.Git.Diff, nil
	}
	targetArgs := make([]string, 0, 4)
	if opts.Staged {
		targetArgs = append(targetArgs, "--staged")
	} else if opts.Base != "" {
		targetArgs = append(targetArgs, opts.Base)
		if opts.Head != "" {
			targetArgs = append(targetArgs, opts.Head)
		}
	}

	numstatArgs := make([]string, 0, 7+len(targetArgs))
	numstatArgs = append(numstatArgs, "diff", "--no-ext-diff", "--no-textconv", "--numstat", "-z")
	numstatArgs = append(numstatArgs, targetArgs...)
	numstatArgs = append(numstatArgs, "--")
	if cleanPath != "" {
		numstatArgs = append(numstatArgs, cleanPath)
	}

	numOut, _, numCode, numStderr, err := h.runGitBounded(ctx, MaxExecStreamBytes, numstatArgs...)
	if err != nil {
		return contract.GitDiff{}, err
	}
	if numCode != 0 {
		return contract.GitDiff{}, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: h.redactor.String(fmt.Sprintf("git diff failed: %s", strings.TrimSpace(numStderr))),
		}
	}

	diff := contract.GitDiff{
		Files: parseGitNumstatZ(numOut),
	}

	if !opts.StatOnly {
		patchArgs := make([]string, 0, 5+len(targetArgs))
		patchArgs = append(patchArgs, "diff", "--no-ext-diff", "--no-textconv")
		patchArgs = append(patchArgs, targetArgs...)
		patchArgs = append(patchArgs, "--")
		if cleanPath != "" {
			patchArgs = append(patchArgs, cleanPath)
		}
		patchOut, truncated, patchCode, patchStderr, patchErr := h.runGitBounded(ctx, MaxGitDiffPatchBytes, patchArgs...)
		if patchErr != nil {
			return contract.GitDiff{}, patchErr
		}
		if patchCode != 0 {
			return contract.GitDiff{}, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: h.redactor.String(fmt.Sprintf("git diff patch failed: %s", strings.TrimSpace(patchStderr))),
			}
		}
		diff.Patch = h.redactor.String(string(patchOut))
		diff.Truncated = truncated
	}

	return diff, nil
}

func parseGitNumstatZ(out []byte) []contract.GitDiffFile {
	tokens := bytes.Split(out, []byte{0})
	files := make([]contract.GitDiffFile, 0)

	for i := 0; i < len(tokens); i++ {
		tok := string(tokens[i])
		if tok == "" {
			continue
		}
		parts := strings.SplitN(tok, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		filePath := parts[2]
		// In -z numstat mode, renames/copies put empty path here followed by old\0new\0.
		if filePath == "" {
			if i+2 < len(tokens) {
				filePath = string(tokens[i+2])
				i += 2
			} else {
				continue
			}
		}

		binary := parts[0] == "-" || parts[1] == "-"
		var adds, dels int
		if !binary {
			adds, _ = strconv.Atoi(parts[0])
			dels, _ = strconv.Atoi(parts[1])
		}
		files = append(files, contract.GitDiffFile{
			Path:      filePath,
			Additions: adds,
			Deletions: dels,
			Binary:    binary,
		})
	}
	return files
}
