package contract_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func compileSchemaFromBytes(t *testing.T, id string, data []byte) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unmarshal schema %s: %v", id, err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if addErr := c.AddResource(id, doc); addErr != nil {
		t.Fatalf("add schema resource %s: %v", id, addErr)
	}
	sch, compileErr := c.Compile(id)
	if compileErr != nil {
		t.Fatalf("compile schema %s: %v", id, compileErr)
	}
	return sch
}

func compileSchemaFile(t *testing.T, path string) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema %s: %v", path, err)
	}
	return compileSchemaFromBytes(t, "file://"+filepath.ToSlash(path), data)
}

func compileSchemaMap(t *testing.T, id string, schemaMap map[string]any) *jsonschema.Schema {
	t.Helper()
	raw, err := json.Marshal(schemaMap)
	if err != nil {
		t.Fatalf("marshal schema map %s: %v", id, err)
	}
	return compileSchemaFromBytes(t, id, raw)
}

func decodeJSONForValidation(t *testing.T, data []byte) any {
	t.Helper()
	val, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unmarshal json for validation: %v", err)
	}
	return val
}

func validateMapAgainstSchema(t *testing.T, sch *jsonschema.Schema, obj map[string]any) error {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal object for validation: %v", err)
	}
	val := decodeJSONForValidation(t, raw)
	return sch.Validate(val)
}

func decodeStrictJSON(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	return nil
}

func TestContractSchemasCompile(t *testing.T) {
	root := repoRoot(t)
	_ = compileSchemaFile(t, filepath.Join(root, "schemas", "manifest.schema.json"))
	_ = compileSchemaFile(t, filepath.Join(root, "schemas", "tests.schema.json"))
}

func TestContractExampleBundles(t *testing.T) {
	root := repoRoot(t)
	manifestSchema := compileSchemaFile(t, filepath.Join(root, "schemas", "manifest.schema.json"))
	testsSchema := compileSchemaFile(t, filepath.Join(root, "schemas", "tests.schema.json"))

	examplesDir := filepath.Join(root, "examples")
	entries, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("read examples dir: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 example bundles, got %d", len(entries))
	}

	forbiddenJS := []string{
		"async ",
		"await ",
		"require(",
		"fetch(",
		"process.",
		"setTimeout(",
		"setInterval(",
		"console.",
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		bundleName := entry.Name()
		bundleDir := filepath.Join(examplesDir, bundleName)

		t.Run(bundleName, func(t *testing.T) {
			manifestBytes, readErr := os.ReadFile(filepath.Join(bundleDir, "manifest.json"))
			if readErr != nil {
				t.Fatalf("read manifest.json: %v", readErr)
			}
			manifestVal := decodeJSONForValidation(t, manifestBytes)
			if valErr := manifestSchema.Validate(manifestVal); valErr != nil {
				t.Fatalf("manifest.json schema validation failed: %v", valErr)
			}

			var manifest contract.Manifest
			if decErr := decodeStrictJSON(manifestBytes, &manifest); decErr != nil {
				t.Fatalf("decode manifest.json into struct: %v", decErr)
			}
			if manifest.Name != bundleName {
				t.Fatalf("manifest.name = %q, want directory name %q", manifest.Name, bundleName)
			}
			if semErr := contract.ValidateManifestSemantic(manifest); semErr != nil {
				t.Fatalf("manifest semantic validation failed: %v", semErr)
			}

			inputSchema := compileSchemaMap(t, "in://"+bundleName, manifest.InputSchema)
			outputSchema := compileSchemaMap(t, "out://"+bundleName, manifest.OutputSchema)

			testsBytes, readTestsErr := os.ReadFile(filepath.Join(bundleDir, "tests.json"))
			if readTestsErr != nil {
				t.Fatalf("read tests.json: %v", readTestsErr)
			}
			testsVal := decodeJSONForValidation(t, testsBytes)
			if valErr := testsSchema.Validate(testsVal); valErr != nil {
				t.Fatalf("tests.json schema validation failed: %v", valErr)
			}

			var suite contract.TestSuite
			if decErr := decodeStrictJSON(testsBytes, &suite); decErr != nil {
				t.Fatalf("decode tests.json into struct: %v", decErr)
			}
			if semErr := contract.ValidateTestSuiteSemantic(manifest, suite); semErr != nil {
				t.Fatalf("tests semantic validation failed: %v", semErr)
			}

			for _, tc := range suite.Cases {
				inErr := validateMapAgainstSchema(t, inputSchema, tc.Input)
				if tc.Expect.ErrorCode == contract.ErrInvalidInput {
					if inErr == nil {
						t.Errorf("case %q expects INVALID_INPUT, but input passed input_schema", tc.Name)
					}
					continue
				}
				if inErr != nil {
					t.Errorf("case %q input failed input_schema: %v", tc.Name, inErr)
				}
				if tc.Expect.OK && tc.Expect.Output != nil {
					if outErr := validateMapAgainstSchema(t, outputSchema, tc.Expect.Output); outErr != nil {
						t.Errorf("case %q expected output failed output_schema: %v", tc.Name, outErr)
					}
				}
			}

			jsBytes, readJSErr := os.ReadFile(filepath.Join(bundleDir, "tool.js"))
			if readJSErr != nil {
				t.Fatalf("read tool.js: %v", readJSErr)
			}
			jsSrc := string(jsBytes)
			if !strings.Contains(jsSrc, "function main(") {
				t.Errorf("tool.js does not define 'function main('")
			}
			for _, bad := range forbiddenJS {
				if strings.Contains(jsSrc, bad) {
					t.Errorf("tool.js contains unsupported JS construct %q", bad)
				}
			}
		})
	}
}

func validBaseManifest() map[string]any {
	return map[string]any{
		"schema_version": "1",
		"name":           "sample-tool",
		"version":        1,
		"description":    "Sample read-only diagnostic tool for contract testing",
		"read_only":      true,
		"input_schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		"output_schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		"capabilities": map[string]any{
			"fs": map[string]any{
				"read": []string{"**/*.md"},
			},
		},
	}
}

func TestContractSchemaRejectsInvalidManifests(t *testing.T) {
	root := repoRoot(t)
	manifestSchema := compileSchemaFile(t, filepath.Join(root, "schemas", "manifest.schema.json"))

	cases := []struct {
		name   string
		mutate func(m map[string]any)
	}{
		{
			name: "bare binary exec with empty args",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"exec": map[string]any{
						"templates": []map[string]any{
							{
								"id":          "bare-git",
								"program":     "git",
								"args":        []string{},
								"read_only":   true,
								"description": "Bare git invocation",
							},
						},
					},
				}
			},
		},
		{
			name: "placeholder in args[0]",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"exec": map[string]any{
						"templates": []map[string]any{
							{
								"id":          "dyn-subcmd",
								"program":     "git",
								"args":        []string{"${arg:subcmd}"},
								"args_schema": map[string]any{"subcmd": map[string]any{"pattern": "^status$"}},
								"read_only":   true,
								"description": "Dynamic subcommand",
							},
						},
					},
				}
			},
		},
		{
			name: "shell binary bash forbidden",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"exec": map[string]any{
						"templates": []map[string]any{
							{
								"id":          "shell-run",
								"program":     "bash",
								"args":        []string{"-c", "echo hi"},
								"read_only":   true,
								"description": "Forbidden shell invocation",
							},
						},
					},
				}
			},
		},
		{
			name: "shell binary sh forbidden",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"exec": map[string]any{
						"templates": []map[string]any{
							{
								"id":          "sh-run",
								"program":     "sh",
								"args":        []string{"-c", "ls"},
								"read_only":   true,
								"description": "Forbidden sh invocation",
							},
						},
					},
				}
			},
		},
		{
			name: "wildcard HTTP domain rejected",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"http": map[string]any{
						"allow": []map[string]any{
							{"domain": "*", "methods": []string{"GET"}},
						},
					},
				}
			},
		},
		{
			name: "IPv4 metadata HTTP domain rejected",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"http": map[string]any{
						"allow": []map[string]any{
							{"domain": "169.254.169.254", "methods": []string{"GET"}},
						},
					},
				}
			},
		},
		{
			name: "undeclared secret placeholder rejected semantically",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"exec": map[string]any{
						"templates": []map[string]any{
							{
								"id":          "auth-curl",
								"program":     "curl",
								"args":        []string{"-H", "Authorization: ${secret:MISSING_TOKEN}", "https://example.com"},
								"read_only":   true,
								"description": "Fetch URL with missing secret",
							},
						},
					},
				}
			},
		},
		{
			name: "undeclared arg placeholder rejected semantically",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"exec": map[string]any{
						"templates": []map[string]any{
							{
								"id":          "git-log",
								"program":     "git",
								"args":        []string{"log", "-n", "${arg:limit}"},
								"read_only":   true,
								"description": "Run git log without arg schema",
							},
						},
					},
				}
			},
		},
		{
			name: "read_only tool with non-read-only exec template rejected",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"exec": map[string]any{
						"templates": []map[string]any{
							{
								"id":          "git-commit",
								"program":     "git",
								"args":        []string{"commit", "-m", "msg"},
								"read_only":   false,
								"description": "Mutating git commit",
							},
						},
					},
				}
			},
		},
		{
			name: "read_only tool with HTTP POST rejected",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"http": map[string]any{
						"allow": []map[string]any{
							{"domain": "api.example.com", "methods": []string{"POST"}},
						},
					},
				}
			},
		},
		{
			name: "path traversal in fs.read rejected",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"fs": map[string]any{
						"read": []string{"../etc/passwd"},
					},
				}
			},
		},
		{
			name: "absolute path in fs.read rejected",
			mutate: func(m map[string]any) {
				m["capabilities"] = map[string]any{
					"fs": map[string]any{
						"read": []string{"/etc/passwd"},
					},
				}
			},
		},
		{
			name: "unknown top-level manifest field rejected",
			mutate: func(m map[string]any) {
				m["elevated"] = false
			},
		},
		{
			name: "invalid tool name rejected",
			mutate: func(m map[string]any) {
				m["name"] = "Invalid_Tool_Name"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := validBaseManifest()
			tc.mutate(doc)

			raw, marshalErr := json.Marshal(doc)
			if marshalErr != nil {
				t.Fatalf("marshal manifest: %v", marshalErr)
			}

			schemaErr := manifestSchema.Validate(decodeJSONForValidation(t, raw))
			if schemaErr != nil {
				return
			}

			var parsed contract.Manifest
			if decErr := decodeStrictJSON(raw, &parsed); decErr != nil {
				return
			}
			if semErr := contract.ValidateManifestSemantic(parsed); semErr != nil {
				return
			}

			t.Fatalf("expected manifest %q to be rejected by JSON Schema or semantic validation, but it passed", tc.name)
		})
	}
}

func TestContractSchemaRejectsInvalidTests(t *testing.T) {
	root := repoRoot(t)
	testsSchema := compileSchemaFile(t, filepath.Join(root, "schemas", "tests.schema.json"))

	manifest := contract.Manifest{
		SchemaVersion: "1",
		Name:          "sample-tool",
		Version:       1,
		Description:   "Sample read-only diagnostic tool for contract testing",
		ReadOnly:      true,
		Capabilities: contract.Capabilities{
			Exec: &contract.ExecCapability{
				Templates: []contract.ExecTemplate{
					{
						ID:          "git-status",
						Program:     "git",
						Args:        []string{"status"},
						ReadOnly:    true,
						Description: "Check git status",
					},
				},
			},
		},
		Secrets: []contract.SecretRef{
			{Name: "API_TOKEN", Required: false, Description: "Optional token"},
		},
	}

	cases := []struct {
		name string
		doc  map[string]any
	}{
		{
			name: "empty cases array",
			doc: map[string]any{
				"schema_version": "1",
				"cases":          []any{},
			},
		},
		{
			name: "ok true with error_code",
			doc: map[string]any{
				"schema_version": "1",
				"cases": []map[string]any{
					{
						"name":  "bad-case",
						"input": map[string]any{},
						"expect": map[string]any{
							"ok":         true,
							"output":     map[string]any{"x": 1},
							"error_code": "TOOL_ERROR",
						},
					},
				},
			},
		},
		{
			name: "ok false without error_code",
			doc: map[string]any{
				"schema_version": "1",
				"cases": []map[string]any{
					{
						"name":   "missing-err",
						"input":  map[string]any{},
						"expect": map[string]any{"ok": false},
					},
				},
			},
		},
		{
			name: "both output and output_subset set",
			doc: map[string]any{
				"schema_version": "1",
				"cases": []map[string]any{
					{
						"name":  "ambiguous-output",
						"input": map[string]any{},
						"expect": map[string]any{
							"ok":            true,
							"output":        map[string]any{"a": 1},
							"output_subset": map[string]any{"a": 1},
						},
					},
				},
			},
		},
		{
			name: "undeclared mock template_id",
			doc: map[string]any{
				"schema_version": "1",
				"cases": []map[string]any{
					{
						"name":  "bad-exec-mock",
						"input": map[string]any{},
						"mocks": map[string]any{
							"exec": []map[string]any{
								{
									"template_id": "nonexistent-template",
									"result":      map[string]any{"exit_code": 0, "stdout": "ok"},
								},
							},
						},
						"expect": map[string]any{
							"ok":     true,
							"output": map[string]any{},
						},
					},
				},
			},
		},
		{
			name: "undeclared mock secret",
			doc: map[string]any{
				"schema_version": "1",
				"cases": []map[string]any{
					{
						"name":  "bad-secret-mock",
						"input": map[string]any{},
						"mocks": map[string]any{
							"secrets": []string{"UNDECLARED_SECRET"},
						},
						"expect": map[string]any{
							"ok":     true,
							"output": map[string]any{},
						},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, marshalErr := json.Marshal(tc.doc)
			if marshalErr != nil {
				t.Fatalf("marshal test doc: %v", marshalErr)
			}

			if schemaErr := testsSchema.Validate(decodeJSONForValidation(t, raw)); schemaErr != nil {
				return
			}

			var suite contract.TestSuite
			if decErr := decodeStrictJSON(raw, &suite); decErr != nil {
				return
			}
			if semErr := contract.ValidateTestSuiteSemantic(manifest, suite); semErr != nil {
				return
			}

			t.Fatalf("expected invalid tests.json %q to fail validation, but it passed", tc.name)
		})
	}
}

func TestContractHelpersAndMCPRoundTrip(t *testing.T) {
	root := repoRoot(t)

	repoManifestBytes, err := os.ReadFile(filepath.Join(root, "examples", "repo-context", "manifest.json"))
	if err != nil {
		t.Fatalf("read repo-context manifest: %v", err)
	}
	var repoManifest contract.Manifest
	if decErr := decodeStrictJSON(repoManifestBytes, &repoManifest); decErr != nil {
		t.Fatalf("decode repo-context manifest: %v", decErr)
	}
	if repoManifest.IsElevated() {
		t.Errorf("repo-context IsElevated() = true, want false")
	}
	if got := repoManifest.EffectiveTimeoutMs(); got != 10000 {
		t.Errorf("repo-context EffectiveTimeoutMs() = %d, want 10000", got)
	}

	vpsManifestBytes, err := os.ReadFile(filepath.Join(root, "examples", "vps-health", "manifest.json"))
	if err != nil {
		t.Fatalf("read vps-health manifest: %v", err)
	}
	var vpsManifest contract.Manifest
	if decErr := decodeStrictJSON(vpsManifestBytes, &vpsManifest); decErr != nil {
		t.Fatalf("decode vps-health manifest: %v", decErr)
	}
	if !vpsManifest.IsElevated() {
		t.Errorf("vps-health IsElevated() = false, want true")
	}
	if got := vpsManifest.EffectiveTimeoutMs(); got != 20000 {
		t.Errorf("vps-health EffectiveTimeoutMs() = %d, want 20000", got)
	}

	defaultTimeoutManifest := contract.Manifest{}
	if got := defaultTimeoutManifest.EffectiveTimeoutMs(); got != contract.DefaultTimeoutMs {
		t.Errorf("zero timeout EffectiveTimeoutMs() = %d, want %d", got, contract.DefaultTimeoutMs)
	}
	clampedTimeoutManifest := contract.Manifest{TimeoutMs: 999999}
	if got := clampedTimeoutManifest.EffectiveTimeoutMs(); got != contract.MaxTimeoutMs {
		t.Errorf("over-max timeout EffectiveTimeoutMs() = %d, want %d", got, contract.MaxTimeoutMs)
	}

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	assertRoundTrip(t, contract.DiscoverRequest{
		Query: "repo",
		Tags:  []string{"git"},
		Scope: contract.ScopeAll,
		Limit: 25,
	})
	assertRoundTrip(t, contract.DiscoverResponse{
		Tools: []contract.ToolSummary{
			{
				Name:        "repo-context",
				Version:     1,
				Scope:       contract.ScopeGlobal,
				Description: repoManifest.Description,
				Tags:        repoManifest.Tags,
				ReadOnly:    true,
				Elevated:    false,
			},
		},
	})
	assertRoundTrip(t, contract.InspectRequest{
		Name:    "repo-context",
		Scope:   contract.ScopeAuto,
		Version: 1,
	})
	assertRoundTrip(t, contract.InspectResponse{
		Name:              "repo-context",
		Version:           1,
		Scope:             contract.ScopeGlobal,
		ActiveVersion:     1,
		AvailableVersions: []int{1},
		Elevated:          false,
		Manifest:          repoManifest,
	})
	assertRoundTrip(t, contract.RunRequest{
		Name:      "repo-context",
		Input:     map[string]any{"include_tree": true},
		Scope:     contract.ScopeAuto,
		TimeoutMs: 5000,
	})
	assertRoundTrip(t, contract.RunResponse{
		OK:         true,
		Tool:       "repo-context",
		Version:    1,
		Scope:      contract.ScopeGlobal,
		DurationMs: 42,
		Output:     map[string]any{"is_git_repo": true},
	})
	assertRoundTrip(t, contract.ProposeRequest{
		Name:     "repo-context",
		Scope:    contract.ScopeRepo,
		Summary:  "Initial repo-context tool",
		Manifest: repoManifest,
		Source:   "function main(input) { return {}; }",
		Tests: contract.TestSuite{
			SchemaVersion: "1",
			Cases: []contract.TestCase{
				{
					Name:   "smoke",
					Input:  map[string]any{},
					Expect: contract.TestExpectation{OK: true, Output: map[string]any{}},
				},
			},
		},
	})
	assertRoundTrip(t, contract.ProposeResponse{
		ProposalID:    "prop_01",
		Status:        contract.ProposalPending,
		Tool:          "repo-context",
		TargetVersion: 1,
		BaseVersion:   0,
		Scope:         contract.ScopeRepo,
		Elevated:      false,
		Validation: contract.ValidationReport{
			SchemaValid: true,
			Compiled:    true,
			TestsPassed: 1,
			TestsTotal:  1,
			Failures:    []string{},
		},
		CapabilityDiff: contract.CapabilityDiff{
			Changes: []contract.CapabilityChange{},
		},
		DryRun: &contract.DryRunPreview{
			Executed:   true,
			DurationMs: 12,
			OK:         true,
		},
		ApprovalCommand: "knurl approve prop_01",
	})
	assertRoundTrip(t, contract.StatusRequest{ProposalID: "prop_01"})
	assertRoundTrip(t, contract.StatusResponse{
		ProposalID:      "prop_01",
		Status:          contract.ProposalApproved,
		Tool:            "repo-context",
		TargetVersion:   1,
		BaseVersion:     0,
		Scope:           contract.ScopeRepo,
		CreatedAt:       now,
		DecidedAt:       &now,
		DecisionNote:    "approved",
		ApprovalCommand: "",
	})
}

func assertRoundTrip[T any](t *testing.T, v T) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var out T
	if decErr := decodeStrictJSON(raw, &out); decErr != nil {
		t.Fatalf("unmarshal %T: %v (json=%s)", v, decErr, raw)
	}
}
