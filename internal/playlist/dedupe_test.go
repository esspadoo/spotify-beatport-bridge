package playlist

import (
	"reflect"
	"testing"

	"github.com/bpbridge/bpbridge/internal/model"
)

func TestMarkSourceDuplicatesPreservesOrder(t *testing.T) {
	t.Parallel()
	tracks := []model.SourceTrack{
		{Title: "Déjà Vu", Artists: []string{"Artist A", "Artist B"}, Position: 1},
		{Title: "Other", Artists: []string{"Artist"}, Position: 2},
		{Title: "Deja Vu", Artists: []string{"artist b", "artist a"}, Position: 3},
	}
	got := MarkSourceDuplicates(tracks)
	if got[0].InputDuplicate || got[1].InputDuplicate || !got[2].InputDuplicate {
		t.Fatalf("duplicate flags = %v, %v, %v", got[0].InputDuplicate, got[1].InputDuplicate, got[2].InputDuplicate)
	}
	if got[0].Position != 1 || got[1].Position != 2 || got[2].Position != 3 {
		t.Fatalf("order changed: %#v", got)
	}
	if tracks[2].InputDuplicate {
		t.Fatal("input slice was mutated")
	}
}

func TestBuildPlanAllDuplicateClasses(t *testing.T) {
	t.Parallel()
	track10 := model.BeatportTrack{ID: 10, Name: "First"}
	track20 := model.BeatportTrack{ID: 20, Name: "Existing"}
	track30 := model.BeatportTrack{ID: 30, Name: "Unresolved"}
	outcomes := []model.TrackOutcome{
		{Input: model.SourceTrack{Title: "First", Artists: []string{"A"}}, Chosen: &track10, Status: model.StatusMatched},
		{Input: model.SourceTrack{Title: "Alternate spelling", Artists: []string{"A"}}, Chosen: &track10, Status: model.StatusMatched},
		{Input: model.SourceTrack{Title: "Existing", Artists: []string{"B"}}, Chosen: &track20, Status: model.StatusMatched},
		{Input: model.SourceTrack{Title: "First", Artists: []string{"A"}}, Chosen: &track30, Status: model.StatusMatched},
		{Input: model.SourceTrack{Title: "Needs choice", Artists: []string{"C"}}, Chosen: &track30, Status: model.StatusAmbiguous},
	}
	existing := map[int64]struct{}{20: {}}
	plan := BuildPlan(outcomes, existing)
	if len(plan.Tracks) != 1 || plan.Tracks[0].ID != 10 {
		t.Fatalf("Tracks = %#v", plan.Tracks)
	}
	wantStatuses := []model.TrackStatus{
		model.StatusMatched,
		model.StatusInputDuplicate,
		model.StatusAlreadyExists,
		model.StatusInputDuplicate,
		model.StatusAmbiguous,
	}
	gotStatuses := make([]model.TrackStatus, len(plan.Outcomes))
	for index := range plan.Outcomes {
		gotStatuses[index] = plan.Outcomes[index].Status
	}
	if !reflect.DeepEqual(gotStatuses, wantStatuses) {
		t.Fatalf("statuses = %v, want %v", gotStatuses, wantStatuses)
	}
	if _, mutated := existing[10]; mutated {
		t.Fatal("existing ID map was mutated")
	}
}

func TestExistingTrackIDs(t *testing.T) {
	t.Parallel()
	items := []model.BeatportPlaylistItem{
		{Track: model.BeatportTrack{ID: 1}},
		{Track: model.BeatportTrack{ID: 2}},
		{Track: model.BeatportTrack{ID: 1}},
		{Track: model.BeatportTrack{ID: 0}},
	}
	ids := ExistingTrackIDs(items)
	if len(ids) != 2 {
		t.Fatalf("IDs = %#v", ids)
	}
}

func TestDeduplicateTrackIDs(t *testing.T) {
	t.Parallel()
	toAdd, statuses := DeduplicateTrackIDs([]int64{1, 2, 1, 3, 0}, map[int64]struct{}{2: {}})
	if !reflect.DeepEqual(toAdd, []int64{1, 3}) {
		t.Fatalf("toAdd = %v", toAdd)
	}
	want := []model.TrackStatus{model.StatusMatched, model.StatusAlreadyExists, model.StatusInputDuplicate, model.StatusMatched, model.StatusFailed}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}
}
