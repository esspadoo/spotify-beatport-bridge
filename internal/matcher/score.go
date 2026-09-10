package matcher

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/bpbridge/bpbridge/internal/model"
)

// Config controls auto-selection thresholds and the Extended Mix preference.
// Scores use a 0..100 scale.
type Config struct {
	HighThreshold      float64
	AmbiguousThreshold float64
	MinimumMargin      float64
	PreferExtended     bool
}

// DefaultConfig is conservative enough to reject title-only and wrong-artist
// matches while auto-selecting strong normalized artist/title matches.
func DefaultConfig() Config {
	return Config{
		HighThreshold:      85,
		AmbiguousThreshold: 70,
		MinimumMargin:      3,
		PreferExtended:     true,
	}
}

// ScoreCandidate evaluates one Beatport result and records every contribution.
func ScoreCandidate(source model.SourceTrack, candidate model.BeatportTrack, config Config) model.ScoredCandidate {
	config = normalizedConfig(config)
	breakdown := model.ScoreBreakdown{}
	explanation := make([]string, 0, 10)

	if isExactBeatportID(source, candidate) {
		breakdown.Explanation = []string{"exact Beatport track ID"}
		return model.ScoredCandidate{
			Track: candidate, Score: 100, Confidence: model.ConfidenceHigh, Breakdown: breakdown,
		}
	}

	sourceParts := sourceTitleParts(source)
	candidateParts := candidateTitleParts(candidate)
	// Beatport exposes the mix in a separate field while streaming catalogs
	// often append it to the title. Compare title text independently and let the
	// dedicated version signal account for that schema difference.
	baseSimilarity := Similarity(sourceParts.Base, candidateParts.Base)
	// Full titles are useful when catalogs retain the same decoration, but
	// Beatport normally stores mix names separately. Keep a conservative floor
	// from the base-title identity so that this schema difference cannot defeat
	// an otherwise correct Extended counterpart.
	titleSimilarity := math.Max(Similarity(source.Title, candidate.Name), 0.9*baseSimilarity)
	breakdown.Title = 20 * titleSimilarity
	breakdown.BaseTitle = 35 * baseSimilarity
	if baseSimilarity >= 0.92 {
		explanation = append(explanation, "base title strong match")
	}

	sourceArtists := sourceArtistNames(source, sourceParts)
	candidateArtists := candidateArtistNames(candidate)
	artistSimilarity := ArtistSimilarity(sourceArtists, candidateArtists)
	if len(sourceArtists) == 0 {
		// A title-only input may be presented for manual resolution, but cannot
		// become a high-confidence match on title alone.
		breakdown.Artists = 15
		explanation = append(explanation, "source artist missing")
	} else {
		breakdown.Artists = 30 * artistSimilarity
		if artistSimilarity >= 0.9 {
			explanation = append(explanation, "artist strong match")
		}
	}

	versionScore, extendedScore, versionPenalty, versionExplanation := assessVersion(sourceParts.Version, candidateParts.Version, candidate, config.PreferExtended)
	breakdown.Version = versionScore
	breakdown.Extended = extendedScore
	breakdown.Penalty += versionPenalty
	if versionExplanation != "" {
		explanation = append(explanation, versionExplanation)
	}

	if source.ISRC != "" && candidate.ISRC != "" {
		if normalizeISRC(source.ISRC) == normalizeISRC(candidate.ISRC) {
			breakdown.ISRC = 22
			explanation = append(explanation, "exact ISRC")
		} else if IsExtendedMix(candidateParts.Version) && !isNamedOrSpecificVersion(sourceParts.Version) {
			explanation = append(explanation, "different ISRC allowed for Extended Mix counterpart")
		} else {
			breakdown.ISRC = -3
			explanation = append(explanation, "different ISRC")
		}
	}

	breakdown.Duration = durationSignal(source.DurationMS, candidateDuration(candidate), sourceParts.Version, candidateParts.Version)
	if breakdown.Duration >= 3 {
		explanation = append(explanation, "duration agrees")
	} else if breakdown.Duration < 0 {
		explanation = append(explanation, "duration differs")
	}

	// Protect against same-name recordings by unrelated artists and against
	// fuzzy results that merely share a word. Exact ISRC may safely override.
	exactISRC := breakdown.ISRC == 22
	if !exactISRC && baseSimilarity < 0.48 {
		breakdown.Penalty += 25
		explanation = append(explanation, "weak base title")
	}
	if !exactISRC && len(sourceArtists) > 0 && artistSimilarity < 0.60 {
		breakdown.Penalty += 25
		explanation = append(explanation, "artist mismatch")
	}

	if source.Album != "" && candidate.Release.Name != "" {
		releaseSimilarity := Similarity(source.Album, candidate.Release.Name)
		if releaseSimilarity >= 0.55 {
			breakdown.Release = 2 * releaseSimilarity
			explanation = append(explanation, "release metadata agrees")
		}
	}

	score := breakdown.Title + breakdown.BaseTitle + breakdown.Artists + breakdown.Version +
		breakdown.ISRC + breakdown.Duration + breakdown.Extended + breakdown.Release - breakdown.Penalty
	score = math.Round(clamp(score, 0, 100)*10) / 10
	confidence := confidenceForScore(score, config)
	breakdown.Explanation = explanation
	return model.ScoredCandidate{Track: candidate, Score: score, Confidence: confidence, Breakdown: breakdown}
}

// RankCandidates scores and sorts candidates deterministically. A top result
// within MinimumMargin of the runner-up is ambiguous even if its absolute score
// clears the high threshold.
func RankCandidates(source model.SourceTrack, candidates []model.BeatportTrack, config Config) []model.ScoredCandidate {
	config = normalizedConfig(config)
	ranked := make([]model.ScoredCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ranked = append(ranked, ScoreCandidate(source, candidate, config))
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].Score != ranked[right].Score {
			return ranked[left].Score > ranked[right].Score
		}
		leftExtended := IsExtendedMix(candidateTitleParts(ranked[left].Track).Version)
		rightExtended := IsExtendedMix(candidateTitleParts(ranked[right].Track).Version)
		if leftExtended != rightExtended && config.PreferExtended {
			return leftExtended
		}
		if ranked[left].Track.ID != ranked[right].Track.ID {
			return ranked[left].Track.ID < ranked[right].Track.ID
		}
		return Normalize(ranked[left].Track.Name) < Normalize(ranked[right].Track.Name)
	})
	if len(ranked) > 1 && ranked[0].Confidence == model.ConfidenceHigh && ranked[0].Score-ranked[1].Score < config.MinimumMargin {
		ranked[0].Confidence = model.ConfidenceAmbiguous
		ranked[0].Breakdown.Explanation = append(ranked[0].Breakdown.Explanation, "runner-up score is too close")
	}
	return ranked
}

// Match selects only an unambiguous high-confidence result. Ambiguous and low
// candidates remain available to the interactive resolver.
func Match(source model.SourceTrack, candidates []model.BeatportTrack, config Config) model.MatchResult {
	ranked := RankCandidates(source, candidates, config)
	result := model.MatchResult{Source: source, Candidates: ranked, Confidence: model.ConfidenceLow}
	if len(ranked) == 0 {
		result.Reason = "no Beatport candidates"
		return result
	}
	result.Confidence = ranked[0].Confidence
	switch ranked[0].Confidence {
	case model.ConfidenceHigh:
		chosen := ranked[0]
		result.Chosen = &chosen
		result.Reason = Explain(chosen)
	case model.ConfidenceAmbiguous:
		result.Reason = "top candidate requires manual confirmation: " + Explain(ranked[0])
	default:
		result.Reason = "no candidate met the matching threshold"
	}
	return result
}

// Explain formats the score and its deterministic signal summary.
func Explain(candidate model.ScoredCandidate) string {
	if len(candidate.Breakdown.Explanation) == 0 {
		return fmt.Sprintf("%.1f%%", candidate.Score)
	}
	return fmt.Sprintf("%.1f%% - %s", candidate.Score, strings.Join(candidate.Breakdown.Explanation, "; "))
}

func normalizedConfig(config Config) Config {
	defaults := DefaultConfig()
	zeroConfig := config == (Config{})
	if config.HighThreshold <= 0 {
		config.HighThreshold = defaults.HighThreshold
	}
	if config.AmbiguousThreshold <= 0 {
		config.AmbiguousThreshold = defaults.AmbiguousThreshold
	}
	if config.MinimumMargin < 0 {
		config.MinimumMargin = 0
	} else if config.MinimumMargin == 0 {
		config.MinimumMargin = defaults.MinimumMargin
	}
	if config.AmbiguousThreshold > config.HighThreshold {
		config.AmbiguousThreshold = config.HighThreshold
	}
	// A completely zero-valued Config should be safe and useful.
	if zeroConfig {
		config.PreferExtended = true
	}
	return config
}

func confidenceForScore(score float64, config Config) model.Confidence {
	if score >= config.HighThreshold {
		return model.ConfidenceHigh
	}
	if score >= config.AmbiguousThreshold {
		return model.ConfidenceAmbiguous
	}
	return model.ConfidenceLow
}

func sourceTitleParts(source model.SourceTrack) TitleParts {
	parts := ExtractTitleParts(source.Title)
	if strings.TrimSpace(source.BaseTitle) != "" {
		parts.Base = strings.TrimSpace(source.BaseTitle)
	}
	if strings.TrimSpace(source.Version) != "" {
		parts.Version = strings.TrimSpace(source.Version)
	}
	return parts
}

func candidateTitleParts(candidate model.BeatportTrack) TitleParts {
	parts := ExtractTitleParts(candidate.Name)
	if strings.TrimSpace(candidate.MixName) != "" {
		parts.Version = strings.TrimSpace(candidate.MixName)
	}
	return parts
}

func sourceArtistNames(source model.SourceTrack, parts TitleParts) []string {
	artists := append([]string(nil), source.Artists...)
	if source.PrimaryArtist != "" {
		artists = appendUniqueNormalized(artists, source.PrimaryArtist)
	}
	artists = appendUniqueNormalized(artists, parts.FeaturedArtists...)
	return artists
}

func candidateArtistNames(candidate model.BeatportTrack) []string {
	artists := make([]string, 0, len(candidate.Artists)+len(candidate.Remixers))
	for _, artist := range candidate.Artists {
		artists = appendUniqueNormalized(artists, artist.Name)
	}
	return artists
}

func assessVersion(sourceVersion, candidateVersion string, candidate model.BeatportTrack, preferExtended bool) (score, extended, penalty float64, explanation string) {
	source := NormalizeVersion(sourceVersion)
	target := NormalizeVersion(candidateVersion)
	targetExtended := IsExtendedMix(target)
	sourceExtended := IsExtendedMix(source)
	sourceRemixer := RemixerName(source)
	targetRemixer := RemixerName(target)

	if sourceExtended {
		if targetExtended {
			return 10, 0, 0, "requested Extended Mix"
		}
		return -10, 0, 0, "candidate is not requested Extended Mix"
	}

	if sourceRemixer != "" {
		remixerSimilarity := Similarity(sourceRemixer, targetRemixer)
		for _, remixer := range candidate.Remixers {
			remixerSimilarity = math.Max(remixerSimilarity, Similarity(sourceRemixer, remixer.Name))
		}
		if remixerSimilarity >= 0.82 {
			return 12 * remixerSimilarity, 0, 0, "named remixer agrees"
		}
		return 0, 0, 24, "conflicting named remix"
	}

	if isNamedOrSpecificVersion(source) {
		if Similarity(source, target) >= 0.85 || meaningfulVersionContainment(source, target) {
			return 10, 0, 0, "requested version agrees"
		}
		return 0, 0, 18, "candidate conflicts with requested version"
	}

	if targetRemixer != "" || isNamedOrSpecificVersion(target) {
		return 0, 0, 20, "unrequested remix/version"
	}

	if targetExtended && preferExtended {
		bonus := 7.0
		if source == "radio edit" || source == "original mix" {
			bonus = 8
		}
		return 1, bonus, 0, "Extended Mix preference"
	}

	switch source {
	case "radio edit":
		if target == "radio edit" {
			return 6, 0, 0, "radio version agrees"
		}
	case "original mix":
		if target == "original mix" {
			return 5, 0, 0, "original version agrees"
		}
	case "":
		if target == "" {
			return 3, 0, 0, "default version"
		}
		if target == "original mix" {
			return 2, 0, 0, "Original Mix counterpart"
		}
	}
	return 0, 0, 0, ""
}

func meaningfulVersionContainment(left, right string) bool {
	left = NormalizeVersion(left)
	right = NormalizeVersion(right)
	if left == "" || right == "" {
		return false
	}
	shorter, longer := left, right
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	// Three meaningful words prevents generic labels such as "mix" or
	// "original mix" from agreeing merely because one string contains them.
	return len(strings.Fields(shorter)) >= 3 && strings.Contains(longer, shorter)
}

func isNamedOrSpecificVersion(version string) bool {
	version = NormalizeVersion(version)
	if version == "" || version == "extended mix" || version == "original mix" || version == "radio edit" {
		return false
	}
	return LooksLikeVersion(version)
}

func durationSignal(sourceMS, candidateMS int64, sourceVersion, candidateVersion string) float64 {
	if sourceMS <= 0 || candidateMS <= 0 {
		return 0
	}
	difference := math.Abs(float64(sourceMS - candidateMS))
	switch {
	case difference <= 3_000:
		return 4
	case difference <= 8_000:
		return 3
	case difference <= 15_000:
		return 1.5
	case IsExtendedMix(candidateVersion) && !IsExtendedMix(sourceVersion):
		return 0
	case difference > 60_000:
		return -4
	case difference > 30_000:
		return -2
	default:
		return 0
	}
}

func candidateDuration(candidate model.BeatportTrack) int64 {
	if candidate.LengthMS > 0 {
		return candidate.LengthMS
	}
	parts := strings.Split(candidate.Length, ":")
	if len(parts) != 2 {
		return 0
	}
	minutes, minuteError := strconv.ParseInt(parts[0], 10, 64)
	seconds, secondError := strconv.ParseInt(parts[1], 10, 64)
	if minuteError != nil || secondError != nil || minutes < 0 || seconds < 0 || seconds >= 60 {
		return 0
	}
	return (minutes*60 + seconds) * 1000
}

func isExactBeatportID(source model.SourceTrack, candidate model.BeatportTrack) bool {
	if !strings.EqualFold(strings.TrimSpace(source.Source), "beatport") || source.SourceID == "" {
		return false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(source.SourceID), 10, 64)
	return err == nil && id == candidate.ID
}
