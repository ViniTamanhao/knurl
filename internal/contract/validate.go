package contract

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var (
	argKeyRegexp     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	secretNameRegexp = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
)

// ValidateManifestSemantic enforces cross-field and security invariants on a
// parsed Manifest that cannot be expressed purely in JSON Schema.
func ValidateManifestSemantic(m Manifest) error {
	declaredSecrets, err := collectDeclaredSecrets(m.Secrets)
	if err != nil {
		return err
	}
	if err = validateFSManifest(m.Capabilities.FS); err != nil {
		return err
	}
	if err = validateHTTPManifest(m.ReadOnly, m.Capabilities.HTTP, declaredSecrets); err != nil {
		return err
	}
	return validateExecManifest(m.ReadOnly, m.Capabilities.Exec, declaredSecrets)
}

func collectDeclaredSecrets(secrets []SecretRef) (map[string]struct{}, error) {
	declared := make(map[string]struct{}, len(secrets))
	for _, s := range secrets {
		if _, dup := declared[s.Name]; dup {
			return nil, fmt.Errorf("duplicate secret declaration %q", s.Name)
		}
		declared[s.Name] = struct{}{}
	}
	return declared, nil
}

func validateFSManifest(fsCap *FSCapability) error {
	if fsCap == nil {
		return nil
	}
	for _, pat := range fsCap.Read {
		if pat == "" {
			return fmt.Errorf("capabilities.fs.read contains an empty pattern")
		}
		if strings.HasPrefix(pat, "/") || strings.Contains(pat, `\`) {
			return fmt.Errorf("capabilities.fs.read pattern %q must be a relative slash-separated glob", pat)
		}
		if slices.Contains(strings.Split(pat, "/"), "..") {
			return fmt.Errorf("capabilities.fs.read pattern %q must not contain '..' segments", pat)
		}
	}
	return nil
}

func validateHTTPManifest(readOnly bool, httpCap *HTTPCapability, declaredSecrets map[string]struct{}) error {
	if httpCap == nil {
		return nil
	}
	for i, rule := range httpCap.Allow {
		if readOnly && slices.Contains(rule.Methods, "POST") {
			return fmt.Errorf("capabilities.http.allow[%d] (%s): POST method is incompatible with read_only=true", i, rule.Domain)
		}
		for _, sec := range rule.Secrets {
			if _, ok := declaredSecrets[sec]; !ok {
				return fmt.Errorf("capabilities.http.allow[%d] (%s): references undeclared secret %q", i, rule.Domain, sec)
			}
		}
	}
	return nil
}

func validateExecManifest(readOnly bool, execCap *ExecCapability, declaredSecrets map[string]struct{}) error {
	if execCap == nil {
		return nil
	}
	seenIDs := make(map[string]struct{}, len(execCap.Templates))
	for _, tmpl := range execCap.Templates {
		if _, dup := seenIDs[tmpl.ID]; dup {
			return fmt.Errorf("duplicate exec template id %q", tmpl.ID)
		}
		seenIDs[tmpl.ID] = struct{}{}

		if err := validateExecTemplate(readOnly, tmpl, declaredSecrets); err != nil {
			return err
		}
	}
	return nil
}

func validateExecTemplate(readOnly bool, tmpl ExecTemplate, declaredSecrets map[string]struct{}) error {
	if readOnly && !tmpl.ReadOnly {
		return fmt.Errorf("exec template %q has read_only=false on a read_only=true tool", tmpl.ID)
	}
	if len(tmpl.Args) == 0 {
		return fmt.Errorf("exec template %q must specify at least one argument", tmpl.ID)
	}

	firstArgs, firstSecrets, err := extractPlaceholders(tmpl.Args[0])
	if err != nil {
		return fmt.Errorf("exec template %q args[0]: %w", tmpl.ID, err)
	}
	if len(firstArgs) > 0 || len(firstSecrets) > 0 {
		return fmt.Errorf("exec template %q args[0] (%q) must be a literal string with no placeholders", tmpl.ID, tmpl.Args[0])
	}

	referencedArgs := make(map[string]struct{})
	for idx, arg := range tmpl.Args {
		argKeys, secNames, parseErr := extractPlaceholders(arg)
		if parseErr != nil {
			return fmt.Errorf("exec template %q args[%d]: %w", tmpl.ID, idx, parseErr)
		}
		for _, sec := range secNames {
			if _, ok := declaredSecrets[sec]; !ok {
				return fmt.Errorf("exec template %q args[%d]: references undeclared secret %q", tmpl.ID, idx, sec)
			}
		}
		for _, key := range argKeys {
			if _, ok := tmpl.ArgsSchema[key]; !ok {
				return fmt.Errorf("exec template %q args[%d]: placeholder ${arg:%s} missing from args_schema", tmpl.ID, idx, key)
			}
			referencedArgs[key] = struct{}{}
		}
	}

	for key, spec := range tmpl.ArgsSchema {
		if _, used := referencedArgs[key]; !used {
			return fmt.Errorf("exec template %q args_schema[%q] is not referenced in args", tmpl.ID, key)
		}
		if argErr := validateArgSchema(tmpl.ID, key, spec); argErr != nil {
			return argErr
		}
	}

	for envKey, envVal := range tmpl.Env {
		argKeys, secNames, parseErr := extractPlaceholders(envVal)
		if parseErr != nil {
			return fmt.Errorf("exec template %q env[%s]: %w", tmpl.ID, envKey, parseErr)
		}
		if len(argKeys) > 0 {
			return fmt.Errorf("exec template %q env[%s]: ${arg:...} placeholders are not permitted in env", tmpl.ID, envKey)
		}
		for _, sec := range secNames {
			if _, ok := declaredSecrets[sec]; !ok {
				return fmt.Errorf("exec template %q env[%s]: references undeclared secret %q", tmpl.ID, envKey, sec)
			}
		}
	}

	return nil
}

func validateArgSchema(templateID, key string, spec ExecArgSchema) error {
	if _, err := regexp.Compile(spec.Pattern); err != nil {
		return fmt.Errorf("exec template %q args_schema[%q] invalid RE2 pattern %q: %w", templateID, key, spec.Pattern, err)
	}
	anchored, err := regexp.Compile(`\A(?:` + spec.Pattern + `)\z`)
	if err != nil {
		return fmt.Errorf("exec template %q args_schema[%q] anchored RE2 compile failed: %w", templateID, key, err)
	}
	for _, val := range spec.Enum {
		if spec.MaxLength > 0 && len(val) > spec.MaxLength {
			return fmt.Errorf("exec template %q args_schema[%q] enum value %q exceeds max_length %d", templateID, key, val, spec.MaxLength)
		}
		if !anchored.MatchString(val) {
			return fmt.Errorf("exec template %q args_schema[%q] enum value %q does not match pattern %q", templateID, key, val, spec.Pattern)
		}
	}
	return nil
}

func extractPlaceholders(s string) (argKeys, secretNames []string, err error) {
	rest := s
	for {
		start := strings.Index(rest, "${")
		if start == -1 {
			return argKeys, secretNames, nil
		}
		after := rest[start+2:]
		end := strings.IndexByte(after, '}')
		if end == -1 {
			return nil, nil, fmt.Errorf("unclosed placeholder in %q", s)
		}
		token := after[:end]
		if strings.Contains(token, "${") || token == "" {
			return nil, nil, fmt.Errorf("malformed placeholder ${%s} in %q", token, s)
		}
		switch {
		case strings.HasPrefix(token, "arg:"):
			key := strings.TrimPrefix(token, "arg:")
			if !argKeyRegexp.MatchString(key) {
				return nil, nil, fmt.Errorf("invalid arg placeholder key %q", key)
			}
			argKeys = append(argKeys, key)
		case strings.HasPrefix(token, "secret:"):
			name := strings.TrimPrefix(token, "secret:")
			if !secretNameRegexp.MatchString(name) {
				return nil, nil, fmt.Errorf("invalid secret placeholder name %q", name)
			}
			secretNames = append(secretNames, name)
		default:
			return nil, nil, fmt.Errorf("unsupported placeholder ${%s} (expected arg: or secret:)", token)
		}
		rest = after[end+1:]
	}
}

// ValidateTestSuiteSemantic enforces manifest cross-references and uniqueness
// across a parsed TestSuite.
func ValidateTestSuiteSemantic(m Manifest, ts TestSuite) error {
	declaredSecrets := make(map[string]struct{}, len(m.Secrets))
	for _, s := range m.Secrets {
		declaredSecrets[s.Name] = struct{}{}
	}

	execTemplates := make(map[string]ExecTemplate)
	if m.Capabilities.Exec != nil {
		for _, tmpl := range m.Capabilities.Exec.Templates {
			execTemplates[tmpl.ID] = tmpl
		}
	}

	seenCases := make(map[string]struct{}, len(ts.Cases))
	for _, tc := range ts.Cases {
		if _, dup := seenCases[tc.Name]; dup {
			return fmt.Errorf("duplicate test case name %q", tc.Name)
		}
		seenCases[tc.Name] = struct{}{}

		if err := validateCaseMocks(tc, m.Capabilities.HTTP, declaredSecrets, execTemplates); err != nil {
			return fmt.Errorf("test case %q: %w", tc.Name, err)
		}
	}
	return nil
}

func validateCaseMocks(
	tc TestCase,
	httpCap *HTTPCapability,
	declaredSecrets map[string]struct{},
	execTemplates map[string]ExecTemplate,
) error {
	if tc.Mocks == nil {
		return nil
	}
	for _, sec := range tc.Mocks.Secrets {
		if _, ok := declaredSecrets[sec]; !ok {
			return fmt.Errorf("mocks.secrets references undeclared secret %q", sec)
		}
	}
	for i, em := range tc.Mocks.Exec {
		tmpl, ok := execTemplates[em.TemplateID]
		if !ok {
			return fmt.Errorf("mocks.exec[%d] references undeclared template_id %q", i, em.TemplateID)
		}
		if em.Result != nil {
			for k := range em.Params {
				if _, exists := tmpl.ArgsSchema[k]; !exists {
					return fmt.Errorf("mocks.exec[%d] param %q not declared in template %q args_schema", i, k, em.TemplateID)
				}
			}
		}
	}
	for i, hm := range tc.Mocks.HTTP {
		if tc.Expect.OK && hm.Result != nil {
			if !httpMockMatchesManifest(hm, httpCap) {
				return fmt.Errorf("mocks.http[%d] (%s %s) does not match capabilities.http.allow", i, hm.Method, hm.URL)
			}
		}
	}
	return nil
}

func httpMockMatchesManifest(hm HTTPMock, httpCap *HTTPCapability) bool {
	if httpCap == nil {
		return false
	}
	parsed, err := url.Parse(hm.URL)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	scheme := strings.ToLower(parsed.Scheme)
	reqPath := parsed.EscapedPath()
	if reqPath == "" {
		reqPath = "/"
	}

	for _, rule := range httpCap.Allow {
		if scheme != "https" && (scheme != "http" || !rule.AllowHTTP) {
			continue
		}
		if !slices.Contains(rule.Methods, hm.Method) {
			continue
		}
		if !domainMatchesRule(host, rule.Domain) {
			continue
		}
		if len(rule.PathPrefixes) > 0 && !pathMatchesAnyPrefix(reqPath, rule.PathPrefixes) {
			continue
		}
		return true
	}
	return false
}

func domainMatchesRule(host, pattern string) bool {
	if after, ok := strings.CutPrefix(pattern, "*."); ok {
		suffix := "." + after
		if !strings.HasSuffix(host, suffix) {
			return false
		}
		prefix := strings.TrimSuffix(host, suffix)
		return prefix != "" && !strings.Contains(prefix, ".")
	}
	return host == pattern
}

func pathMatchesAnyPrefix(reqPath string, prefixes []string) bool {
	if reqPath == "" {
		reqPath = "/"
	}
	for _, p := range prefixes {
		if p == "/" {
			return true
		}
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(reqPath, p) {
				return true
			}
			continue
		}
		if reqPath == p || strings.HasPrefix(reqPath, p+"/") {
			return true
		}
	}
	return false
}
