package cli

import (
	"fmt"
	"strings"

	"github.com/bpbridge/bpbridge/internal/model"
	"github.com/bpbridge/bpbridge/internal/report"
)

func (app *App) printProgress(current, total int, outcome model.TrackOutcome) {
	input := outcome.Input.Title
	if outcome.Input.PrimaryArtist != "" {
		input = outcome.Input.PrimaryArtist + " - " + input
	}
	destination := string(outcome.Status)
	if outcome.Chosen != nil {
		destination = beatportTrackName(*outcome.Chosen)
		if outcome.Score > 0 {
			destination += fmt.Sprintf(" [%.1f%%]", outcome.Score)
		}
	}
	fmt.Fprintf(app.out, "[%d/%d] %s -> %s\n", current, total, input, destination)
	if app.global.verbose || app.global.debug {
		if outcome.Reason != "" {
			fmt.Fprintf(app.out, "         %s\n", outcome.Reason)
		}
		if app.global.debug && outcome.Breakdown != nil {
			breakdown := outcome.Breakdown
			fmt.Fprintf(app.out, "         score: title=%.1f base=%.1f artists=%.1f version=%.1f isrc=%.1f duration=%.1f release=%.1f extended=%.1f penalty=%.1f\n",
				breakdown.Title, breakdown.BaseTitle, breakdown.Artists, breakdown.Version, breakdown.ISRC,
				breakdown.Duration, breakdown.Release, breakdown.Extended, breakdown.Penalty)
		}
	}
}

func (app *App) printSummary(result report.Report) {
	result.Summarize()
	fmt.Fprintln(app.out)
	fmt.Fprintf(app.out, "Source tracks:        %d\n", result.Summary.SourceTracks)
	fmt.Fprintf(app.out, "Matched:              %d\n", result.Summary.Matched)
	if result.DryRun {
		fmt.Fprintf(app.out, "Would add:            %d\n", countStatus(result.Outcomes, model.StatusMatched))
	} else {
		fmt.Fprintf(app.out, "Added:                %d\n", result.Summary.Added)
	}
	fmt.Fprintf(app.out, "Already in playlist: %d\n", result.Summary.AlreadyInPlaylist)
	fmt.Fprintf(app.out, "Input duplicates:     %d\n", result.Summary.InputDuplicates)
	fmt.Fprintf(app.out, "Ambiguous:            %d\n", result.Summary.Ambiguous)
	fmt.Fprintf(app.out, "Not found:            %d\n", result.Summary.NotFound)
	fmt.Fprintf(app.out, "Skipped:              %d\n", result.Summary.Skipped)
	fmt.Fprintf(app.out, "Failed:               %d\n", result.Summary.Failed)

	printedHeader := false
	for _, outcome := range result.Outcomes {
		unresolved := outcome.Status == model.StatusAmbiguous || outcome.Status == model.StatusNotFound || outcome.Status == model.StatusSkipped || outcome.Status == model.StatusFailed
		if !unresolved && !(app.global.verbose || app.global.debug) {
			continue
		}
		if !printedHeader {
			fmt.Fprintln(app.out, "\nTrack details:")
			printedHeader = true
		}
		name := outcome.Input.Title
		if outcome.Input.PrimaryArtist != "" {
			name = outcome.Input.PrimaryArtist + " - " + name
		}
		fmt.Fprintf(app.out, "- [%s] %s", outcome.Status, name)
		if outcome.Chosen != nil {
			fmt.Fprintf(app.out, " -> %s (%.1f%%)", beatportTrackName(*outcome.Chosen), outcome.Score)
		}
		fmt.Fprintln(app.out)
		if outcome.Reason != "" {
			fmt.Fprintf(app.out, "  %s\n", outcome.Reason)
		}
		if outcome.Error != "" {
			fmt.Fprintf(app.out, "  Error: %s\n", outcome.Error)
		}
	}
}

func beatportTrackName(track model.BeatportTrack) string {
	artists := make([]string, 0, len(track.Artists))
	for _, artist := range track.Artists {
		if strings.TrimSpace(artist.Name) != "" {
			artists = append(artists, artist.Name)
		}
	}
	name := track.Name
	if track.MixName != "" {
		name += " (" + track.MixName + ")"
	}
	if len(artists) > 0 {
		name = strings.Join(artists, ", ") + " - " + name
	}
	return name
}

func countStatus(outcomes []model.TrackOutcome, status model.TrackStatus) int {
	count := 0
	for _, outcome := range outcomes {
		if outcome.Status == status {
			count++
		}
	}
	return count
}
