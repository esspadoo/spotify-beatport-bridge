package cli

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bpbridge/bpbridge/internal/syncer"
)

func TestCommandSurfaceAndVersionJSON(t *testing.T) {
	t.Parallel()
	var output, errorOutput bytes.Buffer
	command := NewRootCommand(strings.NewReader(""), &output, &errorOutput)
	for _, name := range []string{"setup", "auth", "logout", "spotify", "text", "songs", "interactive", "playlists", "cache", "version"} {
		found := false
		for _, child := range command.Commands() {
			if child.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("root command does not contain %q", name)
		}
	}
	if err := Execute(context.Background(), []string{"version", "--json"}, strings.NewReader(""), &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"version": "`) || !strings.Contains(output.String(), `"commit":`) {
		t.Fatalf("version JSON = %q", output.String())
	}
}

func TestSpotifyAuthUsesRegistrableFixedPortRedirect(t *testing.T) {
	t.Parallel()
	command := NewRootCommand(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	spotifyAuth, _, err := command.Find([]string{"auth", "spotify"})
	if err != nil {
		t.Fatal(err)
	}
	flag := spotifyAuth.Flags().Lookup("redirect-uri")
	if flag == nil || flag.DefValue != "http://127.0.0.1:8000/callback" {
		t.Fatalf("redirect-uri default = %#v, want fixed registrable loopback URI", flag)
	}
}

func TestImportCommandsExposeNoCacheDiagnosticFlag(t *testing.T) {
	t.Parallel()
	command := NewRootCommand(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	for _, path := range [][]string{{"spotify"}, {"text"}, {"songs"}} {
		child, _, err := command.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		flag := child.Flags().Lookup("no-cache")
		if flag == nil || flag.DefValue != "false" {
			t.Fatalf("%s --no-cache flag = %#v", path[0], flag)
		}
	}
}

func TestParsePlaylistID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		id    int64
		ok    bool
	}{
		{"12345", 12345, true},
		{"https://www.beatport.com/library/playlists/54321", 54321, true},
		{"https://www.beatport.com/playlist/my-list/777", 777, true},
		{"https://api.beatport.com/v4/my/playlists/42/", 42, true},
		{"https://evil.example/playlists/42", 0, false},
		{"http://www.beatport.com/library/playlists/42", 0, false},
		{"https://user@www.beatport.com/library/playlists/42", 0, false},
		{"My Playlist", 0, false},
		{"0", 0, false},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			id, ok := parsePlaylistID(test.value)
			if id != test.id || ok != test.ok {
				t.Fatalf("parsePlaylistID(%q) = (%d,%t), want (%d,%t)", test.value, id, ok, test.id, test.ok)
			}
		})
	}
}

func TestMutationRequiresConsentWhenPromptUnavailable(t *testing.T) {
	t.Parallel()
	app := &App{reader: bufio.NewReader(strings.NewReader("")), out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	target := syncer.Target{Name: "New"}
	if err := app.approveMutation(false, target, 2); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("approveMutation without terminal = %v, want --yes error", err)
	}
	app.global.yes = true
	if err := app.approveMutation(false, target, 2); err != nil {
		t.Fatalf("--yes approval failed: %v", err)
	}
	app.global.yes = false
	if err := app.approveMutation(true, target, 2); err != nil {
		t.Fatalf("dry-run approval failed: %v", err)
	}
}
