# Beatport API notes

Verified on 2026-09-02. Beatport does not publish a supported, stable API contract
for every playlist-write operation. All undocumented routes are confined to
`internal/beatport` and covered by mock-server tests so they can be changed
without touching matching or CLI code.

## Sources and licensing

- BeatportDL repository: <https://github.com/unspok3n/beatportdl>, inspected at
  commit `878d3f61c5a98cffd43b9e0306b8cbc75488999e` dated 2026-06-30.
- Current Beatport web application: `https://www.beatport.com/`, release marker
  `BP-v1.174.1`; its production runtime configuration and public JavaScript
  bundles were inspected on 2026-09-01.
- Beatport API documentation shell: <https://api.beatport.com/v4/docs/>.

BeatportDL is GPL-3.0. BPBridge is not a fork and contains no download or stream
logic. The implementation here was written independently from the observed HTTP
contract. BeatportDL was used only to validate protocol behavior and response
fields.

## Bases and published frontend identifiers

| Purpose | Current value |
|---|---|
| API v4 | `https://api.beatport.com/v4` |
| Search API | `https://api.beatport.com/search/v1` |
| Identity service | `https://account.beatport.com` |
| Frontend API4 client ID | `1xmvMPWqWYowVmAW9ezqB4Xwvcd7zHYVIG8Celtz` |
| Frontend identity client ID | `eHToND3lsv1Xdpa645DdF4wwBUceBniuKPT2dUB1` |
| BeatportDL legacy OAuth client ID | `ryZ8LuyQVPqbK2mBX2Hwt4qSMtnWuTYSqBPO92yQ` |

These identifiers are public application identifiers, not user secrets. They are
not guaranteed stable. `BEATPORT_CLIENT_ID` overrides BPBridge's legacy login
client ID. The current website obtains its values from environment-specific
runtime configuration embedded in its frontend bundle; BPBridge does not fetch
and execute remote JavaScript at runtime.

## Authentication

The BeatportDL flow observed in June 2026 is:

1. `POST /auth/login/` with JSON `{ "username": "...", "password": "..." }`.
   A successful response sets a `sessionid` cookie.
2. `GET /auth/o/authorize/?client_id=<id>&response_type=code` with that session
   cookie. Redirects are intentionally not followed; the authorization code is
   read from the `Location` query.
3. `POST /auth/o/token/` as `application/x-www-form-urlencoded` with
   `client_id`, `grant_type=authorization_code`, and `code`.
4. Authenticated API requests use `Authorization: Bearer <access_token>`.
5. Refresh uses `POST /auth/o/token/` with `client_id`,
   `grant_type=refresh_token`, and `refresh_token`.

Tokens contain `access_token`, `refresh_token`, `expires_in`, `token_type`, and
`scope`. BPBridge records an issuance time, refreshes five minutes early, retains
an old refresh token if the response omits a replacement, and makes at most one
401-driven refresh attempt for a request.

The current browser application uses Beatport's identity service through a
server-managed session. That browser flow is not suitable for copying a user
password into a CLI OAuth client and may include MFA. Therefore BPBridge keeps
the verified legacy username/password flow isolated and also supports secure
token JSON import with `bpbridge auth beatport --token`. Passwords are never
stored. Imported and refreshed tokens are stored in Windows Credential Manager.

## Catalog and search

Operational search used by BPBridge:

```text
GET https://api.beatport.com/search/v1/tracks/?q=<query>&count=25
```

The September 2026 storefront frontend was observed adding `preorder=true`, but
a real token issued with `AppScopes.APP_LOCKER` rejects that optional filter with
HTTP 400 (`preorder=True not permitted`). BPBridge therefore omits it. This
keeps ordinary catalog search compatible with the token used for owned-playlist
operations and does not affect the matcher.

The June 2026 v4 route, retained in these notes as a compatibility reference,
is:

```text
GET /catalog/search/?q=<query>&order_by=-publish_date&is_available_for_streaming=true
```

The current search response is `{ "data": [...] }` and uses compact fields such
as `track_id`, `track_name`, `mix_name`, `artists[].artist_id`/
`artist_name`, `release`, `label`, and numeric `length`. No search offset/page
parameter was found in the current frontend, so BPBridge does not invent one;
it performs one bounded request per generated query. The older v4 response has
top-level `tracks`, `releases`, and `labels`. Individual track
metadata is available at `GET /catalog/tracks/{id}/`. Useful matching fields
include `id`, `name`, `mix_name`, `artists`, `remixers`, `isrc`, `length_ms`,
`bpm`, and `release`.

## Playlist reads and writes

The following routes were recovered from the current Beatport frontend bundle,
not invented from REST naming conventions:

| Operation | Request |
|---|---|
| List the authenticated user's playlists | `GET /my/playlists/?page=N&per_page=100` |
| Create playlist | `POST /my/playlists/` |
| Read/update/delete one owned playlist | `GET/PATCH/DELETE /my/playlists/{id}/` |
| Read owned-playlist tracks | `GET /my/playlists/{id}/tracks/?page=N&per_page=100` |
| Read track IDs optimization | `GET /my/playlists/{id}/tracks/ids/` |
| Add tracks | `POST /my/playlists/{id}/tracks/bulk/` |
| Reorder tracks | `PATCH /my/playlists/{id}/tracks/bulk/` |
| Remove tracks | `DELETE /my/playlists/{id}/tracks/bulk/` |

The current create form sends a trimmed `name`, emits the JSON string `"true"`
when its public toggle is checked, and omits `is_public` for the conservative
private default. BPBridge mirrors that observed payload exactly.

The current add payload is:

```json
{ "track_ids": [123, 456, 789] }
```

The IDs route currently returns a `tracks` array whose entries contain
`track_id`; BPBridge accepts that shape as well as older numeric-array shapes.
BPBridge submits sequential batches, preserving the source order. Before any
write it paginates all target items and creates a track-ID set. The IDs-only
route is used only as an optimization; full paginated reads remain the fallback
and source of truth.

Public catalog playlists use `GET /catalog/playlists/{id}/` and
`GET /catalog/playlists/{id}/tracks/?page=N`, but those routes do not prove
ownership and are not used for writes.

## Errors, throttling, and compatibility

Observed v4 errors commonly use `{ "detail": ... }` or `{ "error": ... }`.
BPBridge also accepts a `message` field and returns a typed error containing
HTTP status and route. Safe idempotent reads honor `Retry-After` and retry
bounded 429, transport, and 5xx failures. Playlist-creation/addition POSTs are
not blindly retried because a response may be lost after the server commits a
write; failed adds are reconciled against fresh playlist membership. A 401 gets
at most one authentication refresh. Permanent 4xx errors are not retried.

The routes and payloads above were verified statically in the current frontend,
but no private Beatport account was available during the build to prove that a
legacy-client token retains playlist-write scope end to end. Because the write
API is undocumented, a future Beatport deployment can change
it without notice. If a previously working write fails, run with `--debug`,
redact any user-specific content before sharing logs, and compare this document
with the current frontend network behavior. Never paste an access or refresh
token into an issue or log.
