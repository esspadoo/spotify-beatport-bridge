package matcher

import (
	"reflect"
	"testing"

	"github.com/bpbridge/bpbridge/internal/model"
)

func TestGenerateQueries(t *testing.T) {
	t.Parallel()
	source := model.SourceTrack{
		Title:         "Midnight (Radio Edit)",
		Artists:       []string{"Artist One", "Artist Two"},
		PrimaryArtist: "Artist One",
	}
	want := []string{
		"Artist One Artist Two Midnight (Radio Edit)",
		"Artist One Artist Two Midnight",
		"Artist One Midnight (Radio Edit)",
		"Artist One Midnight",
		"Artist Two Midnight (Radio Edit)",
		"Artist Two Midnight",
		"Midnight (Radio Edit)",
		"Midnight",
	}
	if got := GenerateQueries(source); !reflect.DeepEqual(got, want) {
		t.Fatalf("GenerateQueries() = %#v, want %#v", got, want)
	}
}

func TestGenerateQueriesKnownCollaboratorRegressions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source model.SourceTrack
		want   []string
	}{
		{
			name: "fuzzy second collaborator and apostrophe fallback",
			source: model.SourceTrack{
				Title: "Don't Worry Baby", Artists: []string{"Dom Dolla", "Tyga"}, PrimaryArtist: "Dom Dolla",
			},
			want: []string{
				"Dom Dolla Tyga Don't Worry Baby",
				"Dom Dolla Don't Worry Baby",
				"Tyga Don't Worry Baby",
				"Don't Worry Baby",
				"Dont Worry Baby",
			},
		},
		{
			name: "reversed collaborators",
			source: model.SourceTrack{
				Title: "Beat Goes On", Artists: []string{"Rafael", "Adam Ten"}, PrimaryArtist: "Rafael",
			},
			want: []string{
				"Rafael Adam Ten Beat Goes On",
				"Rafael Beat Goes On",
				"Adam Ten Beat Goes On",
				"Beat Goes On",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := GenerateQueries(test.source); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("GenerateQueries() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestGenerateQueryPassesAreOrderedFromSpecificToBroad(t *testing.T) {
	t.Parallel()
	passes := GenerateQueryPasses(model.SourceTrack{
		Title: "Don't Worry Baby", Artists: []string{"Dom Dolla", "Tyga"}, PrimaryArtist: "Dom Dolla",
	})
	wantNames := []string{"all-artists", "individual-artists", "title", "sanitized-title"}
	if len(passes) != len(wantNames) {
		t.Fatalf("passes = %#v", passes)
	}
	for index, want := range wantNames {
		if passes[index].Name != want || len(passes[index].Queries) == 0 {
			t.Fatalf("pass[%d] = %#v, want named %q with queries", index, passes[index], want)
		}
	}
}

func TestGenerateQueriesTitleOnlyAndDeduplicates(t *testing.T) {
	t.Parallel()
	if got := GenerateQueries(model.SourceTrack{Title: "Only a Song"}); !reflect.DeepEqual(got, []string{"Only a Song"}) {
		t.Fatalf("GenerateQueries() = %#v", got)
	}
}
