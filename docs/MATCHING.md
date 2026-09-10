# Matching

Matching is deterministic and explainable. No model, embedding service, or
opaque recommendation endpoint is used.

## Normalization

Comparison values are Unicode-normalized, lowercased, stripped of combining
marks and irrelevant punctuation, and whitespace-collapsed. `&` and `and`, plus
`feat.`, `ft.`, and `featuring`, are normalized consistently. Original strings
are retained for display and reports.

Titles are split into a base title and version metadata. Recognized metadata
includes Extended Mix, Original Mix, Radio Edit, Club Mix, Dub, VIP, named
remixes, edits, reworks, instrumental, mixed, and remastered variants. This
information is separated, not simply discarded.

## Queries

The matcher searches in bounded, most-specific-first passes:

1. all source artists plus the full and base titles;
2. each source artist separately plus the full and base titles;
3. full and base titles without artists;
4. a conservatively sanitized base title (for example, without apostrophes).

After every pass, all results found so far are de-duplicated by Beatport track
ID and scored together. A HIGH, unambiguous match stops the search; empty,
irrelevant, LOW, or AMBIGUOUS results allow the next broader pass. This avoids
both needless requests and the old failure mode where one over-specific query
or one page of unrelated results prevented a valid title fallback.

Plain-text collaborator separators such as spaced `&`, `+`, `x`, `vs`, commas,
and `feat.` are parsed into individual artists. Unspaced names such as `A&B`
remain intact. Spotify's structured artist array bypasses this text heuristic.

Results are cached by normalized query and a cache schema/version. Candidate
IDs are de-duplicated before scoring so repeated queries do not bias ranking.
Use `--no-cache` to diagnose live retrieval without reading or writing cached
search results.

## Score

The final 0–100 score combines full-title similarity, base-title similarity,
primary/additional artist set overlap, named version/remixer agreement, ISRC,
duration, and limited release evidence. Debug output includes the component
breakdown and reasons.

Artist-set comparison uses a deterministic one-to-one assignment so one
candidate artist cannot satisfy multiple source artists. It tolerates artist
ordering and minor spelling differences, while exact primary-artist evidence
still carries the most weight. Two or more exact collaborator matches may
outweigh a missing primary credit, which accommodates catalog-credit omissions
without weakening single-artist identity checks. There are no track-specific
aliases or ID special cases.

Version recognition includes named remixes, `Edit by ...`, remix/rework-by
forms, and acapella spelling variants. A longer named mix can agree with a
contained three-or-more-word version (for example an artist possessive prefix
present in only one catalog); generic words such as `mix` cannot trigger that
rule.

An exact ISRC is a very strong positive signal. A different ISRC is not an
automatic rejection because radio and Extended mixes may legitimately be
different recordings. In that case strong title, artist, and version evidence
is required.

The default confidence bands used by the CLI are configurable:

- HIGH: safe to auto-select (default 85).
- AMBIGUOUS: plausible but requires a user choice (default 70–84.99).
- LOW: not added automatically (below 70).

Guardrails also consider the margin between the top candidates and reject a
same-title result by the wrong artist even if some title tokens happen to match.

## Extended Mix policy

Extended Mix is a bounded preference, never a substitute for identity:

- an explicitly requested Extended Mix receives the strongest version bonus;
- a specifically named remix must match that remix and is not replaced by an
  unrelated Extended Mix;
- a default/radio source can prefer a strongly matching Extended counterpart;
- when Original and Extended candidates are otherwise near-equal, Extended wins;
- artist and base-title correctness dominate every Extended bonus.

This makes a wrong track containing “Extended Mix” unable to outrank a clearly
correct track.
