package host

import (
	"fmt"
	"os"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

var sanitizedEnvKeys = []string{
	"PATH",
	"HOME",
	"LANG",
	"LC_ALL",
	"TERM",
	"TZ",
}

// SanitizedBaseEnv builds the minimal deterministic environment passed to
// isolated _worker and constrained exec child processes.
func SanitizedBaseEnv(lookup func(string) (string, bool)) []string {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	env := make([]string, 0, len(sanitizedEnvKeys))
	for _, key := range sanitizedEnvKeys {
		if val, ok := lookup(key); ok {
			env = append(env, key+"="+val)
		}
	}
	return env
}

// ResolveSecrets resolves declared manifest secrets from the host environment.
// Required secrets that are unset or empty fail closed with ErrSecretMissing.
func ResolveSecrets(refs []contract.SecretRef, lookup func(string) (string, bool)) (map[string]string, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	resolved := make(map[string]string, len(refs))
	for _, ref := range refs {
		val, ok := lookup(ref.Name)
		if !ok || val == "" {
			if ref.Required {
				return nil, &contract.ToolError{
					Code:    contract.ErrSecretMissing,
					Message: fmt.Sprintf("required secret %s is not configured on host", ref.Name),
					Details: map[string]any{"secret": ref.Name},
				}
			}
			continue
		}
		resolved[ref.Name] = val
	}
	return resolved, nil
}
