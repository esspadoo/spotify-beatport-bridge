package ui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/bpbridge/bpbridge/internal/model"
	"github.com/bpbridge/bpbridge/internal/syncer"
)

type Resolver struct {
	In  *bufio.Reader
	Out io.Writer
}

func NewResolver(in io.Reader, out io.Writer) *Resolver {
	return &Resolver{In: bufio.NewReader(in), Out: out}
}

func (r *Resolver) Resolve(ctx context.Context, source model.SourceTrack, candidates []model.ScoredCandidate, search syncer.SearchFunc) (*model.ScoredCandidate, error) {
	if r.In == nil || r.Out == nil {
		return nil, fmt.Errorf("interactive resolver has no terminal streams")
	}
	current := candidates
	visible := min(5, len(current))
	for {
		fmt.Fprintf(r.Out, "\nInput: %s\n", displaySource(source))
		for index := 0; index < visible; index++ {
			candidate := current[index]
			fmt.Fprintf(r.Out, "%d. %s - %s (%s) [%s] %.0f BPM - %.1f%%\n",
				index+1,
				displayArtists(candidate.Track.Artists),
				candidate.Track.Name,
				firstNonEmpty(candidate.Track.MixName, "Default Mix"),
				candidate.Track.Release.Label.Name,
				candidate.Track.BPM,
				candidate.Score,
			)
		}
		fmt.Fprintln(r.Out, "0. Skip")
		fmt.Fprintln(r.Out, "S. Search again")
		if visible < len(current) {
			fmt.Fprintln(r.Out, "M. More candidates")
		}
		fmt.Fprint(r.Out, "Selection: ")
		answer, err := r.In.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		reachedEOF := err == io.EOF
		answer = strings.TrimSpace(answer)
		if reachedEOF && answer == "" {
			return nil, nil
		}
		switch strings.ToLower(answer) {
		case "0", "skip":
			return nil, nil
		case "s", "search":
			fmt.Fprint(r.Out, "Beatport search query: ")
			query, readErr := r.In.ReadString('\n')
			if readErr != nil && readErr != io.EOF {
				return nil, readErr
			}
			queryEOF := readErr == io.EOF
			query = strings.TrimSpace(query)
			if query == "" {
				if queryEOF {
					return nil, nil
				}
				fmt.Fprintln(r.Out, "Search query cannot be empty.")
				continue
			}
			current, err = search(ctx, query)
			if err != nil {
				fmt.Fprintf(r.Out, "Search failed: %v\n", err)
				continue
			}
			if len(current) == 0 {
				fmt.Fprintln(r.Out, "No candidates found.")
			}
			visible = min(5, len(current))
			if reachedEOF || queryEOF {
				return nil, nil
			}
			continue
		case "m", "more":
			visible = min(visible+5, len(current))
			if reachedEOF {
				return nil, nil
			}
			continue
		}
		selection, conversionErr := strconv.Atoi(answer)
		if conversionErr == nil && selection >= 1 && selection <= visible {
			chosen := current[selection-1]
			return &chosen, nil
		}
		if reachedEOF {
			return nil, nil
		}
		fmt.Fprintln(r.Out, "Choose a displayed number, 0, S, or M.")
	}
}

func Prompt(reader *bufio.Reader, out io.Writer, label, defaultValue string) (string, error) {
	if defaultValue == "" {
		fmt.Fprintf(out, "%s: ", label)
	} else {
		fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
	}
	value, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultValue
	}
	return value, nil
}

func displaySource(source model.SourceTrack) string {
	if source.PrimaryArtist != "" {
		return source.PrimaryArtist + " - " + source.Title
	}
	return source.Title
}

func displayArtists(artists []model.Artist) string {
	values := make([]string, 0, len(artists))
	for _, artist := range artists {
		if artist.Name != "" {
			values = append(values, artist.Name)
		}
	}
	if len(values) == 0 {
		return "Unknown artist"
	}
	return strings.Join(values, ", ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
