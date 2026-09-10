# BPBridge

BPBridge is a standalone Windows terminal application that turns Spotify
tracks/playlists or ordinary song lists into a Beatport playlist. It is a
metadata and playlist-membership tool only: it never downloads or streams
audio.

It provides deterministic, explainable matching; an intelligent Extended Mix
preference; interactive resolution of uncertain results; full target-playlist
duplicate protection; ordered additions; dry runs; and JSON/CSV reports.

## Install

Download the Windows x86-64 ZIP, extract it, and run `bpbridge.exe` in
PowerShell or Windows Terminal. Go is not required to run the executable.

For a first-time setup:

```powershell
.\bpbridge.exe setup
.\bpbridge.exe auth spotify
.\bpbridge.exe auth beatport
.\bpbridge.exe auth status
```

`setup` writes non-secret settings to
`%LOCALAPPDATA%\BPBridge\config.toml`. Spotify secrets and both providers'
tokens are stored in Windows Credential Manager, never in that TOML file.

## Spotify setup and the 2026 API rules

1. Create an app in the [Spotify Developer Dashboard](https://developer.spotify.com/dashboard).
   The app owner must currently have Spotify Premium for Development Mode.
2. Copy the Client ID into `bpbridge setup` or set `SPOTIFY_CLIENT_ID`.
3. For individual catalog-track imports, store the Client Secret through the
   hidden `setup` prompt, set `SPOTIFY_CLIENT_SECRET`, or run
   `bpbridge auth spotify` and enter it when prompted.
4. For playlist imports, add this exact URI to the app's allowed redirect URIs:

   ```text
   http://127.0.0.1:8000/callback
   ```

   Then run:

```powershell
.\bpbridge.exe auth spotify --user
```

The user flow uses Authorization Code with PKCE, an explicit loopback IP, and
the minimal `playlist-read-private playlist-read-collaborative` scopes. The
callback listens only on `127.0.0.1:8000`. Spotify permits HTTP for explicit
loopback IPs; do not register `localhost`. If port 8000 is unavailable, register
another fixed loopback port and pass the identical URI with `--redirect-uri`.

Spotify changed Development Mode substantially in 2026. Playlist items may be
available only when the authenticated user owns or collaborates on the
playlist. A public web page does not imply API access. BPBridge uses only the
official Web API and explicitly treats HTTP 403 or an omitted playlist `items`
field as a restriction—not as an empty playlist. It never scrapes
open.spotify.com. Use an eligible app/API mode, an owned/collaborative
playlist, or export/paste the track list with `text` when this occurs.

Development Mode currently has a five-user allowlist. Spotify's July 2026
update permits up to 25 Client IDs per developer, but those IDs share the
developer quota. A 429 with `QUOTA_EXCEEDED` is surfaced immediately rather
than retried as a short rolling-window rate limit. See Spotify's
[migration guide](https://developer.spotify.com/documentation/web-api/tutorials/february-2026-migration-guide),
[quota modes](https://developer.spotify.com/documentation/web-api/concepts/quota-modes),
[quota update](https://developer.spotify.com/blog/2026-07-23-web-api-quota-updates),
and [redirect URI rules](https://developer.spotify.com/documentation/web-api/concepts/redirect_uri).

## Beatport authentication

Normal authentication prompts for a username/email and a hidden password. The
password is used only for the verified login/authorization-code exchange and
is never stored or logged. Access and refresh tokens go to Windows Credential
Manager and refresh five minutes before expiry.

```powershell
.\bpbridge.exe auth beatport
```

Beatport's current browser identity flow may require MFA and is not exposed as
a stable third-party API. If the legacy flow does not support your account,
import token JSON obtained through your own legitimate authenticated session:

```powershell
.\bpbridge.exe auth beatport --token
```

Paste a one-line object containing `access_token`, `refresh_token`, and
`expires_in`; terminal input is hidden. For redirected input, keep the token
file private and delete it afterward:

```powershell
Get-Content .\beatport-token.json -Raw | .\bpbridge.exe auth beatport --token
```

Never put a password or token directly in command arguments. Logout removes
stored credentials:

```powershell
.\bpbridge.exe logout beatport
.\bpbridge.exe logout spotify
```

The public Beatport authorization client ID observed in BeatportDL is the
default for the legacy code flow. It is not a user secret and can be replaced
with `BEATPORT_CLIENT_ID`. The website's separate API4 client currently has
code grant disabled, so BPBridge does not blindly substitute it. Details and
the exact observed routes are in [Beatport API notes](docs/BEATPORT_API_NOTES.md).

## Examples

```powershell
# Spotify playlist -> new private Beatport playlist
.\bpbridge.exe spotify "https://open.spotify.com/playlist/PLAYLIST_ID" --new "My Set"

# Spotify track -> existing Beatport playlist
.\bpbridge.exe spotify "spotify:track:TRACK_ID" --playlist 123456

# Text -> new public playlist
.\bpbridge.exe text .\tracks.txt --new "Imports" --public

# Exact dry-run against an existing playlist
.\bpbridge.exe text .\tracks.txt --playlist 123456 --dry-run --verbose

# Redirected stdin (real non-interactive mutations require --yes)
Get-Content .\tracks.txt | .\bpbridge.exe text - --playlist 123456 --yes

# Direct arguments
.\bpbridge.exe songs "Artist - Track" "Another Artist | Another Track" --new "Quick List"

# Guided multiline paste
.\bpbridge.exe interactive

# List targets and produce reports
.\bpbridge.exe playlists
.\bpbridge.exe text .\tracks.txt --playlist "My Existing List" --report .\report.json
.\bpbridge.exe songs "Artist - Song" --new "Preview" --dry-run --json
```

For automation, use `--non-interactive --yes`. Non-interactive matching accepts
only HIGH-confidence candidates; ambiguous results remain unresolved unless
you deliberately add `--auto-ambiguous`. `--json` also disables interactive
match/target prompts so stdout remains valid JSON.

## Text formats

Input is UTF-8 (a UTF-8 BOM is accepted), one track per line. Blank lines are
ignored and text files may use `#` comments. Supported rows include:

```text
Artist - Track Name
Artist – Track Name
Artist — Track Name
Artist | Track Name
Title Without Artist
```

ASCII hyphens split only when surrounded by whitespace, so `Jay-Z` and
`Hard-Knock Life` remain intact. Duplicate input rows stay visible in the
report as `INPUT_DUPLICATE`.

## Matching and duplicate safety

BPBridge generates a bounded set of artist/title queries and scores candidates
using normalized full/base title, artist sets, version/remixer metadata, ISRC,
duration, release evidence, and guarded penalties. The default HIGH threshold
is 85 and AMBIGUOUS threshold is 70. Matching is deterministic and every
choice has a human-readable reason.

Extended Mix is a bounded preference. It helps an otherwise strong match and
can beat an Original Mix or Radio Edit counterpart, but never compensates for
the wrong artist/title. An explicitly named remix remains preferred over an
unrelated Extended Mix. See [matching details](docs/MATCHING.md).

Before changing an existing playlist, BPBridge fetches every page of its
tracks and builds a Beatport track-ID set. It also de-duplicates converging
matches in source order. Writes are sequential. Non-idempotent POSTs are not
blindly retried; after an uncertain add response, membership is fetched again
before the result is classified. `--dry-run` follows all read/match/duplicate
steps but never creates a playlist or posts a track.

## Reports and cache

Use `--report result.json` or `--report result.csv`. Reports include every
source row, chosen candidate, score, status, reason, and error, but never
credentials. CSV text is neutralized against spreadsheet-formula injection.

Beatport searches are cached with a versioned 24-hour default TTL under
`%LOCALAPPDATA%\BPBridge\cache`. Clear them with `bpbridge cache clear`, or
bypass them for one diagnostic import with `--no-cache`.

## Configuration

The generated TOML is non-secret. Environment variables override it. Supported
environment names include:

- `SPOTIFY_CLIENT_ID`, `SPOTIFY_CLIENT_SECRET`
- `BEATPORT_CLIENT_ID`, `BPBRIDGE_MARKET`
- `BPBRIDGE_MATCH_THRESHOLD`, `BPBRIDGE_AMBIGUOUS_THRESHOLD`
- `BPBRIDGE_PREFER_EXTENDED_MIX`, `BPBRIDGE_BEATPORT_SEARCH_RESULT_COUNT`
- `BPBRIDGE_REQUEST_TIMEOUT`, `BPBRIDGE_MAX_RETRIES`, `BPBRIDGE_CACHE_TTL`
- `BPBRIDGE_DEFAULT_PLAYLIST_VISIBILITY`, `BPBRIDGE_LOG_LEVEL`

Use `--config <path>` for a different non-secret TOML file, `--market IT` for a
one-run Spotify market, and `--match-threshold 90` for a one-run threshold.
`.env.example` contains names only; BPBridge does not automatically load `.env`.

## Build from source

Install Go 1.27 or newer, then run:

```powershell
.\scripts\build.ps1 -Version "1.0.0"
.\scripts\build.ps1 -Version "1.0.0" -Arm64
```

The script runs tests and vet, injects version/commit/time metadata, writes
`dist\bpbridge.exe`, and creates Windows ZIP packages containing
`bpbridge.exe`, this README, the license, and `.env.example`.

## Troubleshooting

- **Spotify playlist 403 or missing items:** authorize with
  `auth spotify --user`, verify the user is allowlisted and owns/collaborates
  on the playlist, or use TXT/pasted input. A public link alone is insufficient.
- **Spotify authorization fails:** register exactly
  `http://127.0.0.1:8000/callback`; do not use `localhost`. If port 8000 is
  occupied, register another fixed port and supply it with `--redirect-uri`.
  If a refresh token is rejected, log out and authorize again.
- **Beatport login fails/MFA is required:** use `auth beatport --token`. The
  browser flow and undocumented write API can change without notice.
- **Expired Beatport refresh token:** authenticate again or securely import a
  fresh token. Invalid credentials are never retried indefinitely.
- **429:** ordinary rate limits honor `Retry-After` for safe reads. Spotify
  `QUOTA_EXCEEDED` and uncertain write responses are not tight-looped.
- **Track not found:** retry once with `--dry-run --debug --no-cache`. Debug
  output shows parsed metadata, every staged query, returned track IDs,
  candidate provenance, score components, and the final decision without
  exposing credentials. Interactive mode can search again with a custom query.
  Title-only input naturally has lower confidence.
- **Ambiguous match:** select a numbered candidate, search again, or leave it
  unresolved in `--non-interactive` mode.
- **Duplicate:** `ALREADY_EXISTS` means the Beatport ID was already in the
  target; `INPUT_DUPLICATE` also covers multiple rows converging on one ID.
- **Corrupt cache:** run `bpbridge cache clear`; imports otherwise continue
  without caching and print a warning.

## External API status

Spotify calls documented official endpoints. Beatport owned-playlist write
routes are undocumented and were statically verified in the current production
frontend release identified in the API notes. Mocked HTTP tests cover their
request/response contracts. No private credentials are included, so an actual
account's legacy-token write scope and end-to-end remote mutation cannot be
validated during a normal build. BPBridge surfaces such failures rather than
falling back to guessed endpoints.

Licensed under MIT. BeatportDL was inspected only as a GPL-3.0 protocol
reference; BPBridge is an independent implementation and contains no download
code.
