package input

import (
	"errors"
	"fmt"

	spotifyapi "github.com/bpbridge/bpbridge/internal/spotify"
)

// SpotifyKind identifies the resource represented by a Spotify reference.
type SpotifyKind string

const (
	SpotifyTrack    SpotifyKind = "track"
	SpotifyPlaylist SpotifyKind = "playlist"
)

var ErrInvalidSpotifyReference = errors.New("invalid Spotify reference")

// SpotifyReference is the display-friendly form of the canonical parser in
// internal/spotify. Keeping parsing in one place prevents the CLI and API
// client from disagreeing as Spotify URL formats evolve.
type SpotifyReference struct {
	Kind SpotifyKind `json:"kind"`
	ID   string      `json:"id"`
	URI  string      `json:"uri"`
	URL  string      `json:"url"`
}

func ParseSpotifyReference(raw string) (SpotifyReference, error) {
	reference, err := spotifyapi.ParseReference(raw)
	if err != nil {
		return SpotifyReference{}, fmt.Errorf("%w: %v", ErrInvalidSpotifyReference, err)
	}
	kind := SpotifyKind(reference.Type)
	return SpotifyReference{
		Kind: kind,
		ID:   reference.ID,
		URI:  reference.URI(),
		URL:  fmt.Sprintf("https://open.spotify.com/%s/%s", kind, reference.ID),
	}, nil
}

func ParseSpotify(raw string) (SpotifyReference, error) {
	return ParseSpotifyReference(raw)
}
