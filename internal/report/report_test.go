package report

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bpbridge/bpbridge/internal/credentials"
	"github.com/bpbridge/bpbridge/internal/model"
)

func sampleReport() Report {
	target := model.BeatportPlaylist{ID: 99, Name: "Destination", IsPublic: false}
	chosen := model.BeatportTrack{
		ID:      42,
		Name:    "Track",
		MixName: "Extended Mix",
		Artists: []model.Artist{{Name: "Artist"}},
		ISRC:    "GB-AAA-26-00001",
	}
	result := New("text", "tracks.txt", &target, true, []model.TrackOutcome{
		{
			Input:  model.SourceTrack{Source: "text", Title: "Track", Artists: []string{"Artist"}, Position: 1},
			Chosen: &chosen, Score: 94.25, Confidence: model.ConfidenceHigh, Status: model.StatusMatched,
			Reason: "artist/title and Extended Mix",
		},
		{
			Input:  model.SourceTrack{Source: "text", Title: "Missing", Position: 2},
			Status: model.StatusNotFound,
			Error:  "Authorization: Bearer top-secret-token",
		},
	})
	result.Timestamp = time.Date(2026, 9, 1, 12, 0, 0, 123, time.UTC)
	return result
}

func TestWriteJSONSummarizesAndRedacts(t *testing.T) {
	var output bytes.Buffer
	if err := WriteJSON(&output, sampleReport()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "top-secret-token") {
		t.Fatalf("JSON leaked token: %s", output.String())
	}
	if !strings.Contains(output.String(), credentials.Redacted) {
		t.Fatalf("JSON has no redaction marker: %s", output.String())
	}
	var decoded Report
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Summary.SourceTracks != 2 || decoded.Summary.Matched != 1 || decoded.Summary.NotFound != 1 {
		t.Fatalf("summary = %+v", decoded.Summary)
	}
	if decoded.Outcomes[0].Chosen == nil || decoded.Outcomes[0].Chosen.ID != 42 {
		t.Fatalf("chosen track = %#v", decoded.Outcomes[0].Chosen)
	}
}

func TestWriteCSVProducesStableColumnsAndEscapes(t *testing.T) {
	result := sampleReport()
	result.Outcomes[0].Reason = "strong, explainable match"
	var output bytes.Buffer
	if err := WriteCSV(&output, result); err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(output.Bytes())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("CSV rows = %d, want 3", len(records))
	}
	if records[0][0] != "timestamp" || records[1][21] != "MATCHED" {
		t.Fatalf("unexpected CSV records: %#v", records)
	}
	if records[1][22] != "strong, explainable match" {
		t.Fatalf("reason = %q", records[1][22])
	}
	if strings.Contains(output.String(), "top-secret-token") {
		t.Fatalf("CSV leaked token: %s", output.String())
	}
}

func TestWriteCSVNeutralizesSpreadsheetFormulas(t *testing.T) {
	t.Parallel()
	result := New("text", "=HYPERLINK(\"https://bad.invalid\")", nil, true, []model.TrackOutcome{{
		Input:  model.SourceTrack{Title: "+SUM(1,1)", Artists: []string{"@attacker"}, PrimaryArtist: "@attacker", Position: 1},
		Status: model.StatusNotFound,
		Reason: "-2+3",
	}})
	var output bytes.Buffer
	if err := WriteCSV(&output, result); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(output.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%v", rows)
	}
	for _, index := range []int{2, 10, 11, 22} {
		if !strings.HasPrefix(rows[1][index], "'") {
			t.Errorf("cell %d=%q was not neutralized", index, rows[1][index])
		}
	}
}

func TestWriteFileAndUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.json")
	if err := WriteFile(path, sampleReport()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("invalid JSON report: %s", data)
	}
	if err := WriteFile(filepath.Join(t.TempDir(), "report.txt"), sampleReport()); err == nil {
		t.Fatal("WriteFile unexpectedly accepted .txt")
	}
}

func TestSummaryStatuses(t *testing.T) {
	statuses := []model.TrackStatus{
		model.StatusAdded,
		model.StatusAlreadyExists,
		model.StatusInputDuplicate,
		model.StatusAmbiguous,
		model.StatusNotFound,
		model.StatusSkipped,
		model.StatusFailed,
	}
	result := Report{Outcomes: make([]model.TrackOutcome, len(statuses))}
	for index, status := range statuses {
		result.Outcomes[index].Status = status
	}
	result.Summarize()
	if result.Summary.SourceTracks != 7 || result.Summary.Matched != 2 || result.Summary.Added != 1 ||
		result.Summary.AlreadyInPlaylist != 1 || result.Summary.InputDuplicates != 1 || result.Summary.Ambiguous != 1 ||
		result.Summary.NotFound != 1 || result.Summary.Skipped != 1 || result.Summary.Failed != 1 {
		t.Fatalf("summary = %+v", result.Summary)
	}
}
