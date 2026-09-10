package matcher

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/bpbridge/bpbridge/internal/model"
)

// QueryPass is one retrieval stage. Callers may score the accumulated pool
// after each pass and avoid broader requests once an unambiguous strong match
// has already been discovered.
type QueryPass struct {
	Name    string
	Queries []string
}

// GenerateQueries builds a bounded, most-specific-first set of Beatport search
// queries. Equivalent queries are removed after normalization.
func GenerateQueries(source model.SourceTrack) []string {
	passes := GenerateQueryPasses(source)
	var queries []string
	for _, pass := range passes {
		queries = append(queries, pass.Queries...)
	}
	return queries
}

// GenerateQueryPasses separates high-precision queries from increasingly
// broad recall fallbacks. Query text is deduplicated without erasing meaningful
// punctuation differences that Beatport's search service may interpret.
func GenerateQueryPasses(source model.SourceTrack) []QueryPass {
	parts := ExtractTitleParts(source.Title)
	base := strings.TrimSpace(source.BaseTitle)
	if base == "" {
		base = parts.Base
	}
	full := strings.TrimSpace(source.Title)
	version := strings.TrimSpace(source.Version)
	if version == "" {
		version = parts.Version
	}
	if full == "" {
		full = base
		if version != "" {
			full = fmt.Sprintf("%s (%s)", base, version)
		}
	}

	artists := appendUniqueNormalized([]string(nil), source.Artists...)
	primary := strings.TrimSpace(source.PrimaryArtist)
	if primary == "" && len(artists) > 0 {
		primary = strings.TrimSpace(artists[0])
	}
	if primary != "" && len(artists) == 0 {
		artists = []string{primary}
	}

	if primary != "" {
		artists = appendUniqueNormalized(artists, primary)
	}

	passes := make([]QueryPass, 0, 4)
	seen := make(map[string]struct{}, 8)
	addPass := func(name string, values ...string) {
		pass := QueryPass{Name: name}
		for _, query := range values {
			query = strings.Join(strings.Fields(query), " ")
			key := strings.ToLower(query)
			if query == "" || key == "" {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			pass.Queries = append(pass.Queries, query)
		}
		if len(pass.Queries) > 0 {
			passes = append(passes, pass)
		}
	}

	if len(artists) > 0 {
		all := strings.Join(artists, " ")
		addPass("all-artists", all+" "+full, all+" "+base)
	}
	individual := make([]string, 0, len(artists)*2)
	for _, artist := range artists {
		individual = append(individual, artist+" "+full, artist+" "+base)
	}
	addPass("individual-artists", individual...)
	addPass("title", full, base)
	addPass("sanitized-title", sanitizeSearchText(base))
	return passes
}

func sanitizeSearchText(value string) string {
	var builder strings.Builder
	space := true
	for _, char := range value {
		switch {
		case unicode.IsLetter(char) || unicode.IsDigit(char):
			builder.WriteRune(char)
			space = false
		case char == '\'' || char == '’' || char == '‘' || char == 'ʼ':
			continue
		default:
			if !space {
				builder.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

// SourceKey identifies repeat source rows without depending on capitalization,
// punctuation or artist ordering. Source-native IDs and ISRCs take precedence.
func SourceKey(source model.SourceTrack) string {
	if source.SourceID != "" {
		return "source:" + Normalize(source.Source) + ":" + strings.ToLower(strings.TrimSpace(source.SourceID))
	}
	if source.ISRC != "" {
		return "isrc:" + normalizeISRC(source.ISRC)
	}
	parts := ExtractTitleParts(source.Title)
	base := source.BaseTitle
	if strings.TrimSpace(base) == "" {
		base = parts.Base
	}
	version := source.Version
	if strings.TrimSpace(version) == "" {
		version = parts.Version
	}
	artists := append([]string(nil), source.Artists...)
	if source.PrimaryArtist != "" {
		artists = append(artists, source.PrimaryArtist)
	}
	return "metadata:" + ArtistSetKey(artists) + "\x1e" + Normalize(base) + "\x1e" + NormalizeVersion(version)
}

func normalizeISRC(isrc string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(isrc), "-", ""), " ", ""))
}
