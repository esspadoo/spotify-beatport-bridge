package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/bpbridge/bpbridge/internal/beatport"
	"github.com/bpbridge/bpbridge/internal/config"
	"github.com/bpbridge/bpbridge/internal/model"
	"github.com/bpbridge/bpbridge/internal/syncer"
	"github.com/bpbridge/bpbridge/internal/ui"
)

func (app *App) resolveTarget(ctx context.Context, client *beatport.Client, settings config.Config, options importOptions, defaultName string) (syncer.Target, error) {
	if options.newName != "" && options.playlist != "" {
		return syncer.Target{}, errors.New("--new and --playlist cannot be used together")
	}
	if options.public && options.private {
		return syncer.Target{}, errors.New("--public and --private cannot be used together")
	}
	if (options.public || options.private) && strings.TrimSpace(options.newName) == "" && strings.TrimSpace(options.playlist) != "" {
		return syncer.Target{}, errors.New("--public and --private apply only to a playlist created with --new")
	}
	public := settings.UI.DefaultPlaylistVisibility == "public"
	if options.public {
		public = true
	}
	if options.private {
		public = false
	}
	if strings.TrimSpace(options.newName) != "" {
		return syncer.Target{Name: strings.TrimSpace(options.newName), Public: public}, nil
	}
	reference := strings.TrimSpace(options.playlist)
	if reference == "" {
		if app.global.nonInteractive || app.global.json || !app.canPrompt() {
			return syncer.Target{}, errors.New("choose a target with --new or --playlist")
		}
		if strings.TrimSpace(defaultName) == "" {
			defaultName = defaultImportName()
		}
		value, err := ui.Prompt(app.reader, app.errOut, "Target (new:<name>, playlist ID/URL, or exact name)", "new:"+defaultName)
		if err != nil {
			return syncer.Target{}, err
		}
		if strings.HasPrefix(strings.ToLower(value), "new:") {
			name := strings.TrimSpace(value[len("new:"):])
			if name == "" {
				return syncer.Target{}, errors.New("new playlist name cannot be empty")
			}
			return syncer.Target{Name: name, Public: public}, nil
		}
		reference = value
	}
	if id, ok := parsePlaylistID(reference); ok {
		return syncer.Target{ExistingID: id}, nil
	}
	playlists, err := client.ListPlaylists(ctx)
	if err != nil {
		return syncer.Target{}, fmt.Errorf("list Beatport playlists while resolving %q: %w", reference, err)
	}
	var matching []model.BeatportPlaylist
	for _, playlist := range playlists {
		if strings.EqualFold(strings.TrimSpace(playlist.Name), reference) {
			matching = append(matching, playlist)
		}
	}
	switch len(matching) {
	case 0:
		return syncer.Target{}, fmt.Errorf("no Beatport playlist has the exact name %q", reference)
	case 1:
		return syncer.Target{ExistingID: matching[0].ID}, nil
	}
	if app.global.nonInteractive || app.global.json || !app.canPrompt() {
		return syncer.Target{}, fmt.Errorf("%d Beatport playlists are named %q; use a numeric ID in non-interactive mode", len(matching), reference)
	}
	fmt.Fprintf(app.errOut, "Multiple playlists are named %q:\n", reference)
	for index, playlist := range matching {
		updated := "unknown"
		if !playlist.UpdatedDate.IsZero() {
			updated = playlist.UpdatedDate.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(app.errOut, "%d. ID %d | %d tracks | updated %s\n", index+1, playlist.ID, playlist.TrackCount, updated)
	}
	choice, err := ui.Prompt(app.reader, app.errOut, "Selection number", "")
	if err != nil {
		return syncer.Target{}, err
	}
	selected, err := strconv.Atoi(choice)
	if err != nil || selected < 1 || selected > len(matching) {
		return syncer.Target{}, errors.New("invalid playlist selection")
	}
	return syncer.Target{ExistingID: matching[selected-1].ID}, nil
}

func parsePlaylistID(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if id, err := strconv.ParseInt(value, 10, 64); err == nil && id > 0 {
		return id, true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return 0, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "beatport.com" && host != "www.beatport.com" && host != "api.beatport.com" {
		return 0, false
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return 0, false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index, segment := range segments {
		if segment != "playlists" || index+1 >= len(segments) {
			continue
		}
		for _, candidate := range segments[index+1:] {
			if id, parseErr := strconv.ParseInt(candidate, 10, 64); parseErr == nil && id > 0 {
				return id, true
			}
		}
	}
	// Public playlist URLs commonly end in /playlist/<slug>/<id>.
	if len(segments) >= 2 && (segments[0] == "playlist" || segments[0] == "library") {
		if id, parseErr := strconv.ParseInt(segments[len(segments)-1], 10, 64); parseErr == nil && id > 0 {
			return id, true
		}
	}
	return 0, false
}

func (app *App) confirmMutation(target syncer.Target, trackCount int) (bool, error) {
	action := fmt.Sprintf("Add up to %d tracks to Beatport playlist ID %d", trackCount, target.ExistingID)
	if target.ExistingID <= 0 {
		action = fmt.Sprintf("Create Beatport playlist %q and add up to %d tracks", target.Name, trackCount)
	}
	answer, err := ui.Prompt(app.reader, app.errOut, action+"? (y/N)", "N")
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func (app *App) approveMutation(dryRun bool, target syncer.Target, trackCount int) error {
	if dryRun || app.global.yes {
		return nil
	}
	if !app.canPrompt() {
		return errors.New("refusing a non-interactive Beatport mutation without --yes (use --dry-run to preview safely)")
	}
	ok, err := app.confirmMutation(target, trackCount)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("operation cancelled")
	}
	return nil
}
