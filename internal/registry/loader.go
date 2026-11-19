package registry

import (
	"os"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

// DefaultToolsDir is the default local directory searched for Phase 1 bundles.
const DefaultToolsDir = "./examples"

// Loader loads tool bundles from a configured tools directory or direct path.
// Superseded by Registry in Phase 2; kept temporarily until CLI/MCP wire Registry.
type Loader struct {
	ToolsDir string
}

// EffectiveToolsDir returns the configured tools directory or DefaultToolsDir.
func (l *Loader) EffectiveToolsDir() string {
	if l != nil && l.ToolsDir != "" {
		return l.ToolsDir
	}
	return DefaultToolsDir
}

// Load resolves a tool bundle either by direct directory path or by tool name.
func (l *Loader) Load(nameOrPath string) (*Bundle, error) {
	if nameOrPath == "" {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: "tool name or path must not be empty",
		}
	}
	if looksLikePath(nameOrPath) || isExistingDir(nameOrPath) {
		return loadToolFromDir(nameOrPath, "", contract.ScopeRepo, 0)
	}
	return l.LoadByName(nameOrPath)
}

// LoadByName resolves a tool bundle strictly by validated kebab-case tool name.
func (l *Loader) LoadByName(name string) (*Bundle, error) {
	reg := &Registry{OverrideDir: l.EffectiveToolsDir()}
	b, err := reg.Resolve(name, contract.ScopeOverride)
	if err != nil {
		return nil, err
	}
	b.Scope = contract.ScopeRepo
	return b, nil
}

func isExistingDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
