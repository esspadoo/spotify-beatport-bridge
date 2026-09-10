package matcher

import (
	"regexp"
	"strings"
)

var (
	trailingDecorationPattern  = regexp.MustCompile(`(?s)^(.*?)[[:space:]]*[\(\[]([^\(\)\[\]]+)[\)\]][[:space:]]*$`)
	inlineFeaturingPattern     = regexp.MustCompile(`(?i)^(.*?)[[:space:]]+(?:feat\.?|ft\.?|featuring)[[:space:]]+(.+?)$`)
	artistSeparatorPattern     = regexp.MustCompile(`(?i)[[:space:]]*(?:,|;|[[:space:]]+(?:feat\.?|ft\.?|featuring)[[:space:]]+)[[:space:]]*`)
	textArtistSeparatorPattern = regexp.MustCompile(`(?i)(?:[[:space:]]*[,;][[:space:]]*|[[:space:]]+(?:&|\+|x|vs\.?|feat\.?|ft\.?|featuring)[[:space:]]+)`)
)

// TitleParts separates searchable title text from mix/version and featured
// artist metadata while leaving the caller's original value untouched.
type TitleParts struct {
	Base            string
	Version         string
	FeaturedArtists []string
}

// ExtractTitleParts recognizes conventional bracketed and dash-suffixed mix
// labels. Unknown parentheticals remain part of the title.
func ExtractTitleParts(title string) TitleParts {
	base := strings.TrimSpace(title)
	parts := TitleParts{Base: base}

	// Decorations can be stacked, e.g. "Song (Extended Mix) (feat. A)".
	for {
		match := trailingDecorationPattern.FindStringSubmatch(base)
		if len(match) == 0 {
			break
		}
		decoration := strings.TrimSpace(match[2])
		if featured, ok := featuringValue(decoration); ok {
			parts.FeaturedArtists = appendUniqueNormalized(parts.FeaturedArtists, SplitArtists(featured)...)
			base = strings.TrimSpace(match[1])
			continue
		}
		if parts.Version == "" && LooksLikeVersion(decoration) {
			parts.Version = strings.TrimSpace(decoration)
			base = strings.TrimSpace(match[1])
			continue
		}
		break
	}

	if match := inlineFeaturingPattern.FindStringSubmatch(base); len(match) > 0 {
		parts.FeaturedArtists = appendUniqueNormalized(parts.FeaturedArtists, SplitArtists(match[2])...)
		base = strings.TrimSpace(match[1])
	}

	if parts.Version == "" {
		if before, after, ok := splitTrailingVersion(base); ok {
			base = before
			parts.Version = after
		}
	}
	parts.Base = strings.TrimSpace(base)
	return parts
}

// SplitArtists handles reliable metadata delimiters while deliberately keeping
// '&' and "and" inside a name: both are common in permanent group names.
func SplitArtists(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	items := artistSeparatorPattern.Split(value, -1)
	result := make([]string, 0, len(items))
	return appendUniqueNormalized(result, items...)
}

// SplitTextArtists handles collaborator notation in a plain-text artist
// column. Ampersand, plus, x, and vs require surrounding whitespace so names
// such as ATB or an unspaced A&B are not damaged. Structured Spotify artists
// bypass this parser entirely.
func SplitTextArtists(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	items := textArtistSeparatorPattern.Split(value, -1)
	result := make([]string, 0, len(items))
	return appendUniqueNormalized(result, items...)
}

// NormalizeVersion maps common spelling variants to stable version labels.
func NormalizeVersion(version string) string {
	value := Normalize(strings.TrimSpace(version))
	switch value {
	case "extended", "extended version", "extended mix":
		return "extended mix"
	case "original", "original version", "original mix":
		return "original mix"
	case "radio", "radio version", "radio mix", "radio edit":
		return "radio edit"
	case "club", "club version", "club mix":
		return "club mix"
	case "dub", "dub version", "dub mix":
		return "dub mix"
	case "instrumental", "instrumental version", "instrumental mix":
		return "instrumental"
	case "acapella", "acappella", "a cappella", "acapella version", "acappella version":
		return "acapella"
	case "vip", "vip version", "vip mix":
		return "vip mix"
	case "remaster", "remastered", "remastered version":
		return "remastered"
	default:
		return value
	}
}

// LooksLikeVersion reports whether a decoration is recognized as recording
// version metadata rather than a meaningful part of the song title.
func LooksLikeVersion(value string) bool {
	normalized := NormalizeVersion(value)
	if normalized == "" {
		return false
	}
	known := map[string]struct{}{
		"extended mix": {}, "original mix": {}, "radio edit": {},
		"club mix": {}, "dub mix": {}, "instrumental": {},
		"vip mix": {}, "remix": {}, "edit": {}, "rework": {}, "remastered": {},
		"mixed": {}, "acoustic": {}, "live": {}, "acapella": {},
	}
	if _, ok := known[normalized]; ok {
		return true
	}
	for _, prefix := range []string{"edit by ", "remix by ", "rework by "} {
		if strings.HasPrefix(normalized, prefix) && len(strings.TrimSpace(strings.TrimPrefix(normalized, prefix))) > 0 {
			return true
		}
	}
	for _, suffix := range []string{" remix", " mix", " edit", " rework", " dub", " bootleg", " version", " acapella", " acappella", " a cappella"} {
		if strings.HasSuffix(normalized, suffix) && len(strings.TrimSpace(strings.TrimSuffix(normalized, suffix))) > 0 {
			return true
		}
	}
	return false
}

// IsExtendedMix intentionally recognizes only a generic Extended Mix label.
// A named remix which happens to contain "extended" does not qualify.
func IsExtendedMix(version string) bool {
	return NormalizeVersion(version) == "extended mix"
}

// RemixerName returns the named portion of a remix label.
func RemixerName(version string) string {
	value := NormalizeVersion(version)
	if !strings.HasSuffix(value, " remix") {
		return ""
	}
	name := strings.TrimSpace(strings.TrimSuffix(value, " remix"))
	if name == "" || name == "extended" || name == "original" {
		return ""
	}
	return name
}

func featuringValue(value string) (string, bool) {
	match := regexp.MustCompile(`(?i)^(?:feat\.?|ft\.?|featuring)[[:space:]]+(.+)$`).FindStringSubmatch(strings.TrimSpace(value))
	if len(match) == 0 {
		return "", false
	}
	return strings.TrimSpace(match[1]), true
}

func splitTrailingVersion(value string) (string, string, bool) {
	for _, separator := range []string{" - ", " – ", " — "} {
		index := strings.LastIndex(value, separator)
		if index <= 0 {
			continue
		}
		before := strings.TrimSpace(value[:index])
		after := strings.TrimSpace(value[index+len(separator):])
		if before != "" && LooksLikeVersion(after) {
			return before, after, true
		}
	}
	return value, "", false
}

func appendUniqueNormalized(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(values))
	for _, value := range existing {
		seen[NormalizeArtist(value)] = struct{}{}
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := NormalizeArtist(value)
		if key == "" {
			continue
		}
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, value)
	}
	return existing
}
