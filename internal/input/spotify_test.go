package input

import (
	"errors"
	"testing"
)

func TestParseSpotifyReferenceValid(t *testing.T) {
	t.Parallel()
	const trackID = "11dFghVXANMlKmJXsNCbNl"
	const playlistID = "37i9dQZF1DXcBWIGoYBM5M"
	tests := []struct {
		name string
		in   string
		kind SpotifyKind
		id   string
	}{
		{"track URL", "https://open.spotify.com/track/" + trackID, SpotifyTrack, trackID},
		{"playlist URL", "https://open.spotify.com/playlist/" + playlistID, SpotifyPlaylist, playlistID},
		{"tracking query", " https://open.spotify.com/track/" + trackID + "?si=abc&utm_source=test#fragment ", SpotifyTrack, trackID},
		{"track URI", "spotify:track:" + trackID, SpotifyTrack, trackID},
		{"playlist URI", "spotify:playlist:" + playlistID, SpotifyPlaylist, playlistID},
		{"locale URL", "https://open.spotify.com/intl-it/track/" + trackID, SpotifyTrack, trackID},
		{"embed URL", "https://open.spotify.com/embed/playlist/" + playlistID, SpotifyPlaylist, playlistID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSpotifyReference(test.in)
			if err != nil {
				t.Fatalf("ParseSpotifyReference() error = %v", err)
			}
			if got.Kind != test.kind || got.ID != test.id {
				t.Fatalf("ParseSpotifyReference() = %#v, want kind %q ID %q", got, test.kind, test.id)
			}
			if got.URI != "spotify:"+string(test.kind)+":"+test.id {
				t.Errorf("URI = %q", got.URI)
			}
			if got.URL != "https://open.spotify.com/"+string(test.kind)+"/"+test.id {
				t.Errorf("URL = %q", got.URL)
			}
		})
	}
}

func TestParseSpotifyReferenceMalformed(t *testing.T) {
	t.Parallel()
	tests := []string{
		"",
		"not a URL",
		"http://open.spotify.com/track/11dFghVXANMlKmJXsNCbNl",
		"https://example.com/track/11dFghVXANMlKmJXsNCbNl",
		"https://open.spotify.com/album/11dFghVXANMlKmJXsNCbNl",
		"https://open.spotify.com/track/too-short",
		"https://open.spotify.com/track/11dFghVXANMlKmJXsNCbNl/extra",
		"spotify:album:11dFghVXANMlKmJXsNCbNl",
		"spotify:track:",
		"spotify:track:11dFghVXANMlKmJXsNCbNl:extra",
	}
	for _, value := range tests {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			_, err := ParseSpotifyReference(value)
			if !errors.Is(err, ErrInvalidSpotifyReference) {
				t.Fatalf("error = %v, want ErrInvalidSpotifyReference", err)
			}
		})
	}
}
