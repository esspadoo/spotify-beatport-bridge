package credentials

import "regexp"

const Redacted = "[REDACTED]"

var secretPatterns = []*regexp.Regexp{
	// HTTP Authorization headers and log-friendly key/value variants.
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer|basic)\s+)[^\s,;]+`),
	regexp.MustCompile(`(?i)(\b(?:access[_-]?token|refresh[_-]?token|client[_-]?secret|password|sessionid)\b\s*[:=]\s*)[^\s,;]+`),
	// JSON string values. Kept separate so the closing quote remains valid.
	regexp.MustCompile(`(?i)("(?:access_token|refresh_token|client_secret|password|sessionid)"\s*:\s*")[^"]*(")`),
	// Cookie headers can contain several values; redact just Beatport's session.
	regexp.MustCompile(`(?i)(\bsessionid=)[^;\s]+`),
}

// RedactSecret returns a constant marker and never reveals length or a prefix.
func RedactSecret(value string) string {
	if value == "" {
		return ""
	}
	return Redacted
}

// RedactSecrets removes common OAuth/password/cookie material from error and
// debug text while leaving surrounding context useful.
func RedactSecrets(value string) string {
	for _, pattern := range secretPatterns {
		value = pattern.ReplaceAllString(value, `${1}`+Redacted+`${2}`)
	}
	return value
}
