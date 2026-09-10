// Package playlist contains local, side-effect-free playlist planning helpers.
package playlist

import (
	"fmt"

	"github.com/bpbridge/bpbridge/internal/matcher"
	"github.com/bpbridge/bpbridge/internal/model"
)

// PlanResult is an ordered, duplicate-safe add plan. Tracks contains only
// unique candidates which are not in the target playlist.
type PlanResult struct {
	Outcomes []model.TrackOutcome
	Tracks   []model.BeatportTrack
}

// MarkSourceDuplicates returns a copy with every occurrence after the first
// marked InputDuplicate. It never removes or reorders a source track.
func MarkSourceDuplicates(tracks []model.SourceTrack) []model.SourceTrack {
	result := append([]model.SourceTrack(nil), tracks...)
	seen := make(map[string]struct{}, len(result))
	for index := range result {
		key := matcher.SourceKey(result[index])
		if _, exists := seen[key]; exists {
			result[index].InputDuplicate = true
			continue
		}
		seen[key] = struct{}{}
	}
	return result
}

// ExistingTrackIDs builds a membership set from every fetched playlist item.
// Callers are responsible for fetching all API pages before invoking it.
func ExistingTrackIDs(items []model.BeatportPlaylistItem) map[int64]struct{} {
	ids := make(map[int64]struct{}, len(items))
	for _, item := range items {
		if item.Track.ID > 0 {
			ids[item.Track.ID] = struct{}{}
		}
	}
	return ids
}

// TrackIDs builds an ID membership set from catalog tracks.
func TrackIDs(tracks []model.BeatportTrack) map[int64]struct{} {
	ids := make(map[int64]struct{}, len(tracks))
	for _, track := range tracks {
		if track.ID > 0 {
			ids[track.ID] = struct{}{}
		}
	}
	return ids
}

// BuildPlan resolves all three duplicate classes in source order: repeated
// source rows, separate matches converging on one Beatport ID, and IDs already
// present in an existing playlist. The supplied membership map is not mutated.
func BuildPlan(outcomes []model.TrackOutcome, existingIDs map[int64]struct{}) PlanResult {
	result := PlanResult{Outcomes: append([]model.TrackOutcome(nil), outcomes...)}
	result.Tracks = make([]model.BeatportTrack, 0, len(outcomes))
	seenSource := make(map[string]struct{}, len(outcomes))
	seenMatched := make(map[int64]struct{}, len(outcomes))

	for index := range result.Outcomes {
		outcome := &result.Outcomes[index]
		sourceKey := matcher.SourceKey(outcome.Input)
		_, repeatedSource := seenSource[sourceKey]
		if !repeatedSource {
			seenSource[sourceKey] = struct{}{}
		}
		if outcome.Input.InputDuplicate || repeatedSource {
			outcome.Input.InputDuplicate = true
			outcome.Status = model.StatusInputDuplicate
			outcome.Reason = "duplicate source track; first occurrence retained"
			continue
		}

		// Do not turn unresolved/manual outcomes into additions just because a
		// caller retained a candidate for display.
		if outcome.Status == model.StatusAmbiguous || outcome.Status == model.StatusNotFound ||
			outcome.Status == model.StatusSkipped || outcome.Status == model.StatusFailed {
			continue
		}
		if outcome.Chosen == nil {
			continue
		}
		trackID := outcome.Chosen.ID
		if trackID <= 0 {
			outcome.Status = model.StatusFailed
			outcome.Reason = "matched Beatport track has no valid ID"
			continue
		}

		if _, exists := existingIDs[trackID]; exists {
			outcome.Status = model.StatusAlreadyExists
			outcome.Reason = fmt.Sprintf("Beatport track %d already exists in target playlist", trackID)
			continue
		}
		if _, exists := seenMatched[trackID]; exists {
			outcome.Status = model.StatusInputDuplicate
			outcome.Reason = fmt.Sprintf("another source track already matched Beatport track %d", trackID)
			continue
		}

		seenMatched[trackID] = struct{}{}
		if outcome.Status == "" {
			outcome.Status = model.StatusMatched
		}
		result.Tracks = append(result.Tracks, *outcome.Chosen)
	}
	return result
}

// ResolveDuplicates is an alias which emphasizes BuildPlan's status-updating
// role at call sites.
func ResolveDuplicates(outcomes []model.TrackOutcome, existingIDs map[int64]struct{}) PlanResult {
	return BuildPlan(outcomes, existingIDs)
}

// PlanMatches converts matcher results into status-bearing outcomes and then
// applies duplicate protection. Ambiguous and low-confidence results are never
// included in Tracks.
func PlanMatches(matches []model.MatchResult, existingIDs map[int64]struct{}) PlanResult {
	outcomes := make([]model.TrackOutcome, 0, len(matches))
	for _, match := range matches {
		outcome := model.TrackOutcome{
			Input:      match.Source,
			Confidence: match.Confidence,
			Reason:     match.Reason,
		}
		switch match.Confidence {
		case model.ConfidenceHigh:
			if match.Chosen == nil {
				outcome.Status = model.StatusNotFound
				outcome.Reason = "high-confidence match has no chosen candidate"
				break
			}
			chosen := match.Chosen.Track
			breakdown := match.Chosen.Breakdown
			outcome.Chosen = &chosen
			outcome.Score = match.Chosen.Score
			outcome.Breakdown = &breakdown
			outcome.Status = model.StatusMatched
		case model.ConfidenceAmbiguous:
			outcome.Status = model.StatusAmbiguous
		default:
			outcome.Status = model.StatusNotFound
		}
		outcomes = append(outcomes, outcome)
	}
	return BuildPlan(outcomes, existingIDs)
}

// DeduplicateTrackIDs is a compact helper for clients that already have IDs.
// It returns a status for every input position and a unique ordered add list.
func DeduplicateTrackIDs(ids []int64, existingIDs map[int64]struct{}) ([]int64, []model.TrackStatus) {
	toAdd := make([]int64, 0, len(ids))
	statuses := make([]model.TrackStatus, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for index, id := range ids {
		if _, exists := existingIDs[id]; exists {
			statuses[index] = model.StatusAlreadyExists
			continue
		}
		if id <= 0 {
			statuses[index] = model.StatusFailed
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			statuses[index] = model.StatusInputDuplicate
			continue
		}
		seen[id] = struct{}{}
		statuses[index] = model.StatusMatched
		toAdd = append(toAdd, id)
	}
	return toAdd, statuses
}
