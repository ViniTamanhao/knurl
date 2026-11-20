package approval

import (
	"github.com/ViniTamanhao/knurl/internal/contract"
	"testing"
)

func TestPermissionContainment(t *testing.T) {
	old := contract.Manifest{Name: "sample", ReadOnly: true, Secrets: []contract.SecretRef{{Name: "TOKEN", Required: true, Description: "old"}}, Capabilities: contract.Capabilities{
		HTTP: &contract.HTTPCapability{Allow: []contract.HTTPRule{{Domain: "*.example.com", Methods: []string{"GET", "POST"}, PathPrefixes: []string{"/api"}, Secrets: []string{"TOKEN"}, AllowHTTP: true}}},
		Exec: &contract.ExecCapability{Templates: []contract.ExecTemplate{{ID: "check", Program: "git", Args: []string{"show", "${arg:ref}"}, Description: "old", ReadOnly: true, ArgsSchema: map[string]contract.ExecArgSchema{"ref": {Pattern: "[a-z]+", MaxLength: 20, Enum: []string{"main", "dev"}, Description: "old"}}}}},
	}}
	for _, tc := range []struct {
		name, capability string
		value            any
		escalated        bool
	}{
		{"http narrowing", "http.allow", contract.HTTPRule{Domain: "api.example.com", Methods: []string{"GET"}, PathPrefixes: []string{"/api/v1"}}, false},
		{"http path widening", "http.allow", contract.HTTPRule{Domain: "api.example.com", Methods: []string{"GET"}}, true},
		{"http sibling path", "http.allow", contract.HTTPRule{Domain: "api.example.com", Methods: []string{"GET"}, PathPrefixes: []string{"/apix"}}, true},
		{"optional secret", "secret", contract.SecretRef{Name: "TOKEN", Required: false}, false},
		{"new secret", "secret", contract.SecretRef{Name: "OTHER"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := old
			switch v := tc.value.(type) {
			case contract.HTTPRule:
				next.Capabilities = contract.Capabilities{HTTP: &contract.HTTPCapability{Allow: []contract.HTTPRule{v}}}
			case contract.SecretRef:
				next.Secrets = []contract.SecretRef{v}
			}
			if got := Diff(old, next).HasEscalation; got != tc.escalated {
				t.Fatalf("escalated=%t want %t", got, tc.escalated)
			}
		})
	}
	next := old
	template := old.Capabilities.Exec.Templates[0]
	template.Description = "new"
	template.ArgsSchema = map[string]contract.ExecArgSchema{"ref": {Pattern: "[a-z]+", MaxLength: 20, Enum: []string{"dev", "main"}, Description: "new"}}
	next.Capabilities.Exec = &contract.ExecCapability{Templates: []contract.ExecTemplate{template}}
	next.Secrets = []contract.SecretRef{{Name: "TOKEN", Required: true, Description: "new"}}
	diff := Diff(old, next)
	if diff.HasEscalation {
		t.Fatal("metadata escalated", diff)
	}
	for _, c := range diff.Changes {
		if c.Kind != "unchanged" {
			t.Fatal("metadata changed permissions", c)
		}
	}
	for _, tc := range []struct {
		name          string
		before, after contract.ExecArgSchema
		covered       bool
	}{
		{"length narrowing", contract.ExecArgSchema{Pattern: ".*", MaxLength: 20}, contract.ExecArgSchema{Pattern: ".*", MaxLength: 10}, true},
		{"length widening", contract.ExecArgSchema{Pattern: ".*", MaxLength: 10}, contract.ExecArgSchema{Pattern: ".*", MaxLength: 20}, false},
		{"enum subset", contract.ExecArgSchema{Pattern: "[a-z]+", Enum: []string{"main", "dev"}}, contract.ExecArgSchema{Pattern: ".*", Enum: []string{"main"}}, true},
		{"enum expansion", contract.ExecArgSchema{Pattern: "[a-z]+", Enum: []string{"main"}}, contract.ExecArgSchema{Pattern: "[a-z]+", Enum: []string{"main", "dev"}}, false},
		{"unknown regex", contract.ExecArgSchema{Pattern: "[a-z]+"}, contract.ExecArgSchema{Pattern: "[a-z]{1,3}"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if argCovered(tc.after, tc.before) != tc.covered {
				t.Fatal("incorrect argument containment")
			}
		})
	}
}
