package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

const (
	maxBundleFileBytes   = 1024 * 1024 // 1 MiB
	maxMetadataFileBytes = 64 * 1024   // 64 KiB
)

var versionDirRegexp = regexp.MustCompile(`^v([1-9][0-9]*)$`)

// Bundle holds a validated tool bundle resolved from a registry tier or direct path.
type Bundle struct {
	Name              string
	Version           int
	ActiveVersion     int
	AvailableVersions []int
	Scope             contract.Scope
	Dir               string
	ToolDir           string
	Manifest          contract.Manifest
	Source            string
	Approval          *contract.ApprovalRecord
}

// ToolEntry describes a resolved active tool discovered while listing registry tiers.
type ToolEntry struct {
	Name              string
	Version           int
	ActiveVersion     int
	AvailableVersions []int
	Scope             contract.Scope
	Dir               string
	Manifest          contract.Manifest
	Summary           contract.ToolSummary
}

func parseVersionDirName(name string) (int, bool) {
	m := versionDirRegexp.FindStringSubmatch(name)
	if len(m) != 2 {
		return 0, false
	}
	v, err := strconv.Atoi(m[1])
	if err != nil || v < 1 {
		return 0, false
	}
	return v, true
}

func loadToolFromDir(toolDir, expectedName string, scope contract.Scope, requestedVersion int) (*Bundle, error) {
	absDir, absErr := filepath.Abs(toolDir)
	if absErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: fmt.Sprintf("resolve tool directory %q: %v", toolDir, absErr),
		}
	}
	info, statErr := os.Stat(absDir)
	if statErr != nil || !info.IsDir() {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: fmt.Sprintf("tool %q not found", displayToolName(expectedName, toolDir)),
		}
	}
	canonicalToolDir, evalErr := filepath.EvalSymlinks(absDir)
	if evalErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: fmt.Sprintf("resolve canonical tool directory %q: %v", toolDir, evalErr),
		}
	}

	root, openErr := os.OpenRoot(canonicalToolDir)
	if openErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrToolNotFound,
			Message: fmt.Sprintf("open tool directory %q: %v", canonicalToolDir, openErr),
		}
	}
	defer func() { _ = root.Close() }()

	availableVersions, symlinkVerErr := scanVersionDirs(root, canonicalToolDir)
	if symlinkVerErr != nil {
		return nil, symlinkVerErr
	}

	_, activeLstatErr := root.Lstat("active.json")
	hasActivePtr := activeLstatErr == nil
	if activeLstatErr != nil && !errors.Is(activeLstatErr, fs.ErrNotExist) {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("inspect active.json: %v", activeLstatErr),
		}
	}

	if hasActivePtr {
		ptr, ptrErr := readActivePointer(root)
		if ptrErr != nil {
			return nil, ptrErr
		}
		targetVer := ptr.ActiveVersion
		if requestedVersion > 0 {
			targetVer = requestedVersion
		}
		return loadVersionBundle(root, canonicalToolDir, expectedName, targetVer, ptr.ActiveVersion, availableVersions, scope)
	}

	if len(availableVersions) > 0 {
		activeVer := availableVersions[len(availableVersions)-1]
		targetVer := activeVer
		if requestedVersion > 0 {
			targetVer = requestedVersion
		}
		return loadVersionBundle(root, canonicalToolDir, expectedName, targetVer, activeVer, availableVersions, scope)
	}

	return loadFlatBundle(root, canonicalToolDir, expectedName, requestedVersion, scope)
}

func scanVersionDirs(root *os.Root, canonicalToolDir string) ([]int, error) {
	entries, err := os.ReadDir(canonicalToolDir)
	if err != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("read tool directory %q: %v", canonicalToolDir, err),
		}
	}
	versions := make([]int, 0)
	for _, entry := range entries {
		verNum, ok := parseVersionDirName(entry.Name())
		if !ok {
			continue
		}
		linfo, lstatErr := root.Lstat(entry.Name())
		if lstatErr != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidManifest,
				Message: fmt.Sprintf("inspect version directory %s: %v", entry.Name(), lstatErr),
			}
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidManifest,
				Message: fmt.Sprintf("version directory %s must not be a symlink", entry.Name()),
			}
		}
		if !linfo.IsDir() {
			continue
		}
		versions = append(versions, verNum)
	}
	sort.Ints(versions)
	return versions, nil
}

func readActivePointer(root *os.Root) (contract.ActiveVersionPointer, error) {
	raw, err := readBundleFile(root, "active.json", maxMetadataFileBytes)
	if err != nil {
		return contract.ActiveVersionPointer{}, err
	}
	var ptr contract.ActiveVersionPointer
	if decErr := decodeStrictJSON(raw, &ptr, "active.json"); decErr != nil {
		return contract.ActiveVersionPointer{}, decErr
	}
	if ptr.ActiveVersion < 1 {
		return contract.ActiveVersionPointer{}, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("active.json active_version must be >= 1, got %d", ptr.ActiveVersion),
		}
	}
	return ptr, nil
}

func loadVersionBundle(
	root *os.Root,
	canonicalToolDir string,
	expectedName string,
	targetVer int,
	activeVer int,
	availableVersions []int,
	scope contract.Scope,
) (*Bundle, error) {
	verDirName := fmt.Sprintf("v%d", targetVer)
	linfo, lstatErr := root.Lstat(verDirName)
	if lstatErr != nil {
		if errors.Is(lstatErr, fs.ErrNotExist) {
			return nil, &contract.ToolError{
				Code:    contract.ErrVersionNotFound,
				Message: fmt.Sprintf("tool %q version %s not found (active: v%d)", displayToolName(expectedName, canonicalToolDir), verDirName, activeVer),
			}
		}
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("inspect version directory %s: %v", verDirName, lstatErr),
		}
	}
	if linfo.Mode()&os.ModeSymlink != 0 || !linfo.IsDir() {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("version entry %s is not a real directory", verDirName),
		}
	}

	manifestBytes, readManifestErr := readBundleFile(root, verDirName+"/manifest.json", maxBundleFileBytes)
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
	if manifest.Version != targetVer {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("manifest version %d does not match directory %s", manifest.Version, verDirName),
		}
	}

	sourceBytes, readSrcErr := readBundleFile(root, verDirName+"/tool.js", maxBundleFileBytes)
	if readSrcErr != nil {
		return nil, readSrcErr
	}
	if len(strings.TrimSpace(string(sourceBytes))) == 0 {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("%s/tool.js is empty", verDirName),
		}
	}

	approval, appErr := readOptionalApproval(root, verDirName+"/approval.json")
	if appErr != nil {
		return nil, appErr
	}

	return &Bundle{
		Name:              manifest.Name,
		Version:           manifest.Version,
		ActiveVersion:     activeVer,
		AvailableVersions: slices.Clone(availableVersions),
		Scope:             scope,
		Dir:               filepath.Join(canonicalToolDir, verDirName),
		ToolDir:           canonicalToolDir,
		Manifest:          manifest,
		Source:            string(sourceBytes),
		Approval:          approval,
	}, nil
}

func loadFlatBundle(
	root *os.Root,
	canonicalToolDir string,
	expectedName string,
	requestedVersion int,
	scope contract.Scope,
) (*Bundle, error) {
	manifestBytes, readManifestErr := readBundleFile(root, "manifest.json", maxBundleFileBytes)
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
	if requestedVersion > 0 && requestedVersion != manifest.Version {
		return nil, &contract.ToolError{
			Code:    contract.ErrVersionNotFound,
			Message: fmt.Sprintf("tool %q version v%d not found (active: v%d)", manifest.Name, requestedVersion, manifest.Version),
		}
	}

	sourceBytes, readSrcErr := readBundleFile(root, "tool.js", maxBundleFileBytes)
	if readSrcErr != nil {
		return nil, readSrcErr
	}
	if len(strings.TrimSpace(string(sourceBytes))) == 0 {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: "tool.js is empty",
		}
	}

	approval, appErr := readOptionalApproval(root, "approval.json")
	if appErr != nil {
		return nil, appErr
	}

	return &Bundle{
		Name:              manifest.Name,
		Version:           manifest.Version,
		ActiveVersion:     manifest.Version,
		AvailableVersions: []int{manifest.Version},
		Scope:             scope,
		Dir:               canonicalToolDir,
		ToolDir:           canonicalToolDir,
		Manifest:          manifest,
		Source:            string(sourceBytes),
		Approval:          approval,
	}, nil
}

func readOptionalApproval(root *os.Root, relPath string) (*contract.ApprovalRecord, error) {
	_, lstatErr := root.Lstat(relPath)
	if errors.Is(lstatErr, fs.ErrNotExist) {
		return nil, nil
	}
	if lstatErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("inspect %s: %v", relPath, lstatErr),
		}
	}
	raw, readErr := readBundleFile(root, relPath, maxMetadataFileBytes)
	if readErr != nil {
		return nil, readErr
	}
	var rec contract.ApprovalRecord
	if decErr := decodeStrictJSON(raw, &rec, relPath); decErr != nil {
		return nil, decErr
	}
	return &rec, nil
}

func readBundleFile(root *os.Root, filename string, maxBytes int64) ([]byte, error) {
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
	if statErr != nil || info.IsDir() || !info.Mode().IsRegular() {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("bundle %s is not a regular file", filename),
		}
	}

	data, readErr := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if readErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("read bundle %s: %v", filename, readErr),
		}
	}
	if int64(len(data)) > maxBytes {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("bundle %s exceeds %d byte limit", filename, maxBytes),
		}
	}
	return data, nil
}

func decodeStrictJSON(data []byte, target any, label string) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("decode %s: %v", label, err),
		}
	}
	var trailing any
	if trailErr := dec.Decode(&trailing); !errors.Is(trailErr, io.EOF) {
		return &contract.ToolError{
			Code:    contract.ErrInvalidManifest,
			Message: fmt.Sprintf("%s contains trailing JSON data", label),
		}
	}
	return nil
}

func displayToolName(expectedName, toolDir string) string {
	if expectedName != "" {
		return expectedName
	}
	return toolDir
}
