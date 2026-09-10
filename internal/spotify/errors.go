package spotify

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrPlaylistRestricted identifies Spotify's current playlist-content
	// ownership/collaboration restriction. Use errors.Is or errors.As.
	ErrPlaylistRestricted = errors.New("Spotify playlist items are restricted")
	// ErrQuotaExceeded identifies the structured QUOTA_EXCEEDED response used
	// by Spotify Development Mode. It is not retried as a short rate limit.
	ErrQuotaExceeded = errors.New("Spotify development-mode quota exceeded")
	// ErrUnsupportedItem identifies local, deleted, episode, and future item
	// types that cannot be converted to a catalog track.
	ErrUnsupportedItem = errors.New("unsupported Spotify playlist item")
	// ErrReauthorizationRequired identifies an invalid, expired, or revoked
	// refresh token. The user must complete authorization again.
	ErrReauthorizationRequired = errors.New("Spotify authorization is required again")
)

// APIError is a non-success response from the Spotify Web API.
type APIError struct {
	StatusCode int
	Message    string
	Reason     string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	message := e.Message
	if message == "" {
		message = "request failed"
	}
	if e.Reason != "" {
		return fmt.Sprintf("Spotify API: HTTP %d: %s (%s)", e.StatusCode, message, e.Reason)
	}
	return fmt.Sprintf("Spotify API: HTTP %d: %s", e.StatusCode, message)
}

func (e *APIError) Is(target error) bool {
	return target == ErrQuotaExceeded && e.Reason == "QUOTA_EXCEEDED"
}

// QuotaExceeded reports whether the response represents a Development Mode
// quota rather than a short rolling-window rate limit.
func (e *APIError) QuotaExceeded() bool { return e.Reason == "QUOTA_EXCEEDED" }

// PlaylistRestrictionError is returned for a 403 from either playlist
// endpoint, or when Get Playlist omits both its current items field and the
// deprecated tracks fallback. An empty, present items collection is not an
// error.
type PlaylistRestrictionError struct {
	PlaylistID   string
	StatusCode   int
	MissingItems bool
	Cause        error
}

func (e *PlaylistRestrictionError) Error() string {
	return fmt.Sprintf(
		"Spotify playlist %s contents are unavailable to this application: arbitrary public playlist contents are unavailable through this Spotify API configuration, which currently limits items to playlists owned by or shared collaboratively with the authenticated user; use an eligible app/API mode and account, use an owned/collaborative playlist, or export/paste the track list using text input",
		e.PlaylistID,
	)
}

func (e *PlaylistRestrictionError) Unwrap() error { return e.Cause }

func (e *PlaylistRestrictionError) Is(target error) bool {
	return target == ErrPlaylistRestricted
}

// ResponseDecodeError indicates a successful response whose JSON body was not
// valid for the documented response shape.
type ResponseDecodeError struct {
	Endpoint string
	Cause    error
}

func (e *ResponseDecodeError) Error() string {
	return fmt.Sprintf("decode Spotify response from %s: %v", e.Endpoint, e.Cause)
}

func (e *ResponseDecodeError) Unwrap() error { return e.Cause }

// OAuthError is an RFC 6749 error from accounts.spotify.com.
type OAuthError struct {
	StatusCode  int
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("Spotify OAuth: HTTP %d: %s: %s", e.StatusCode, e.Code, e.Description)
	}
	return fmt.Sprintf("Spotify OAuth: HTTP %d: %s", e.StatusCode, e.Code)
}

func (e *OAuthError) Is(target error) bool {
	return target == ErrReauthorizationRequired && e.Code == "invalid_grant"
}

// CallbackError is an error returned to a PKCE loopback redirect.
type CallbackError struct {
	Code        string
	Description string
}

func (e *CallbackError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("Spotify authorization callback: %s: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("Spotify authorization callback: %s", e.Code)
}
