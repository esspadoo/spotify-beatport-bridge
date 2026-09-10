// Package spotify implements the supported Spotify Web API integration.
//
// It deliberately uses only documented Spotify endpoints. In particular, it
// never scrapes open.spotify.com when playlist items are unavailable.
package spotify

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// ResourceType identifies a Spotify resource accepted as import input.
type ResourceType string

const (
	ResourceTrack    ResourceType = "track"
	ResourcePlaylist ResourceType = "playlist"
)

// Reference is a normalized Spotify URL or URI.
type Reference struct {
	Type ResourceType
	ID   string
}

// URI returns the canonical Spotify URI for the reference.
func (r Reference) URI() string {
	return "spotify:" + string(r.Type) + ":" + r.ID
}

func (r Reference) String() string { return r.URI() }

// ReferenceError reports why an input is not a supported Spotify track or
// playlist reference.
type ReferenceError struct {
	Input  string
	Reason string
}

func (e *ReferenceError) Error() string {
	return fmt.Sprintf("invalid Spotify reference %q: %s", e.Input, e.Reason)
}

// ParseReference accepts documented Spotify track/playlist URIs and HTTPS
// open.spotify.com links. Query parameters (including Spotify's tracking
// parameters) are ignored. Embed and locale-prefixed links are accepted too.
func ParseReference(input string) (Reference, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return Reference{}, referenceError(input, "input is empty")
	}

	if strings.HasPrefix(strings.ToLower(raw), "spotify:") {
		parts := strings.Split(raw, ":")
		if len(parts) != 3 {
			return Reference{}, referenceError(input, "URI must have the form spotify:track:<id> or spotify:playlist:<id>")
		}
		return makeReference(input, parts[1], parts[2])
	}

	u, err := url.Parse(raw)
	if err != nil {
		return Reference{}, referenceError(input, "malformed URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return Reference{}, referenceError(input, "URL must use HTTPS")
	}
	host := strings.ToLower(u.Hostname())
	if u.User != nil || (u.Port() != "" && u.Port() != "443") || (host != "open.spotify.com" && host != "www.open.spotify.com") {
		return Reference{}, referenceError(input, "URL host must be open.spotify.com")
	}

	parts := splitPath(u.EscapedPath())
	if len(parts) > 0 && strings.HasPrefix(strings.ToLower(parts[0]), "intl-") {
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.EqualFold(parts[0], "embed") {
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return Reference{}, referenceError(input, "URL path must identify one track or playlist")
	}
	typ, err := url.PathUnescape(parts[0])
	if err != nil {
		return Reference{}, referenceError(input, "malformed URL path")
	}
	id, err := url.PathUnescape(parts[1])
	if err != nil {
		return Reference{}, referenceError(input, "malformed Spotify ID")
	}
	return makeReference(input, typ, id)
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func makeReference(input, typ, id string) (Reference, error) {
	var resourceType ResourceType
	switch strings.ToLower(typ) {
	case string(ResourceTrack):
		resourceType = ResourceTrack
	case string(ResourcePlaylist):
		resourceType = ResourcePlaylist
	default:
		return Reference{}, referenceError(input, "only track and playlist resources are supported")
	}
	if !validSpotifyID(id) {
		return Reference{}, referenceError(input, "Spotify ID must contain exactly 22 base-62 characters")
	}
	return Reference{Type: resourceType, ID: id}, nil
}

func validSpotifyID(id string) bool {
	if len(id) != 22 {
		return false
	}
	for _, r := range id {
		if r > unicode.MaxASCII || !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func referenceError(input, reason string) error {
	return &ReferenceError{Input: input, Reason: reason}
}
