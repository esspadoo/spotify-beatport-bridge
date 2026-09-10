// Package matcher provides deterministic normalization, query generation and
// explainable Beatport candidate scoring.
package matcher

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var latinFold = strings.NewReplacer(
	"ø", "o", "Ø", "o",
	"ł", "l", "Ł", "l",
	"đ", "d", "Đ", "d",
	"ð", "d", "Ð", "d",
	"þ", "th", "Þ", "th",
	"æ", "ae", "Æ", "ae",
	"œ", "oe", "Œ", "oe",
	"ß", "ss",
)

// Normalize returns a stable comparison form. It applies Unicode NFKD,
// removes diacritics, folds case, normalizes '&' to "and", canonicalizes
// featuring abbreviations and treats other punctuation as word boundaries.
func Normalize(value string) string {
	value = latinFold.Replace(value)
	value = strings.ToLower(norm.NFKD.String(strings.ToLower(value)))

	var builder strings.Builder
	builder.Grow(len(value))
	space := true
	for _, char := range value {
		switch {
		case unicode.Is(unicode.Mn, char):
			continue
		case unicode.IsLetter(char) || unicode.IsDigit(char):
			builder.WriteRune(char)
			space = false
		case char == '&':
			if !space {
				builder.WriteByte(' ')
			}
			builder.WriteString("and")
			builder.WriteByte(' ')
			space = true
		case char == '\'' || char == '’' || char == '‘' || char == 'ʼ':
			// Apostrophes are removed rather than made word boundaries, so
			// "don't" and "dont" compare identically.
			continue
		default:
			if !space {
				builder.WriteByte(' ')
				space = true
			}
		}
	}

	words := strings.Fields(builder.String())
	for index, word := range words {
		switch word {
		case "ft", "featuring":
			words[index] = "feat"
		}
	}
	return strings.Join(words, " ")
}

// NormalizeTitle removes recognized trailing version and featuring metadata
// before normalizing the underlying title.
func NormalizeTitle(title string) string {
	return Normalize(ExtractTitleParts(title).Base)
}

// NormalizeArtist normalizes one artist name without guessing whether an
// ampersand represents collaboration or is part of a group name.
func NormalizeArtist(artist string) string {
	return Normalize(artist)
}

// NormalizeArtistSet returns a sorted, duplicate-free representation so artist
// order does not influence equality checks.
func NormalizeArtistSet(artists []string) []string {
	set := make(map[string]struct{}, len(artists))
	for _, artist := range artists {
		value := NormalizeArtist(artist)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// ArtistSetKey is the canonical string form of an artist set.
func ArtistSetKey(artists []string) string {
	return strings.Join(NormalizeArtistSet(artists), "\x1f")
}
