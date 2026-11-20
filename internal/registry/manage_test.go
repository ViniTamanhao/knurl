package registry_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/registry"
)

func promoteSample(t *testing.T, r *registry.Registry, version, base int, scope contract.Scope) {
	t.Helper()
	m := contract.Manifest{SchemaVersion: "1", Name: "sample-tool", Version: version, Description: "Sample registry tool", ReadOnly: true, Tags: []string{"sample"}, InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Promote(m, map[string][]byte{"manifest.json": data, "tool.js": []byte(`function main(){return {};}`), "tests.json": []byte(`{"schema_version":"1","cases":[]}`)}, base, contract.ApprovalRecord{ProposalID: "test", ApprovedAt: time.Now().UTC()}, scope); err != nil {
		t.Fatal(err)
	}
}
func TestPromotionRollbackAndDiscovery(t *testing.T) {
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &registry.Registry{WorkDir: work, GlobalDir: filepath.Join(t.TempDir(), "tools")}
	promoteSample(t, r, 1, 0, contract.ScopeGlobal)
	promoteSample(t, r, 1, 0, contract.ScopeRepo)
	promoteSample(t, r, 2, 1, contract.ScopeRepo)
	found, err := r.Discover(contract.DiscoverRequest{Scope: contract.ScopeAll, Query: "SAMPLE", Tags: []string{"sample"}})
	if err != nil || len(found.Tools) != 1 || found.Tools[0].Scope != contract.ScopeRepo || found.Tools[0].Version != 2 {
		t.Fatalf("discover %+v %v", found, err)
	}
	found, err = r.Discover(contract.DiscoverRequest{Scope: contract.ScopeGlobal})
	if err != nil || len(found.Tools) != 1 || found.Tools[0].Version != 1 {
		t.Fatalf("global discover %+v %v", found, err)
	}
	found, err = r.Discover(contract.DiscoverRequest{Tags: []string{"missing"}})
	if err != nil || len(found.Tools) != 0 {
		t.Fatalf("tag filter %+v %v", found, err)
	}
	inspect, err := r.Inspect(contract.InspectRequest{Name: "sample-tool"})
	if err != nil || len(inspect.AvailableVersions) != 2 {
		t.Fatalf("inspect %+v %v", inspect, err)
	}
	before, after, err := r.Rollback("sample-tool", 0, contract.ScopeRepo)
	if err != nil || before != 2 || after != 1 {
		t.Fatalf("rollback %d %d %v", before, after, err)
	}
	before, after, err = r.Rollback("sample-tool", 0, contract.ScopeRepo)
	if err != nil || before != 1 || after != 2 {
		t.Fatalf("forward recovery %d %d %v", before, after, err)
	}
	if _, _, err = r.Rollback("sample-tool", 3, contract.ScopeRepo); err == nil {
		t.Fatal("missing version accepted")
	}
	if _, _, err = r.Rollback("sample-tool", 0, contract.ScopeGlobal); err == nil {
		t.Fatal("missing prior accepted")
	}
}
func TestPromotionLockAndPointerIntegrity(t *testing.T) {
	r := &registry.Registry{GlobalDir: filepath.Join(t.TempDir(), "tools")}
	promoteSample(t, r, 1, 0, contract.ScopeGlobal)
	root, err := os.OpenRoot(r.GlobalDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	unlock, err := registry.Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Lock(root); err == nil {
		t.Fatal("concurrent writer accepted")
	}
	if _, _, err = r.Rollback("sample-tool", 1, contract.ScopeGlobal); err == nil {
		t.Fatal("locked rollback accepted")
	}
	unlock()
	b, err := r.Resolve("sample-tool", contract.ScopeGlobal)
	if err != nil || b.Version != 1 {
		t.Fatal("failed writer changed pointer", err)
	}
}

func TestDiscoverMalformedRepoShadowsGlobal(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".knurl", "tools", "sample-tool"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &registry.Registry{WorkDir: work, GlobalDir: filepath.Join(t.TempDir(), "tools")}
	promoteSample(t, r, 1, 0, contract.ScopeGlobal)
	out, err := r.Discover(contract.DiscoverRequest{Scope: contract.ScopeAll})
	if err != nil || len(out.Tools) != 0 {
		t.Fatalf("malformed local tool exposed global fallback: %+v %v", out, err)
	}
}
