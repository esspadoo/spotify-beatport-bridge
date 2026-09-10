package syncer

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bpbridge/bpbridge/internal/matcher"
	"github.com/bpbridge/bpbridge/internal/model"
)

type fakeBeatport struct {
	searchResults map[string][]model.BeatportTrack
	searchQueries []string
	playlist      model.BeatportPlaylist
	items         []model.BeatportPlaylistItem
	created       int
	added         []int64
	failID        int64
	commitThenErr int64
}

func (f *fakeBeatport) Search(_ context.Context, query string, _, _ int) ([]model.BeatportTrack, error) {
	f.searchQueries = append(f.searchQueries, query)
	return f.searchResults[matcher.Normalize(query)], nil
}
func (f *fakeBeatport) GetPlaylist(context.Context, int64) (model.BeatportPlaylist, error) {
	return f.playlist, nil
}
func (f *fakeBeatport) GetPlaylistTracks(context.Context, int64) ([]model.BeatportPlaylistItem, error) {
	return f.items, nil
}
func (f *fakeBeatport) CreatePlaylist(_ context.Context, name string, public bool) (model.BeatportPlaylist, error) {
	f.created++
	return model.BeatportPlaylist{ID: 99, Name: name, IsPublic: public}, nil
}
func (f *fakeBeatport) AddTracks(_ context.Context, _ int64, ids []int64) error {
	if len(ids) != 1 {
		return errors.New("test expects single-track adds")
	}
	if ids[0] == f.failID {
		return errors.New("simulated add failure")
	}
	if ids[0] == f.commitThenErr {
		f.items = append(f.items, model.BeatportPlaylistItem{Track: model.BeatportTrack{ID: ids[0]}})
		return errors.New("simulated lost response after commit")
	}
	f.added = append(f.added, ids[0])
	return nil
}

func TestUncertainAddIsReconciledWithoutRetry(t *testing.T) {
	t.Parallel()
	source := model.SourceTrack{Source: "text", Title: "One", Artists: []string{"Artist"}, PrimaryArtist: "Artist", Position: 1}
	candidate := model.BeatportTrack{ID: 1, Name: "One", Artists: []model.Artist{{Name: "Artist"}}}
	fake := &fakeBeatport{
		searchResults: map[string][]model.BeatportTrack{
			matcher.Normalize("Artist One"): {candidate},
			matcher.Normalize("One"):        {candidate},
		},
		playlist:      model.BeatportPlaylist{ID: 5, Name: "Target"},
		commitThenErr: 1,
	}
	result, err := New(fake).Run(context.Background(), Request{
		SourceType: "text", Tracks: []model.SourceTrack{source}, Target: Target{ExistingID: 5},
	}, Options{NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.added) != 0 || result.Outcomes[0].Status != model.StatusAdded {
		t.Fatalf("added calls=%v outcome=%#v", fake.added, result.Outcomes[0])
	}
	if result.Outcomes[0].Reason != "track membership confirmed after an uncertain Beatport add response" {
		t.Fatalf("reason=%q", result.Outcomes[0].Reason)
	}
}

func TestDryRunDoesNotMutateAndPreventsDuplicates(t *testing.T) {
	t.Parallel()
	source := []model.SourceTrack{
		{Source: "text", Title: "Alpha", Artists: []string{"Artist"}, PrimaryArtist: "Artist", Position: 1},
		{Source: "text", Title: "Alpha", Artists: []string{"Artist"}, PrimaryArtist: "Artist", Position: 2},
	}
	candidate := model.BeatportTrack{ID: 7, Name: "Alpha", MixName: "Extended Mix", Artists: []model.Artist{{Name: "Artist"}}}
	fake := &fakeBeatport{
		searchResults: map[string][]model.BeatportTrack{matcher.Normalize("Artist Alpha"): {candidate}, matcher.Normalize("Alpha"): {candidate}},
		playlist:      model.BeatportPlaylist{ID: 5, Name: "Target"},
		items:         []model.BeatportPlaylistItem{{Track: candidate}},
	}
	result, err := New(fake).Run(context.Background(), Request{
		SourceType: "text", Tracks: source, Target: Target{ExistingID: 5},
	}, Options{DryRun: true, NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if fake.created != 0 || len(fake.added) != 0 {
		t.Fatalf("dry run mutated remote state: created=%d added=%v", fake.created, fake.added)
	}
	statuses := []model.TrackStatus{result.Outcomes[0].Status, result.Outcomes[1].Status}
	want := []model.TrackStatus{model.StatusAlreadyExists, model.StatusInputDuplicate}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("statuses=%v want %v", statuses, want)
	}
}

func TestDryRunWithNewTargetDoesNotCreatePlaylist(t *testing.T) {
	t.Parallel()
	source := model.SourceTrack{Source: "text", Title: "Alpha", Artists: []string{"Artist"}, PrimaryArtist: "Artist", Position: 1}
	candidate := model.BeatportTrack{ID: 7, Name: "Alpha", MixName: "Extended Mix", Artists: []model.Artist{{Name: "Artist"}}}
	fake := &fakeBeatport{searchResults: map[string][]model.BeatportTrack{
		matcher.Normalize("Artist Alpha"): {candidate}, matcher.Normalize("Alpha"): {candidate},
	}}
	result, err := New(fake).Run(context.Background(), Request{
		SourceType: "text", Tracks: []model.SourceTrack{source}, Target: Target{Name: "Preview", Public: false},
	}, Options{DryRun: true, NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if fake.created != 0 || len(fake.added) != 0 || result.Target.ID != 0 || result.Target.Name != "Preview" {
		t.Fatalf("dry-run mutation/target: created=%d added=%v target=%#v", fake.created, fake.added, result.Target)
	}
	if result.Outcomes[0].Status != model.StatusMatched {
		t.Fatalf("outcome=%#v", result.Outcomes[0])
	}
}

func TestAmbiguousMatchIsSafeUnlessExplicitlySelected(t *testing.T) {
	t.Parallel()
	source := model.SourceTrack{Source: "text", Title: "Alpha", Artists: []string{"Artist"}, PrimaryArtist: "Artist", Position: 1}
	candidates := []model.BeatportTrack{
		{ID: 8, Name: "Alpha", MixName: "Original Mix", Artists: []model.Artist{{Name: "Artist"}}},
		{ID: 7, Name: "Alpha", MixName: "Original Mix", Artists: []model.Artist{{Name: "Artist"}}},
	}
	makeFake := func() *fakeBeatport {
		return &fakeBeatport{searchResults: map[string][]model.BeatportTrack{
			matcher.Normalize("Artist Alpha"): candidates, matcher.Normalize("Alpha"): candidates,
		}}
	}
	request := Request{SourceType: "text", Tracks: []model.SourceTrack{source}, Target: Target{Name: "Preview"}}

	safe, err := New(makeFake()).Run(context.Background(), request, Options{DryRun: true, NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if safe.Outcomes[0].Status != model.StatusAmbiguous || safe.Outcomes[0].Chosen != nil {
		t.Fatalf("safe outcome=%#v", safe.Outcomes[0])
	}

	automatic, err := New(makeFake()).Run(context.Background(), request, Options{DryRun: true, NonInteractive: true, AutoAmbiguous: true})
	if err != nil {
		t.Fatal(err)
	}
	if automatic.Outcomes[0].Status != model.StatusMatched || automatic.Outcomes[0].Chosen == nil || automatic.Outcomes[0].Chosen.ID != 7 {
		t.Fatalf("automatic outcome=%#v", automatic.Outcomes[0])
	}
}

func TestOrderedAddsContinueAfterFailure(t *testing.T) {
	t.Parallel()
	tracks := []model.SourceTrack{
		{Source: "text", Title: "One", Artists: []string{"Artist"}, PrimaryArtist: "Artist", Position: 1},
		{Source: "text", Title: "Two", Artists: []string{"Artist"}, PrimaryArtist: "Artist", Position: 2},
		{Source: "text", Title: "Three", Artists: []string{"Artist"}, PrimaryArtist: "Artist", Position: 3},
	}
	fake := &fakeBeatport{searchResults: map[string][]model.BeatportTrack{}, failID: 2}
	for index, source := range tracks {
		candidate := model.BeatportTrack{ID: int64(index + 1), Name: source.Title, Artists: []model.Artist{{Name: "Artist"}}}
		fake.searchResults[matcher.Normalize("Artist "+source.Title)] = []model.BeatportTrack{candidate}
		fake.searchResults[matcher.Normalize(source.Title)] = []model.BeatportTrack{candidate}
	}
	result, err := New(fake).Run(context.Background(), Request{
		SourceType: "songs", Tracks: tracks, Target: Target{Name: "New", Public: false},
	}, Options{NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if fake.created != 1 || !reflect.DeepEqual(fake.added, []int64{1, 3}) {
		t.Fatalf("created=%d added=%v", fake.created, fake.added)
	}
	if result.Outcomes[1].Status != model.StatusFailed || result.Outcomes[2].Status != model.StatusAdded {
		t.Fatalf("outcomes=%#v", result.Outcomes)
	}
}

func TestStagedSearchFindsKnownTrackByIndividualCollaborator(t *testing.T) {
	t.Parallel()
	source := model.SourceTrack{
		Source: "text", Title: "Don't Worry Baby", BaseTitle: "Don't Worry Baby",
		Artists: []string{"Dom Dolla", "Tyga"}, PrimaryArtist: "Dom Dolla", Position: 1,
	}
	correct := model.BeatportTrack{
		ID: 28665093, Name: "Don't Worry Baby", MixName: "Extended Mix",
		Artists: []model.Artist{{Name: "Dom Dolla"}, {Name: "Tiga"}},
	}
	fake := &fakeBeatport{searchResults: map[string][]model.BeatportTrack{
		matcher.Normalize("Dom Dolla Don't Worry Baby"): {correct},
	}}
	result, err := New(fake).Run(context.Background(), Request{
		SourceType: "text", Tracks: []model.SourceTrack{source}, Target: Target{Name: "Preview"},
	}, Options{DryRun: true, NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if outcome := result.Outcomes[0]; outcome.Status != model.StatusMatched || outcome.Chosen == nil || outcome.Chosen.ID != correct.ID {
		t.Fatalf("outcome = %#v", outcome)
	}
	if !containsString(fake.searchQueries, "Dom Dolla Don't Worry Baby") {
		t.Fatalf("queries = %#v, individual collaborator query missing", fake.searchQueries)
	}
}

func TestStagedSearchContinuesPastIrrelevantResultsToTitleFallback(t *testing.T) {
	t.Parallel()
	source := model.SourceTrack{
		Source: "text", Title: "Beat Goes On", BaseTitle: "Beat Goes On",
		Artists: []string{"Rafael", "Adam Ten"}, PrimaryArtist: "Rafael", Position: 1,
	}
	wrong := model.BeatportTrack{
		ID: 100, Name: "Beat Goes On", MixName: "Extended Mix",
		Artists: []model.Artist{{Name: "Unrelated Artist"}},
	}
	correct := model.BeatportTrack{
		ID: 29486878, Name: "Beat Goes On", MixName: "Extended Mix",
		Artists: []model.Artist{{Name: "Rafael"}, {Name: "Adam Ten"}},
	}
	fake := &fakeBeatport{searchResults: map[string][]model.BeatportTrack{
		matcher.Normalize("Rafael Adam Ten Beat Goes On"): {wrong},
		matcher.Normalize("Beat Goes On"):                 {correct},
	}}
	result, err := New(fake).Run(context.Background(), Request{
		SourceType: "text", Tracks: []model.SourceTrack{source}, Target: Target{Name: "Preview"},
	}, Options{DryRun: true, NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if outcome := result.Outcomes[0]; outcome.Status != model.StatusMatched || outcome.Chosen == nil || outcome.Chosen.ID != correct.ID {
		t.Fatalf("outcome = %#v", outcome)
	}
	if !containsString(fake.searchQueries, "Beat Goes On") {
		t.Fatalf("queries = %#v, title fallback was not reached", fake.searchQueries)
	}
}

func TestSearchCacheKeyPreservesMeaningfulPunctuation(t *testing.T) {
	t.Parallel()
	withApostrophe := searchCacheKey("  Don't   Worry Baby ", 25, 1)
	withoutApostrophe := searchCacheKey("Dont Worry Baby", 25, 1)
	if withApostrophe == withoutApostrophe {
		t.Fatalf("punctuation-distinct queries share cache key %q", withApostrophe)
	}
	if got, want := searchCacheKey("DON'T WORRY BABY", 25, 1), withApostrophe; got != want {
		t.Fatalf("case/whitespace variants differ: %q != %q", got, want)
	}
	if got := searchCacheKey("Don't Worry Baby", 50, 1); got == withApostrophe {
		t.Fatalf("different result limits share cache key %q", got)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
