package matcher

import (
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"capitalization and Unicode", "  BÉYONCÉ  ", "beyonce"},
		{"punctuation", "Don't.Stop! (Now)", "dont stop now"},
		{"ampersand", "Above & Beyond", "above and beyond"},
		{"and", "Above and Beyond", "above and beyond"},
		{"featuring", "Track FT. Guest", "track feat guest"},
		{"compatibility characters", "Ｍｕｓｉｃ №１", "music no1"},
		{"extended Latin", "Bjørn Łódź", "bjorn lodz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Normalize(test.in); got != test.want {
				t.Errorf("Normalize(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestNormalizeArtistSetIgnoresOrderAndDuplicates(t *testing.T) {
	t.Parallel()
	left := NormalizeArtistSet([]string{"Beyoncé", "JAY-Z", "Beyoncé"})
	right := NormalizeArtistSet([]string{"jay z", "BEYONCE"})
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("sets differ: %v != %v", left, right)
	}
}

func TestNormalizeKnownTitleRegressions(t *testing.T) {
	t.Parallel()
	if straight, curly := Normalize("Don't Worry Baby"), Normalize("Don\u2019t Worry Baby"); straight != curly {
		t.Fatalf("apostrophe variants differ: %q != %q", straight, curly)
	}
	if got := Normalize("Beat Goes On"); got != "beat goes on" {
		t.Fatalf("Normalize(Beat Goes On) = %q", got)
	}
}

func TestExtractTitleParts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in       string
		base     string
		version  string
		featured []string
	}{
		{"Song (Extended Mix)", "Song", "Extended Mix", nil},
		{"Song [Radio Edit]", "Song", "Radio Edit", nil},
		{"Song - Alice Remix", "Song", "Alice Remix", nil},
		{"Song (feat. Alice & Bob)", "Song", "", []string{"Alice & Bob"}},
		{"Song feat. Alice", "Song", "", []string{"Alice"}},
		{"Song (A Parenthetical Title)", "Song (A Parenthetical Title)", "", nil},
		{"Love-Hate Relationship", "Love-Hate Relationship", "", nil},
		{"Song (Extended Mix) (ft. Alice)", "Song", "Extended Mix", []string{"Alice"}},
		{"Mystery (Edit by DJ Kaos)", "Mystery", "Edit by DJ Kaos", nil},
		{"I Gotta Let You Go (DJ Tonka Acapella)", "I Gotta Let You Go", "DJ Tonka Acapella", nil},
	}
	for _, test := range tests {
		t.Run(test.in, func(t *testing.T) {
			t.Parallel()
			got := ExtractTitleParts(test.in)
			if got.Base != test.base || got.Version != test.version || !reflect.DeepEqual(got.FeaturedArtists, test.featured) {
				t.Errorf("ExtractTitleParts(%q) = %#v", test.in, got)
			}
		})
	}
}

func TestNormalizeVersion(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"EXTENDED VERSION": "extended mix",
		"Original":         "original mix",
		"Radio Mix":        "radio edit",
		"Alice Remix":      "alice remix",
		"ACAPPELLA":        "acapella",
		"A Cappella":       "acapella",
	}
	for input, want := range tests {
		if got := NormalizeVersion(input); got != want {
			t.Errorf("NormalizeVersion(%q) = %q, want %q", input, got, want)
		}
	}
	if IsExtendedMix("Alice Extended Remix") {
		t.Error("named extended remix must not count as generic Extended Mix")
	}
	for _, version := range []string{"DJ Tonka Acapella", "Edit by DJ Kaos", "Remix by Alice"} {
		if !LooksLikeVersion(version) {
			t.Errorf("LooksLikeVersion(%q) = false", version)
		}
	}
}
