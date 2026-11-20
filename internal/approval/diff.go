package approval

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

func permissions(m contract.Manifest) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	add := func(capability, detail string) {
		if out[capability] == nil {
			out[capability] = map[string]bool{}
		}
		out[capability][detail] = true
	}
	if m.Name != "" && !m.ReadOnly {
		add("read_only", "false")
	}
	if m.Capabilities.FS != nil {
		for _, p := range m.Capabilities.FS.Read {
			add("fs.read", p)
		}
	}
	if m.Capabilities.Git != nil && m.Capabilities.Git.Read {
		add("git.read", "branch,status,log,diff")
	}
	if m.Capabilities.HTTP != nil {
		for _, rule := range m.Capabilities.HTTP.Allow {
			rule.Methods = slices.Clone(rule.Methods)
			rule.PathPrefixes = slices.Clone(rule.PathPrefixes)
			rule.Secrets = slices.Clone(rule.Secrets)
			slices.Sort(rule.Methods)
			slices.Sort(rule.PathPrefixes)
			slices.Sort(rule.Secrets)
			b, _ := json.Marshal(rule)
			add("http.allow", string(b))
		}
	}
	if m.Capabilities.Exec != nil {
		for _, rule := range m.Capabilities.Exec.Templates {
			rule.Description = ""
			rule.ArgsSchema = cloneArgSchemas(rule.ArgsSchema)
			b, _ := json.Marshal(rule)
			add("exec.template", string(b))
		}
	}
	for _, ref := range m.Secrets {
		ref.Description = ""
		b, _ := json.Marshal(ref)
		add("secret", string(b))
	}
	return out
}

// Diff returns deterministic permission deltas, conservatively flagging all additions.
func Diff(before, after contract.Manifest) contract.CapabilityDiff {
	a, b := permissions(before), permissions(after)
	caps := map[string]bool{}
	for c := range a {
		caps[c] = true
	}
	for c := range b {
		caps[c] = true
	}
	keys := []string{}
	for c := range caps {
		keys = append(keys, c)
	}
	slices.Sort(keys)
	d := contract.CapabilityDiff{ElevatedBefore: before.Name != "" && before.IsElevated(), ElevatedAfter: after.IsElevated(), Changes: []contract.CapabilityChange{}}
	for _, c := range keys {
		details := map[string]bool{}
		for p := range a[c] {
			details[p] = true
		}
		for p := range b[c] {
			details[p] = true
		}
		paths := []string{}
		for p := range details {
			paths = append(paths, p)
		}
		slices.Sort(paths)
		for _, p := range paths {
			kind := "unchanged"
			if !a[c][p] {
				kind = "added"
				d.HasEscalation = d.HasEscalation || !permissionCovered(c, p, before)
			} else if !b[c][p] {
				kind = "removed"
			}
			d.Changes = append(d.Changes, contract.CapabilityChange{Kind: kind, Capability: c, Detail: p})
		}
	}
	d.HasEscalation = d.HasEscalation || (!d.ElevatedBefore && d.ElevatedAfter)
	return d
}

func cloneArgSchemas(in map[string]contract.ExecArgSchema) map[string]contract.ExecArgSchema {
	out := make(map[string]contract.ExecArgSchema, len(in))
	for name, spec := range in {
		spec.Description = ""
		spec.Enum = slices.Clone(spec.Enum)
		slices.Sort(spec.Enum)
		out[name] = spec
	}
	return out
}

// SourceDiff emits a unified hunk with surrounding context without external programs.
func SourceDiff(before, after string) string {
	if before == after {
		return ""
	}
	lines := func(source string) []string {
		if source == "" {
			return nil
		}
		parts := strings.SplitAfter(source, "\n")
		if parts[len(parts)-1] == "" {
			parts = parts[:len(parts)-1]
		}
		return parts
	}
	old := lines(before)
	newLines := lines(after)
	prefix := 0
	for prefix < min(len(old), len(newLines)) && old[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < min(len(old), len(newLines))-prefix && old[len(old)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	start := max(0, prefix-3)
	oldEnd, newEnd := len(old)-suffix, len(newLines)-suffix
	contextEnd := min(suffix, 3)
	var out strings.Builder
	emit := func(sign, line string) {
		out.WriteString(sign)
		out.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			out.WriteString("\n\\ No newline at end of file\n")
		}
	}
	oldStart, newStart := start+1, start+1
	if len(old) == 0 {
		oldStart = 0
	}
	if len(newLines) == 0 {
		newStart = 0
	}
	fmt.Fprintf(&out, "--- a/tool.js\n+++ b/tool.js\n@@ -%d,%d +%d,%d @@\n", oldStart, oldEnd+contextEnd-start, newStart, newEnd+contextEnd-start)
	for _, line := range old[start:prefix] {
		emit(" ", line)
	}
	for _, line := range old[prefix:oldEnd] {
		emit("-", line)
	}
	for _, line := range newLines[prefix:newEnd] {
		emit("+", line)
	}
	for _, line := range old[oldEnd : oldEnd+contextEnd] {
		emit(" ", line)
	}
	return out.String()
}
