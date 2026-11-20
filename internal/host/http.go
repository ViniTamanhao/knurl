package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

const (
	// DefaultHTTPTimeoutMs is the default per-request timeout for knurl.http (10s).
	DefaultHTTPTimeoutMs = 10000
	// MaxHTTPBodyBytes is the maximum response body retained by knurl.http (1 MiB).
	MaxHTTPBodyBytes = 1048576
	// MaxHTTPRequestBytes is the maximum request body size accepted by knurl.http.post (1 MiB).
	MaxHTTPRequestBytes = 1048576

	maxHTTPRedirects = 10
)

var blockedSSRFPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
}

// HTTPOptions configures an outbound knurl.http request.
type HTTPOptions struct {
	Headers    map[string]string
	TimeoutMs  int
	HasTimeout bool
}

// HTTPResponse matches the knurl.http JS return contract.
type HTTPResponse struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Truncated bool              `json:"truncated"`
}

// HTTPGet executes a domain-allowlisted synchronous HTTP GET request.
func (h *Host) HTTPGet(ctx context.Context, rawURL string, opts HTTPOptions) (HTTPResponse, error) {
	return h.doHTTP(ctx, http.MethodGet, rawURL, nil, false, opts)
}

// HTTPPost executes a domain-allowlisted synchronous HTTP POST request.
func (h *Host) HTTPPost(ctx context.Context, rawURL string, body []byte, isJSON bool, opts HTTPOptions) (HTTPResponse, error) {
	return h.doHTTP(ctx, http.MethodPost, rawURL, body, isJSON, opts)
}

func (h *Host) doHTTP(
	ctx context.Context,
	method string,
	rawURL string,
	body []byte,
	isJSON bool,
	opts HTTPOptions,
) (HTTPResponse, error) {
	if h.manifest.Capabilities.HTTP == nil || len(h.manifest.Capabilities.HTTP.Allow) == 0 {
		return HTTPResponse{}, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: "capabilities.http.allow is not granted",
		}
	}
	if method != http.MethodGet && method != http.MethodPost {
		return HTTPResponse{}, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("unsupported HTTP method %q", method),
		}
	}
	if len(body) > MaxHTTPRequestBytes {
		return HTTPResponse{}, &contract.ToolError{
			Code:    contract.ErrLimitExceeded,
			Message: fmt.Sprintf("HTTP request body size %d exceeds %d byte cap", len(body), MaxHTTPRequestBytes),
		}
	}

	timeoutMs := DefaultHTTPTimeoutMs
	if opts.HasTimeout {
		if opts.TimeoutMs <= 0 {
			return HTTPResponse{}, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("http timeout_ms must be positive, got %d", opts.TimeoutMs),
			}
		}
		timeoutMs = min(opts.TimeoutMs, contract.MaxTimeoutMs)
	}

	parsedURL, matchedRules, err := h.validateAndMatchHTTPTarget(method, rawURL)
	if err != nil {
		return HTTPResponse{}, h.redactor.ToolError(asHostToolError(err))
	}

	expandedHeaders, secretHeaderKeys, hdrErr := h.prepareHTTPHeaders(opts.Headers, matchedRules)
	if hdrErr != nil {
		return HTTPResponse{}, h.redactor.ToolError(asHostToolError(hdrErr))
	}

	if h.mocks != nil {
		return h.mockHTTP(method, rawURL, expandedHeaders)
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	var bodyReader io.Reader = http.NoBody
	if method == http.MethodPost && len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, reqErr := http.NewRequestWithContext(reqCtx, method, parsedURL.String(), bodyReader)
	if reqErr != nil {
		return HTTPResponse{}, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: h.redactor.String(fmt.Sprintf("construct HTTP request: %v", reqErr)),
		}
	}

	for k, v := range expandedHeaders {
		req.Header.Set(k, v)
	}
	if method == http.MethodPost && isJSON && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{
		Transport: h.httpTransport,
		CheckRedirect: func(nextReq *http.Request, via []*http.Request) error {
			return h.checkHTTPRedirect(nextReq, via, secretHeaderKeys)
		},
	}

	resp, doErr := client.Do(req)
	if doErr != nil {
		var toolErr *contract.ToolError
		if errors.As(doErr, &toolErr) {
			return HTTPResponse{}, h.redactor.ToolError(toolErr)
		}
		if reqCtx.Err() != nil {
			return HTTPResponse{}, &contract.ToolError{
				Code:    contract.ErrTimeout,
				Message: "HTTP request timed out",
			}
		}
		var netErr net.Error
		if errors.As(doErr, &netErr) && netErr.Timeout() {
			return HTTPResponse{}, &contract.ToolError{
				Code:    contract.ErrTimeout,
				Message: "HTTP request timed out",
			}
		}
		return HTTPResponse{}, &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: h.redactor.String(fmt.Sprintf("HTTP request failed: %v", doErr)),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxHTTPBodyBytes+1))
	if readErr != nil {
		if reqCtx.Err() != nil {
			return HTTPResponse{}, &contract.ToolError{
				Code:    contract.ErrTimeout,
				Message: "HTTP response read timed out",
			}
		}
		return HTTPResponse{}, &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: h.redactor.String(fmt.Sprintf("read HTTP response body: %v", readErr)),
		}
	}

	truncated := false
	if len(respBytes) > MaxHTTPBodyBytes {
		truncated = true
		respBytes = trimTrailingIncompleteUTF8(respBytes[:MaxHTTPBodyBytes])
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for k, vals := range resp.Header {
		respHeaders[strings.ToLower(k)] = h.redactor.String(strings.Join(vals, ", "))
	}

	return HTTPResponse{
		Status:    resp.StatusCode,
		Headers:   respHeaders,
		Body:      h.redactor.String(string(respBytes)),
		Truncated: truncated,
	}, nil
}

func (h *Host) validateAndMatchHTTPTarget(method, rawURL string) (*url.URL, []contract.HTTPRule, error) {
	if rawURL == "" {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "URL must not be empty",
		}
	}
	if hasControlChars(rawURL) {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "URL must not contain control characters",
		}
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("invalid URL: %v", err),
		}
	}
	if parsed.User != nil {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "URL userinfo is not allowed",
		}
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("URL scheme %q is not allowed", parsed.Scheme),
		}
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "URL must include a hostname",
		}
	}
	if strings.HasSuffix(host, ".") {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("hostname %q must not end with trailing dot", host),
		}
	}

	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("blocked SSRF target %q", host),
		}
	}
	if _, ipErr := netip.ParseAddr(host); ipErr == nil {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("blocked SSRF IP literal %q", host),
		}
	}

	if strings.ContainsRune(parsed.Path, '\\') || strings.ContainsRune(parsed.RawPath, '\\') {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: "URL path must not contain backslashes",
		}
	}
	for _, seg := range strings.Split(parsed.Path, "/") {
		if seg == ".." {
			return nil, nil, &contract.ToolError{
				Code:    contract.ErrCapabilityDenied,
				Message: "URL path must not contain '..' traversal segments",
			}
		}
	}
	reqPath := parsed.EscapedPath()
	if reqPath == "" {
		reqPath = "/"
	}

	httpCap := h.manifest.Capabilities.HTTP
	if httpCap == nil {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: "capabilities.http.allow is not granted",
		}
	}

	matched := make([]contract.HTTPRule, 0)
	for _, rule := range httpCap.Allow {
		if scheme != "https" && (scheme != "http" || !rule.AllowHTTP) {
			continue
		}
		if !slices.Contains(rule.Methods, method) {
			continue
		}
		if !domainMatchesHTTPRule(host, strings.ToLower(rule.Domain)) {
			continue
		}
		if len(rule.PathPrefixes) > 0 && !pathMatchesAnyHTTPPrefix(reqPath, rule.PathPrefixes) {
			continue
		}
		matched = append(matched, rule)
	}
	if len(matched) == 0 {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("%s %s is outside capabilities.http.allow", method, parsed.Redacted()),
		}
	}
	return parsed, matched, nil
}

func (h *Host) prepareHTTPHeaders(
	rawHeaders map[string]string,
	matchedRules []contract.HTTPRule,
) (map[string]string, map[string]struct{}, error) {
	if len(rawHeaders) == 0 {
		return nil, nil, nil
	}

	keys := make([]string, 0, len(rawHeaders))
	for k := range rawHeaders {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	expanded := make(map[string]string, len(rawHeaders))
	secretHeaderKeys := make(map[string]struct{})
	referencedSecrets := make(map[string]struct{})

	for _, k := range keys {
		if strings.TrimSpace(k) == "" || hasControlChars(k) {
			return nil, nil, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("invalid HTTP header name %q", k),
			}
		}
		if strings.EqualFold(k, "Host") {
			return nil, nil, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: "overriding Host header is not allowed",
			}
		}
		rawVal := rawHeaders[k]
		if hasControlChars(rawVal) {
			return nil, nil, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("HTTP header %q contains control characters", k),
			}
		}
		secretsInHeader, parseErr := extractHTTPSecretPlaceholders(rawVal)
		if parseErr != nil {
			return nil, nil, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("HTTP header %q: %v", k, parseErr),
			}
		}
		for _, sec := range secretsInHeader {
			referencedSecrets[sec] = struct{}{}
			secretHeaderKeys[http.CanonicalHeaderKey(k)] = struct{}{}
		}
	}

	if len(referencedSecrets) > 0 {
		for sec := range referencedSecrets {
			if _, declared := h.declaredSecrets[sec]; !declared {
				return nil, nil, &contract.ToolError{
					Code:    contract.ErrCapabilityDenied,
					Message: fmt.Sprintf("secret %q is not declared in manifest.secrets", sec),
				}
			}
		}
		if !anyRuleAllowsAllSecrets(matchedRules, referencedSecrets) {
			return nil, nil, &contract.ToolError{
				Code:    contract.ErrCapabilityDenied,
				Message: "referenced HTTP header secret is not permitted by matching capabilities.http.allow rule",
			}
		}
	}

	for _, k := range keys {
		val, expErr := h.expandHTTPHeaderValue(rawHeaders[k])
		if expErr != nil {
			return nil, nil, expErr
		}
		if hasControlChars(val) {
			return nil, nil, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("expanded HTTP header %q contains control characters", k),
			}
		}
		expanded[k] = val
	}
	return expanded, secretHeaderKeys, nil
}

func (h *Host) expandHTTPHeaderValue(raw string) (string, error) {
	var b strings.Builder
	pos := 0
	for pos < len(raw) {
		relStart := strings.Index(raw[pos:], "${")
		if relStart < 0 {
			b.WriteString(raw[pos:])
			break
		}
		start := pos + relStart
		b.WriteString(raw[pos:start])

		afterStart := start + 2
		relEnd := strings.IndexByte(raw[afterStart:], '}')
		if relEnd < 0 {
			return "", &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: "unclosed placeholder in HTTP header",
			}
		}
		token := raw[afterStart : afterStart+relEnd]
		name, ok := strings.CutPrefix(token, "secret:")
		if !ok || name == "" {
			return "", &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("unsupported placeholder ${%s} in HTTP header", token),
			}
		}
		if _, declared := h.declaredSecrets[name]; !declared {
			return "", &contract.ToolError{
				Code:    contract.ErrCapabilityDenied,
				Message: fmt.Sprintf("secret %q is not declared in manifest.secrets", name),
			}
		}
		secVal, exists := h.secrets[name]
		if !exists || secVal == "" {
			return "", &contract.ToolError{
				Code:    contract.ErrSecretMissing,
				Message: fmt.Sprintf("secret %s is not configured on host", name),
				Details: map[string]any{"secret": name},
			}
		}
		b.WriteString(secVal)
		pos = afterStart + relEnd + 1
	}
	return b.String(), nil
}

func extractHTTPSecretPlaceholders(s string) ([]string, error) {
	rest := s
	var names []string
	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			return names, nil
		}
		after := rest[start+2:]
		end := strings.IndexByte(after, '}')
		if end < 0 {
			return nil, fmt.Errorf("unclosed placeholder")
		}
		token := after[:end]
		if token == "" || strings.Contains(token, "${") {
			return nil, fmt.Errorf("malformed placeholder ${%s}", token)
		}
		name, ok := strings.CutPrefix(token, "secret:")
		if !ok || name == "" {
			return nil, fmt.Errorf("unsupported placeholder ${%s} (expected secret:NAME)", token)
		}
		names = append(names, name)
		rest = after[end+1:]
	}
}

func anyRuleAllowsAllSecrets(rules []contract.HTTPRule, needed map[string]struct{}) bool {
	for _, rule := range rules {
		allPresent := true
		for sec := range needed {
			if !slices.Contains(rule.Secrets, sec) {
				allPresent = false
				break
			}
		}
		if allPresent {
			return true
		}
	}
	return false
}

func (h *Host) checkHTTPRedirect(
	nextReq *http.Request,
	via []*http.Request,
	secretHeaderKeys map[string]struct{},
) error {
	if len(via) >= maxHTTPRedirects {
		return &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("exceeded maximum of %d HTTP redirects", maxHTTPRedirects),
		}
	}

	origReq := via[0]
	prevReq := via[len(via)-1]

	if (strings.EqualFold(origReq.URL.Scheme, "https") || strings.EqualFold(prevReq.URL.Scheme, "https")) &&
		strings.EqualFold(nextReq.URL.Scheme, "http") {
		return &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("HTTPS to HTTP redirect downgrade to %q is forbidden", nextReq.URL.Redacted()),
		}
	}

	origHost := strings.ToLower(origReq.URL.Hostname())
	nextHost := strings.ToLower(nextReq.URL.Hostname())
	if nextHost == "" || nextHost != origHost {
		return &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("cross-domain HTTP redirect from %q to %q is forbidden", origHost, nextHost),
		}
	}

	if _, _, err := h.validateAndMatchHTTPTarget(nextReq.Method, nextReq.URL.String()); err != nil {
		return &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("disallowed HTTP redirect target: %v", err),
		}
	}

	for key := range secretHeaderKeys {
		nextReq.Header.Del(key)
	}
	nextReq.Header.Del("Authorization")
	nextReq.Header.Del("Proxy-Authorization")
	nextReq.Header.Del("Cookie")
	return nil
}

func (h *Host) ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("invalid dial address %q: %v", addr, splitErr),
		}
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("blocked SSRF target %q", host),
		}
	}
	if _, ipErr := netip.ParseAddr(host); ipErr == nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("blocked SSRF IP literal %q", host),
		}
	}

	lookup := h.httpLookupIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	ips, lookupErr := lookup(ctx, host)
	if lookupErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("DNS lookup failed for %q: %v", host, lookupErr),
		}
	}
	if len(ips) == 0 {
		return nil, &contract.ToolError{
			Code:    contract.ErrHTTPError,
			Message: fmt.Sprintf("DNS lookup returned no addresses for %q", host),
		}
	}

	allowedIPs := make([]netip.Addr, 0, len(ips))
	for _, ipAddr := range ips {
		parsed, ok := netip.AddrFromSlice(ipAddr.IP)
		if !ok {
			return nil, &contract.ToolError{
				Code:    contract.ErrHTTPError,
				Message: fmt.Sprintf("blocked unparseable IP resolved for %q", host),
			}
		}
		parsed = parsed.Unmap()
		if isBlockedSSRFAddr(parsed) {
			return nil, &contract.ToolError{
				Code:    contract.ErrHTTPError,
				Message: fmt.Sprintf("blocked SSRF IP %s resolved for host %q", parsed.String(), host),
			}
		}
		allowedIPs = append(allowedIPs, parsed)
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var lastErr error
	for _, ip := range allowedIPs {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, &contract.ToolError{
		Code:    contract.ErrHTTPError,
		Message: fmt.Sprintf("dial %q failed: %v", host, lastErr),
	}
}

func isBlockedSSRFAddr(addr netip.Addr) bool {
	if !addr.IsValid() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsInterfaceLocalMulticast() ||
		addr.IsUnspecified() {
		return true
	}
	for _, prefix := range blockedSSRFPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func domainMatchesHTTPRule(host, pattern string) bool {
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

func pathMatchesAnyHTTPPrefix(reqPath string, prefixes []string) bool {
	if reqPath == "" {
		reqPath = "/"
	}
	for _, prefix := range prefixes {
		if prefix == "/" {
			return true
		}
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(reqPath, prefix) {
				return true
			}
			continue
		}
		if reqPath == prefix || strings.HasPrefix(reqPath, prefix+"/") {
			return true
		}
	}
	return false
}

func hasControlChars(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

func asHostToolError(err error) *contract.ToolError {
	if err == nil {
		return nil
	}
	var toolErr *contract.ToolError
	if errors.As(err, &toolErr) && toolErr != nil {
		return toolErr
	}
	return &contract.ToolError{
		Code:    contract.ErrInternalError,
		Message: err.Error(),
	}
}
