package approval

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

func subsetStrings(after, before []string) bool {
	for _, v := range after {
		if !slices.Contains(before, v) {
			return false
		}
	}
	return true
}
func pathCovered(path string, before []string) bool {
	if len(before) == 0 {
		return true
	}
	for _, p := range before {
		if p == "/" || path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}
func domainCovered(after, before string) bool {
	if after == before {
		return true
	}
	if strings.HasPrefix(before, "*.") && !strings.HasPrefix(after, "*.") {
		suffix := strings.TrimPrefix(before, "*")
		label, ok := strings.CutSuffix(after, suffix)
		return ok && label != "" && !strings.Contains(label, ".")
	}
	return false
}
func httpCovered(after, before contract.HTTPRule) bool {
	if !domainCovered(after.Domain, before.Domain) || after.AllowHTTP && !before.AllowHTTP || !subsetStrings(after.Methods, before.Methods) || !subsetStrings(after.Secrets, before.Secrets) {
		return false
	}
	if len(after.PathPrefixes) == 0 {
		return len(before.PathPrefixes) == 0
	}
	for _, p := range after.PathPrefixes {
		if !pathCovered(p, before.PathPrefixes) {
			return false
		}
	}
	return true
}
func argCovered(after, before contract.ExecArgSchema) bool {
	if len(after.Enum) > 0 {
		pattern, err := regexp.Compile(`\A(?:` + before.Pattern + `)\z`)
		if err != nil {
			return false
		}
		for _, v := range after.Enum {
			if !pattern.MatchString(v) || (before.MaxLength > 0 && len(v) > before.MaxLength) || (len(before.Enum) > 0 && !slices.Contains(before.Enum, v)) {
				return false
			}
		}
		return true
	}
	return len(before.Enum) == 0 && after.Pattern == before.Pattern && (before.MaxLength == 0 || (after.MaxLength > 0 && after.MaxLength <= before.MaxLength))
}
func execCovered(after, before contract.ExecTemplate) bool {
	if after.ID != before.ID || after.Program != before.Program || !slices.Equal(after.Args, before.Args) || !maps.Equal(after.Env, before.Env) || before.ReadOnly && !after.ReadOnly {
		return false
	}
	for name, old := range before.ArgsSchema {
		next, ok := after.ArgsSchema[name]
		if !ok || !argCovered(next, old) {
			return false
		}
	}
	return true
}
func permissionCovered(capability, detail string, before contract.Manifest) bool {
	switch capability {
	case "http.allow":
		var next contract.HTTPRule
		if json.Unmarshal([]byte(detail), &next) != nil || before.Capabilities.HTTP == nil {
			return false
		}
		for _, old := range before.Capabilities.HTTP.Allow {
			if httpCovered(next, old) {
				return true
			}
		}
	case "exec.template":
		var next contract.ExecTemplate
		if json.Unmarshal([]byte(detail), &next) != nil || before.Capabilities.Exec == nil {
			return false
		}
		for _, old := range before.Capabilities.Exec.Templates {
			if execCovered(next, old) {
				return true
			}
		}
	case "secret":
		var next contract.SecretRef
		if json.Unmarshal([]byte(detail), &next) != nil {
			return false
		}
		for _, old := range before.Secrets {
			if old.Name == next.Name && (!next.Required || old.Required) {
				return true
			}
		}
	case "fs.read":
		if before.Capabilities.FS != nil {
			for _, old := range before.Capabilities.FS.Read {
				if detail == old || old == "**" {
					return true
				}
				if strings.HasSuffix(old, "/**") {
					prefix := strings.TrimSuffix(old, "**")
					if !strings.ContainsAny(prefix, "*?[") && strings.HasPrefix(detail, prefix) {
						return true
					}
				}
			}
		}
	}
	return false
}
