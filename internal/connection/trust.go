package connection

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

type authority struct {
	hostname string
	port     string
}

func validateTrustedHosts(entries []string) ([]authority, error) {
	trusted := make([]authority, 0, len(entries))
	for _, entry := range entries {
		parsed, explicitPort, err := parseTrustedAuthority(entry)
		if err != nil {
			return nil, fmt.Errorf("connection: trustedHosts entry %q is not a bare host[:port] authority", entry)
		}
		if !explicitPort {
			parsed.port = "*"
		}
		trusted = append(trusted, parsed)
	}
	return trusted, nil
}

func isTrustedAPIRequest(httpRequest *http.Request, trusted []authority) bool {
	hostAuthority, err := parseRequestAuthority(httpRequest.Host)
	if err != nil {
		return false
	}
	if !isLoopback(hostAuthority.hostname) && !matchesTrusted(hostAuthority, trusted) {
		return false
	}
	if httpRequest.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := httpRequest.Header.Get("Origin")
	if origin == "" {
		return true
	}
	originURL, err := url.Parse(origin)
	if err != nil || originURL.Scheme == "" || originURL.Host == "" || originURL.User != nil {
		return false
	}
	originAuthority, err := normalizeURLAuthority(originURL)
	if err != nil {
		return false
	}
	return originAuthority == hostAuthority
}

func parseTrustedAuthority(entry string) (authority, bool, error) {
	if entry == "" || strings.TrimSpace(entry) != entry || strings.IndexFunc(entry, unicode.IsSpace) >= 0 {
		return authority{}, false, errorsInvalidAuthority()
	}
	if strings.ContainsAny(entry, "/?#@\\") || strings.HasSuffix(entry, ":") {
		return authority{}, false, errorsInvalidAuthority()
	}
	parsedURL, err := url.Parse("http://" + entry)
	if err != nil || parsedURL.Host == "" || parsedURL.Path != "" || parsedURL.User != nil {
		return authority{}, false, errorsInvalidAuthority()
	}
	explicitPort := authorityHasPort(entry)
	port := parsedURL.Port()
	if explicitPort {
		rawPort := rawAuthorityPort(entry)
		if rawPort == "" || (len(rawPort) > 1 && rawPort[0] == '0') || rawPort != port {
			return authority{}, false, errorsInvalidAuthority()
		}
		portNumber, conversionErr := strconv.Atoi(port)
		if conversionErr != nil || portNumber < 0 || portNumber > 65535 {
			return authority{}, false, errorsInvalidAuthority()
		}
	}
	hostname, err := canonicalHostname(parsedURL.Hostname())
	if err != nil || hostname != strings.ToLower(parsedURL.Hostname()) {
		return authority{}, false, errorsInvalidAuthority()
	}
	if port == "80" {
		port = ""
	}
	return authority{hostname: hostname, port: port}, explicitPort, nil
}

func parseRequestAuthority(raw string) (authority, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return authority{}, errorsInvalidAuthority()
	}
	parsedURL, err := url.Parse("http://" + raw)
	if err != nil || parsedURL.Host == "" || parsedURL.Path != "" || parsedURL.User != nil {
		return authority{}, errorsInvalidAuthority()
	}
	hostname, err := canonicalHostname(parsedURL.Hostname())
	if err != nil {
		return authority{}, err
	}
	port := parsedURL.Port()
	if port == "80" {
		port = ""
	}
	return authority{hostname: hostname, port: port}, nil
}

func normalizeURLAuthority(parsedURL *url.URL) (authority, error) {
	hostname, err := canonicalHostname(parsedURL.Hostname())
	if err != nil {
		return authority{}, err
	}
	port := parsedURL.Port()
	if (parsedURL.Scheme == "http" && port == "80") || (parsedURL.Scheme == "https" && port == "443") {
		port = ""
	}
	return authority{hostname: hostname, port: port}, nil
}

func canonicalHostname(raw string) (string, error) {
	hostname := strings.ToLower(raw)
	if hostname == "" || strings.ContainsAny(hostname, " %") {
		return "", errorsInvalidAuthority()
	}
	if parsedIP := net.ParseIP(hostname); parsedIP != nil {
		canonical := parsedIP.String()
		if strings.Contains(hostname, ":") {
			return canonical, nil
		}
		if canonical != hostname {
			return "", errorsInvalidAuthority()
		}
		return canonical, nil
	}
	parts := strings.Split(hostname, ".")
	if len(parts) == 4 {
		numericAddress := true
		for _, part := range parts {
			if !isNumericAddressPart(part) {
				numericAddress = false
				break
			}
		}
		if numericAddress {
			return "", errorsInvalidAuthority()
		}
	}
	for _, character := range hostname {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '.' {
			continue
		}
		return "", errorsInvalidAuthority()
	}
	return hostname, nil
}

func isNumericAddressPart(part string) bool {
	if part == "" {
		return false
	}
	if strings.HasPrefix(part, "0x") {
		if len(part) == 2 {
			return false
		}
		for _, character := range part[2:] {
			if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
				return false
			}
		}
		return true
	}
	for _, character := range part {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func authorityHasPort(raw string) bool {
	if strings.HasPrefix(raw, "[") {
		closing := strings.LastIndex(raw, "]")
		return closing >= 0 && closing+1 < len(raw) && raw[closing+1] == ':'
	}
	return strings.Count(raw, ":") == 1
}

func rawAuthorityPort(raw string) string {
	if strings.HasPrefix(raw, "[") {
		closing := strings.LastIndex(raw, "]")
		if closing < 0 || closing+2 > len(raw) {
			return ""
		}
		return raw[closing+2:]
	}
	_, port, err := net.SplitHostPort(raw)
	if err == nil {
		return port
	}
	separator := strings.LastIndex(raw, ":")
	if separator < 0 {
		return ""
	}
	return raw[separator+1:]
}

func matchesTrusted(target authority, trusted []authority) bool {
	for _, candidate := range trusted {
		if candidate.hostname != target.hostname {
			continue
		}
		if candidate.port == "*" || candidate.port == target.port {
			return true
		}
	}
	return false
}

func isLoopback(hostname string) bool {
	if hostname == "localhost" || hostname == "::1" {
		return true
	}
	parsedIP := net.ParseIP(hostname)
	return parsedIP != nil && parsedIP.To4() != nil && parsedIP.To4()[0] == 127
}

func errorsInvalidAuthority() error {
	return fmt.Errorf("invalid authority")
}
