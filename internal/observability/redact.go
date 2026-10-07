package observability

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Redacted replaces a removed value.
const Redacted = "[REDACTED]"

// MaxStringLength bounds every string an observability view returns.
const MaxStringLength = 32 << 10

// secretKeys are field names whose values are never shown (normalized:
// lower case, "-" -> "_").
var secretKeys = map[string]bool{
	"api_key": true, "apikey": true, "authorization": true, "proxy_authorization": true,
	"password": true, "passwd": true, "secret": true, "client_secret": true,
	"token": true, "access_token": true, "refresh_token": true, "id_token": true, "bearer": true,
	"private_key": true, "credential": true, "credentials": true, "encrypted_data": true,
	"ciphertext": true, "cookie": true, "set_cookie": true, "x_api_key": true,
}

var secretValue = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}|\bsk-[A-Za-z0-9_-]{12,}|\bBasic\s+[A-Za-z0-9+/=]{12,}`)

// IsSecretKey reports whether a field of this name must be redacted.
func IsSecretKey(key string) bool {
	return secretKeys[strings.ReplaceAll(strings.ToLower(key), "-", "_")]
}

// RedactString removes secret-looking substrings (bearer tokens, API keys)
// and truncates very long strings.
func RedactString(s string) string {
	s = secretValue.ReplaceAllString(s, Redacted)
	if len(s) > MaxStringLength {
		cut := MaxStringLength
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…[truncated]"
	}
	return s
}

// Redact returns a copy of v with secret fields and secret-looking strings
// removed. It never mutates v.
func Redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return RedactMap(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = Redact(x)
		}
		return out
	case string:
		return RedactString(t)
	default:
		return v
	}
}

// RedactMap is Redact for objects (nil stays nil).
func RedactMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if IsSecretKey(k) {
			out[k] = Redacted
			continue
		}
		out[k] = Redact(v)
	}
	return out
}
