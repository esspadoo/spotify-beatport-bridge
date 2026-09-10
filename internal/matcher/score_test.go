package matcher

import (
	"strings"
	"testing"

	"github.com/bpbridge/bpbridge/internal/model"
)

func TestScoreCandidateCoreSignals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		source     model.SourceTrack
		candidate  model.BeatportTrack
		confidence model.Confidence
	}{
		{
			name:       "exact artist and title",
			source:     sourceTrack("Beyoncé", "Break My Soul"),
			candidate:  beatportCandidate(1, "Beyonce", "Break My Soul", "Original Mix"),
			confidence: model.ConfidenceHigh,
		},
		{
			name:       "different punctuation",
			source:     sourceTrack("ACME", "Don't Stop!"),
			candidate:  beatportCandidate(2, "ACME", "Dont Stop", "Original Mix"),
			confidence: model.ConfidenceHigh,
		},
		{
			name: "featured artists",
			source: model.SourceTrack{
				Title: "Signal (feat. Guest)", Artists: []string{"Main"}, PrimaryArtist: "Main",
			},
			candidate: model.BeatportTrack{
				ID: 3, Name: "Signal", MixName: "Original Mix",
				Artists: []model.Artist{{Name: "Main"}, {Name: "Guest"}},
			},
			confidence: model.ConfidenceHigh,
		},
		{
			name:       "same title different artist",
			source:     sourceTrack("Correct Artist", "Home"),
			candidate:  beatportCandidate(4, "Unrelated Artist", "Home", "Original Mix"),
			confidence: model.ConfidenceLow,
		},
		{
			name:       "weak fuzzy false positive",
			source:     sourceTrack("Correct Artist", "Blue Monday"),
			candidate:  beatportCandidate(5, "Someone Else", "Monday Morning News", "Extended Mix"),
			confidence: model.ConfidenceLow,
		},
		{
			name: "exact ISRC",
			source: model.SourceTrack{
				Title: "Signal", Artists: []string{"Main"}, PrimaryArtist: "Main", ISRC: "GB-ABC-12-34567",
			},
			candidate: func() model.BeatportTrack {
				value := beatportCandidate(6, "Main", "Signal", "Original Mix")
				value.ISRC = "gbabc1234567"
				return value
			}(),
			confidence: model.ConfidenceHigh,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := ScoreCandidate(test.source, test.candidate, Config{})
			if got.Confidence != test.confidence {
				t.Fatalf("confidence = %s (score %.1f, %v), want %s", got.Confidence, got.Score, got.Breakdown, test.confidence)
			}
		})
	}
}

func TestExtendedMixPreference(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		sourceTitle string
		candidates  []model.BeatportTrack
		wantID      int64
		wantHigh    bool
	}{
		{
			name: "explicit extended", sourceTitle: "Energy (Extended Mix)",
			candidates: []model.BeatportTrack{
				beatportCandidate(1, "Artist", "Energy", "Original Mix"),
				beatportCandidate(2, "Artist", "Energy", "Extended Mix"),
			},
			wantID: 2, wantHigh: true,
		},
		{
			name: "radio to extended", sourceTitle: "Energy (Radio Edit)",
			candidates: []model.BeatportTrack{
				beatportCandidate(3, "Artist", "Energy", "Radio Edit"),
				beatportCandidate(4, "Artist", "Energy", "Extended Mix"),
			},
			wantID: 4, wantHigh: true,
		},
		{
			name: "original to extended", sourceTitle: "Energy (Original Mix)",
			candidates: []model.BeatportTrack{
				beatportCandidate(5, "Artist", "Energy", "Original Mix"),
				beatportCandidate(6, "Artist", "Energy", "Extended Mix"),
			},
			wantID: 6, wantHigh: true,
		},
		{
			name: "default to extended", sourceTitle: "Energy",
			candidates: []model.BeatportTrack{
				beatportCandidate(7, "Artist", "Energy", "Original Mix"),
				beatportCandidate(8, "Artist", "Energy", "Extended Mix"),
			},
			wantID: 8, wantHigh: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ranked := RankCandidates(sourceTrack("Artist", test.sourceTitle), test.candidates, Config{})
			if len(ranked) == 0 || ranked[0].Track.ID != test.wantID {
				t.Fatalf("top = %#v, want ID %d", ranked, test.wantID)
			}
			if test.wantHigh && ranked[0].Confidence != model.ConfidenceHigh {
				t.Fatalf("confidence = %s, score %.1f, explanation %v", ranked[0].Confidence, ranked[0].Score, ranked[0].Breakdown.Explanation)
			}
		})
	}
}

func TestNamedRemixIsRespected(t *testing.T) {
	t.Parallel()
	source := sourceTrack("Artist", "Energy (Alice Remix)")
	named := beatportCandidate(10, "Artist", "Energy", "Alice Remix")
	named.Remixers = []model.Artist{{Name: "Alice"}}
	extended := beatportCandidate(11, "Artist", "Energy", "Extended Mix")
	unrelated := beatportCandidate(12, "Artist", "Energy", "Bob Remix")
	unrelated.Remixers = []model.Artist{{Name: "Bob"}}
	ranked := RankCandidates(source, []model.BeatportTrack{extended, unrelated, named}, Config{})
	if ranked[0].Track.ID != named.ID || ranked[0].Confidence != model.ConfidenceHigh {
		t.Fatalf("ranked = %#v", ranked)
	}
	if score := ScoreCandidate(source, unrelated, Config{}); score.Confidence != model.ConfidenceLow {
		t.Fatalf("unrelated remix confidence = %s, score %.1f", score.Confidence, score.Score)
	}
}

func TestUnrequestedRemixDoesNotWin(t *testing.T) {
	t.Parallel()
	source := sourceTrack("Artist", "Energy")
	wrong := beatportCandidate(20, "Artist", "Energy", "Alice Extended Remix")
	correct := beatportCandidate(21, "Artist", "Energy", "Original Mix")
	ranked := RankCandidates(source, []model.BeatportTrack{wrong, correct}, Config{})
	if ranked[0].Track.ID != correct.ID {
		t.Fatalf("top ID = %d, want %d", ranked[0].Track.ID, correct.ID)
	}
	if IsExtendedMix(wrong.MixName) {
		t.Fatal("named remix was classified as Extended Mix")
	}
}

func TestDifferentISRCValidExtendedCounterpart(t *testing.T) {
	t.Parallel()
	source := sourceTrack("Artist", "Energy (Radio Edit)")
	source.ISRC = "USAAA1111111"
	source.DurationMS = 180_000
	extended := beatportCandidate(30, "Artist", "Energy", "Extended Mix")
	extended.ISRC = "USAAA2222222"
	extended.LengthMS = 360_000
	score := ScoreCandidate(source, extended, Config{})
	if score.Confidence != model.ConfidenceHigh {
		t.Fatalf("confidence = %s, score %.1f, breakdown %#v", score.Confidence, score.Score, score.Breakdown)
	}
	if score.Breakdown.ISRC != 0 || score.Breakdown.Duration != 0 {
		t.Errorf("counterpart was penalized: %#v", score.Breakdown)
	}
	if !strings.Contains(Explain(score), "different ISRC allowed") {
		t.Errorf("explanation = %q", Explain(score))
	}
}

func TestCloseCandidatesAreAmbiguous(t *testing.T) {
	t.Parallel()
	source := sourceTrack("Artist", "Energy")
	candidates := []model.BeatportTrack{
		beatportCandidate(40, "Artist", "Energy", "Original Mix"),
		beatportCandidate(41, "Artist", "Energy", "Original Mix"),
	}
	match := Match(source, candidates, Config{})
	if match.Confidence != model.ConfidenceAmbiguous || match.Chosen != nil {
		t.Fatalf("Match() = %#v", match)
	}
}

func TestFullTitleAndBaseTitleAreDistinctSignals(t *testing.T) {
	t.Parallel()
	source := sourceTrack("Artist", "Energy (Live)")
	exactDecoration := beatportCandidate(50, "Artist", "Energy (Live)", "")
	separateDecoration := beatportCandidate(51, "Artist", "Energy", "Live")
	exact := ScoreCandidate(source, exactDecoration, Config{})
	separate := ScoreCandidate(source, separateDecoration, Config{})
	if exact.Breakdown.BaseTitle != separate.Breakdown.BaseTitle {
		t.Fatalf("base title differs: exact=%v separate=%v", exact.Breakdown, separate.Breakdown)
	}
	if exact.Breakdown.Title <= separate.Breakdown.Title {
		t.Fatalf("full-title signal was not distinct: exact=%v separate=%v", exact.Breakdown, separate.Breakdown)
	}
}

func TestTieOrderingDeterministicWhenExtendedPreferenceDisabled(t *testing.T) {
	t.Parallel()
	source := sourceTrack("Artist", "Energy")
	extended := beatportCandidate(99, "Artist", "Energy", "Extended Mix")
	radio := beatportCandidate(1, "Artist", "Energy", "Radio Edit")
	settings := Config{HighThreshold: 85, AmbiguousThreshold: 70, MinimumMargin: 3, PreferExtended: false}
	for iteration := 0; iteration < 20; iteration++ {
		input := []model.BeatportTrack{extended, radio}
		if iteration%2 != 0 {
			input[0], input[1] = input[1], input[0]
		}
		ranked := RankCandidates(source, input, settings)
		if ranked[0].Score != ranked[1].Score || ranked[0].Track.ID != 1 {
			t.Fatalf("iteration %d ranked=%#v", iteration, ranked)
		}
	}
}

func TestKnownBeatportCollaboratorRegressions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		source     model.SourceTrack
		candidates []model.BeatportTrack
		wantID     int64
	}{
		{
			name: "Dom Dolla and fuzzy Tiga spelling",
			source: model.SourceTrack{
				Title: "Don't Worry Baby", BaseTitle: "Don't Worry Baby",
				Artists: []string{"Dom Dolla", "Tyga"}, PrimaryArtist: "Dom Dolla",
			},
			candidates: []model.BeatportTrack{
				{ID: 28665094, Name: "Don't Worry Baby", MixName: "Extended Mix", Artists: []model.Artist{{Name: "The Beach Boys"}}},
				{ID: 28665093, Name: "Don't Worry Baby", MixName: "Extended Mix", Artists: []model.Artist{{Name: "Dom Dolla"}, {Name: "Tiga"}}},
			},
			wantID: 28665093,
		},
		{
			name: "Rafael and Adam Ten exact collaborators",
			source: model.SourceTrack{
				Title: "Beat Goes On", BaseTitle: "Beat Goes On",
				Artists: []string{"Rafael", "Adam Ten"}, PrimaryArtist: "Rafael",
			},
			candidates: []model.BeatportTrack{
				{ID: 29486879, Name: "Beat Goes On", MixName: "Extended Mix", Artists: []model.Artist{{Name: "Unrelated Artist"}}},
				{ID: 29486878, Name: "Beat Goes On", MixName: "Extended Mix", Artists: []model.Artist{{Name: "Rafael"}, {Name: "Adam Ten"}}},
			},
			wantID: 29486878,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			match := Match(test.source, test.candidates, Config{})
			if match.Confidence != model.ConfidenceHigh || match.Chosen == nil || match.Chosen.Track.ID != test.wantID {
				t.Fatalf("Match() = %#v, want HIGH ID %d", match, test.wantID)
			}
		})
	}
}

func TestArtistCorrectnessOutranksExtendedMix(t *testing.T) {
	t.Parallel()
	source := model.SourceTrack{
		Title: "Don't Worry Baby", BaseTitle: "Don't Worry Baby",
		Artists: []string{"Dom Dolla", "Tyga"}, PrimaryArtist: "Dom Dolla",
	}
	wrong := model.BeatportTrack{
		ID: 1, Name: "Don't Worry Baby", MixName: "Extended Mix",
		Artists: []model.Artist{{Name: "The Beach Boys"}},
	}
	score := ScoreCandidate(source, wrong, Config{})
	if score.Confidence != model.ConfidenceLow {
		t.Fatalf("unrelated artist confidence = %s, score %.1f, breakdown=%#v", score.Confidence, score.Score, score.Breakdown)
	}
}

func TestArtistComparisonIsOrderTolerantAndOneToOne(t *testing.T) {
	t.Parallel()
	comparison := CompareArtists([]string{"Rafael", "Adam Ten"}, []string{"Adam Ten", "Rafael"})
	if comparison.ExactMatches != 2 || comparison.SetSimilarity != 1 || comparison.Similarity < 0.9 {
		t.Fatalf("reversed exact comparison = %#v", comparison)
	}
	fuzzy := CompareArtists([]string{"Dom Dolla", "Tyga"}, []string{"Dom Dolla", "Tiga"})
	if fuzzy.ExactMatches != 1 || !fuzzy.PrimaryExact || fuzzy.Similarity < 0.9 {
		t.Fatalf("fuzzy collaborator comparison = %#v", fuzzy)
	}
}

func TestOneArtistTypoPlusExactCollaboratorIsHigh(t *testing.T) {
	t.Parallel()
	source := model.SourceTrack{
		Title: "Unique Song", BaseTitle: "Unique Song",
		Artists: []string{"Artist One", "Artst Two"}, PrimaryArtist: "Artist One",
	}
	candidate := model.BeatportTrack{
		ID: 71, Name: "Unique Song", MixName: "Extended Mix",
		Artists: []model.Artist{{Name: "Artist Two"}, {Name: "Artist One"}},
	}
	score := ScoreCandidate(source, candidate, Config{})
	if score.Confidence != model.ConfidenceHigh {
		t.Fatalf("confidence=%s score=%.1f breakdown=%#v", score.Confidence, score.Score, score.Breakdown)
	}
}

func TestExactSecondaryCollaboratorsCanOutweighMissingPrimary(t *testing.T) {
	t.Parallel()
	source := model.SourceTrack{
		Title: "I Gotta Let You Go (DJ Tonka Acapella)", BaseTitle: "I Gotta Let You Go",
		Version: "DJ Tonka Acapella", Artists: []string{"Dominica", "DJ Tonka", "Stefan Rio"}, PrimaryArtist: "Dominica",
	}
	candidate := model.BeatportTrack{
		ID: 6620422, Name: "I Gotta Let You Go", MixName: "DJ Tonka Acapella",
		Artists: []model.Artist{{Name: "DJ Tonka"}, {Name: "Stefan Rio"}},
	}
	score := ScoreCandidate(source, candidate, Config{})
	if score.Confidence != model.ConfidenceHigh {
		t.Fatalf("confidence=%s score=%.1f breakdown=%#v", score.Confidence, score.Score, score.Breakdown)
	}
}

func TestVersionContainmentAllowsOmittedPossessiveCredit(t *testing.T) {
	t.Parallel()
	source := model.SourceTrack{
		Title: "The Corner (Harry's Deep In Jersey Dub)", BaseTitle: "The Corner", Version: "Harry's Deep In Jersey Dub",
		Artists: []string{"Harry Romero", "Brothers Macklovitch", "Austin Ato"}, PrimaryArtist: "Harry Romero",
	}
	candidate := model.BeatportTrack{
		ID: 28923124, Name: "The Corner", MixName: "Deep In Jersey Dub",
		Artists: []model.Artist{{Name: "Austin Ato"}, {Name: "Harry Romero"}, {Name: "Brothers Macklovitch"}},
	}
	score := ScoreCandidate(source, candidate, Config{})
	if score.Confidence != model.ConfidenceHigh || score.Breakdown.Version != 10 {
		t.Fatalf("confidence=%s score=%.1f breakdown=%#v", score.Confidence, score.Score, score.Breakdown)
	}
}

func TestEditByVersionIsRecognizedAndMatched(t *testing.T) {
	t.Parallel()
	source := sourceTrack("Dangerous Dan", "Mystery (Edit by DJ Kaos)")
	candidate := beatportCandidate(18153247, "Dangerous Dan", "Mystery (Edit by DJ Kaos)", "Edit by DJ Kaos")
	score := ScoreCandidate(source, candidate, Config{})
	if score.Confidence != model.ConfidenceHigh || score.Breakdown.Version != 10 {
		t.Fatalf("confidence=%s score=%.1f breakdown=%#v", score.Confidence, score.Score, score.Breakdown)
	}
}

func sourceTrack(artist, title string) model.SourceTrack {
	return model.SourceTrack{Title: title, Artists: []string{artist}, PrimaryArtist: artist}
}

func beatportCandidate(id int64, artist, title, version string) model.BeatportTrack {
	return model.BeatportTrack{ID: id, Name: title, MixName: version, Artists: []model.Artist{{Name: artist}}}
}
