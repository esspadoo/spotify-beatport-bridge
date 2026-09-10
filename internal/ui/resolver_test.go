package ui

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/bpbridge/bpbridge/internal/model"
)

func TestResolverSelectsCandidateAndStopsOnEOF(t *testing.T) {
	t.Parallel()
	candidates := []model.ScoredCandidate{{Track: model.BeatportTrack{ID: 7, Name: "Signal"}, Score: 88}}
	selected, err := NewResolver(strings.NewReader("1\n"), io.Discard).Resolve(
		context.Background(), model.SourceTrack{Title: "Signal"}, candidates, nil,
	)
	if err != nil || selected == nil || selected.Track.ID != 7 {
		t.Fatalf("selected=%#v err=%v", selected, err)
	}

	selected, err = NewResolver(strings.NewReader(""), io.Discard).Resolve(
		context.Background(), model.SourceTrack{Title: "Signal"}, candidates, nil,
	)
	if err != nil || selected != nil {
		t.Fatalf("EOF selected=%#v err=%v", selected, err)
	}
}

func TestReadPasswordFallbackDoesNotEchoValue(t *testing.T) {
	t.Parallel()
	var output strings.Builder
	value, err := ReadPassword(nil, bufio.NewReader(strings.NewReader("very-secret\n")), &output, "Password: ")
	if err != nil || value != "very-secret" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if strings.Contains(output.String(), value) {
		t.Fatalf("password was echoed: %q", output.String())
	}
}
