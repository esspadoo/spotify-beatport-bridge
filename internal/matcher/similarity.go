package matcher

import (
	"math"
	"sort"
	"strings"
)

// Similarity returns a deterministic 0..1 score. It combines normalized edit
// distance with token overlap, making punctuation/word-order variations cheap
// without making unrelated titles with one shared word look convincing.
func Similarity(left, right string) float64 {
	left = Normalize(left)
	right = Normalize(right)
	if left == right {
		if left == "" {
			return 0
		}
		return 1
	}
	if left == "" || right == "" {
		return 0
	}

	leftRunes, rightRunes := []rune(left), []rune(right)
	distance := levenshtein(leftRunes, rightRunes)
	characterScore := 1 - float64(distance)/float64(max(len(leftRunes), len(rightRunes)))
	tokenScore := tokenDice(strings.Fields(left), strings.Fields(right))

	// Token overlap is especially helpful when collaborator order differs. A
	// weighted maximum prevents a single common token from dominating.
	combined := math.Max(characterScore, 0.65*tokenScore+0.35*characterScore)
	return clamp(combined, 0, 1)
}

// ArtistComparison exposes the deterministic set comparison used by scoring.
// ExactMatches and PrimaryExact are diagnostic evidence, not separate bonuses.
type ArtistComparison struct {
	Similarity        float64
	SetSimilarity     float64
	PrimarySimilarity float64
	ExactMatches      int
	PrimaryExact      bool
}

// CompareArtists compares normalized artist sets using a maximum-weight
// one-to-one assignment. Composite display names are expanded for comparison,
// allowing plain-text collaborators to match APIs that model the same credit
// as either one display string or several structured artist objects.
func CompareArtists(source, candidate []string) ArtistComparison {
	comparison := ArtistComparison{}
	if len(source) == 0 || len(candidate) == 0 {
		return comparison
	}
	primary := NormalizeArtist(source[0])
	originalCandidates := NormalizeArtistSet(candidate)
	source = normalizeExpandedArtistSet(source)
	candidate = normalizeExpandedArtistSet(candidate)
	if len(source) == 0 || len(candidate) == 0 {
		return comparison
	}

	total := optimalArtistAssignment(source, candidate)
	comparison.SetSimilarity = clamp(2*total/float64(len(source)+len(candidate)), 0, 1)
	for _, sourceArtist := range source {
		for _, candidateArtist := range candidate {
			if sourceArtist == candidateArtist {
				comparison.ExactMatches++
				break
			}
		}
	}
	primaryCandidates := append(append([]string(nil), originalCandidates...), candidate...)
	for _, candidateArtist := range primaryCandidates {
		comparison.PrimarySimilarity = math.Max(comparison.PrimarySimilarity, Similarity(primary, candidateArtist))
		if primary != "" && primary == candidateArtist {
			comparison.PrimaryExact = true
		}
	}
	comparison.Similarity = clamp(0.7*comparison.PrimarySimilarity+0.3*comparison.SetSimilarity, 0, 1)
	// When at least two collaborators match exactly, the set itself is strong
	// identity evidence even if Beatport omits or reorders the source's first
	// credit. Do not let one missing primary artist erase those exact matches.
	if comparison.ExactMatches >= 2 {
		comparison.Similarity = math.Max(comparison.Similarity, comparison.SetSimilarity)
	}
	return comparison
}

// ArtistSimilarity is the compact scoring API retained for callers that do
// not need diagnostic details.
func ArtistSimilarity(source, candidate []string) float64 {
	return CompareArtists(source, candidate).Similarity
}

func normalizeExpandedArtistSet(artists []string) []string {
	var expanded []string
	for _, artist := range artists {
		parts := SplitTextArtists(artist)
		if len(parts) == 0 {
			parts = []string{artist}
		}
		expanded = append(expanded, parts...)
	}
	return NormalizeArtistSet(expanded)
}

func optimalArtistAssignment(left, right []string) float64 {
	// Track the smaller set in the bitmask. Artist credit lists are normally
	// tiny; the fallback keeps pathological metadata bounded.
	if len(left) > len(right) {
		left, right = right, left
	}
	if len(left) > 15 {
		return greedyArtistAssignment(left, right)
	}
	states := 1 << len(left)
	dp := make([]float64, states)
	for index := 1; index < states; index++ {
		dp[index] = math.Inf(-1)
	}
	for _, rightArtist := range right {
		next := append([]float64(nil), dp...)
		for mask, value := range dp {
			if math.IsInf(value, -1) {
				continue
			}
			for leftIndex, leftArtist := range left {
				bit := 1 << leftIndex
				if mask&bit != 0 {
					continue
				}
				next[mask|bit] = math.Max(next[mask|bit], value+Similarity(leftArtist, rightArtist))
			}
		}
		dp = next
	}
	return dp[states-1]
}

func greedyArtistAssignment(left, right []string) float64 {
	type pair struct {
		left, right int
		score       float64
	}
	var pairs []pair
	for leftIndex, leftArtist := range left {
		for rightIndex, rightArtist := range right {
			pairs = append(pairs, pair{left: leftIndex, right: rightIndex, score: Similarity(leftArtist, rightArtist)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].score != pairs[j].score {
			return pairs[i].score > pairs[j].score
		}
		if pairs[i].left != pairs[j].left {
			return pairs[i].left < pairs[j].left
		}
		return pairs[i].right < pairs[j].right
	})
	usedLeft := make([]bool, len(left))
	usedRight := make([]bool, len(right))
	total := 0.0
	for _, pair := range pairs {
		if usedLeft[pair.left] || usedRight[pair.right] {
			continue
		}
		usedLeft[pair.left], usedRight[pair.right] = true, true
		total += pair.score
	}
	return total
}

func levenshtein(left, right []rune) int {
	if len(left) == 0 {
		return len(right)
	}
	if len(right) == 0 {
		return len(left)
	}
	if len(left) > len(right) {
		left, right = right, left
	}
	previous := make([]int, len(left)+1)
	current := make([]int, len(left)+1)
	for index := range previous {
		previous[index] = index
	}
	for rightIndex, rightRune := range right {
		current[0] = rightIndex + 1
		for leftIndex, leftRune := range left {
			cost := 1
			if leftRune == rightRune {
				cost = 0
			}
			current[leftIndex+1] = min(
				current[leftIndex]+1,
				previous[leftIndex+1]+1,
				previous[leftIndex]+cost,
			)
		}
		previous, current = current, previous
	}
	return previous[len(left)]
}

func tokenDice(left, right []string) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	intersection := 0
	for leftIndex, rightIndex := 0, 0; leftIndex < len(left) && rightIndex < len(right); {
		switch strings.Compare(left[leftIndex], right[rightIndex]) {
		case -1:
			leftIndex++
		case 1:
			rightIndex++
		default:
			intersection++
			leftIndex++
			rightIndex++
		}
	}
	return 2 * float64(intersection) / float64(len(left)+len(right))
}

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
