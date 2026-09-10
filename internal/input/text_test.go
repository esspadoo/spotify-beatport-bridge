package input

import (
	"strings"
	"testing"
)

func TestParseTextFormats(t *testing.T) {
	t.Parallel()
	text := "\ufeff Artist - Track Name\r\n" +
		"Artist Two – En Dash\n" +
		"Artist Three — Em Dash\n" +
		"Artist Four | Pipe Title\n" +
		"Title-Only Song\n" +
		"Björk - Jóga\n" +
		"Jay-Z - Hard-Knock Life - Part 2\n" +
		"Artist Five â€“ Mojibake En Dash\n" +
		"Artist Six â€” Mojibake Em Dash\n" +
		"\n  # exported playlist\n" +
		"Artist - Track Name\n"

	tracks, err := ParseText(text)
	if err != nil {
		t.Fatalf("ParseText() error = %v", err)
	}
	if len(tracks) != 10 {
		t.Fatalf("len(tracks) = %d, want 10: %#v", len(tracks), tracks)
	}
	tests := []struct {
		index  int
		artist string
		title  string
		base   string
		dupe   bool
		pos    int
	}{
		{0, "Artist", "Track Name", "Track Name", false, 1},
		{1, "Artist Two", "En Dash", "En Dash", false, 2},
		{2, "Artist Three", "Em Dash", "Em Dash", false, 3},
		{3, "Artist Four", "Pipe Title", "Pipe Title", false, 4},
		{4, "", "Title-Only Song", "Title-Only Song", false, 5},
		{5, "Björk", "Jóga", "Jóga", false, 6},
		{6, "Jay-Z", "Hard-Knock Life - Part 2", "Hard-Knock Life - Part 2", false, 7},
		{7, "Artist Five", "Mojibake En Dash", "Mojibake En Dash", false, 8},
		{8, "Artist Six", "Mojibake Em Dash", "Mojibake Em Dash", false, 9},
		{9, "Artist", "Track Name", "Track Name", true, 10},
	}
	for _, test := range tests {
		track := tracks[test.index]
		if track.PrimaryArtist != test.artist || track.Title != test.title || track.BaseTitle != test.base || track.InputDuplicate != test.dupe || track.Position != test.pos {
			t.Errorf("track[%d] = %#v, want artist=%q title=%q base=%q duplicate=%v position=%d", test.index, track, test.artist, test.title, test.base, test.dupe, test.pos)
		}
	}
}

func TestParseTextExtractsVersionAndFeaturedArtists(t *testing.T) {
	t.Parallel()
	tracks, err := ParseText("Main Artist feat. Guest - Héllo (Extended Mix)\nOther - Song - Alice Remix")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tracks[0].Version, "Extended Mix"; got != want {
		t.Errorf("Version = %q, want %q", got, want)
	}
	if got, want := strings.Join(tracks[0].Artists, ","), "Main Artist,Guest"; got != want {
		t.Errorf("Artists = %q, want %q", got, want)
	}
	if got, want := tracks[0].BaseTitle, "Héllo"; got != want {
		t.Errorf("BaseTitle = %q, want %q", got, want)
	}
	if got, want := tracks[1].Version, "Alice Remix"; got != want {
		t.Errorf("dash-suffixed Version = %q, want %q", got, want)
	}
}

func TestParseTextSplitsCollaboratorSeparators(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		want string
	}{
		{"Dom Dolla & Tyga - Don't Worry Baby", "Dom Dolla,Tyga"},
		{"Rafael & Adam Ten - Beat Goes On", "Rafael,Adam Ten"},
		{"One, Two - Signal", "One,Two"},
		{"One x Two - Signal", "One,Two"},
		{"One vs Two - Signal", "One,Two"},
		{"One feat. Two - Signal", "One,Two"},
		{"A&B - Signal", "A&B"},
	}
	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			t.Parallel()
			track, err := ParseTextLine(test.line)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(track.Artists, ","); got != test.want {
				t.Fatalf("Artists = %q, want %q; track=%#v", got, test.want, track)
			}
			if track.PrimaryArtist != track.Artists[0] {
				t.Fatalf("PrimaryArtist = %q, want %q", track.PrimaryArtist, track.Artists[0])
			}
		})
	}
}

func TestParseTextRecognizesNamedEditAndAcapellaVersions(t *testing.T) {
	t.Parallel()
	tracks, err := ParseText("Dangerous Dan & Nicky Night Time - Mystery (Edit by DJ Kaos)\nDominica x DJ Tonka, Stefan Rio - I Gotta Let You Go (DJ Tonka Acapella)")
	if err != nil {
		t.Fatal(err)
	}
	if tracks[0].BaseTitle != "Mystery" || tracks[0].Version != "Edit by DJ Kaos" {
		t.Fatalf("edit track = %#v", tracks[0])
	}
	if tracks[1].BaseTitle != "I Gotta Let You Go" || tracks[1].Version != "DJ Tonka Acapella" {
		t.Fatalf("acapella track = %#v", tracks[1])
	}
}

func TestParseTextCommentOption(t *testing.T) {
	t.Parallel()
	tracks, err := ParseTextReader(strings.NewReader("# literal title\nSong"), TextOptions{KeepComments: true, Source: "stdin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 || tracks[0].Title != "# literal title" || tracks[0].Source != "stdin" {
		t.Fatalf("tracks = %#v", tracks)
	}
}

func TestParseTextLineRejectsBlank(t *testing.T) {
	t.Parallel()
	if _, err := ParseTextLine(" \ufeff "); err == nil {
		t.Fatal("ParseTextLine() error = nil, want error")
	}
}

func TestParseLinesKeepsEachArgumentAsOneTrack(t *testing.T) {
	t.Parallel()
	tracks, err := ParseLines([]string{"Artist - Multi\nLine", "Other - Song"}, "songs")
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 || tracks[0].Title != "Multi Line" || tracks[0].Position != 1 || tracks[1].Position != 2 {
		t.Fatalf("tracks=%#v", tracks)
	}
}

func TestTextParserRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()
	invalid := string([]byte{'A', 0xff, 'B'})
	if _, err := ParseText(invalid); err == nil {
		t.Fatal("ParseText invalid UTF-8 error=nil")
	}
	if _, err := ParseLines([]string{invalid}, "songs"); err == nil {
		t.Fatal("ParseLines invalid UTF-8 error=nil")
	}
}
