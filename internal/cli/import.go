package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bpbridge/bpbridge/internal/cache"
	"github.com/bpbridge/bpbridge/internal/credentials"
	inputparser "github.com/bpbridge/bpbridge/internal/input"
	"github.com/bpbridge/bpbridge/internal/matcher"
	"github.com/bpbridge/bpbridge/internal/model"
	"github.com/bpbridge/bpbridge/internal/report"
	"github.com/bpbridge/bpbridge/internal/spotify"
	"github.com/bpbridge/bpbridge/internal/syncer"
	"github.com/bpbridge/bpbridge/internal/ui"
	"github.com/spf13/cobra"
)

type importOptions struct {
	newName       string
	playlist      string
	public        bool
	private       bool
	dryRun        bool
	reportPath    string
	autoAmbiguous bool
	noCache       bool
}

type sourceBundle struct {
	typ            string
	identifier     string
	defaultNewName string
	tracks         []model.SourceTrack
}

func (app *App) addImportFlags(command *cobra.Command, options *importOptions) {
	flags := command.Flags()
	flags.StringVar(&options.newName, "new", "", "create a new Beatport playlist with this name")
	flags.StringVar(&options.playlist, "playlist", "", "existing Beatport playlist ID, URL, or exact name")
	flags.BoolVar(&options.public, "public", false, "make a newly created playlist public")
	flags.BoolVar(&options.private, "private", false, "make a newly created playlist private")
	flags.BoolVar(&options.dryRun, "dry-run", false, "match and report without creating or modifying a playlist")
	flags.StringVar(&options.reportPath, "report", "", "write a .json or .csv operation report")
	flags.BoolVar(&options.autoAmbiguous, "auto-ambiguous", false, "select the highest-scoring ambiguous candidate (unsafe opt-in)")
	flags.BoolVar(&options.noCache, "no-cache", false, "bypass local Beatport search results for this import")
	command.MarkFlagsMutuallyExclusive("new", "playlist")
	command.MarkFlagsMutuallyExclusive("public", "private")
}

func (app *App) newSpotifyCommand() *cobra.Command {
	options := &importOptions{}
	command := &cobra.Command{
		Use:   "spotify <track-or-playlist-url-or-uri>",
		Short: "Import an official Spotify track or accessible playlist",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			bundle, err := app.loadSpotifySource(command.Context(), args[0])
			if err != nil {
				return err
			}
			return app.runImport(command.Context(), bundle, *options)
		},
	}
	app.addImportFlags(command, options)
	return command
}

func (app *App) loadSpotifySource(ctx context.Context, raw string) (sourceBundle, error) {
	reference, err := spotify.ParseReference(raw)
	if err != nil {
		return sourceBundle{}, err
	}
	settings, err := app.loadConfig()
	if err != nil {
		return sourceBundle{}, err
	}
	session, err := app.spotifyServices(ctx, settings)
	if err != nil {
		return sourceBundle{}, err
	}
	tracks, playlist, fetchErr := session.client.FetchReference(ctx, reference)
	persistContext, cancelPersist := context.WithTimeout(context.Background(), 5*time.Second)
	persistErr := session.persist(persistContext)
	cancelPersist()
	if persistErr != nil {
		persistErr = fmt.Errorf("save refreshed Spotify token: %w", persistErr)
		if fetchErr != nil {
			fetchErr = errors.Join(fetchErr, persistErr)
		} else {
			return sourceBundle{}, persistErr
		}
	}
	if fetchErr != nil {
		if errors.Is(fetchErr, spotify.ErrReauthorizationRequired) {
			_ = session.store.Delete(context.Background(), credentials.KeySpotifyToken)
			return sourceBundle{}, fmt.Errorf("%w; run 'bpbridge auth spotify --user' again", fetchErr)
		}
		if errors.Is(fetchErr, spotify.ErrPlaylistRestricted) {
			return sourceBundle{}, fetchErr
		}
		return sourceBundle{}, fmt.Errorf("load Spotify %s: %w", reference.Type, fetchErr)
	}
	enrichSourceTracks(tracks)
	bundle := sourceBundle{typ: "spotify_" + string(reference.Type), identifier: reference.URI(), tracks: tracks}
	if playlist != nil {
		bundle.defaultNewName = playlist.Name
	}
	return bundle, nil
}

func (app *App) newTextCommand() *cobra.Command {
	options := &importOptions{}
	command := &cobra.Command{
		Use:   "text <file-or->",
		Short: "Import UTF-8 song rows from a text file or stdin",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			var reader io.Reader
			identifier := args[0]
			if args[0] == "-" {
				reader = app.reader
				identifier = "stdin"
			} else {
				file, err := os.Open(args[0])
				if err != nil {
					return fmt.Errorf("open text source %q: %w", args[0], err)
				}
				defer file.Close()
				reader = file
				if absolute, absoluteErr := filepath.Abs(args[0]); absoluteErr == nil {
					identifier = absolute
				}
			}
			tracks, err := inputparser.ParseTextReader(reader, inputparser.TextOptions{Source: "text"})
			if err != nil {
				return err
			}
			return app.runImport(command.Context(), sourceBundle{
				typ: "text", identifier: identifier, defaultNewName: defaultImportName(), tracks: tracks,
			}, *options)
		},
	}
	app.addImportFlags(command, options)
	return command
}

func (app *App) newSongsCommand() *cobra.Command {
	options := &importOptions{}
	command := &cobra.Command{
		Use:   "songs <artist-title> [artist-title...]",
		Short: "Import one or more song references supplied as arguments",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			tracks, err := inputparser.ParseLines(args, "songs")
			if err != nil {
				return err
			}
			return app.runImport(command.Context(), sourceBundle{
				typ: "songs", identifier: "command line", defaultNewName: defaultImportName(), tracks: tracks,
			}, *options)
		},
	}
	app.addImportFlags(command, options)
	return command
}

func (app *App) newInteractiveCommand() *cobra.Command {
	options := &importOptions{}
	command := &cobra.Command{
		Use:   "interactive",
		Short: "Run the guided Spotify or multiline text workflow",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if app.global.nonInteractive || app.global.json {
				return errors.New("interactive mode cannot be combined with --non-interactive or --json")
			}
			fmt.Fprintln(app.errOut, "BPBridge guided import")
			kind, err := ui.Prompt(app.reader, app.errOut, "Input type (spotify/text)", "text")
			if err != nil {
				return err
			}
			var bundle sourceBundle
			switch strings.ToLower(strings.TrimSpace(kind)) {
			case "spotify", "s":
				reference, promptErr := ui.Prompt(app.reader, app.errOut, "Spotify track/playlist URL or URI", "")
				if promptErr != nil {
					return promptErr
				}
				bundle, err = app.loadSpotifySource(command.Context(), reference)
			case "text", "t", "":
				fmt.Fprintln(app.errOut, "Paste one song per line. Enter a single . line to finish:")
				var lines []string
				for {
					line, readErr := app.reader.ReadString('\n')
					line = strings.TrimRight(line, "\r\n")
					if strings.TrimSpace(line) == "." {
						break
					}
					if line != "" {
						lines = append(lines, line)
					}
					if readErr == io.EOF {
						break
					}
					if readErr != nil {
						return readErr
					}
				}
				var tracks []model.SourceTrack
				tracks, err = inputparser.ParseText(strings.Join(lines, "\n"))
				bundle = sourceBundle{typ: "interactive_text", identifier: "interactive paste", defaultNewName: defaultImportName(), tracks: tracks}
			default:
				return fmt.Errorf("unsupported interactive input type %q", kind)
			}
			if err != nil {
				return err
			}
			return app.runImport(command.Context(), bundle, *options)
		},
	}
	app.addImportFlags(command, options)
	return command
}

func (app *App) runImport(ctx context.Context, source sourceBundle, importFlags importOptions) error {
	if len(source.tracks) == 0 {
		return errors.New("source contains no usable tracks")
	}
	settings, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, beatportClient, err := app.beatportServices(settings)
	if err != nil {
		return err
	}
	target, err := app.resolveTarget(ctx, beatportClient, settings, importFlags, source.defaultNewName)
	if err != nil {
		return err
	}
	if err := app.approveMutation(importFlags.dryRun, target, len(source.tracks)); err != nil {
		return err
	}

	var searchCache *cache.SearchCache
	if !importFlags.noCache {
		var cacheErr error
		searchCache, cacheErr = cache.Open("", settings.Cache.TTL.Value())
		if cacheErr != nil {
			fmt.Fprintf(app.errOut, "Warning: search cache is unavailable (%v); continuing without it. Run 'bpbridge cache clear' to reset it.\n", cacheErr)
			searchCache = nil
		}
	}
	matchConfig := matcher.Config{
		HighThreshold:      settings.Matching.MatchThreshold,
		AmbiguousThreshold: settings.Matching.AmbiguousThreshold,
		MinimumMargin:      matcher.DefaultConfig().MinimumMargin,
		PreferExtended:     settings.Matching.PreferExtendedMix,
	}
	effectiveNonInteractive := app.global.nonInteractive || app.global.json
	engineOptions := syncer.Options{
		Matcher: matchConfig, SearchPerPage: settings.Beatport.SearchResultCount, SearchPages: 1,
		DryRun: importFlags.dryRun, NonInteractive: effectiveNonInteractive,
		AutoAmbiguous: importFlags.autoAmbiguous, Cache: searchCache,
	}
	if app.global.debug {
		engineOptions.Debugf = app.debugf
	}
	if !effectiveNonInteractive {
		engineOptions.Resolver = &ui.Resolver{In: app.reader, Out: app.errOut}
	}
	if !app.global.json {
		engineOptions.Progress = app.printProgress
	}
	app.debugf("source=%s tracks=%d target_existing_id=%d target_name=%q dry_run=%t cache=%v", source.typ, len(source.tracks), target.ExistingID, target.Name, importFlags.dryRun, searchCache != nil)
	result, runErr := syncer.New(beatportClient).Run(ctx, syncer.Request{
		SourceType: source.typ, SourceIdentifier: source.identifier, Tracks: source.tracks, Target: target,
	}, engineOptions)
	operationReport := report.New(result.SourceType, result.SourceIdentifier, &result.Target, result.DryRun, result.Outcomes)
	operationReport.Timestamp = result.Timestamp
	if importFlags.reportPath != "" {
		if reportErr := report.WriteFile(importFlags.reportPath, operationReport); reportErr != nil {
			if runErr != nil {
				return errors.Join(runErr, reportErr)
			}
			return reportErr
		}
	}
	if app.global.json {
		if outputErr := report.WriteJSON(app.out, operationReport); outputErr != nil {
			return outputErr
		}
	} else {
		app.printSummary(operationReport)
		if importFlags.reportPath != "" {
			fmt.Fprintf(app.out, "Report:              %s\n", importFlags.reportPath)
		}
	}
	return runErr
}

func enrichSourceTracks(tracks []model.SourceTrack) {
	for index := range tracks {
		parts := matcher.ExtractTitleParts(tracks[index].Title)
		if tracks[index].BaseTitle == "" {
			tracks[index].BaseTitle = parts.Base
		}
		if tracks[index].Version == "" {
			tracks[index].Version = parts.Version
		}
		if tracks[index].Position <= 0 {
			tracks[index].Position = index + 1
		}
	}
}

func defaultImportName() string {
	return "BPBridge Import " + time.Now().Format("2006-01-02")
}
