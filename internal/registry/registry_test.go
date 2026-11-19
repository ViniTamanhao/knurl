package registry_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/registry"
)

func assertRegistryErrCode(t *testing.T, err error, want contract.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s, got nil", want)
	}
	var toolErr *contract.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected *contract.ToolError, got %T: %v", err, err)
	}
	if toolErr.Code != want {
		t.Fatalf("error code = %s, want %s (msg=%q)", toolErr.Code, want, toolErr.Message)
	}
}

func writeVersionedTool(t *testing.T, toolsRoot, name string, version int, desc string) string {
	t.Helper()
	verDir := filepath.Join(toolsRoot, name, fmt.Sprintf("v%d", version))
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", verDir, err)
	}
	manifest := fmt.Sprintf(`{
  "schema_version": "1",
  "name": %q,
  "version": %d,
  "description": %q,
  "tags": ["test"],
  "read_only": true,
  "input_schema": {"type": "object"},
  "output_schema": {"type": "object"},
  "capabilities": {}
}`, name, version, desc)
	if err := os.WriteFile(filepath.Join(verDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	source := fmt.Sprintf(`function main(input) { return { version: %d }; }`, version)
	if err := os.WriteFile(filepath.Join(verDir, "tool.js"), []byte(source), 0o600); err != nil {
		t.Fatalf("write tool.js: %v", err)
	}
	return verDir
}

func writeActivePointer(t *testing.T, toolsRoot, name string, active, previous int) {
	t.Helper()
	toolDir := filepath.Join(toolsRoot, name)
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", toolDir, err)
	}
	content := fmt.Sprintf(`{"active_version": %d, "previous_version": %d, "updated_at": "2026-11-19T12:00:00Z"}`, active, previous)
	if err := os.WriteFile(filepath.Join(toolDir, "active.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("write active.json: %v", err)
	}
}

func TestValidateToolNameRejectsInvalidAndTraversal(t *testing.T) {
	valid := []string{"ab", "repo-context", "pr-ready", "tool-2"}
	for _, name := range valid {
		if err := registry.ValidateToolName(name); err != nil {
			t.Errorf("ValidateToolName(%q) unexpected error: %v", name, err)
		}
	}

	invalid := []string{
		"",
		"a",
		".",
		"..",
		"../evil",
		"../../etc/passwd",
		"/etc/passwd",
		`foo\bar`,
		"foo/bar",
		"Upper-Case",
		"snake_case",
		"-leading-dash",
		"trailing-dash-",
		"double--dash",
		"bad\x00name",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 65 chars
	}
	for _, name := range invalid {
		err := registry.ValidateToolName(name)
		assertRegistryErrCode(t, err, contract.ErrToolNotFound)
	}
}

func TestDiscoverRepoToolsDirAndGitBoundary(t *testing.T) {
	outer := t.TempDir()
	outerKnurl := filepath.Join(outer, ".knurl", "tools")
	if err := os.MkdirAll(outerKnurl, 0o755); err != nil {
		t.Fatalf("mkdir outer knurl: %v", err)
	}

	// Repo with .git directory inside outer: must stop at .git and NOT pick outer/.knurl/tools.
	repoWithGitDir := filepath.Join(outer, "repo-dir")
	nestedInDir := filepath.Join(repoWithGitDir, "a", "b")
	if err := os.MkdirAll(filepath.Join(repoWithGitDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.MkdirAll(nestedInDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	foundDir, found, err := registry.DiscoverRepoToolsDir(nestedInDir)
	if err != nil {
		t.Fatalf("DiscoverRepoToolsDir(git dir boundary): %v", err)
	}
	if found || foundDir != "" {
		t.Fatalf("expected discovery to stop at .git directory, got (%q, %v)", foundDir, found)
	}

	// Repo with .git FILE (Git worktree/submodule): must also stop at .git!
	repoWithGitFile := filepath.Join(outer, "repo-worktree")
	nestedInWorktree := filepath.Join(repoWithGitFile, "sub", "pkg")
	if mkErr := os.MkdirAll(nestedInWorktree, 0o755); mkErr != nil {
		t.Fatalf("mkdir worktree nested: %v", mkErr)
	}
	if writeErr := os.WriteFile(filepath.Join(repoWithGitFile, ".git"), []byte("gitdir: ../.git/worktrees/wt\n"), 0o600); writeErr != nil {
		t.Fatalf("write .git file: %v", writeErr)
	}
	foundDir, found, err = registry.DiscoverRepoToolsDir(nestedInWorktree)
	if err != nil {
		t.Fatalf("DiscoverRepoToolsDir(git file boundary): %v", err)
	}
	if found || foundDir != "" {
		t.Fatalf("expected discovery to stop at .git file, got (%q, %v)", foundDir, found)
	}

	// Repo containing .knurl/tools at repo root alongside .git should discover it from nested subdir.
	repoKnurl := filepath.Join(repoWithGitDir, ".knurl", "tools")
	if mkErr := os.MkdirAll(repoKnurl, 0o755); mkErr != nil {
		t.Fatalf("mkdir repo knurl: %v", mkErr)
	}
	canonicalRepoKnurl, evalErr := filepath.EvalSymlinks(repoKnurl)
	if evalErr != nil {
		t.Fatalf("eval symlinks: %v", evalErr)
	}
	foundDir, found, err = registry.DiscoverRepoToolsDir(nestedInDir)
	if err != nil {
		t.Fatalf("DiscoverRepoToolsDir(found): %v", err)
	}
	if !found || foundDir != canonicalRepoKnurl {
		t.Fatalf("DiscoverRepoToolsDir = (%q, %v), want (%q, true)", foundDir, found, canonicalRepoKnurl)
	}
}

func TestResolveGlobalToolsDirPrecedence(t *testing.T) {
	makeEnv := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := m[k]
			return v, ok
		}
	}

	dir, err := registry.ResolveGlobalToolsDir(makeEnv(map[string]string{
		"KNURL_HOME":       "/knurl-home",
		"KNURL_CONFIG_DIR": "/knurl-cfg",
		"XDG_CONFIG_HOME":  "/xdg-cfg",
		"HOME":             "/home/user",
	}))
	if err != nil || dir != filepath.Clean("/knurl-home/tools") {
		t.Fatalf("KNURL_HOME precedence got (%q, %v)", dir, err)
	}

	dir, err = registry.ResolveGlobalToolsDir(makeEnv(map[string]string{
		"KNURL_CONFIG_DIR": "/knurl-cfg",
		"XDG_CONFIG_HOME":  "/xdg-cfg",
		"HOME":             "/home/user",
	}))
	if err != nil || dir != filepath.Clean("/knurl-cfg/tools") {
		t.Fatalf("KNURL_CONFIG_DIR precedence got (%q, %v)", dir, err)
	}

	dir, err = registry.ResolveGlobalToolsDir(makeEnv(map[string]string{
		"XDG_CONFIG_HOME": "/xdg-cfg",
		"HOME":            "/home/user",
	}))
	if err != nil || dir != filepath.Clean("/xdg-cfg/knurl/tools") {
		t.Fatalf("XDG_CONFIG_HOME precedence got (%q, %v)", dir, err)
	}

	dir, err = registry.ResolveGlobalToolsDir(makeEnv(map[string]string{
		"HOME": "/home/user",
	}))
	if err != nil || dir != filepath.Clean("/home/user/.config/knurl/tools") {
		t.Fatalf("HOME fallback got (%q, %v)", dir, err)
	}
}

func TestTwoTierResolutionVersioningAndList(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "repo")
	workDir := filepath.Join(repoRoot, "pkg", "sub")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}

	repoTools := filepath.Join(repoRoot, ".knurl", "tools")
	globalHome := filepath.Join(root, "global-home")
	globalTools := filepath.Join(globalHome, "tools")

	// Shared tool exists in both repo (v1, v2 with active_version=1 rollback) and global (v1).
	v1Dir := writeVersionedTool(t, repoTools, "shared-tool", 1, "Repo shared tool version 1")
	writeVersionedTool(t, repoTools, "shared-tool", 2, "Repo shared tool version 2")
	writeActivePointer(t, repoTools, "shared-tool", 1, 2)
	approvalJSON := `{"proposal_id":"prop_01","approved_at":"2026-11-19T12:00:00Z","bundle_sha256":"abc","summary":"init","capability_diff":{"elevated_before":false,"elevated_after":false,"has_escalation":false,"changes":[]}}`
	if err := os.WriteFile(filepath.Join(v1Dir, "approval.json"), []byte(approvalJSON), 0o600); err != nil {
		t.Fatalf("write approval.json: %v", err)
	}

	writeVersionedTool(t, globalTools, "shared-tool", 1, "Global shared tool version 1")
	writeActivePointer(t, globalTools, "shared-tool", 1, 0)

	// Global-only tool without active.json: resolves latest v<N> (v2).
	writeVersionedTool(t, globalTools, "global-only", 1, "Global only tool version 1")
	writeVersionedTool(t, globalTools, "global-only", 2, "Global only tool version 2")

	// Repo-only tool with active_version = 2 and previous_version = 1.
	writeVersionedTool(t, repoTools, "repo-only", 1, "Repo only tool version 1")
	writeVersionedTool(t, repoTools, "repo-only", 2, "Repo only tool version 2")
	writeActivePointer(t, repoTools, "repo-only", 2, 1)

	reg := &registry.Registry{
		WorkDir: workDir,
		EnvLookup: func(k string) (string, bool) {
			if k == "KNURL_HOME" {
				return globalHome, true
			}
			return "", false
		},
	}

	// Auto resolves repo-local shadowing global, honoring active.json rollback pointer (v1).
	b, err := reg.Resolve("shared-tool", contract.ScopeAuto)
	if err != nil {
		t.Fatalf("Resolve(shared-tool, auto): %v", err)
	}
	if b.Scope != contract.ScopeRepo || b.Version != 1 || b.ActiveVersion != 1 {
		t.Fatalf("unexpected resolved bundle: scope=%s ver=%d active=%d", b.Scope, b.Version, b.ActiveVersion)
	}
	if !slices.Equal(b.AvailableVersions, []int{1, 2}) {
		t.Fatalf("AvailableVersions = %v, want [1 2]", b.AvailableVersions)
	}
	if b.Approval == nil || b.Approval.ProposalID != "prop_01" {
		t.Fatalf("expected parsed approval record, got %+v", b.Approval)
	}

	// Repo-only tool resolves active_version=2.
	rOnly, err := reg.Resolve("repo-only", contract.ScopeAuto)
	if err != nil {
		t.Fatalf("Resolve(repo-only, auto): %v", err)
	}
	if rOnly.Scope != contract.ScopeRepo || rOnly.Version != 2 || rOnly.ActiveVersion != 2 {
		t.Fatalf("unexpected repo-only bundle: scope=%s ver=%d active=%d", rOnly.Scope, rOnly.Version, rOnly.ActiveVersion)
	}

	// Explicit LoadVersion v2 on shared-tool succeeds even though active_version is 1.
	bV2, err := reg.LoadVersion("shared-tool", 2, contract.ScopeAuto)
	if err != nil {
		t.Fatalf("LoadVersion(shared-tool, 2): %v", err)
	}
	if bV2.Version != 2 || bV2.ActiveVersion != 1 || bV2.Scope != contract.ScopeRepo {
		t.Fatalf("unexpected v2 bundle: ver=%d active=%d scope=%s", bV2.Version, bV2.ActiveVersion, bV2.Scope)
	}

	// Explicit global scope accesses global copy despite repo shadow.
	bGlobal, err := reg.Resolve("shared-tool", contract.ScopeGlobal)
	if err != nil {
		t.Fatalf("Resolve(shared-tool, global): %v", err)
	}
	if bGlobal.Scope != contract.ScopeGlobal || bGlobal.Manifest.Description != "Global shared tool version 1" {
		t.Fatalf("unexpected global bundle: %+v", bGlobal)
	}

	// Global-only tool resolves latest v2 when active.json is absent; absent in repo scope.
	gOnly, err := reg.Resolve("global-only", contract.ScopeAuto)
	if err != nil {
		t.Fatalf("Resolve(global-only, auto): %v", err)
	}
	if gOnly.Scope != contract.ScopeGlobal || gOnly.Version != 2 {
		t.Fatalf("unexpected global-only: scope=%s ver=%d", gOnly.Scope, gOnly.Version)
	}
	_, err = reg.Resolve("global-only", contract.ScopeRepo)
	assertRegistryErrCode(t, err, contract.ErrToolNotFound)

	// Missing version returns VERSION_NOT_FOUND.
	_, err = reg.LoadVersion("global-only", 99, contract.ScopeAuto)
	assertRegistryErrCode(t, err, contract.ErrVersionNotFound)

	// List(ScopeAuto): repo shadows global (3 distinct tools).
	autoList, err := reg.List(contract.ScopeAuto)
	if err != nil {
		t.Fatalf("List(auto): %v", err)
	}
	if len(autoList) != 3 {
		t.Fatalf("List(auto) count = %d, want 3", len(autoList))
	}
	for _, entry := range autoList {
		if entry.Name == "shared-tool" && entry.Scope != contract.ScopeRepo {
			t.Fatalf("expected repo shared-tool in auto list, got %s", entry.Scope)
		}
	}

	// List(ScopeAll): includes both repo and global entries for shared-tool (4 entries total).
	allList, err := reg.List(contract.ScopeAll)
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(allList) != 4 {
		t.Fatalf("List(all) count = %d, want 4", len(allList))
	}

	// Missing tier dir returns empty slice, nil.
	emptyReg := &registry.Registry{WorkDir: t.TempDir(), GlobalDir: filepath.Join(root, "does-not-exist")}
	emptyList, err := emptyReg.List(contract.ScopeAll)
	if err != nil || len(emptyList) != 0 {
		t.Fatalf("List on missing dirs = (%v, %v), want empty slice nil", emptyList, err)
	}
}

func TestRegistryFailClosedCasesAndOverride(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "repo")
	repoTools := filepath.Join(repoRoot, ".knurl", "tools")
	globalTools := filepath.Join(root, "global", "tools")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	// 1. Corrupt repo-local active.json must fail closed and NEVER fall back to valid global tool.
	writeVersionedTool(t, globalTools, "corrupt-tool", 1, "Valid global fallback that must not be reached")
	writeVersionedTool(t, repoTools, "corrupt-tool", 1, "Corrupt repo tool version 1")
	if err := os.WriteFile(filepath.Join(repoTools, "corrupt-tool", "active.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write corrupt active.json: %v", err)
	}

	reg := &registry.Registry{WorkDir: repoRoot, GlobalDir: globalTools}
	_, err := reg.Resolve("corrupt-tool", contract.ScopeAuto)
	assertRegistryErrCode(t, err, contract.ErrInvalidManifest)

	// 2. Manifest version mismatch against v<N> directory fails closed.
	writeVersionedTool(t, repoTools, "mismatch-ver", 2, "Mismatched version manifest")
	if mkErr := os.Rename(filepath.Join(repoTools, "mismatch-ver", "v2"), filepath.Join(repoTools, "mismatch-ver", "v1")); mkErr != nil {
		t.Fatalf("rename dir: %v", mkErr)
	}
	_, err = reg.Resolve("mismatch-ver", contract.ScopeRepo)
	assertRegistryErrCode(t, err, contract.ErrInvalidManifest)

	// 3. Symlinked tool.js escaping outside bundle directory fails closed.
	outsideSecret := filepath.Join(root, "outside.js")
	if writeErr := os.WriteFile(outsideSecret, []byte("function main(){return {};}"), 0o600); writeErr != nil {
		t.Fatalf("write outside.js: %v", writeErr)
	}
	symVerDir := writeVersionedTool(t, repoTools, "symlink-tool", 1, "Symlink escape test bundle")
	if rmErr := os.Remove(filepath.Join(symVerDir, "tool.js")); rmErr != nil {
		t.Fatalf("remove tool.js: %v", rmErr)
	}
	if symErr := os.Symlink(outsideSecret, filepath.Join(symVerDir, "tool.js")); symErr != nil {
		t.Fatalf("symlink tool.js: %v", symErr)
	}
	_, err = reg.Resolve("symlink-tool", contract.ScopeRepo)
	assertRegistryErrCode(t, err, contract.ErrInvalidManifest)

	// 4. Symlinked v1 directory fails closed.
	symDirTarget := filepath.Join(root, "external-v1")
	if mkErr := os.MkdirAll(symDirTarget, 0o755); mkErr != nil {
		t.Fatalf("mkdir symDirTarget: %v", mkErr)
	}
	symVerToolDir := filepath.Join(repoTools, "symlink-ver")
	if mkErr := os.MkdirAll(symVerToolDir, 0o755); mkErr != nil {
		t.Fatalf("mkdir symlink-ver: %v", mkErr)
	}
	if symErr := os.Symlink(symDirTarget, filepath.Join(symVerToolDir, "v1")); symErr != nil {
		t.Fatalf("symlink v1: %v", symErr)
	}
	writeActivePointer(t, repoTools, "symlink-ver", 1, 0)
	_, err = reg.Resolve("symlink-ver", contract.ScopeRepo)
	assertRegistryErrCode(t, err, contract.ErrInvalidManifest)

	// 5. Path traversal tool name fails closed.
	_, err = reg.Resolve("../../etc/passwd", contract.ScopeAuto)
	assertRegistryErrCode(t, err, contract.ErrToolNotFound)

	// 6. Override directory + direct flat bundle loading (examples/).
	examplesDir := filepath.Join("..", "..", "examples")
	overrideReg := &registry.Registry{WorkDir: repoRoot, OverrideDir: examplesDir}
	ex, err := overrideReg.Resolve("repo-context", contract.ScopeAuto)
	if err != nil {
		t.Fatalf("Resolve(repo-context via OverrideDir): %v", err)
	}
	if ex.Scope != contract.ScopeOverride || ex.Version != 1 {
		t.Fatalf("unexpected override bundle: scope=%s ver=%d", ex.Scope, ex.Version)
	}
	// OverrideDir isolates from global tier.
	_, err = overrideReg.Resolve("corrupt-tool", contract.ScopeAuto)
	assertRegistryErrCode(t, err, contract.ErrToolNotFound)

	direct, err := reg.LoadTarget(filepath.Join(examplesDir, "vps-health"), contract.ScopeAuto, 0)
	if err != nil {
		t.Fatalf("LoadTarget(direct path): %v", err)
	}
	if direct.Scope != contract.ScopeOverride || direct.Name != "vps-health" {
		t.Fatalf("unexpected direct bundle: %+v", direct)
	}
}
