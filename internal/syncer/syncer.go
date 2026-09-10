// Package syncer orchestrates source-to-Beatport playlist operations.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bpbridge/bpbridge/internal/matcher"
	"github.com/bpbridge/bpbridge/internal/model"
	"github.com/bpbridge/bpbridge/internal/playlist"
)

type BeatportClient interface {
	Search(context.Context, string, int, int) ([]model.BeatportTrack, error)
	GetPlaylist(context.Context, int64) (model.BeatportPlaylist, error)
	GetPlaylistTracks(context.Context, int64) ([]model.BeatportPlaylistItem, error)
	CreatePlaylist(context.Context, string, bool) (model.BeatportPlaylist, error)
	AddTracks(context.Context, int64, []int64) error
}

type SearchCache interface {
	Get(string) ([]model.BeatportTrack, bool)
	Put(string, []model.BeatportTrack) error
}

type SearchFunc func(context.Context, string) ([]model.ScoredCandidate, error)

// AmbiguousResolver returns nil to skip. It may call search to offer a custom
// query or more results without coupling the engine to terminal I/O.
type AmbiguousResolver interface {
	Resolve(context.Context, model.SourceTrack, []model.ScoredCandidate, SearchFunc) (*model.ScoredCandidate, error)
}

type Target struct {
	ExistingID int64  `json:"existing_id,omitempty"`
	Name       string `json:"name"`
	Public     bool   `json:"public"`
}

type Options struct {
	Matcher        matcher.Config
	SearchPerPage  int
	SearchPages    int
	DryRun         bool
	NonInteractive bool
	AutoAmbiguous  bool
	Resolver       AmbiguousResolver
	Cache          SearchCache
	Progress       func(current, total int, outcome model.TrackOutcome)
	Debugf         func(format string, arguments ...any)
}

type Request struct {
	SourceType       string
	SourceIdentifier string
	Tracks           []model.SourceTrack
	Target           Target
}

type Result struct {
	Timestamp        time.Time              `json:"timestamp"`
	CompletedAt      time.Time              `json:"completed_at"`
	SourceType       string                 `json:"source_type"`
	SourceIdentifier string                 `json:"source_identifier,omitempty"`
	Target           model.BeatportPlaylist `json:"target_beatport_playlist"`
	DryRun           bool                   `json:"dry_run"`
	Outcomes         []model.TrackOutcome   `json:"tracks"`
}

type Summary struct {
	SourceTracks   int `json:"source_tracks"`
	Matched        int `json:"matched"`
	Added          int `json:"added"`
	AlreadyExists  int `json:"already_exists"`
	InputDuplicate int `json:"input_duplicate"`
	Ambiguous      int `json:"ambiguous"`
	NotFound       int `json:"not_found"`
	Skipped        int `json:"skipped"`
	Failed         int `json:"failed"`
}

func (r Result) Summary() Summary {
	summary := Summary{SourceTracks: len(r.Outcomes)}
	for _, outcome := range r.Outcomes {
		switch outcome.Status {
		case model.StatusMatched:
			summary.Matched++
		case model.StatusAdded:
			summary.Matched++
			summary.Added++
		case model.StatusAlreadyExists:
			summary.Matched++
			summary.AlreadyExists++
		case model.StatusInputDuplicate:
			summary.InputDuplicate++
		case model.StatusAmbiguous:
			summary.Ambiguous++
		case model.StatusNotFound:
			summary.NotFound++
		case model.StatusSkipped:
			summary.Skipped++
		case model.StatusFailed:
			summary.Failed++
		}
	}
	return summary
}

type Engine struct {
	beatport BeatportClient
	now      func() time.Time
}

func New(beatport BeatportClient) *Engine {
	return &Engine{beatport: beatport, now: time.Now}
}

func (e *Engine) Run(ctx context.Context, request Request, options Options) (Result, error) {
	result := Result{
		Timestamp:        e.now().UTC(),
		SourceType:       request.SourceType,
		SourceIdentifier: request.SourceIdentifier,
		DryRun:           options.DryRun,
	}
	if e.beatport == nil {
		return result, errors.New("Beatport client is not configured")
	}
	if len(request.Tracks) == 0 {
		return result, errors.New("source contains no tracks")
	}
	if request.Target.ExistingID <= 0 && strings.TrimSpace(request.Target.Name) == "" {
		return result, errors.New("target requires an existing playlist ID or a new playlist name")
	}
	options = normalizeOptions(options)
	tracks := playlist.MarkSourceDuplicates(request.Tracks)

	existingIDs := map[int64]struct{}{}
	if request.Target.ExistingID > 0 {
		target, err := e.beatport.GetPlaylist(ctx, request.Target.ExistingID)
		if err != nil {
			return result, fmt.Errorf("load target Beatport playlist: %w", err)
		}
		items, err := e.beatport.GetPlaylistTracks(ctx, request.Target.ExistingID)
		if err != nil {
			return result, fmt.Errorf("load all target Beatport playlist tracks: %w", err)
		}
		result.Target = target
		existingIDs = playlist.ExistingTrackIDs(items)
	} else {
		result.Target = model.BeatportPlaylist{Name: strings.TrimSpace(request.Target.Name), IsPublic: request.Target.Public}
	}

	outcomes := make([]model.TrackOutcome, 0, len(tracks))
	for index, source := range tracks {
		outcome := model.TrackOutcome{Input: source}
		if source.InputDuplicate {
			outcome.Status = model.StatusInputDuplicate
			outcome.Reason = "duplicate source track; first occurrence retained"
			outcomes = append(outcomes, outcome)
			emitProgress(options, index, len(tracks), outcome)
			continue
		}

		candidates, searchErr := e.searchSource(ctx, source, options)
		if searchErr != nil {
			outcome.Status = model.StatusFailed
			outcome.Error = searchErr.Error()
			outcome.Reason = "Beatport search failed"
			outcomes = append(outcomes, outcome)
			emitProgress(options, index, len(tracks), outcome)
			continue
		}
		match := matcher.Match(source, candidates, options.Matcher)
		debugMatch(options, match)
		outcome.Confidence = match.Confidence
		outcome.Reason = match.Reason
		switch match.Confidence {
		case model.ConfidenceHigh:
			if match.Chosen != nil {
				choose(&outcome, *match.Chosen)
			} else {
				outcome.Status = model.StatusNotFound
			}
		case model.ConfidenceAmbiguous:
			if options.AutoAmbiguous && len(match.Candidates) > 0 {
				choose(&outcome, match.Candidates[0])
				outcome.Reason += "; selected by --auto-ambiguous"
			} else if !options.NonInteractive && options.Resolver != nil {
				search := func(searchCtx context.Context, query string) ([]model.ScoredCandidate, error) {
					custom, _, err := e.searchQuery(searchCtx, strings.TrimSpace(query), options)
					if err != nil {
						return nil, err
					}
					return matcher.RankCandidates(source, custom, options.Matcher), nil
				}
				selected, err := options.Resolver.Resolve(ctx, source, match.Candidates, search)
				if err != nil {
					outcome.Status = model.StatusFailed
					outcome.Error = err.Error()
				} else if selected == nil {
					outcome.Status = model.StatusSkipped
					outcome.Reason = "ambiguous match skipped by user"
				} else {
					choose(&outcome, *selected)
					outcome.Reason = "manually selected: " + matcher.Explain(*selected)
				}
			} else {
				outcome.Status = model.StatusAmbiguous
				outcome.Reason = "ambiguous match not selected in non-interactive mode"
			}
		default:
			outcome.Status = model.StatusNotFound
			if len(match.Candidates) == 0 {
				outcome.Reason = "no Beatport candidates found"
			}
		}
		outcomes = append(outcomes, outcome)
		emitProgress(options, index, len(tracks), outcome)
	}

	plan := playlist.BuildPlan(outcomes, existingIDs)
	result.Outcomes = plan.Outcomes
	if options.DryRun {
		result.CompletedAt = e.now().UTC()
		return result, nil
	}
	if len(plan.Tracks) == 0 {
		result.CompletedAt = e.now().UTC()
		return result, nil
	}

	if request.Target.ExistingID <= 0 {
		created, err := e.beatport.CreatePlaylist(ctx, request.Target.Name, request.Target.Public)
		if err != nil {
			for index := range result.Outcomes {
				if result.Outcomes[index].Status == model.StatusMatched {
					result.Outcomes[index].Status = model.StatusFailed
					result.Outcomes[index].Error = err.Error()
					result.Outcomes[index].Reason = "target playlist creation failed"
				}
			}
			result.CompletedAt = e.now().UTC()
			return result, fmt.Errorf("create Beatport playlist: %w", err)
		}
		result.Target = created
	}

	for index := range result.Outcomes {
		outcome := &result.Outcomes[index]
		if outcome.Status != model.StatusMatched || outcome.Chosen == nil {
			continue
		}
		trackID := outcome.Chosen.ID
		if _, exists := existingIDs[trackID]; exists {
			outcome.Status = model.StatusAlreadyExists
			outcome.Reason = "track appeared in target before add"
			continue
		}
		if err := e.beatport.AddTracks(ctx, result.Target.ID, []int64{trackID}); err != nil {
			// A write response can be lost after Beatport committed the append.
			// Re-read authoritative membership before reporting failure; never
			// blindly POST the same track again.
			items, reconcileErr := e.beatport.GetPlaylistTracks(ctx, result.Target.ID)
			if reconcileErr == nil {
				refreshedIDs := playlist.ExistingTrackIDs(items)
				if _, present := refreshedIDs[trackID]; present {
					existingIDs[trackID] = struct{}{}
					outcome.Status = model.StatusAdded
					outcome.Reason = "track membership confirmed after an uncertain Beatport add response"
					continue
				}
			} else {
				err = errors.Join(err, fmt.Errorf("reconcile playlist after add failure: %w", reconcileErr))
			}
			outcome.Status = model.StatusFailed
			outcome.Error = err.Error()
			outcome.Reason = "Beatport add failed; remaining tracks continued"
			continue
		}
		existingIDs[trackID] = struct{}{}
		outcome.Status = model.StatusAdded
		outcome.Reason = "added to Beatport playlist"
	}
	result.CompletedAt = e.now().UTC()
	return result, nil
}

func (e *Engine) searchSource(ctx context.Context, source model.SourceTrack, options Options) ([]model.BeatportTrack, error) {
	byID := make(map[int64]model.BeatportTrack)
	provenance := make(map[int64][]string)
	var joined error
	debugf(options, "SOURCE title=%q base_title=%q normalized_title=%q artists=%q normalized_artists=%q version=%q",
		source.Title, source.BaseTitle, matcher.NormalizeTitle(source.BaseTitle), source.Artists,
		matcher.NormalizeArtistSet(source.Artists), source.Version)
	for _, pass := range matcher.GenerateQueryPasses(source) {
		debugf(options, "SEARCH_PASS name=%q queries=%q", pass.Name, pass.Queries)
		for _, query := range pass.Queries {
			tracks, cached, err := e.searchQuery(ctx, query, options)
			if err != nil {
				debugf(options, "SEARCH query=%q error=%v", query, err)
				joined = errors.Join(joined, fmt.Errorf("query %q: %w", query, err))
				continue
			}
			debugf(options, "SEARCH query=%q normalized=%q cached=%t results=%d ids=%v",
				query, matcher.Normalize(query), cached, len(tracks), trackIDs(tracks))
			for _, track := range tracks {
				if track.ID > 0 {
					byID[track.ID] = track
					provenance[track.ID] = appendUniqueString(provenance[track.ID], query)
				}
			}
		}
		preliminary := matcher.Match(source, candidatePool(byID), options.Matcher)
		if preliminary.Chosen != nil && preliminary.Confidence == model.ConfidenceHigh {
			debugf(options, "SEARCH_PASS name=%q stop=true top_id=%d score=%.1f reason=%q",
				pass.Name, preliminary.Chosen.Track.ID, preliminary.Chosen.Score, preliminary.Reason)
			break
		}
		debugf(options, "SEARCH_PASS name=%q stop=false accumulated=%d top_confidence=%s reason=%q",
			pass.Name, len(byID), preliminary.Confidence, preliminary.Reason)
	}
	if len(byID) == 0 && joined != nil {
		return nil, joined
	}
	result := candidatePool(byID)
	debugf(options, "CANDIDATE_POOL count=%d ids=%v", len(result), trackIDs(result))
	for _, track := range result {
		debugf(options, "CANDIDATE id=%d title=%q mix=%q artists=%q remixers=%q queries=%q",
			track.ID, track.Name, track.MixName, beatportArtistNames(track.Artists),
			beatportArtistNames(track.Remixers), provenance[track.ID])
	}
	return result, nil
}

func (e *Engine) searchQuery(ctx context.Context, query string, options Options) ([]model.BeatportTrack, bool, error) {
	key := searchCacheKey(query, options.SearchPerPage, options.SearchPages)
	if options.Cache != nil {
		if cached, ok := options.Cache.Get(key); ok {
			return cached, true, nil
		}
	}
	var all []model.BeatportTrack
	for pageNumber := 1; pageNumber <= options.SearchPages; pageNumber++ {
		current, err := e.beatport.Search(ctx, query, pageNumber, options.SearchPerPage)
		if err != nil {
			return nil, false, err
		}
		all = append(all, current...)
		if len(current) < options.SearchPerPage {
			break
		}
	}
	if options.Cache != nil {
		_ = options.Cache.Put(key, all)
	}
	return all, false, nil
}

func searchCacheKey(query string, perPage, pages int) string {
	// Preserve punctuation because Beatport search may interpret apostrophes,
	// plus signs, or other symbols differently. Only case and insignificant
	// whitespace are canonicalized for cache identity.
	canonicalQuery := strings.ToLower(strings.Join(strings.Fields(query), " "))
	return fmt.Sprintf("search-v3|count=%d|pages=%d|%s", perPage, pages, canonicalQuery)
}

func debugMatch(options Options, match model.MatchResult) {
	for _, candidate := range match.Candidates {
		breakdown := candidate.Breakdown
		candidateArtists := beatportArtistNames(candidate.Track.Artists)
		artistMatch := matcher.CompareArtists(match.Source.Artists, candidateArtists)
		debugf(options, "SCORE id=%d title=%q mix=%q artists=%q title=%.1f base=%.1f artist_similarity=%.3f exact_artists=%d primary_artist_exact=%t artists_score=%.1f version=%.1f isrc=%.1f duration=%.1f release=%.1f extended=%.1f penalty=%.1f final=%.1f confidence=%s reasons=%q",
			candidate.Track.ID, candidate.Track.Name, candidate.Track.MixName,
			candidateArtists, breakdown.Title, breakdown.BaseTitle, artistMatch.Similarity,
			artistMatch.ExactMatches, artistMatch.PrimaryExact,
			breakdown.Artists, breakdown.Version, breakdown.ISRC, breakdown.Duration,
			breakdown.Release, breakdown.Extended, breakdown.Penalty, candidate.Score,
			candidate.Confidence, breakdown.Explanation)
	}
	debugf(options, "DECISION confidence=%s chosen=%t reason=%q", match.Confidence, match.Chosen != nil, match.Reason)
}

func candidatePool(byID map[int64]model.BeatportTrack) []model.BeatportTrack {
	result := make([]model.BeatportTrack, 0, len(byID))
	for _, track := range byID {
		result = append(result, track)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func debugf(options Options, format string, arguments ...any) {
	if options.Debugf != nil {
		options.Debugf(format, arguments...)
	}
}

func trackIDs(tracks []model.BeatportTrack) []int64 {
	ids := make([]int64, 0, len(tracks))
	for _, track := range tracks {
		if track.ID > 0 {
			ids = append(ids, track.ID)
		}
	}
	return ids
}

func beatportArtistNames(artists []model.Artist) []string {
	names := make([]string, 0, len(artists))
	for _, artist := range artists {
		if strings.TrimSpace(artist.Name) != "" {
			names = append(names, artist.Name)
		}
	}
	return names
}

func appendUniqueString(existing []string, value string) []string {
	for _, current := range existing {
		if current == value {
			return existing
		}
	}
	return append(existing, value)
}

func normalizeOptions(options Options) Options {
	if options.Matcher == (matcher.Config{}) {
		options.Matcher = matcher.DefaultConfig()
	}
	if options.SearchPerPage <= 0 {
		options.SearchPerPage = 25
	}
	if options.SearchPages <= 0 {
		options.SearchPages = 1
	}
	if options.SearchPages > 10 {
		options.SearchPages = 10
	}
	return options
}

func choose(outcome *model.TrackOutcome, candidate model.ScoredCandidate) {
	track := candidate.Track
	breakdown := candidate.Breakdown
	outcome.Chosen = &track
	outcome.Score = candidate.Score
	outcome.Confidence = candidate.Confidence
	outcome.Breakdown = &breakdown
	outcome.Status = model.StatusMatched
	outcome.Reason = matcher.Explain(candidate)
}

func emitProgress(options Options, index, total int, outcome model.TrackOutcome) {
	if options.Progress != nil {
		options.Progress(index+1, total, outcome)
	}
}
