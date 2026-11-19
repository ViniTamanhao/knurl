package host

import (
	"encoding/base64"
	"net/url"
	"sort"
	"strings"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

const minRedactSecretLength = 4

type redactNeedle struct {
	needle      string
	placeholder string
	secretName  string
}

// Redactor scrubs resolved secret values (and common encoded forms) from text,
// structured outputs, diagnostics, and errors before leaving the Go boundary.
type Redactor struct {
	replacer *strings.Replacer
}

// NewRedactor constructs a Redactor for the provided resolved secret values.
func NewRedactor(secrets map[string]string) *Redactor {
	if len(secrets) == 0 {
		return &Redactor{}
	}

	// Deterministic secret iteration order so equal secret values map deterministically.
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)

	seen := make(map[string]string)
	needles := make([]redactNeedle, 0, len(names)*7)
	for _, name := range names {
		val := secrets[name]
		if len(val) < minRedactSecretLength {
			continue
		}
		placeholder := "[REDACTED:" + name + "]"
		variants := []string{
			val,
			base64.StdEncoding.EncodeToString([]byte(val)),
			base64.RawStdEncoding.EncodeToString([]byte(val)),
			base64.URLEncoding.EncodeToString([]byte(val)),
			base64.RawURLEncoding.EncodeToString([]byte(val)),
			url.QueryEscape(val),
			url.PathEscape(val),
		}
		for _, variant := range variants {
			if len(variant) < minRedactSecretLength {
				continue
			}
			if _, exists := seen[variant]; exists {
				continue
			}
			seen[variant] = placeholder
			needles = append(needles, redactNeedle{
				needle:      variant,
				placeholder: placeholder,
				secretName:  name,
			})
		}
	}

	if len(needles) == 0 {
		return &Redactor{}
	}

	// Sort longest needle first so encoded/longer forms match before substrings.
	sort.Slice(needles, func(i, j int) bool {
		if len(needles[i].needle) != len(needles[j].needle) {
			return len(needles[i].needle) > len(needles[j].needle)
		}
		if needles[i].secretName != needles[j].secretName {
			return needles[i].secretName < needles[j].secretName
		}
		return needles[i].needle < needles[j].needle
	})

	pairs := make([]string, 0, len(needles)*2)
	for _, n := range needles {
		pairs = append(pairs, n.needle, n.placeholder)
	}
	return &Redactor{replacer: strings.NewReplacer(pairs...)}
}

// String replaces any secret occurrences in s with [REDACTED:<NAME>].
func (r *Redactor) String(s string) string {
	if r == nil || r.replacer == nil || s == "" {
		return s
	}
	return r.replacer.Replace(s)
}

// Value recursively scrubs strings inside maps and slices.
func (r *Redactor) Value(v any) any {
	if r == nil || r.replacer == nil || v == nil {
		return v
	}
	switch x := v.(type) {
	case string:
		return r.String(x)
	case []string:
		out := make([]string, len(x))
		for i, item := range x {
			out[i] = r.String(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = r.Value(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[r.String(k)] = r.Value(val)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(x))
		for k, val := range x {
			out[r.String(k)] = r.String(val)
		}
		return out
	default:
		return v
	}
}

// ToolError scrubs secret occurrences in a ToolError's message and details.
func (r *Redactor) ToolError(err *contract.ToolError) *contract.ToolError {
	if r == nil || r.replacer == nil || err == nil {
		return err
	}
	cloned := &contract.ToolError{
		Code:    err.Code,
		Message: r.String(err.Message),
	}
	if err.Details != nil {
		if scrubbed, ok := r.Value(err.Details).(map[string]any); ok {
			cloned.Details = scrubbed
		}
	}
	return cloned
}

// WorkerResponse scrubs secrets across output, error, and diagnostics in place.
func (r *Redactor) WorkerResponse(resp *contract.WorkerResponse) {
	if r == nil || r.replacer == nil || resp == nil {
		return
	}
	if resp.Output != nil {
		if scrubbed, ok := r.Value(resp.Output).(map[string]any); ok {
			resp.Output = scrubbed
		}
	}
	if resp.Error != nil {
		resp.Error = r.ToolError(resp.Error)
	}
	if len(resp.Diagnostics) > 0 {
		scrubbed := make([]string, len(resp.Diagnostics))
		for i, d := range resp.Diagnostics {
			scrubbed[i] = r.String(d)
		}
		resp.Diagnostics = scrubbed
	}
}
