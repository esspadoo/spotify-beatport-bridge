package input

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bpbridge/bpbridge/internal/matcher"
	"github.com/bpbridge/bpbridge/internal/model"
)

// TextOptions controls parsing of a text source. Comments beginning with '#'
// are ignored by default; KeepComments treats them as ordinary title lines.
type TextOptions struct {
	Source       string
	KeepComments bool
}

// ParseText parses a UTF-8 string using the normal text-import behavior.
func ParseText(text string) ([]model.SourceTrack, error) {
	return ParseTextReader(strings.NewReader(text), TextOptions{})
}

// ParseTextReader parses newline-separated song references. It keeps source
// order and marks duplicate rows rather than removing them.
func ParseTextReader(reader io.Reader, options TextOptions) ([]model.SourceTrack, error) {
	if reader == nil {
		return nil, fmt.Errorf("parse text: reader is nil")
	}
	source := strings.TrimSpace(options.Source)
	if source == "" {
		source = "text"
	}

	scanner := bufio.NewScanner(reader)
	// Playlist exports occasionally contain very long metadata columns. Avoid
	// Scanner's small default token limit while retaining a finite safety bound.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var tracks []model.SourceTrack
	seen := make(map[string]struct{})
	firstLine := true
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if !utf8.ValidString(line) {
			return nil, fmt.Errorf("parse text: line %d is not valid UTF-8", lineNumber)
		}
		if firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			firstLine = false
		}
		line = repairCommonMojibake(line)
		line = strings.TrimSpace(line)
		if line == "" || (!options.KeepComments && strings.HasPrefix(line, "#")) {
			continue
		}

		track := parseTextLine(line)
		track.Source = source
		track.OriginalText = line
		track.Position = len(tracks) + 1
		key := matcher.SourceKey(track)
		if _, duplicate := seen[key]; duplicate {
			track.InputDuplicate = true
		} else {
			seen[key] = struct{}{}
		}
		tracks = append(tracks, track)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse text: %w", err)
	}
	return tracks, nil
}

// ParseLines is convenient for command-line song arguments. Each argument is
// treated as one source row, even if it happens to contain newline characters.
func ParseLines(lines []string, source string) ([]model.SourceTrack, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "text"
	}
	tracks := make([]model.SourceTrack, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for argument, line := range lines {
		if !utf8.ValidString(line) {
			return nil, fmt.Errorf("parse command-line songs: argument %d is not valid UTF-8", argument+1)
		}
		line = strings.Join(strings.Fields(repairCommonMojibake(strings.TrimPrefix(line, "\ufeff"))), " ")
		if line == "" {
			continue
		}
		track := parseTextLine(line)
		track.Source = source
		track.OriginalText = line
		track.Position = len(tracks) + 1
		key := matcher.SourceKey(track)
		if _, duplicate := seen[key]; duplicate {
			track.InputDuplicate = true
		} else {
			seen[key] = struct{}{}
		}
		tracks = append(tracks, track)
	}
	return tracks, nil
}

// ParseTextLine parses one non-empty song reference. It is exported for the
// interactive UI, which needs to validate rows as they are entered.
func ParseTextLine(line string) (model.SourceTrack, error) {
	if !utf8.ValidString(line) {
		return model.SourceTrack{}, fmt.Errorf("song reference is not valid UTF-8")
	}
	line = strings.TrimSpace(repairCommonMojibake(line))
	line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if line == "" {
		return model.SourceTrack{}, fmt.Errorf("song reference is empty")
	}
	track := parseTextLine(line)
	track.Source = "text"
	track.OriginalText = line
	track.Position = 1
	return track, nil
}

func parseTextLine(line string) model.SourceTrack {
	artistText, title := splitArtistAndTitle(line)
	parts := matcher.ExtractTitleParts(title)
	artists := matcher.SplitTextArtists(artistText)
	if len(parts.FeaturedArtists) > 0 {
		artists = appendUniqueArtists(artists, parts.FeaturedArtists...)
	}

	track := model.SourceTrack{
		Title:     strings.TrimSpace(title),
		BaseTitle: parts.Base,
		Artists:   artists,
		Version:   parts.Version,
	}
	if len(artists) > 0 {
		track.PrimaryArtist = artists[0]
	}
	return track
}

func splitArtistAndTitle(line string) (artist, title string) {
	// A vertical bar is unambiguous enough to accept with or without spaces.
	if index := strings.IndexRune(line, '|'); index >= 0 {
		if left, right, ok := nonEmptySides(line, index, 1); ok {
			return left, right
		}
	}

	// En/em dashes are conventional export separators. Accept them without
	// spaces; ASCII hyphens require whitespace on both sides so names such as
	// Jay-Z and titles such as Love-Hate Relationship remain intact.
	for index, char := range line {
		if char == '–' || char == '—' {
			if left, right, ok := nonEmptySides(line, index, len(string(char))); ok {
				return left, right
			}
		}
	}
	for index := 1; index < len(line)-1; index++ {
		if line[index] != '-' || !unicode.IsSpace(rune(line[index-1])) || !unicode.IsSpace(rune(line[index+1])) {
			continue
		}
		if left, right, ok := nonEmptySides(line, index, 1); ok {
			return left, right
		}
	}
	return "", strings.TrimSpace(line)
}

func nonEmptySides(value string, index, separatorBytes int) (string, string, bool) {
	left := strings.TrimSpace(value[:index])
	right := strings.TrimSpace(value[index+separatorBytes:])
	return left, right, left != "" && right != ""
}

func repairCommonMojibake(value string) string {
	// The first pair is Windows-1252 mojibake and the second is Latin-1
	// mojibake. Both commonly appear in exported M3U/TXT files.
	replacer := strings.NewReplacer(
		"â€“", "–",
		"â€”", "—",
		"â", "–",
		"â", "—",
		"ï»¿", "",
	)
	return replacer.Replace(value)
}

func appendUniqueArtists(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(values))
	for _, artist := range existing {
		seen[matcher.NormalizeArtist(artist)] = struct{}{}
	}
	for _, artist := range values {
		artist = strings.TrimSpace(artist)
		key := matcher.NormalizeArtist(artist)
		if artist == "" || key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, artist)
	}
	return existing
}
