package registry_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/registry"
)

func assertErrCode(t *testing.T, err error, want contract.ErrorCode) {
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

func TestLoaderExamplesAndErrors(t *testing.T) {
	examplesDir := filepath.Join("..", "..", "examples")
	loader := &registry.Loader{ToolsDir: examplesDir}

	// Load by name
	repoBundle, err := loader.Load("repo-context")
	if err != nil {
		t.Fatalf("Load(repo-context): %v", err)
	}
	if repoBundle.Name != "repo-context" || repoBundle.Version != 1 || repoBundle.Source == "" {
		t.Fatalf("unexpected bundle: %+v", repoBundle)
	}

	// Load by direct directory path
	vpsBundle, err := loader.Load(filepath.Join(examplesDir, "vps-health"))
	if err != nil {
		t.Fatalf("Load(direct vps-health path): %v", err)
	}
	if vpsBundle.Name != "vps-health" || vpsBundle.Version != 1 {
		t.Fatalf("unexpected vps-health bundle: %+v", vpsBundle)
	}

	// Missing tool
	_, err = loader.Load("nonexistent-tool")
	assertErrCode(t, err, contract.ErrToolNotFound)

	// Path traversal rejected by LoadByName
	_, err = loader.LoadByName("../repo-context")
	assertErrCode(t, err, contract.ErrToolNotFound)
	_, err = loader.LoadByName("repo-context/../../examples")
	assertErrCode(t, err, contract.ErrToolNotFound)

	// Invalid manifest inside bundle dir
	badDir := t.TempDir()
	toolDir := filepath.Join(badDir, "broken-tool")
	if mkErr := os.MkdirAll(toolDir, 0o755); mkErr != nil {
		t.Fatalf("mkdir: %v", mkErr)
	}
	if writeErr := os.WriteFile(filepath.Join(toolDir, "manifest.json"), []byte(`{"name":"broken-tool"}`), 0o600); writeErr != nil {
		t.Fatalf("write bad manifest: %v", writeErr)
	}
	badLoader := &registry.Loader{ToolsDir: badDir}
	_, err = badLoader.Load("broken-tool")
	assertErrCode(t, err, contract.ErrInvalidManifest)

	// Missing tool.js inside valid manifest dir
	validManifest := `{
		"schema_version": "1",
		"name": "missing-js",
		"version": 1,
		"description": "Valid manifest missing tool.js",
		"read_only": true,
		"input_schema": {"type": "object"},
		"output_schema": {"type": "object"},
		"capabilities": {}
	}`
	noJSDir := filepath.Join(badDir, "missing-js")
	if mkErr := os.MkdirAll(noJSDir, 0o755); mkErr != nil {
		t.Fatalf("mkdir: %v", mkErr)
	}
	if writeErr := os.WriteFile(filepath.Join(noJSDir, "manifest.json"), []byte(validManifest), 0o600); writeErr != nil {
		t.Fatalf("write manifest: %v", writeErr)
	}
	_, err = badLoader.Load("missing-js")
	assertErrCode(t, err, contract.ErrInvalidManifest)
}
