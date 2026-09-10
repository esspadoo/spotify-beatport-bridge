// Package report writes machine-readable import results without credential
// material.
package report

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bpbridge/bpbridge/internal/credentials"
	"github.com/bpbridge/bpbridge/internal/model"
)

const SchemaVersion = 1

type TargetPlaylist struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	IsPublic bool   `json:"is_public"`
}

type Summary struct {
	SourceTracks      int `json:"source_tracks"`
	Matched           int `json:"matched"`
	Added             int `json:"added"`
	AlreadyInPlaylist int `json:"already_in_playlist"`
	InputDuplicates   int `json:"input_duplicates"`
	Ambiguous         int `json:"ambiguous"`
	NotFound          int `json:"not_found"`
	Skipped           int `json:"skipped"`
	Failed            int `json:"failed"`
}

// Report is intentionally composed only of non-secret domain values. Writers
// additionally redact token-like substrings from free-form text before output.
type Report struct {
	Version          int                  `json:"version"`
	Timestamp        time.Time            `json:"timestamp"`
	SourceType       string               `json:"source_type"`
	SourceIdentifier string               `json:"source_identifier,omitempty"`
	Target           *TargetPlaylist      `json:"target_beatport_playlist,omitempty"`
	DryRun           bool                 `json:"dry_run"`
	Summary          Summary              `json:"summary"`
	Outcomes         []model.TrackOutcome `json:"tracks"`
}

// New constructs and summarizes a report. Callers may still append Outcomes;
// every writer recalculates Summary before serialization.
func New(sourceType, sourceIdentifier string, target *model.BeatportPlaylist, dryRun bool, outcomes []model.TrackOutcome) Report {
	result := Report{
		Version:          SchemaVersion,
		Timestamp:        time.Now().UTC(),
		SourceType:       sourceType,
		SourceIdentifier: sourceIdentifier,
		DryRun:           dryRun,
		Outcomes:         append([]model.TrackOutcome(nil), outcomes...),
	}
	if target != nil {
		result.Target = &TargetPlaylist{ID: target.ID, Name: target.Name, IsPublic: target.IsPublic}
	}
	result.Summarize()
	return result
}

// Summarize derives all counts from Outcomes, preventing stale totals.
func (result *Report) Summarize() {
	if result == nil {
		return
	}
	summary := Summary{SourceTracks: len(result.Outcomes)}
	for _, outcome := range result.Outcomes {
		switch outcome.Status {
		case model.StatusAdded:
			summary.Matched++
			summary.Added++
		case model.StatusAlreadyExists:
			summary.Matched++
			summary.AlreadyInPlaylist++
		case model.StatusInputDuplicate:
			summary.InputDuplicates++
		case model.StatusMatched:
			summary.Matched++
		case model.StatusAmbiguous:
			summary.Ambiguous++
		case model.StatusNotFound:
			summary.NotFound++
		case model.StatusSkipped:
			summary.Skipped++
		case model.StatusFailed:
			summary.Failed++
		}
	}
	result.Summary = summary
}

// WriteFile selects JSON or CSV from the filename extension and atomically
// replaces the destination.
func WriteFile(path string, result Report) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("report path cannot be empty")
	}
	var encode func(io.Writer, Report) error
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		encode = WriteJSON
	case ".csv":
		encode = WriteCSV
	default:
		return fmt.Errorf("unsupported report format %q (use .json or .csv)", filepath.Ext(path))
	}
	if err := writeAtomic(path, 0o600, func(writer io.Writer) error { return encode(writer, result) }); err != nil {
		return fmt.Errorf("write report %q: %w", path, err)
	}
	return nil
}

func WriteJSON(writer io.Writer, result Report) error {
	if writer == nil {
		return errors.New("report JSON writer is nil")
	}
	result = sanitized(result)
	result.Summarize()
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("encode JSON report: %w", err)
	}
	return nil
}

func WriteCSV(writer io.Writer, result Report) error {
	if writer == nil {
		return errors.New("report CSV writer is nil")
	}
	result = sanitized(result)
	result.Summarize()
	csvWriter := csv.NewWriter(writer)
	header := []string{
		"timestamp", "source_type", "source_identifier", "dry_run",
		"playlist_id", "playlist_name", "playlist_public",
		"position", "input_source", "input_source_id", "input_artists", "input_title", "input_version", "input_isrc",
		"beatport_track_id", "beatport_artists", "beatport_title", "beatport_mix", "beatport_isrc",
		"confidence_score", "confidence", "status", "reason", "error",
	}
	if err := csvWriter.Write(header); err != nil {
		return fmt.Errorf("write CSV report header: %w", err)
	}
	for _, outcome := range result.Outcomes {
		record := neutralizeCSVRecord(makeCSVRecord(result, outcome))
		if err := csvWriter.Write(record); err != nil {
			return fmt.Errorf("write CSV report row: %w", err)
		}
	}
	csvWriter.Flush()
	if err := csvWriter.Error(); err != nil {
		return fmt.Errorf("flush CSV report: %w", err)
	}
	return nil
}

// neutralizeCSVRecord prevents spreadsheet applications from interpreting
// untrusted source or catalog metadata as a formula. CSV quoting alone does
// not disable formula evaluation in applications such as Excel.
func neutralizeCSVRecord(record []string) []string {
	for index, value := range record {
		trimmed := strings.TrimLeft(value, " \t\r\n")
		if trimmed == "" {
			continue
		}
		switch trimmed[0] {
		case '=', '+', '-', '@':
			record[index] = "'" + value
		}
	}
	return record
}

func makeCSVRecord(result Report, outcome model.TrackOutcome) []string {
	playlistID, playlistName, playlistPublic := "", "", ""
	if result.Target != nil {
		playlistID = strconv.FormatInt(result.Target.ID, 10)
		playlistName = result.Target.Name
		playlistPublic = strconv.FormatBool(result.Target.IsPublic)
	}
	trackID, trackArtists, trackTitle, trackMix, trackISRC := "", "", "", "", ""
	if outcome.Chosen != nil {
		trackID = strconv.FormatInt(outcome.Chosen.ID, 10)
		trackArtists = joinArtists(outcome.Chosen.Artists)
		trackTitle = outcome.Chosen.Name
		trackMix = outcome.Chosen.MixName
		trackISRC = outcome.Chosen.ISRC
	}
	return []string{
		result.Timestamp.UTC().Format(time.RFC3339Nano), result.SourceType, result.SourceIdentifier, strconv.FormatBool(result.DryRun),
		playlistID, playlistName, playlistPublic,
		strconv.Itoa(outcome.Input.Position), outcome.Input.Source, outcome.Input.SourceID, strings.Join(outcome.Input.Artists, "; "), outcome.Input.Title, outcome.Input.Version, outcome.Input.ISRC,
		trackID, trackArtists, trackTitle, trackMix, trackISRC,
		strconv.FormatFloat(outcome.Score, 'f', 2, 64), string(outcome.Confidence), string(outcome.Status), outcome.Reason, outcome.Error,
	}
}

func joinArtists(artists []model.Artist) string {
	names := make([]string, 0, len(artists))
	for _, artist := range artists {
		if artist.Name != "" {
			names = append(names, artist.Name)
		}
	}
	return strings.Join(names, "; ")
}

func sanitized(source Report) Report {
	result := source
	if result.Version == 0 {
		result.Version = SchemaVersion
	}
	if result.Timestamp.IsZero() {
		result.Timestamp = time.Now().UTC()
	} else {
		result.Timestamp = result.Timestamp.UTC()
	}
	result.SourceType = safe(result.SourceType)
	result.SourceIdentifier = safe(result.SourceIdentifier)
	if source.Target != nil {
		target := *source.Target
		target.Name = safe(target.Name)
		result.Target = &target
	}
	result.Outcomes = make([]model.TrackOutcome, len(source.Outcomes))
	for index, outcome := range source.Outcomes {
		result.Outcomes[index] = sanitizeOutcome(outcome)
	}
	return result
}

func sanitizeOutcome(source model.TrackOutcome) model.TrackOutcome {
	result := source
	result.Input = sanitizeSourceTrack(source.Input)
	result.Reason = safe(source.Reason)
	result.Error = safe(source.Error)
	if source.Chosen != nil {
		chosen := sanitizeBeatportTrack(*source.Chosen)
		result.Chosen = &chosen
	}
	if source.Breakdown != nil {
		breakdown := *source.Breakdown
		breakdown.Explanation = safeStrings(source.Breakdown.Explanation)
		result.Breakdown = &breakdown
	}
	return result
}

func sanitizeSourceTrack(source model.SourceTrack) model.SourceTrack {
	result := source
	result.Source = safe(source.Source)
	result.SourceID = safe(source.SourceID)
	result.SpotifyURI = safe(source.SpotifyURI)
	result.Title = safe(source.Title)
	result.BaseTitle = safe(source.BaseTitle)
	result.Artists = safeStrings(source.Artists)
	result.PrimaryArtist = safe(source.PrimaryArtist)
	result.Version = safe(source.Version)
	result.ISRC = safe(source.ISRC)
	result.Album = safe(source.Album)
	result.OriginalText = safe(source.OriginalText)
	return result
}

func sanitizeBeatportTrack(source model.BeatportTrack) model.BeatportTrack {
	result := source
	result.Name = safe(source.Name)
	result.MixName = safe(source.MixName)
	result.Slug = safe(source.Slug)
	result.ISRC = safe(source.ISRC)
	result.Length = safe(source.Length)
	result.PublishDate = safe(source.PublishDate)
	result.URL = safe(source.URL)
	result.Artists = sanitizeArtists(source.Artists)
	result.Remixers = sanitizeArtists(source.Remixers)
	result.Release.Name = safe(source.Release.Name)
	result.Release.Slug = safe(source.Release.Slug)
	result.Release.PublishDate = safe(source.Release.PublishDate)
	result.Release.CatalogNumber = safe(source.Release.CatalogNumber)
	result.Release.Label.Name = safe(source.Release.Label.Name)
	return result
}

func sanitizeArtists(source []model.Artist) []model.Artist {
	if source == nil {
		return nil
	}
	result := append([]model.Artist(nil), source...)
	for index := range result {
		result[index].Name = safe(result[index].Name)
		result[index].Slug = safe(result[index].Slug)
	}
	return result
}

func safeStrings(source []string) []string {
	if source == nil {
		return nil
	}
	result := make([]string, len(source))
	for index, value := range source {
		result[index] = safe(value)
	}
	return result
}

func safe(value string) string {
	return credentials.RedactSecrets(value)
}

func writeAtomic(path string, mode os.FileMode, write func(io.Writer) error) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".bpbridge-report-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(mode); err != nil {
		return err
	}
	if err = write(temporary); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
