# Architecture

BPBridge is a metadata-only pipeline. It never requests an audio download or
stream endpoint.

```text
Spotify URL / TXT / stdin / argv / interactive input
                         |
                         v
                    SourceTrack
                         |
       staged query generation + versioned cache
                         |
                         v
              Beatport catalog candidates
                         |
          deterministic scoring / manual choice
                         |
                         v
      full target pagination -> track-ID duplicate set
                         |
                         v
        dry-run report OR ordered playlist mutations
```

## Packages

- `cmd/bpbridge`: executable entry point and version metadata.
- `internal/cli`: Cobra commands, interactive prompts, output, and dependency
  assembly. Command handlers contain no endpoint constants.
- `internal/model`: source, candidate, playlist, status, and report-safe domain
  types.
- `internal/input`: Spotify reference and Unicode text parsers.
- `internal/spotify`: official Web API, Client Credentials, optional PKCE, current
  `/items` pagination, and capability errors.
- `internal/beatport`: bearer authentication, legacy login/token import, current
  search and owned-playlist endpoints, pagination, and typed API errors.
- `internal/matcher`: normalization, query generation, transparent scoring,
  confidence classification, and Extended Mix policy.
- `internal/playlist`: order-preserving input and target duplicate detection.
- `internal/syncer`: per-track import orchestration and failure isolation.
- `internal/credentials`: Windows Credential Manager abstraction.
- `internal/config`: non-secret TOML settings and environment overrides.
- `internal/cache`: versioned JSON search cache with TTL and atomic replacement.
- `internal/report`: JSON/CSV operation reports with no credentials.
- `internal/ui`: terminal prompts, ambiguous candidate selection, and summaries.

## Security boundaries

`config.toml` contains settings only. Spotify client secrets and both providers'
token JSON live in Windows Credential Manager. Passwords exist only for the
duration of a hidden terminal prompt. HTTP clients build the Authorization
header internally and errors never include it. Debug output describes routes,
statuses, matching features, and redacted credential state.

## Failure and ordering model

Source loading fails the command because there is nothing safe to process.
After loading, each source track is independent: search, ambiguity, and add
failures are recorded and later tracks continue. Additions are sequential.
Non-idempotent POSTs are not transparently retried, so a lost response cannot
blindly append twice. The target-ID set is updated only after a confirmed
success; an uncertain add result is checked against fresh full playlist
membership and is never reposted by the orchestrator.

`--dry-run` follows the same read and match path, including target pagination,
but the create and add methods are never invoked.

With `--debug`, the sync layer reports parsed source metadata, each staged
query, cache use, returned Beatport track IDs, candidate provenance, scoring
components, and the final confidence decision. These diagnostics contain no
authorization headers or token material. `--no-cache` bypasses the search cache
for a single import when validating current Beatport behavior.
