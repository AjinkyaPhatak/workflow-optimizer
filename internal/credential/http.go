package credential

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// ProviderHTTP is the provider of credentials for the HTTP request node
// (Phase C7). Other providers' credentials (e.g. an LLM API key) can never
// be used there, so they cannot be sent to an arbitrary URL; an HTTP
// credential is bound to the hosts it was created for.
const ProviderHTTP = "http"

// MaxAllowedHosts bounds an HTTP credential's host list.
const MaxAllowedHosts = 20

var hostPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeHost turns an allowed-host entry into a lowercase host pattern:
// "api.example.com" or "*.example.com" (any subdomain, not the domain
// itself). A pasted URL is reduced to its host; ports and paths are not
// part of a pattern.
func NormalizeHost(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("%w: %q is not a host", ErrInvalid, s)
		}
		s = u.Hostname()
	}
	s = strings.TrimSuffix(s, "/")
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	if net.ParseIP(s) == nil && (!hostPattern.MatchString(s) || !strings.Contains(strings.TrimPrefix(s, "*."), ".") && s != "localhost") {
		return "", fmt.Errorf("%w: %q is not a host name like api.example.com or *.example.com", ErrInvalid, s)
	}
	return s, nil
}

// HostAllowed reports whether host matches one of the patterns.
func HostAllowed(patterns []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range patterns {
		if rest, ok := strings.CutPrefix(p, "*."); ok {
			if strings.HasSuffix(host, "."+rest) {
				return true
			}
		} else if host == p {
			return true
		}
	}
	return false
}

func httpCredentialHosts(in CreateInput) ([]string, error) {
	switch in.Type {
	case TypeAPIKey, TypeBearerToken:
	case TypeBasicAuth:
		if !strings.Contains(in.Secret.Reveal(), ":") {
			return nil, fmt.Errorf("%w: a basic auth secret is username:password", ErrInvalid)
		}
	default:
		return nil, fmt.Errorf("%w: an HTTP credential is an API key, a bearer token or basic auth", ErrInvalid)
	}
	if len(in.AllowedHosts) == 0 {
		return nil, fmt.Errorf("%w: an HTTP credential needs the hosts it may be sent to", ErrInvalid)
	}
	if len(in.AllowedHosts) > MaxAllowedHosts {
		return nil, fmt.Errorf("%w: at most %d allowed hosts", ErrInvalid, MaxAllowedHosts)
	}
	seen := map[string]bool{}
	var out []string
	for _, h := range in.AllowedHosts {
		n, err := NormalizeHost(h)
		if err != nil {
			return nil, err
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}
