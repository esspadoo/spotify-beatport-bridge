package cli

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/bpbridge/bpbridge/internal/cache"
	"github.com/spf13/cobra"
)

func (app *App) newPlaylistsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "playlists",
		Short: "List all playlists owned by the authenticated Beatport user",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			settings, err := app.loadConfig()
			if err != nil {
				return err
			}
			_, client, err := app.beatportServices(settings)
			if err != nil {
				return err
			}
			playlists, err := client.ListPlaylists(command.Context())
			if err != nil {
				return err
			}
			sort.SliceStable(playlists, func(left, right int) bool {
				if playlists[left].Name != playlists[right].Name {
					return playlists[left].Name < playlists[right].Name
				}
				return playlists[left].ID < playlists[right].ID
			})
			if app.global.json {
				return writeJSON(app.out, map[string]any{"playlists": playlists, "count": len(playlists)})
			}
			if len(playlists) == 0 {
				fmt.Fprintln(app.out, "No Beatport playlists found.")
				return nil
			}
			fmt.Fprintln(app.out, "ID          TRACKS  UPDATED           NAME")
			for _, playlist := range playlists {
				updated := "-"
				if !playlist.UpdatedDate.IsZero() {
					updated = playlist.UpdatedDate.Local().Format("2006-01-02 15:04")
				}
				fmt.Fprintf(app.out, "%-11d %-7d %-17s %s\n", playlist.ID, playlist.TrackCount, updated, playlist.Name)
			}
			return nil
		},
	}
}

func (app *App) newCacheCommand() *cobra.Command {
	command := &cobra.Command{Use: "cache", Short: "Manage the local Beatport search cache", Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Delete all cached Beatport search results",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			path, err := cache.DefaultPath()
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove search cache %q: %w", path, err)
			}
			if app.global.json {
				return writeJSON(app.out, map[string]any{"cleared": true, "path": path})
			}
			fmt.Fprintf(app.out, "Search cache cleared: %s\n", path)
			return nil
		},
	})
	return command
}

func (app *App) canPrompt() bool {
	return app.inFile != nil && isTerminal(app.inFile) && !app.global.nonInteractive && !app.global.json
}
