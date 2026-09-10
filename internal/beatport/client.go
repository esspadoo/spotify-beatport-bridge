package beatport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bpbridge/bpbridge/internal/model"
)

const (
	DefaultAPIBase    = "https://api.beatport.com/v4"
	DefaultSearchBase = "https://api.beatport.com/search/v1"
)

var (
	ErrNotAuthenticated = errors.New("Beatport authentication is required")
	ErrInvalidRefresh   = errors.New("Beatport refresh token is invalid")
)

// TokenProvider deliberately keeps authentication independent from API routes.
type TokenProvider interface {
	AccessToken(context.Context) (string, error)
	Invalidate()
}

type APIError struct {
	Status     int
	Method     string
	URL        string
	Detail     string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("Beatport API %s %s returned HTTP %d", e.Method, e.URL, e.Status)
	}
	return fmt.Sprintf("Beatport API %s %s returned HTTP %d: %s", e.Method, e.URL, e.Status, e.Detail)
}

type Client struct {
	httpClient *http.Client
	apiBase    string
	searchBase string
	auth       TokenProvider
	maxRetries int
	userAgent  string
	sleep      func(context.Context, time.Duration) error
}

type ClientOption func(*Client)

func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

func WithBaseURLs(apiBase, searchBase string) ClientOption {
	return func(c *Client) {
		if apiBase != "" {
			c.apiBase = strings.TrimRight(apiBase, "/")
		}
		if searchBase != "" {
			c.searchBase = strings.TrimRight(searchBase, "/")
		}
	}
}

func WithMaxRetries(n int) ClientOption {
	return func(c *Client) {
		if n >= 0 {
			c.maxRetries = n
		}
	}
}

func WithSleep(fn func(context.Context, time.Duration) error) ClientOption {
	return func(c *Client) {
		if fn != nil {
			c.sleep = fn
		}
	}
}

func NewClient(auth TokenProvider, options ...ClientOption) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiBase:    DefaultAPIBase,
		searchBase: DefaultSearchBase,
		auth:       auth,
		maxRetries: 3,
		userAgent:  "bpbridge/1",
		sleep: func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
	for _, option := range options {
		option(c)
	}
	return c
}

type page[T any] struct {
	Next     *string `json:"next"`
	Previous *string `json:"previous"`
	Count    int     `json:"count"`
	Page     string  `json:"page"`
	PerPage  int     `json:"per_page"`
	Results  []T     `json:"results"`
}

type searchEnvelope struct {
	page[model.BeatportTrack]
	Tracks []model.BeatportTrack `json:"tracks"`
	Data   []frontendSearchTrack `json:"data"`
}

// frontendSearchTrack mirrors the compact document returned by Beatport's
// search-v1 service. It is deliberately kept private: the rest of BPBridge
// works with the stable model.BeatportTrack representation used by API v4.
//
// The current frontend adapter consumes the *_id/*_name fields and length.
// A second frontend surface uses id/name/type and track_length_ms, so those
// aliases are accepted too. Keeping both here makes an API rollout between the
// two observed shapes harmless.
type frontendSearchTrack struct {
	ID            int64                  `json:"id"`
	Name          string                 `json:"name"`
	TrackID       int64                  `json:"track_id"`
	TrackName     string                 `json:"track_name"`
	MixName       string                 `json:"mix_name"`
	Artists       []frontendSearchArtist `json:"artists"`
	ISRC          string                 `json:"isrc"`
	Length        int64                  `json:"length"`
	TrackLengthMS int64                  `json:"track_length_ms"`
	BPM           float64                `json:"bpm"`
	CatalogNumber string                 `json:"catalog_number"`
	PublishDate   string                 `json:"publish_date"`
	ReleaseDate   string                 `json:"release_date"`
	Release       frontendSearchRelease  `json:"release"`
	Label         frontendSearchLabel    `json:"label"`
}

type frontendSearchArtist struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	ArtistID   int64  `json:"artist_id"`
	ArtistName string `json:"artist_name"`
}

type frontendSearchRelease struct {
	ID            int64               `json:"id"`
	Name          string              `json:"name"`
	ReleaseID     int64               `json:"release_id"`
	ReleaseName   string              `json:"release_name"`
	PublishDate   string              `json:"publish_date"`
	ReleaseDate   string              `json:"release_date"`
	CatalogNumber string              `json:"catalog_number"`
	Label         frontendSearchLabel `json:"label"`
}

type frontendSearchLabel struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	LabelID   int64  `json:"label_id"`
	LabelName string `json:"label_name"`
}

func (c *Client) Search(ctx context.Context, query string, pageNumber, perPage int) ([]model.BeatportTrack, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("Beatport search query is empty")
	}
	if pageNumber < 1 {
		pageNumber = 1
	}
	// The current search-v1 frontend contract exposes a bounded `count`, but no
	// verified offset/page parameter. Returning an empty second page lets the
	// orchestration pagination contract terminate without inventing a query
	// parameter that the current Beatport application does not use.
	if pageNumber > 1 {
		return nil, nil
	}
	if perPage < 1 {
		perPage = 25
	}
	values := url.Values{
		"q":     {query},
		"count": {strconv.Itoa(perPage)},
	}
	endpoint := c.searchBase + "/tracks/?" + values.Encode()
	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var result searchEnvelope
	envelopeErr := json.Unmarshal(body, &result)
	if envelopeErr == nil {
		switch {
		case result.Data != nil:
			tracks := make([]model.BeatportTrack, 0, len(result.Data))
			for _, raw := range result.Data {
				tracks = append(tracks, raw.beatportTrack())
			}
			return tracks, nil
		case result.Results != nil:
			return result.Results, nil
		case result.Tracks != nil:
			return result.Tracks, nil
		}
	}
	var tracks []model.BeatportTrack
	if arrayErr := json.Unmarshal(body, &tracks); arrayErr == nil {
		return tracks, nil
	}
	if envelopeErr != nil {
		return nil, fmt.Errorf("decode Beatport search response: %w", envelopeErr)
	}
	return nil, errors.New("decode Beatport search response: unrecognized response shape")
}

func (track frontendSearchTrack) beatportTrack() model.BeatportTrack {
	artists := make([]model.Artist, 0, len(track.Artists))
	remixers := make([]model.Artist, 0, len(track.Artists))
	for _, raw := range track.Artists {
		artist := model.Artist{ID: firstPositive(raw.ID, raw.ArtistID), Name: firstNonEmpty(raw.Name, raw.ArtistName)}
		if artist.ID == 0 && artist.Name == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(raw.Type), "remixer") {
			remixers = append(remixers, artist)
		} else {
			artists = append(artists, artist)
		}
	}

	label := track.Label.beatportLabel()
	if label.ID == 0 && label.Name == "" {
		label = track.Release.Label.beatportLabel()
	}
	catalogNumber := firstNonEmpty(track.CatalogNumber, track.Release.CatalogNumber)
	releaseDate := firstNonEmpty(track.Release.ReleaseDate, track.Release.PublishDate, track.ReleaseDate)
	lengthMS := track.TrackLengthMS
	if lengthMS <= 0 {
		lengthMS = track.Length
	}

	return model.BeatportTrack{
		ID:       firstPositive(track.TrackID, track.ID),
		Name:     firstNonEmpty(track.TrackName, track.Name),
		MixName:  track.MixName,
		Artists:  artists,
		Remixers: remixers,
		ISRC:     track.ISRC,
		LengthMS: lengthMS,
		BPM:      track.BPM,
		Release: model.Release{
			ID:            firstPositive(track.Release.ID, track.Release.ReleaseID),
			Name:          firstNonEmpty(track.Release.Name, track.Release.ReleaseName),
			PublishDate:   releaseDate,
			CatalogNumber: catalogNumber,
			Label:         label,
		},
		PublishDate: firstNonEmpty(track.PublishDate, track.ReleaseDate, releaseDate),
	}
}

func (label frontendSearchLabel) beatportLabel() model.Label {
	return model.Label{
		ID:   firstPositive(label.ID, label.LabelID),
		Name: firstNonEmpty(label.Name, label.LabelName),
	}
}

func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (c *Client) GetTrack(ctx context.Context, id int64) (model.BeatportTrack, error) {
	var track model.BeatportTrack
	body, err := c.get(ctx, fmt.Sprintf("%s/catalog/tracks/%d/", c.apiBase, id))
	if err != nil {
		return track, err
	}
	if err := json.Unmarshal(body, &track); err != nil {
		return track, fmt.Errorf("decode Beatport track: %w", err)
	}
	return track, nil
}

func (c *Client) ListPlaylists(ctx context.Context) ([]model.BeatportPlaylist, error) {
	var all []model.BeatportPlaylist
	for pageNumber := 1; ; pageNumber++ {
		values := url.Values{"page": {strconv.Itoa(pageNumber)}, "per_page": {"100"}}
		body, err := c.get(ctx, c.apiBase+"/my/playlists/?"+values.Encode())
		if err != nil {
			return nil, err
		}
		var current page[model.BeatportPlaylist]
		if err := decodeFlexible(body, &current); err != nil {
			return nil, fmt.Errorf("decode Beatport playlists page %d: %w", pageNumber, err)
		}
		all = append(all, current.Results...)
		if !hasNextPage(current.Page, current.Next, pageNumber, len(current.Results), current.PerPage, current.Count, len(all)) {
			return all, nil
		}
	}
}

func (c *Client) GetPlaylist(ctx context.Context, id int64) (model.BeatportPlaylist, error) {
	var playlist model.BeatportPlaylist
	body, err := c.get(ctx, fmt.Sprintf("%s/my/playlists/%d/", c.apiBase, id))
	if err != nil {
		return playlist, err
	}
	if err := json.Unmarshal(body, &playlist); err != nil {
		return playlist, fmt.Errorf("decode Beatport playlist: %w", err)
	}
	return playlist, nil
}

func (c *Client) GetPlaylistTracks(ctx context.Context, id int64) ([]model.BeatportPlaylistItem, error) {
	var all []model.BeatportPlaylistItem
	for pageNumber := 1; ; pageNumber++ {
		values := url.Values{"page": {strconv.Itoa(pageNumber)}, "per_page": {"100"}}
		endpoint := fmt.Sprintf("%s/my/playlists/%d/tracks/?%s", c.apiBase, id, values.Encode())
		body, err := c.get(ctx, endpoint)
		if err != nil {
			return nil, err
		}
		var current page[model.BeatportPlaylistItem]
		if err := decodeFlexible(body, &current); err != nil {
			return nil, fmt.Errorf("decode Beatport playlist tracks page %d: %w", pageNumber, err)
		}
		all = append(all, current.Results...)
		if !hasNextPage(current.Page, current.Next, pageNumber, len(current.Results), current.PerPage, current.Count, len(all)) {
			return all, nil
		}
	}
}

func (c *Client) GetPlaylistTrackIDs(ctx context.Context, id int64) (map[int64]struct{}, error) {
	endpoint := fmt.Sprintf("%s/my/playlists/%d/tracks/ids/", c.apiBase, id)
	body, err := c.get(ctx, endpoint)
	if err == nil {
		ids, decodeErr := decodeIDs(body)
		if decodeErr == nil {
			return ids, nil
		}
	}
	// The IDs route is an optimization. Full pagination remains authoritative
	// and is required for duplicate prevention if the route changes.
	items, fallbackErr := c.GetPlaylistTracks(ctx, id)
	if fallbackErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, fallbackErr
	}
	ids := make(map[int64]struct{}, len(items))
	for _, item := range items {
		ids[item.Track.ID] = struct{}{}
	}
	return ids, nil
}

func (c *Client) CreatePlaylist(ctx context.Context, name string, public bool) (model.BeatportPlaylist, error) {
	var playlist model.BeatportPlaylist
	name = strings.TrimSpace(name)
	if name == "" {
		return playlist, errors.New("playlist name is empty")
	}
	payload := map[string]any{"name": name}
	if public {
		// Match the current web form exactly. It sends the string "true" for a
		// checked public toggle and omits is_public for the private default.
		payload["is_public"] = "true"
	}
	body, err := c.jsonRequest(ctx, http.MethodPost, c.apiBase+"/my/playlists/", payload)
	if err != nil {
		return playlist, err
	}
	if err := json.Unmarshal(body, &playlist); err != nil {
		return playlist, fmt.Errorf("decode created Beatport playlist: %w", err)
	}
	return playlist, nil
}

// AddTracks appends batches sequentially so source order is preserved.
func (c *Client) AddTracks(ctx context.Context, playlistID int64, trackIDs []int64) error {
	const batchSize = 100
	for start := 0; start < len(trackIDs); start += batchSize {
		end := min(start+batchSize, len(trackIDs))
		payload := map[string]any{"track_ids": trackIDs[start:end]}
		endpoint := fmt.Sprintf("%s/my/playlists/%d/tracks/bulk/", c.apiBase, playlistID)
		if _, err := c.jsonRequest(ctx, http.MethodPost, endpoint, payload); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) get(ctx context.Context, endpoint string) ([]byte, error) {
	return c.request(ctx, http.MethodGet, endpoint, nil, "")
}

func (c *Client) jsonRequest(ctx context.Context, method, endpoint string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.request(ctx, method, endpoint, body, "application/json")
}

func (c *Client) request(ctx context.Context, method, endpoint string, payload []byte, contentType string) ([]byte, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	refreshed := false
	idempotent := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if c.auth == nil {
			return nil, ErrNotAuthenticated
		}
		token, err := c.auth.AccessToken(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if idempotent && attempt < c.maxRetries && retryableNetworkError(err) {
				if sleepErr := c.sleep(ctx, backoff(attempt)); sleepErr != nil {
					return nil, sleepErr
				}
				continue
			}
			return nil, fmt.Errorf("Beatport request failed: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return body, nil
		}
		apiErr := makeAPIError(resp, method, endpoint, body)
		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			refreshed = true
			c.auth.Invalidate()
			continue
		}
		if idempotent && attempt < c.maxRetries && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) {
			delay := apiErr.RetryAfter
			if delay <= 0 {
				delay = backoff(attempt)
			}
			if sleepErr := c.sleep(ctx, delay); sleepErr != nil {
				return nil, sleepErr
			}
			continue
		}
		return nil, apiErr
	}
}

func validateEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("invalid Beatport endpoint %q", endpoint)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" {
		ip := net.ParseIP(parsed.Hostname())
		if ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("refusing non-HTTPS Beatport endpoint %q", endpoint)
}

func makeAPIError(resp *http.Response, method, endpoint string, body []byte) *APIError {
	var envelope struct {
		Detail  any    `json:"detail"`
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &envelope)
	detail := envelope.Message
	if detail == "" && envelope.Detail != nil {
		detail = fmt.Sprint(envelope.Detail)
	}
	if detail == "" && envelope.Error != nil {
		detail = fmt.Sprint(envelope.Error)
	}
	if detail == "" && len(body) > 0 {
		detail = strings.TrimSpace(string(body))
		if len(detail) > 500 {
			detail = detail[:500]
		}
	}
	return &APIError{
		Status:     resp.StatusCode,
		Method:     method,
		URL:        endpoint,
		Detail:     detail,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
}

func decodeFlexible[T any](body []byte, destination *page[T]) error {
	if err := json.Unmarshal(body, destination); err == nil && destination.Results != nil {
		return nil
	}
	var raw []T
	if err := json.Unmarshal(body, &raw); err == nil {
		destination.Results = raw
		return nil
	}
	return json.Unmarshal(body, destination)
}

func decodeIDs(body []byte) (map[int64]struct{}, error) {
	ids := map[int64]struct{}{}
	var raw []int64
	if err := json.Unmarshal(body, &raw); err == nil {
		addIDs(ids, raw)
		return ids, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	recognized := false
	for _, key := range []string{"results", "track_ids", "ids"} {
		encoded, ok := envelope[key]
		if !ok {
			continue
		}
		recognized = true
		var values []int64
		if err := json.Unmarshal(encoded, &values); err != nil {
			return nil, fmt.Errorf("decode Beatport playlist track IDs field %q: %w", key, err)
		}
		addIDs(ids, values)
	}
	if encoded, ok := envelope["tracks"]; ok {
		recognized = true
		var tracks []struct {
			TrackID int64 `json:"track_id"`
			ID      int64 `json:"id"`
		}
		if err := json.Unmarshal(encoded, &tracks); err == nil {
			for _, track := range tracks {
				addIDs(ids, []int64{firstPositive(track.TrackID, track.ID)})
			}
		} else {
			var values []int64
			if numberErr := json.Unmarshal(encoded, &values); numberErr != nil {
				return nil, fmt.Errorf("decode Beatport playlist tracks IDs: %w", err)
			}
			addIDs(ids, values)
		}
	}
	if !recognized {
		return nil, errors.New("decode Beatport playlist track IDs: unrecognized response shape")
	}
	return ids, nil
}

func addIDs(destination map[int64]struct{}, values []int64) {
	for _, id := range values {
		if id > 0 {
			destination[id] = struct{}{}
		}
	}
}

func hasNextPage(pageValue string, next *string, pageNumber, resultCount, perPage, totalCount, accumulated int) bool {
	if next != nil && *next != "" {
		return true
	}
	parts := strings.Split(pageValue, "/")
	if len(parts) == 2 {
		current, currentErr := strconv.Atoi(parts[0])
		total, totalErr := strconv.Atoi(parts[1])
		if currentErr == nil && totalErr == nil {
			return current < total
		}
	}
	if totalCount > accumulated && pageNumber < 10000 {
		return true
	}
	return perPage > 0 && resultCount >= perPage && pageNumber < 10000
}

func retryableNetworkError(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) || errors.Is(err, io.ErrUnexpectedEOF)
}

func backoff(attempt int) time.Duration {
	base := 250 * time.Millisecond * time.Duration(1<<min(attempt, 5))
	return base + time.Duration(rand.IntN(150))*time.Millisecond
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}
