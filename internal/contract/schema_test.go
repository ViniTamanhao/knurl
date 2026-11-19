package contract_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

func loadExampleManifest(t *testing.T, name string) contract.Manifest {
	t.Helper()
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "examples", name, "manifest.json"))
	if err != nil {
		t.Fatalf("read example manifest %s: %v", name, err)
	}
	m, valErr := contract.ValidateManifestJSON(raw)
	if valErr != nil {
		t.Fatalf("ValidateManifestJSON(%s): %v", name, valErr)
	}
	return m
}

func assertToolErrorCode(t *testing.T, err error, wantCode contract.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", wantCode)
	}
	var toolErr *contract.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected *contract.ToolError, got %T: %v", err, err)
	}
	if toolErr.Code != wantCode {
		t.Fatalf("error code = %s, want %s (message=%q)", toolErr.Code, wantCode, toolErr.Message)
	}
}

func TestValidateManifestJSON(t *testing.T) {
	repoManifest := loadExampleManifest(t, "repo-context")
	if repoManifest.Name != "repo-context" {
		t.Fatalf("manifest.Name = %q, want repo-context", repoManifest.Name)
	}
	_ = loadExampleManifest(t, "vps-health")

	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "invalid json",
			raw:  `{bad`,
		},
		{
			name: "trailing json token",
			raw:  `{"schema_version":"1","name":"sample","version":1,"description":"Valid description text","read_only":true,"input_schema":{"type":"object"},"output_schema":{"type":"object"},"capabilities":{}} {}`,
		},
		{
			name: "unknown top-level property",
			raw:  `{"schema_version":"1","name":"sample","version":1,"description":"Valid description text","read_only":true,"elevated":false,"input_schema":{"type":"object"},"output_schema":{"type":"object"},"capabilities":{}}`,
		},
		{
			name: "semantic violation undeclared secret",
			raw: `{
				"schema_version": "1",
				"name": "sample",
				"version": 1,
				"description": "Valid description text",
				"read_only": true,
				"input_schema": {"type": "object"},
				"output_schema": {"type": "object"},
				"capabilities": {
					"exec": {
						"templates": [{
							"id": "echo-secret",
							"program": "echo",
							"args": ["ok", "${secret:MISSING}"],
							"read_only": true,
							"description": "Echo with missing secret"
						}]
					}
				}
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := contract.ValidateManifestJSON([]byte(tc.raw))
			assertToolErrorCode(t, err, contract.ErrInvalidManifest)
		})
	}
}

func TestValidateInput(t *testing.T) {
	repoManifest := loadExampleManifest(t, "repo-context")
	vpsManifest := loadExampleManifest(t, "vps-health")

	if err := contract.ValidateInput(repoManifest, nil); err != nil {
		t.Fatalf("ValidateInput(repo-context, nil) failed: %v", err)
	}
	if err := contract.ValidateInput(repoManifest, map[string]any{
		"include_tree":   true,
		"max_tree_files": 50,
		"recent_commits": 5,
	}); err != nil {
		t.Fatalf("ValidateInput(repo-context, valid) failed: %v", err)
	}

	// Out-of-range integer
	err := contract.ValidateInput(repoManifest, map[string]any{"recent_commits": 500})
	assertToolErrorCode(t, err, contract.ErrInvalidInput)

	// Fractional float for integer property
	err = contract.ValidateInput(repoManifest, map[string]any{"max_tree_files": 12.5})
	assertToolErrorCode(t, err, contract.ErrInvalidInput)

	// Missing required fields for vps-health
	err = contract.ValidateInput(vpsManifest, map[string]any{"host": "prod.example.com"})
	assertToolErrorCode(t, err, contract.ErrInvalidInput)

	// Flag-injection hostname rejected
	err = contract.ValidateInput(vpsManifest, map[string]any{
		"host": "-oProxyCommand=id",
		"user": "ops",
	})
	assertToolErrorCode(t, err, contract.ErrInvalidInput)

	// Valid vps-health input
	if vpsErr := contract.ValidateInput(vpsManifest, map[string]any{
		"host": "prod.example.com",
		"user": "ops",
		"port": 2222,
	}); vpsErr != nil {
		t.Fatalf("ValidateInput(vps-health, valid) failed: %v", vpsErr)
	}
}

func TestValidateOutput(t *testing.T) {
	repoManifest := loadExampleManifest(t, "repo-context")

	assertToolErrorCode(t, contract.ValidateOutput(repoManifest, nil), contract.ErrInvalidOutput)

	assertToolErrorCode(t, contract.ValidateOutput(repoManifest, map[string]any{
		"is_git_repo": false,
	}), contract.ErrInvalidOutput)

	validOut := map[string]any{
		"is_git_repo":     false,
		"file_count":      1,
		"top_directories": []string{},
		"manifests":       []string{},
		"languages":       []any{},
		"git":             nil,
		"tree_files":      []string{"README.md"},
		"readme_excerpt":  "# Hi",
	}
	if err := contract.ValidateOutput(repoManifest, validOut); err != nil {
		t.Fatalf("ValidateOutput(valid) failed: %v", err)
	}

	validOut["extra"] = "disallowed"
	assertToolErrorCode(t, contract.ValidateOutput(repoManifest, validOut), contract.ErrInvalidOutput)
}
