package spotify

import (
	"errors"
	"testing"
)

func TestParseReference(t *testing.T) {
	t.Parallel()

	const trackID = "6rqhFgbbKwnb9MLmUQDhG6"
	const playlistID = "3cEYpjA9oz9GiPac4AsH4n"
	tests := []struct {
		name  string
		input string
		want  Reference
	}{
		{"track URL", "https://open.spotify.com/track/" + trackID, Reference{ResourceTrack, trackID}},
		{"playlist URL with tracking", " https://open.spotify.com/playlist/" + playlistID + "?si=abc&utm_source=test ", Reference{ResourcePlaylist, playlistID}},
		{"track URI", "spotify:track:" + trackID, Reference{ResourceTrack, trackID}},
		{"playlist URI", "spotify:playlist:" + playlistID, Reference{ResourcePlaylist, playlistID}},
		{"locale URL", "https://open.spotify.com/intl-it/track/" + trackID, Reference{ResourceTrack, trackID}},
		{"embed URL", "https://open.spotify.com/embed/playlist/" + playlistID + "/", Reference{ResourcePlaylist, playlistID}},
		{"tracking fragment", "https://open.spotify.com/track/" + trackID + "?si=abc#ignored", Reference{ResourceTrack, trackID}},
		{"www and explicit TLS port", "https://www.open.spotify.com:443/track/" + trackID, Reference{ResourceTrack, trackID}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseReference(test.input)
			if err != nil {
				t.Fatalf("ParseReference() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ParseReference() = %#v, want %#v", got, test.want)
			}
			if got.URI() != "spotify:"+string(got.Type)+":"+got.ID {
				t.Fatalf("URI() = %q", got.URI())
			}
		})
	}
}

func TestParseReferenceRejectsMalformedAndUnsupportedInput(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"not a URL",
		"http://open.spotify.com/track/6rqhFgbbKwnb9MLmUQDhG6",
		"https://example.com/track/6rqhFgbbKwnb9MLmUQDhG6",
		"https://open.spotify.com/album/6rqhFgbbKwnb9MLmUQDhG6",
		"https://open.spotify.com/track/too-short",
		"https://open.spotify.com/track/6rqhFgbbKwnb9MLmUQDhG6/extra",
		"spotify:track:bad:id",
		"spotify:episode:6rqhFgbbKwnb9MLmUQDhG6",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := ParseReference(input)
			if err == nil {
				t.Fatal("ParseReference() unexpectedly succeeded")
			}
			var referenceErr *ReferenceError
			if !errors.As(err, &referenceErr) {
				t.Fatalf("error type = %T, want *ReferenceError", err)
			}
		})
	}
}
