package registry

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

const (
	// DefaultToolsDir is the default local directory searched for Phase 1 bundles.
	DefaultToolsDir = "./examples"

	maxBundleFileBytes = 1024 * 1024 // 1 MiB
)

var toolNameRegexp = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// Bundle holds a validated Phase 1 tool bundle loaded from disk.
type Bundle struct {
	Name     string
	Version  int
	Scope    contract.Scope
	Dir      string
	Manifest contract.Manifest
	Source   string
}

// Loader loads tool bundles from a configured tools directory or direct path.
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

// Load resolves a tool bundle either by direct directory path (when
// nameOrPath contains path separators or references an existing directory)
// or by tool name inside Loader.EffectiveToolsDir().
func (l *Loader) Load(nameOrPath string) (*Bundle, error) {
	if nameOrPath == "" {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: "tool name or path must not be empty",
		}
	}

	if looksLikePath(nameOrPath) || isExistingDir(nameOrPath) {
		return loadBundleDir(nameOrPath, "")
	}

	return l.LoadByName(nameOrPath)
}

// LoadByName resolves a tool bundle strictly by validated kebab-case tool name
// inside Loader.EffectiveToolsDir(), rejecting path traversal or path inputs.
func (l *Loader) LoadByName(name string) (*Bundle, error) {
	if !toolNameRegexp.MatchString(name) {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: fmt.Sprintf("tool %q not found (invalid tool name)", name),
		}
	}
	bundleDir := filepath.Join(l.EffectiveToolsDir(), name)
	return loadBundleDir(bundleDir, name)
}

func looksLikePath(v string) bool {
	return v == "." || v == ".." || strings.ContainsAny(v, `/\`)
}

func isExistingDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func loadBundleDir(dirPath, expectedName string) (*Bundle, error) {
	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: fmt.Sprintf("resolve bundle path %q: %v", dirPath, err),
		}
	}

	info, statErr := os.Stat(absDir)
	if statErr != nil || !info.IsDir() {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: fmt.Sprintf("tool bundle %q not found", dirPath),
		}
	}

	root, openErr := os.OpenRoot(absDir)
	if openErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: fmt.Sprintf("open bundle directory %q: %v", absDir, openErr),
		}
	}
	defer func() { _ = root.Close() }()

	manifestBytes, readManifestErr := readBundleFile(root, "manifest.json")
	if readManifestErr != nil {
		return nil, readManifestErr
	}
	manifest, valErr := contract.ValidateManifestJSON(manifestBytes)
	if valErr != nil {
		return nil, valErr
	}
	if expectedName != "" && manifest.Name != expectedName {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("manifest name %q does not match requested tool %q", manifest.Name, expectedName),
		}
	}

	sourceBytes, readSrcErr := readBundleFile(root, "tool.js")
	if readSrcErr != nil {
		return nil, readSrcErr
	}
	if len(strings.TrimSpace(string(sourceBytes))) == 0 {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: "tool.js is empty",
		}
	}

	return &Bundle{
		Name:     manifest.Name,
		Version:  manifest.Version,
		Scope:    contract.ScopeRepo,
		Dir:      absDir,
		Manifest: manifest,
		Source:   string(sourceBytes),
	}, nil
}

func readBundleFile(root *os.Root, filename string) ([]byte, error) {
	f, err := root.Open(filename)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidManifest,
				Message: fmt.Sprintf("bundle missing required %s", filename),
			}
		}
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("open bundle %s: %v", filename, err),
		}
	}
	defer func() { _ = f.Close() }()

	info, statErr := f.Stat()
	if statErr != nil || info.IsDir() {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("bundle %s is not a regular file", filename),
		}
	}

	data, readErr := io.ReadAll(io.LimitReader(f, maxBundleFileBytes+1))
	if readErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("read bundle %s: %v", filename, readErr),
		}
	}
	if len(data) > maxBundleFileBytes {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("bundle %s exceeds %d byte limit", filename, maxBundleFileBytes),
		}
	}
	return data, nil
}
