package spotify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testTrackID    = "6rqhFgbbKwnb9MLmUQDhG6"
	testTrackID2   = "11dFghVXANMlKmJXsNCbNl"
	testTrackID3   = "1301WleyT98MSxVHPZCA6M"
	testPlaylistID = "3cEYpjA9oz9GiPac4AsH4n"
)

func TestClientFetchTrackMapsSourceTrack(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/tracks/"+testTrackID {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("market") != "IT" {
			t.Errorf("market = %q", request.URL.Query().Get("market"))
		}
		if request.Header.Get("Authorization") != "Bearer access-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		writeJSON(writer, http.StatusOK, `{
			"id":"`+testTrackID+`", "uri":"spotify:track:`+testTrackID+`",
			"name":"L'été – Extended Mix", "type":"track", "duration_ms":321000,
			"artists":[{"name":"Beyoncé"},{"name":"Guest"}],
			"album":{"name":"Dance Album"}, "external_ids":{"isrc":"ITABC2600001"}
		}`)
	}))
	defer server.Close()

	client := testClient(server, WithMarket("it"))
	track, err := client.FetchTrack(context.Background(), testTrackID)
	if err != nil {
		t.Fatalf("FetchTrack() error = %v", err)
	}
	if track.Source != "spotify" || track.SourceID != testTrackID || track.SpotifyURI != "spotify:track:"+testTrackID {
		t.Fatalf("identity fields = %#v", track)
	}
	if track.Title != "L'été – Extended Mix" || track.PrimaryArtist != "Beyoncé" || len(track.Artists) != 2 {
		t.Fatalf("metadata fields = %#v", track)
	}
	if track.Album != "Dance Album" || track.ISRC != "ITABC2600001" || track.DurationMS != 321000 || track.Position != 1 {
		t.Fatalf("catalog fields = %#v", track)
	}
	if track.OriginalText != "Beyoncé, Guest - L'été – Extended Mix" {
		t.Fatalf("OriginalText = %q", track.OriginalText)
	}
}

func TestClientFetchPlaylistPaginatesCurrentItemsAndLegacyFallback(t *testing.T) {
	t.Parallel()

	var offsetsMu sync.Mutex
	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/playlists/" + testPlaylistID:
			writeJSON(writer, http.StatusOK, `{
				"id":"`+testPlaylistID+`", "uri":"spotify:playlist:`+testPlaylistID+`",
				"name":"My Set", "description":"A set", "snapshot_id":"snapshot",
				"public":true, "collaborative":false,
				"owner":{"id":"owner","display_name":"Owner"},
				"items":{"items":[],"total":5}
			}`)
		case "/v1/playlists/" + testPlaylistID + "/items":
			if request.URL.Query().Get("limit") != "50" {
				t.Errorf("limit = %q", request.URL.Query().Get("limit"))
			}
			offset := request.URL.Query().Get("offset")
			offsetsMu.Lock()
			offsets = append(offsets, offset)
			offsetsMu.Unlock()
			switch offset {
			case "0":
				writeJSON(writer, http.StatusOK, `{
					"offset":0,"limit":50,"total":5,"next":"https://api.spotify.test/next?offset=2&limit=50",
					"items":[
						{"is_local":false,"item":`+trackDocument(testTrackID, "First", "track", false)+`},
						{"is_local":false,"item":{"id":"512ojhOuo1ktJprKbVcKyQ","name":"Podcast","type":"episode"}}
					]
				}`)
			case "2":
				writeJSON(writer, http.StatusOK, `{
					"offset":2,"limit":50,"total":5,"next":null,
					"items":[
						{"track":`+trackDocument(testTrackID2, "Legacy", "track", false)+`},
						{"is_local":true,"item":`+trackDocument(testTrackID3, "Local", "track", false)+`},
						{"item":null,"track":null}
					]
				}`)
			default:
				t.Errorf("unexpected offset %q", offset)
				writeJSON(writer, http.StatusInternalServerError, `{}`)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	playlist, err := testClient(server).FetchPlaylist(context.Background(), testPlaylistID)
	if err != nil {
		t.Fatalf("FetchPlaylist() error = %v", err)
	}
	if playlist.ID != testPlaylistID || playlist.Name != "My Set" || playlist.OwnerID != "owner" || !playlist.Public {
		t.Fatalf("playlist metadata = %#v", playlist)
	}
	if len(playlist.Tracks) != 2 {
		t.Fatalf("len(Tracks) = %d, tracks = %#v", len(playlist.Tracks), playlist.Tracks)
	}
	if playlist.Tracks[0].SourceID != testTrackID || playlist.Tracks[0].Position != 1 {
		t.Fatalf("first track = %#v", playlist.Tracks[0])
	}
	if playlist.Tracks[1].SourceID != testTrackID2 || playlist.Tracks[1].Position != 3 {
		t.Fatalf("legacy track = %#v", playlist.Tracks[1])
	}
	if fmt.Sprint(offsets) != "[0 2]" {
		t.Fatalf("offsets = %v", offsets)
	}
}

func TestClientFetchPlaylistDistinguishesEmptyFromMissingItems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		metadata      string
		wantError     bool
		wantItemsCall bool
	}{
		{"current empty", `{"id":"` + testPlaylistID + `","items":{"items":[],"total":0}}`, false, true},
		{"legacy empty", `{"id":"` + testPlaylistID + `","tracks":{"items":[],"total":0}}`, false, true},
		{"missing", `{"id":"` + testPlaylistID + `","name":"Visible metadata only"}`, true, false},
		{"null", `{"id":"` + testPlaylistID + `","items":null,"tracks":null}`, true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var itemCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/items") {
					itemCalls.Add(1)
					writeJSON(writer, http.StatusOK, `{"offset":0,"limit":50,"total":0,"next":null,"items":[]}`)
					return
				}
				writeJSON(writer, http.StatusOK, test.metadata)
			}))
			defer server.Close()

			playlist, err := testClient(server).FetchPlaylist(context.Background(), testPlaylistID)
			if test.wantError {
				if !errors.Is(err, ErrPlaylistRestricted) {
					t.Fatalf("error = %v, want ErrPlaylistRestricted", err)
				}
				var restriction *PlaylistRestrictionError
				if !errors.As(err, &restriction) || !restriction.MissingItems {
					t.Fatalf("error = %#v, want missing-items restriction", err)
				}
				if !strings.Contains(err.Error(), "export/paste") {
					t.Fatalf("error is not actionable: %v", err)
				}
			} else if err != nil || len(playlist.Tracks) != 0 {
				t.Fatalf("playlist = %#v, error = %v", playlist, err)
			}
			if got := itemCalls.Load() > 0; got != test.wantItemsCall {
				t.Fatalf("items endpoint called = %v, want %v", got, test.wantItemsCall)
			}
		})
	}
}

func TestClientFetchPlaylistWrapsForbiddenAsRestriction(t *testing.T) {
	t.Parallel()

	for _, forbiddenAt := range []string{"metadata", "items"} {
		t.Run(forbiddenAt, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				isItems := strings.HasSuffix(request.URL.Path, "/items")
				if (forbiddenAt == "metadata" && !isItems) || (forbiddenAt == "items" && isItems) {
					writeJSON(writer, http.StatusForbidden, `{"error":{"status":403,"message":"Forbidden"}}`)
					return
				}
				writeJSON(writer, http.StatusOK, `{"id":"`+testPlaylistID+`","items":{"items":[],"total":0}}`)
			}))
			defer server.Close()

			_, err := testClient(server).FetchPlaylist(context.Background(), testPlaylistID)
			if !errors.Is(err, ErrPlaylistRestricted) {
				t.Fatalf("error = %v, want restriction", err)
			}
			var restriction *PlaylistRestrictionError
			if !errors.As(err, &restriction) || restriction.StatusCode != http.StatusForbidden {
				t.Fatalf("restriction = %#v", restriction)
			}
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusForbidden {
				t.Fatalf("wrapped API error = %#v", apiError)
			}
		})
	}
}

func TestClientRetries429UsingRetryAfter(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	var delaysMu sync.Mutex
	var delays []time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			writer.Header().Set("Retry-After", "2")
			writeJSON(writer, http.StatusTooManyRequests, `{"error":{"status":429,"message":"slow down"}}`)
			return
		}
		writeJSON(writer, http.StatusOK, trackDocument(testTrackID, "Recovered", "track", false))
	}))
	defer server.Close()

	client := testClient(server,
		WithRetryPolicy(2, time.Millisecond, time.Second),
		WithRetryWait(func(_ context.Context, delay time.Duration) error {
			delaysMu.Lock()
			delays = append(delays, delay)
			delaysMu.Unlock()
			return nil
		}),
	)
	if _, err := client.FetchTrack(context.Background(), testTrackID); err != nil {
		t.Fatalf("FetchTrack() error = %v", err)
	}
	if requests.Load() != 2 || len(delays) != 1 || delays[0] != 2*time.Second {
		t.Fatalf("requests = %d, delays = %v", requests.Load(), delays)
	}
}

func TestClientDoesNotRetryQuotaExceeded(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writeJSON(writer, http.StatusTooManyRequests, `{"error":{"status":429,"message":"Too many requests","reason":"QUOTA_EXCEEDED"}}`)
	}))
	defer server.Close()

	client := testClient(server, WithRetryWait(func(context.Context, time.Duration) error {
		t.Fatal("quota exhaustion must not wait/retry")
		return nil
	}))
	_, err := client.FetchTrack(context.Background(), testTrackID)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("error = %v, want ErrQuotaExceeded", err)
	}
	var apiError *APIError
	if !errors.As(err, &apiError) || !apiError.QuotaExceeded() {
		t.Fatalf("API error = %#v", apiError)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestClientRetriesServerErrorsWithinBound(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requests.Add(1) < 3 {
			writeJSON(writer, http.StatusServiceUnavailable, `{"error":{"status":503,"message":"temporary"}}`)
			return
		}
		writeJSON(writer, http.StatusOK, trackDocument(testTrackID, "Recovered", "track", false))
	}))
	defer server.Close()

	client := testClient(server,
		WithRetryPolicy(2, time.Millisecond, time.Millisecond),
		WithRetryWait(func(context.Context, time.Duration) error { return nil }),
	)
	if _, err := client.FetchTrack(context.Background(), testTrackID); err != nil {
		t.Fatalf("FetchTrack() error = %v", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("requests = %d, want 3", requests.Load())
	}
}

func TestClientStopsAfterServerRetryBudget(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writeJSON(writer, http.StatusBadGateway, `{"error":{"status":502,"message":"still unavailable"}}`)
	}))
	defer server.Close()

	client := testClient(server,
		WithRetryPolicy(2, time.Millisecond, time.Millisecond),
		WithRetryWait(func(context.Context, time.Duration) error { return nil }),
	)
	_, err := client.FetchTrack(context.Background(), testTrackID)
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusBadGateway {
		t.Fatalf("error = %#v", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("requests = %d, want initial request plus two retries", requests.Load())
	}
}

func TestClientRetriesTransientNetworkError(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			return nil, temporaryNetworkError{}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(trackDocument(testTrackID, "Network recovery", "track", false))),
			Request:    request,
		}, nil
	})
	client := NewClient(testTokenSource(),
		WithHTTPClient(&http.Client{Transport: transport}),
		WithAPIBaseURL("https://api.spotify.test/v1"),
		WithRetryPolicy(1, time.Millisecond, time.Millisecond),
		WithRetryWait(func(context.Context, time.Duration) error { return nil }),
	)
	if _, err := client.FetchTrack(context.Background(), testTrackID); err != nil {
		t.Fatalf("FetchTrack() error = %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

func TestClientInvalidatesTokenOnceAfter401(t *testing.T) {
	t.Parallel()

	provider := &invalidatingProvider{}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("Authorization") == "Bearer bad-token" {
			writeJSON(writer, http.StatusUnauthorized, `{"error":{"status":401,"message":"expired"}}`)
			return
		}
		writeJSON(writer, http.StatusOK, trackDocument(testTrackID, "Fresh", "track", false))
	}))
	defer server.Close()

	client := NewClient(provider, WithHTTPClient(server.Client()), WithAPIBaseURL(server.URL+"/v1"))
	if _, err := client.FetchTrack(context.Background(), testTrackID); err != nil {
		t.Fatalf("FetchTrack() error = %v", err)
	}
	if requests.Load() != 2 || provider.invalidations.Load() != 1 {
		t.Fatalf("requests = %d, invalidations = %d", requests.Load(), provider.invalidations.Load())
	}
}

func TestClientReturnsTypedErrorsAndMalformedJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		body       string
		checkError func(*testing.T, error)
	}{
		{"not found", http.StatusNotFound, `{"error":{"status":404,"message":"not found"}}`, func(t *testing.T, err error) {
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.StatusCode != 404 || apiError.Message != "not found" {
				t.Fatalf("error = %#v", err)
			}
		}},
		{"malformed", http.StatusOK, `{"id":`, func(t *testing.T, err error) {
			var decodeError *ResponseDecodeError
			if !errors.As(err, &decodeError) {
				t.Fatalf("error type = %T, want *ResponseDecodeError", err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writeJSON(writer, test.status, test.body)
			}))
			defer server.Close()
			_, err := testClient(server).FetchTrack(context.Background(), testTrackID)
			if err == nil {
				t.Fatal("FetchTrack() unexpectedly succeeded")
			}
			test.checkError(t, err)
		})
	}
}

func testClient(server *httptest.Server, options ...ClientOption) *Client {
	options = append([]ClientOption{WithHTTPClient(server.Client()), WithAPIBaseURL(server.URL + "/v1")}, options...)
	return NewClient(testTokenSource(), options...)
}

func testTokenSource() TokenProvider {
	return StaticTokenSource{TokenValue: Token{AccessToken: "access-token", TokenType: "Bearer"}}
}

func writeJSON(writer http.ResponseWriter, status int, body string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, body)
}

func trackDocument(id, name, typ string, local bool) string {
	return fmt.Sprintf(`{
		"id":%q,"uri":%q,"name":%q,"type":%q,"is_local":%t,"duration_ms":180000,
		"artists":[{"name":"Artist"}],"album":{"name":"Album"},"external_ids":{"isrc":"ISRC"}
	}`, id, "spotify:track:"+id, name, typ, local)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type temporaryNetworkError struct{}

func (temporaryNetworkError) Error() string   { return "temporary network error" }
func (temporaryNetworkError) Timeout() bool   { return false }
func (temporaryNetworkError) Temporary() bool { return true }

type invalidatingProvider struct {
	invalidations atomic.Int32
}

func (p *invalidatingProvider) Token(context.Context) (Token, error) {
	if p.invalidations.Load() == 0 {
		return Token{AccessToken: "bad-token", TokenType: "Bearer"}, nil
	}
	return Token{AccessToken: "good-token", TokenType: "Bearer"}, nil
}

func (p *invalidatingProvider) Invalidate() { p.invalidations.Add(1) }
