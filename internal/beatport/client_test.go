package beatport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rotatingAuth struct {
	mu            sync.Mutex
	tokens        []string
	index         int
	invalidations int
}

func (a *rotatingAuth) AccessToken(context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.tokens) == 0 {
		return "", ErrNotAuthenticated
	}
	return a.tokens[min(a.index, len(a.tokens)-1)], nil
}

func (a *rotatingAuth) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.invalidations++
	if a.index+1 < len(a.tokens) {
		a.index++
	}
}

func TestSearchRetainsGenericEnvelopeCompatibility(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/tracks/" {
			t.Fatalf("path = %q", got)
		}
		wantQuery := url.Values{
			"count": {"10"},
			"q":     {"Artist Track"},
		}
		if !reflect.DeepEqual(r.URL.Query(), wantQuery) {
			t.Fatalf("query = %v", r.URL.Query())
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization header was not set")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": 42, "name": "Track", "mix_name": "Extended Mix"}},
		})
	}))
	defer server.Close()

	client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
	tracks, err := client.Search(context.Background(), "Artist Track", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].ID != 42 || tracks[0].MixName != "Extended Mix" {
		t.Fatalf("tracks = %#v", tracks)
	}
}

func TestSearchQueryIsURLDecodedExactlyByServer(t *testing.T) {
	t.Parallel()
	queries := []string{
		"Rafael & Adam Ten - Beat Goes On",
		"Dom Dolla + Tyga Don't Worry Baby",
		"Beyoncé — Déjà Vu",
	}
	for _, query := range queries {
		query := query
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("q"); got != query {
					t.Fatalf("decoded q = %q, want %q (raw query %q)", got, query, r.URL.RawQuery)
				}
				if len(r.URL.Query()["q"]) != 1 || r.URL.Query().Get("count") != "25" {
					t.Fatalf("query parameters = %#v", r.URL.Query())
				}
				_, _ = w.Write([]byte(`{"results":[]}`))
			}))
			defer server.Close()

			client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
			if _, err := client.Search(context.Background(), query, 1, 25); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSearchMapsCurrentFrontendDataEnvelope(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantQuery := url.Values{
			"count": {"25"},
			"q":     {"Artist Signal"},
		}
		if r.URL.Path != "/tracks/" || !reflect.DeepEqual(r.URL.Query(), wantQuery) {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"track_id": 42001,
					"track_name": "Signal",
					"mix_name": "Extended Mix",
					"artists": [
						{"artist_id": 101, "artist_name": "Main Artist"},
						{"artist_id": 102, "artist_name": "Guest Artist"}
					],
					"isrc": "GBABC2600001",
					"length": 367000,
					"bpm": 127.5,
					"catalog_number": "CAT-LEGACY",
					"publish_date": "2026-08-21T00:00:00Z",
					"release_date": "2026-08-20T00:00:00Z",
					"release": {"release_id": 501, "release_name": "Signals"},
					"label": {"label_id": 601, "label_name": "Example Label"}
				},
				{
					"track_id": 42002,
					"track_name": "Signal Again",
					"mix_name": "Alice Remix",
					"artists": [
						{"id": 201, "name": "Producer", "type": "Artist"},
						{"id": 202, "name": "Alice", "type": "Remixer"}
					],
					"track_length_ms": 401000,
					"release": {
						"id": 502,
						"name": "Signals Two",
						"catalog_number": "CAT-CURRENT",
						"release_date": "2026-08-22",
						"label": {"id": 602, "name": "Nested Label"}
					}
				}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
	tracks, err := client.Search(context.Background(), "Artist Signal", 1, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %#v", tracks)
	}
	first := tracks[0]
	if first.ID != 42001 || first.Name != "Signal" || first.MixName != "Extended Mix" || first.ISRC != "GBABC2600001" {
		t.Fatalf("first track identity = %#v", first)
	}
	if first.LengthMS != 367000 || first.BPM != 127.5 || len(first.Artists) != 2 || first.Artists[1].Name != "Guest Artist" {
		t.Fatalf("first track metadata = %#v", first)
	}
	if first.Release.ID != 501 || first.Release.Name != "Signals" || first.Release.CatalogNumber != "CAT-LEGACY" || first.Release.Label.ID != 601 || first.Release.Label.Name != "Example Label" {
		t.Fatalf("first release = %#v", first.Release)
	}
	if first.PublishDate != "2026-08-21T00:00:00Z" || first.Release.PublishDate != "2026-08-20T00:00:00Z" {
		t.Fatalf("first dates = track %q, release %q", first.PublishDate, first.Release.PublishDate)
	}
	second := tracks[1]
	if second.LengthMS != 401000 || len(second.Artists) != 1 || second.Artists[0].Name != "Producer" || len(second.Remixers) != 1 || second.Remixers[0].Name != "Alice" {
		t.Fatalf("second artists/length = %#v", second)
	}
	if second.Release.ID != 502 || second.Release.CatalogNumber != "CAT-CURRENT" || second.Release.Label.Name != "Nested Label" {
		t.Fatalf("second release = %#v", second.Release)
	}
}

func TestSearchDoesNotInventUnverifiedSecondPageParameter(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
	tracks, err := client.Search(context.Background(), "Artist Signal", 2, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 0 || calls.Load() != 0 {
		t.Fatalf("tracks=%v calls=%d, want empty without request", tracks, calls.Load())
	}
}

func TestListPlaylistsPagination(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		page := r.URL.Query().Get("page")
		switch page {
		case "1":
			_, _ = w.Write([]byte(`{"page":"1/2","per_page":1,"results":[{"id":1,"name":"One"}]}`))
		case "2":
			_, _ = w.Write([]byte(`{"page":"2/2","per_page":1,"results":[{"id":2,"name":"Two"}]}`))
		default:
			t.Fatalf("unexpected page %q", page)
		}
	}))
	defer server.Close()

	client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
	playlists, err := client.ListPlaylists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(playlists) != 2 || playlists[1].ID != 2 {
		t.Fatalf("calls=%d playlists=%#v", calls.Load(), playlists)
	}
}

func TestGetPlaylistAndAllPlaylistTracks(t *testing.T) {
	t.Parallel()
	var trackCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/my/playlists/55/":
			if r.Method != http.MethodGet {
				t.Fatalf("playlist method = %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"id":55,"name":"Target","is_public":false,"track_count":3}`))
		case "/my/playlists/55/tracks/":
			trackCalls.Add(1)
			if r.URL.Query().Get("per_page") != "100" {
				t.Fatalf("per_page = %q", r.URL.Query().Get("per_page"))
			}
			switch r.URL.Query().Get("page") {
			case "1":
				_, _ = w.Write([]byte(`{
					"page":"1/2","per_page":2,"count":3,
					"results":[
						{"id":7001,"position":1,"track":{"id":101,"name":"One","mix_name":"Original Mix"}},
						{"id":7002,"position":2,"track":{"id":102,"name":"Two","mix_name":"Extended Mix"}}
					]
				}`))
			case "2":
				_, _ = w.Write([]byte(`{
					"page":"2/2","per_page":2,"count":3,
					"results":[
						{"id":7003,"position":3,"track":{"id":103,"name":"Three","mix_name":"Alice Remix"}}
					]
				}`))
			default:
				t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
			}
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
	target, err := client.GetPlaylist(context.Background(), 55)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID != 55 || target.Name != "Target" || target.TrackCount != 3 || target.IsPublic {
		t.Fatalf("playlist = %#v", target)
	}
	items, err := client.GetPlaylistTracks(context.Background(), 55)
	if err != nil {
		t.Fatal(err)
	}
	if trackCalls.Load() != 2 || len(items) != 3 || items[0].Track.ID != 101 || items[2].Track.ID != 103 || items[2].Position != 3 {
		t.Fatalf("calls=%d items=%#v", trackCalls.Load(), items)
	}
}

func TestGetPlaylistTrackIDsMapsFrontendTracks(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/my/playlists/55/tracks/ids/" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"tracks":[{"track_id":101},{"track_id":102},{"track_id":101},{"track_id":0}]}`))
	}))
	defer server.Close()

	client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
	ids, err := client.GetPlaylistTrackIDs(context.Background(), 55)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, map[int64]struct{}{101: {}, 102: {}}) {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestCreatePlaylistMatchesFrontendVisibilityPayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		public     bool
		wantPublic bool
	}{
		{name: "private omits is_public", public: false, wantPublic: false},
		{name: "public uses string true", public: true, wantPublic: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/my/playlists/" {
					t.Fatalf("request = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Fatalf("Content-Type = %q", r.Header.Get("Content-Type"))
				}
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if string(payload["name"]) != `"Frontend Semantics"` {
					t.Fatalf("name = %s", payload["name"])
				}
				publicValue, present := payload["is_public"]
				if present != test.wantPublic {
					t.Fatalf("is_public present = %v, payload = %s", present, publicValue)
				}
				if present && string(publicValue) != `"true"` {
					t.Fatalf("is_public = %s, want JSON string true", publicValue)
				}
				_, _ = w.Write([]byte(`{"id":77,"name":"Frontend Semantics"}`))
			}))
			defer server.Close()

			client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
			playlist, err := client.CreatePlaylist(context.Background(), "  Frontend Semantics  ", test.public)
			if err != nil {
				t.Fatal(err)
			}
			if playlist.ID != 77 || playlist.Name != "Frontend Semantics" {
				t.Fatalf("playlist = %#v", playlist)
			}
		})
	}
}

func TestAddTracksPreservesOrderAndPayload(t *testing.T) {
	t.Parallel()
	want := []int64{9, 3, 7}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/my/playlists/55/tracks/bulk/" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			TrackIDs []int64 `json:"track_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(body.TrackIDs, want) {
			t.Fatalf("track_ids=%v want %v", body.TrackIDs, want)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
	if err := client.AddTracks(context.Background(), 55, want); err != nil {
		t.Fatal(err)
	}
}

func TestAddTracksUsesOrderedHundredTrackBatches(t *testing.T) {
	t.Parallel()
	trackIDs := make([]int64, 205)
	for index := range trackIDs {
		trackIDs[index] = int64(index + 1)
	}
	var batches [][]int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/my/playlists/55/tracks/bulk/" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			TrackIDs []int64 `json:"track_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		batches = append(batches, append([]int64(nil), body.TrackIDs...))
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
	if err := client.AddTracks(context.Background(), 55, trackIDs); err != nil {
		t.Fatal(err)
	}
	want := [][]int64{trackIDs[:100], trackIDs[100:200], trackIDs[200:]}
	if !reflect.DeepEqual(batches, want) {
		t.Fatalf("batches = %#v", batches)
	}
}

func TestMutationIsNotBlindlyRetriedAfterServerError(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"uncertain write"}`))
	}))
	defer server.Close()
	client := NewClient(
		&rotatingAuth{tokens: []string{"token"}},
		WithBaseURLs(server.URL, server.URL),
		WithMaxRetries(3),
		WithSleep(func(context.Context, time.Duration) error { return nil }),
	)
	_, err := client.CreatePlaylist(context.Background(), "No Duplicate", false)
	if err == nil {
		t.Fatal("CreatePlaylist succeeded, want error")
	}
	if calls.Load() != 1 {
		t.Fatalf("POST calls=%d, want 1", calls.Load())
	}
}

func TestUnauthorizedInvalidatesOnceAndRetries(t *testing.T) {
	t.Parallel()
	auth := &rotatingAuth{tokens: []string{"old", "new"}}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer old" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"expired"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":1,"name":"ok"}`))
	}))
	defer server.Close()
	client := NewClient(auth, WithBaseURLs(server.URL, server.URL), WithMaxRetries(0))
	track, err := client.GetTrack(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if track.ID != 1 || calls.Load() != 2 || auth.invalidations != 1 {
		t.Fatalf("track=%#v calls=%d invalidations=%d", track, calls.Load(), auth.invalidations)
	}
}

func TestRetryAfterAndServerRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		first      int
		retryAfter string
		wantDelay  time.Duration
	}{
		{name: "rate limit", first: http.StatusTooManyRequests, retryAfter: "2", wantDelay: 2 * time.Second},
		{name: "server error", first: http.StatusInternalServerError},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					if test.retryAfter != "" {
						w.Header().Set("Retry-After", test.retryAfter)
					}
					w.WriteHeader(test.first)
					return
				}
				_, _ = w.Write([]byte(`{"id":8,"name":"ok"}`))
			}))
			defer server.Close()
			var delays []time.Duration
			client := NewClient(
				&rotatingAuth{tokens: []string{"token"}},
				WithBaseURLs(server.URL, server.URL),
				WithMaxRetries(1),
				WithSleep(func(_ context.Context, delay time.Duration) error {
					delays = append(delays, delay)
					return nil
				}),
			)
			if _, err := client.GetTrack(context.Background(), 8); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 || len(delays) != 1 {
				t.Fatalf("calls=%d delays=%v", calls.Load(), delays)
			}
			if test.wantDelay > 0 && delays[0] != test.wantDelay {
				t.Fatalf("delay=%v want %v", delays[0], test.wantDelay)
			}
		})
	}
}

func TestPermanentErrorsAndMalformedJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
	}{
		{name: "forbidden", status: http.StatusForbidden},
		{name: "not found", status: http.StatusNotFound},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"detail":"nope"}`))
			}))
			defer server.Close()
			client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
			_, err := client.GetTrack(context.Background(), 1)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != test.status || apiErr.Detail != "nope" {
				t.Fatalf("error = %#v", err)
			}
		})
	}

	t.Run("malformed json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"id":`))
		}))
		defer server.Close()
		client := NewClient(&rotatingAuth{tokens: []string{"token"}}, WithBaseURLs(server.URL, server.URL))
		if _, err := client.GetTrack(context.Background(), 1); err == nil {
			t.Fatal("expected malformed JSON error")
		}
	})
}
