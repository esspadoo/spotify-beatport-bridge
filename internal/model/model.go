package model

import "time"

// SourceTrack is the canonical input model shared by every source adapter.
type SourceTrack struct {
	Source         string   `json:"source"`
	SourceID       string   `json:"source_id,omitempty"`
	SpotifyURI     string   `json:"spotify_uri,omitempty"`
	Title          string   `json:"title"`
	BaseTitle      string   `json:"base_title,omitempty"`
	Artists        []string `json:"artists,omitempty"`
	PrimaryArtist  string   `json:"primary_artist,omitempty"`
	Version        string   `json:"version,omitempty"`
	ISRC           string   `json:"isrc,omitempty"`
	DurationMS     int64    `json:"duration_ms,omitempty"`
	Album          string   `json:"album,omitempty"`
	OriginalText   string   `json:"original_text,omitempty"`
	Position       int      `json:"position"`
	InputDuplicate bool     `json:"input_duplicate,omitempty"`
}

type Artist struct {
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

type Label struct {
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type Release struct {
	ID            int64  `json:"id,omitempty"`
	Name          string `json:"name,omitempty"`
	Slug          string `json:"slug,omitempty"`
	PublishDate   string `json:"publish_date,omitempty"`
	CatalogNumber string `json:"catalog_number,omitempty"`
	Label         Label  `json:"label,omitempty"`
}

// BeatportTrack contains only metadata required for matching and reporting.
type BeatportTrack struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	MixName     string   `json:"mix_name,omitempty"`
	Slug        string   `json:"slug,omitempty"`
	Artists     []Artist `json:"artists,omitempty"`
	Remixers    []Artist `json:"remixers,omitempty"`
	ISRC        string   `json:"isrc,omitempty"`
	Length      string   `json:"length,omitempty"`
	LengthMS    int64    `json:"length_ms,omitempty"`
	BPM         float64  `json:"bpm,omitempty"`
	Release     Release  `json:"release,omitempty"`
	PublishDate string   `json:"publish_date,omitempty"`
	URL         string   `json:"url,omitempty"`
}

type BeatportPlaylist struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	IsPublic    bool      `json:"is_public,omitempty"`
	TrackCount  int       `json:"track_count,omitempty"`
	CreatedDate time.Time `json:"created_date,omitempty"`
	UpdatedDate time.Time `json:"updated_date,omitempty"`
}

type BeatportPlaylistItem struct {
	ID       int64         `json:"id,omitempty"`
	Position int           `json:"position,omitempty"`
	Track    BeatportTrack `json:"track"`
}

type Confidence string

const (
	ConfidenceHigh      Confidence = "HIGH"
	ConfidenceAmbiguous Confidence = "AMBIGUOUS"
	ConfidenceLow       Confidence = "LOW"
)

type ScoreBreakdown struct {
	Title       float64  `json:"title"`
	BaseTitle   float64  `json:"base_title"`
	Artists     float64  `json:"artists"`
	Version     float64  `json:"version"`
	ISRC        float64  `json:"isrc"`
	Duration    float64  `json:"duration"`
	Release     float64  `json:"release"`
	Extended    float64  `json:"extended_mix"`
	Penalty     float64  `json:"penalty"`
	Explanation []string `json:"explanation,omitempty"`
}

type ScoredCandidate struct {
	Track      BeatportTrack  `json:"track"`
	Score      float64        `json:"score"`
	Confidence Confidence     `json:"confidence"`
	Breakdown  ScoreBreakdown `json:"breakdown"`
}

type MatchResult struct {
	Source     SourceTrack       `json:"source"`
	Chosen     *ScoredCandidate  `json:"chosen,omitempty"`
	Candidates []ScoredCandidate `json:"candidates,omitempty"`
	Confidence Confidence        `json:"confidence"`
	Reason     string            `json:"reason,omitempty"`
}

type TrackStatus string

const (
	StatusAdded          TrackStatus = "ADDED"
	StatusAlreadyExists  TrackStatus = "ALREADY_EXISTS"
	StatusInputDuplicate TrackStatus = "INPUT_DUPLICATE"
	StatusMatched        TrackStatus = "MATCHED"
	StatusAmbiguous      TrackStatus = "AMBIGUOUS"
	StatusNotFound       TrackStatus = "NOT_FOUND"
	StatusSkipped        TrackStatus = "SKIPPED"
	StatusFailed         TrackStatus = "FAILED"
)

type TrackOutcome struct {
	Input      SourceTrack     `json:"input"`
	Chosen     *BeatportTrack  `json:"chosen,omitempty"`
	Score      float64         `json:"confidence_score,omitempty"`
	Confidence Confidence      `json:"confidence,omitempty"`
	Breakdown  *ScoreBreakdown `json:"score_breakdown,omitempty"`
	Status     TrackStatus     `json:"status"`
	Reason     string          `json:"reason,omitempty"`
	Error      string          `json:"error,omitempty"`
}
