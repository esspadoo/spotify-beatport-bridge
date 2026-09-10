package spotify

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bpbridge/bpbridge/internal/model"
)

const (
	DefaultAPIBaseURL = "https://api.spotify.com/v1"
	playlistPageLimit = 50
	maxResponseBytes  = 16 << 20
)

// TokenProvider returns a usable bearer token. Implementations must be safe
// for concurrent use because a Client can be shared by callers.
type TokenProvider interface {
	Token(context.Context) (Token, error)
}

type tokenInvalidator interface {
	Invalidate()
}

// Playlist is playlist metadata plus all accessible catalog tracks in source
// order. Unsupported local/deleted/non-track items are intentionally skipped.
type Playlist struct {
	ID            string
	URI           string
	Name          string
	Description   string
	OwnerID       string
	OwnerName     string
	SnapshotID    string
	Public        bool
	Collaborative bool
	Tracks        []model.SourceTrack
}

// ClientOption configures a Spotify Web API client.
type ClientOption func(*Client)

// WithHTTPClient supplies the transport. The caller retains ownership of it.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// WithAPIBaseURL overrides the official API root, primarily for tests.
func WithAPIBaseURL(baseURL string) ClientOption {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithMarket applies a two-letter market to track and playlist reads.
func WithMarket(market string) ClientOption {
	return func(c *Client) { c.market = strings.ToUpper(strings.TrimSpace(market)) }
}

// WithRetryPolicy sets the maximum number of retries after the initial
// request and the exponential-delay bounds.
func WithRetryPolicy(maxRetries int, baseDelay, maxDelay time.Duration) ClientOption {
	return func(c *Client) {
		if maxRetries >= 0 {
			c.maxRetries = maxRetries
		}
		if baseDelay > 0 {
			c.retryBase = baseDelay
		}
		if maxDelay > 0 {
			c.retryMax = maxDelay
		}
	}
}

// WithRetryWait installs a cancellable wait function. It is useful for
// deterministic tests and embedding environments with their own scheduler.
func WithRetryWait(wait func(context.Context, time.Duration) error) ClientOption {
	return func(c *Client) {
		if wait != nil {
			c.wait = wait
		}
	}
}

// Client is a concurrency-safe Spotify Web API client.
type Client struct {
	baseURL    string
	market     string
	httpClient *http.Client
	tokens     TokenProvider
	maxRetries int
	retryBase  time.Duration
	retryMax   time.Duration
	wait       func(context.Context, time.Duration) error
	jitter     func(time.Duration) time.Duration
}

// NewClient creates a client using the official Spotify API. A TokenManager is
// the usual provider, although any testable TokenProvider can be injected.
func NewClient(tokens TokenProvider, options ...ClientOption) *Client {
	c := &Client{
		baseURL:    DefaultAPIBaseURL,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		tokens:     tokens,
		maxRetries: 3,
		retryBase:  250 * time.Millisecond,
		retryMax:   5 * time.Second,
		wait:       waitContext,
		jitter:     retryJitter,
	}
	for _, option := range options {
		option(c)
	}
	if c.retryMax < c.retryBase {
		c.retryMax = c.retryBase
	}
	return c
}

// FetchTrack retrieves a single catalog track using GET /v1/tracks/{id}.
func (c *Client) FetchTrack(ctx context.Context, id string) (model.SourceTrack, error) {
	if !validSpotifyID(id) {
		return model.SourceTrack{}, referenceError(id, "Spotify ID must contain exactly 22 base-62 characters")
	}
	var track trackWire
	if err := c.getJSON(ctx, "/tracks/"+id, c.marketQuery(nil), &track); err != nil {
		return model.SourceTrack{}, err
	}
	if !usableTrack(&track, false) {
		return model.SourceTrack{}, fmt.Errorf("%w: response is not a non-local catalog track", ErrUnsupportedItem)
	}
	return sourceTrack(track, 1), nil
}

// FetchPlaylist retrieves playlist metadata, detects Spotify's current access
// restriction, then reads every item through the current /items endpoint in
// pages of 50. It never falls back to scraping.
func (c *Client) FetchPlaylist(ctx context.Context, id string) (Playlist, error) {
	if !validSpotifyID(id) {
		return Playlist{}, referenceError(id, "Spotify ID must contain exactly 22 base-62 characters")
	}

	var metadata playlistWire
	if err := c.getJSON(ctx, "/playlists/"+id, c.marketQuery(nil), &metadata); err != nil {
		return Playlist{}, playlistError(id, err)
	}
	if !presentObject(metadata.Items) && !presentObject(metadata.LegacyTracks) {
		return Playlist{}, &PlaylistRestrictionError{
			PlaylistID:   id,
			MissingItems: true,
		}
	}

	playlist := Playlist{
		ID:            firstNonEmpty(metadata.ID, id),
		URI:           metadata.URI,
		Name:          metadata.Name,
		Description:   metadata.Description,
		OwnerID:       metadata.Owner.ID,
		OwnerName:     metadata.Owner.DisplayName,
		SnapshotID:    metadata.SnapshotID,
		Public:        metadata.Public,
		Collaborative: metadata.Collaborative,
	}

	offset := 0
	pageCount := 0
	for {
		pageCount++
		if pageCount > 10_000 {
			return Playlist{}, &ResponseDecodeError{
				Endpoint: "/playlists/" + id + "/items",
				Cause:    errors.New("pagination exceeded 10000 pages"),
			}
		}
		query := c.marketQuery(url.Values{
			"limit":  {strconv.Itoa(playlistPageLimit)},
			"offset": {strconv.Itoa(offset)},
		})
		var page playlistPageWire
		if err := c.getJSON(ctx, "/playlists/"+id+"/items", query, &page); err != nil {
			return Playlist{}, playlistError(id, err)
		}

		for index := range page.Items {
			entry := &page.Items[index]
			track := entry.Item
			if track == nil {
				track = entry.LegacyTrack
			}
			if !usableTrack(track, entry.IsLocal) {
				continue
			}
			playlist.Tracks = append(playlist.Tracks, sourceTrack(*track, offset+index+1))
		}

		if page.Next == "" {
			break
		}
		nextOffset, nextErr := playlistNextOffset(page.Next)
		if nextErr != nil || nextOffset <= offset {
			if nextErr == nil {
				nextErr = fmt.Errorf("next offset %d does not advance past %d", nextOffset, offset)
			}
			return Playlist{}, &ResponseDecodeError{
				Endpoint: "/playlists/" + id + "/items",
				Cause:    fmt.Errorf("invalid next page: %w", nextErr),
			}
		}
		offset = nextOffset
	}
	return playlist, nil
}

func playlistNextOffset(raw string) (int, error) {
	next, err := url.Parse(raw)
	if err != nil {
		return 0, fmt.Errorf("parse next URL: %w", err)
	}
	value := next.Query().Get("offset")
	if value == "" {
		return 0, errors.New("next URL has no offset")
	}
	offset, err := strconv.Atoi(value)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("next URL has invalid offset %q", value)
	}
	return offset, nil
}

// FetchReference resolves either supported reference type.
func (c *Client) FetchReference(ctx context.Context, reference Reference) ([]model.SourceTrack, *Playlist, error) {
	switch reference.Type {
	case ResourceTrack:
		track, err := c.FetchTrack(ctx, reference.ID)
		if err != nil {
			return nil, nil, err
		}
		return []model.SourceTrack{track}, nil, nil
	case ResourcePlaylist:
		playlist, err := c.FetchPlaylist(ctx, reference.ID)
		if err != nil {
			return nil, nil, err
		}
		return playlist.Tracks, &playlist, nil
	default:
		return nil, nil, referenceError(reference.URI(), "unsupported resource type")
	}
}

type trackWire struct {
	ID         string `json:"id"`
	URI        string `json:"uri"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	DurationMS int64  `json:"duration_ms"`
	IsLocal    bool   `json:"is_local"`
	Artists    []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Album struct {
		Name string `json:"name"`
	} `json:"album"`
	ExternalIDs struct {
		ISRC string `json:"isrc"`
	} `json:"external_ids"`
}

type playlistWire struct {
	ID            string          `json:"id"`
	URI           string          `json:"uri"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	SnapshotID    string          `json:"snapshot_id"`
	Public        bool            `json:"public"`
	Collaborative bool            `json:"collaborative"`
	Items         json.RawMessage `json:"items"`
	LegacyTracks  json.RawMessage `json:"tracks"`
	Owner         struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"owner"`
}

type playlistPageWire struct {
	Next   string              `json:"next"`
	Total  int                 `json:"total"`
	Offset int                 `json:"offset"`
	Limit  int                 `json:"limit"`
	Items  []playlistEntryWire `json:"items"`
}

type playlistEntryWire struct {
	IsLocal     bool       `json:"is_local"`
	Item        *trackWire `json:"item"`
	LegacyTrack *trackWire `json:"track"`
}

func usableTrack(track *trackWire, wrapperLocal bool) bool {
	if track == nil || wrapperLocal || track.IsLocal || track.ID == "" {
		return false
	}
	return track.Type == "track"
}

func sourceTrack(track trackWire, position int) model.SourceTrack {
	artists := make([]string, 0, len(track.Artists))
	for _, artist := range track.Artists {
		if name := strings.TrimSpace(artist.Name); name != "" {
			artists = append(artists, name)
		}
	}
	uri := track.URI
	if uri == "" {
		uri = "spotify:track:" + track.ID
	}
	original := track.Name
	if len(artists) != 0 {
		original = strings.Join(artists, ", ") + " - " + track.Name
	}
	result := model.SourceTrack{
		Source:       "spotify",
		SourceID:     track.ID,
		SpotifyURI:   uri,
		Title:        track.Name,
		Artists:      artists,
		ISRC:         track.ExternalIDs.ISRC,
		DurationMS:   track.DurationMS,
		Album:        track.Album.Name,
		OriginalText: original,
		Position:     position,
	}
	if len(artists) != 0 {
		result.PrimaryArtist = artists[0]
	}
	return result
}

func (c *Client) marketQuery(values url.Values) url.Values {
	if values == nil {
		values = make(url.Values)
	}
	if c.market != "" {
		values.Set("market", c.market)
	}
	return values
}

func presentObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func playlistError(id string, err error) error {
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.StatusCode == http.StatusForbidden {
		return &PlaylistRestrictionError{
			PlaylistID: id,
			StatusCode: apiError.StatusCode,
			Cause:      err,
		}
	}
	return err
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, destination any) error {
	if c.tokens == nil {
		return errors.New("Spotify client has no token provider")
	}
	if c.httpClient == nil {
		return errors.New("Spotify client has no HTTP client")
	}
	if c.baseURL == "" {
		return errors.New("Spotify client has no API base URL")
	}
	endpoint := c.baseURL + path
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	if err := validateSecureEndpoint(endpoint); err != nil {
		return err
	}

	transientRetries := 0
	authRetried := false
	for {
		token, err := c.tokens.Token(ctx)
		if err != nil {
			return fmt.Errorf("get Spotify access token: %w", err)
		}
		if token.AccessToken == "" {
			return errors.New("Spotify token provider returned an empty access token")
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("create Spotify request: %w", err)
		}
		tokenType := token.TokenType
		if tokenType == "" {
			tokenType = "Bearer"
		}
		request.Header.Set("Authorization", tokenType+" "+token.AccessToken)
		request.Header.Set("Accept", "application/json")

		response, requestErr := c.httpClient.Do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !transientNetworkError(requestErr) || transientRetries >= c.maxRetries {
				return fmt.Errorf("Spotify request failed: %w", requestErr)
			}
			delay := c.retryDelay(transientRetries)
			transientRetries++
			if err := c.wait(ctx, delay); err != nil {
				return err
			}
			continue
		}

		body, readErr := readResponse(response)
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if transientNetworkError(readErr) && transientRetries < c.maxRetries {
				delay := c.retryDelay(transientRetries)
				transientRetries++
				if err := c.wait(ctx, delay); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("read Spotify response: %w", readErr)
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			if err := json.Unmarshal(body, destination); err != nil {
				return &ResponseDecodeError{Endpoint: path, Cause: err}
			}
			return nil
		}

		apiError := decodeAPIError(response.StatusCode, response.Header, body)
		if response.StatusCode == http.StatusUnauthorized && !authRetried {
			if invalidator, ok := c.tokens.(tokenInvalidator); ok {
				invalidator.Invalidate()
				authRetried = true
				continue
			}
		}
		if apiError.QuotaExceeded() {
			return apiError
		}
		if (response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500) && transientRetries < c.maxRetries {
			delay := apiError.RetryAfter
			if delay <= 0 {
				delay = c.retryDelay(transientRetries)
			}
			transientRetries++
			if err := c.wait(ctx, delay); err != nil {
				return err
			}
			continue
		}
		return apiError
	}
}

func readResponse(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return body, nil
}

func decodeAPIError(status int, header http.Header, body []byte) *APIError {
	var envelope struct {
		Error struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
			Reason  string `json:"reason"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	message := envelope.Error.Message
	if message == "" {
		message = strings.TrimSpace(http.StatusText(status))
	}
	return &APIError{
		StatusCode: status,
		Message:    message,
		Reason:     envelope.Error.Reason,
		RetryAfter: parseRetryAfter(header.Get("Retry-After"), time.Now()),
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseUint(value, 10, 31); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

func transientNetworkError(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (c *Client) retryDelay(attempt int) time.Duration {
	delay := c.retryBase
	for index := 0; index < attempt && delay < c.retryMax/2; index++ {
		delay *= 2
	}
	if delay > c.retryMax {
		delay = c.retryMax
	}
	return c.jitter(delay)
}

func retryJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	// Add up to 25 percent full jitter. crypto/rand avoids global PRNG state and
	// makes concurrent clients independent; failure simply uses the base delay.
	var raw [8]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return delay
	}
	window := uint64(delay / 4)
	if window == 0 {
		return delay
	}
	return delay + time.Duration(binary.LittleEndian.Uint64(raw[:])%(window+1))
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// validateSecureEndpoint permits HTTPS and explicit loopback HTTP. The latter
// is needed for local OAuth callbacks and mocked tests, never remote traffic.
func validateSecureEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return errors.New("Spotify endpoint is malformed")
	}
	if parsed.User != nil {
		return errors.New("Spotify endpoint must not include user information")
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
	return errors.New("Spotify endpoint must use HTTPS")
}
