// Package cli assembles bpbridge's command-line interface.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bpbridge/bpbridge/internal/config"
	"github.com/bpbridge/bpbridge/internal/credentials"
	"github.com/spf13/cobra"
)

type globalOptions struct {
	configPath     string
	market         string
	matchThreshold float64
	verbose        bool
	debug          bool
	json           bool
	nonInteractive bool
	yes            bool
}

// App owns terminal streams and process-level dependency factories. Keeping
// these values out of command globals makes command behavior testable.
type App struct {
	in     io.Reader
	inFile *os.File
	reader *bufio.Reader
	out    io.Writer
	errOut io.Writer
	global globalOptions

	credentialStore func() (credentials.Store, error)
	openBrowser     func(string) error
}

// NewRootCommand constructs the complete CLI. in may be any reader; hidden
// password prompts are enabled automatically when it is an *os.File terminal.
func NewRootCommand(in io.Reader, out, errOut io.Writer) *cobra.Command {
	if in == nil {
		in = strings.NewReader("")
	}
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	app := &App{
		in:              in,
		reader:          bufio.NewReader(in),
		out:             out,
		errOut:          errOut,
		credentialStore: credentials.NewDefaultStore,
		openBrowser:     openURL,
	}
	if file, ok := in.(*os.File); ok {
		app.inFile = file
	}

	root := &cobra.Command{
		Use:           "bpbridge",
		Short:         "Build Beatport playlists from Spotify or text track lists",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       versionString(),
		Args:          cobra.NoArgs,
	}
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")

	flags := root.PersistentFlags()
	flags.StringVar(&app.global.configPath, "config", "", "path to the non-secret TOML configuration file")
	flags.StringVar(&app.global.market, "market", "", "Spotify market as a two-letter country code")
	flags.Float64Var(&app.global.matchThreshold, "match-threshold", -1, "minimum 1-100 score for automatic matching")
	flags.BoolVarP(&app.global.verbose, "verbose", "v", false, "show match reasons and additional progress")
	flags.BoolVar(&app.global.debug, "debug", false, "show redacted diagnostic details")
	flags.BoolVar(&app.global.json, "json", false, "write machine-readable JSON to stdout")
	flags.BoolVar(&app.global.nonInteractive, "non-interactive", false, "never prompt to resolve matches or targets")
	flags.BoolVarP(&app.global.yes, "yes", "y", false, "skip mutation confirmation prompts")

	root.AddCommand(
		app.newSetupCommand(),
		app.newAuthCommand(),
		app.newLogoutCommand(),
		app.newSpotifyCommand(),
		app.newTextCommand(),
		app.newSongsCommand(),
		app.newInteractiveCommand(),
		app.newPlaylistsCommand(),
		app.newCacheCommand(),
		app.newVersionCommand(),
	)
	return root
}

// Execute runs the CLI with explicit arguments and streams.
func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	command := NewRootCommand(in, out, errOut)
	command.SetArgs(args)
	if err := command.ExecuteContext(ctx); err != nil {
		return errors.New(credentials.RedactSecrets(err.Error()))
	}
	return nil
}

func versionString() string {
	parts := []string{Version}
	if Commit != "" && Commit != "unknown" {
		parts = append(parts, "commit "+Commit)
	}
	if BuiltAt != "" && BuiltAt != "unknown" {
		parts = append(parts, "built "+BuiltAt)
	}
	return strings.Join(parts, ", ")
}

func (app *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version information",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if app.global.json {
				return writeJSON(app.out, map[string]string{"version": Version, "commit": Commit, "built_at": BuiltAt})
			}
			_, err := fmt.Fprintf(app.out, "bpbridge %s\n", versionString())
			return err
		},
	}
}

func (app *App) loadConfig() (config.Config, error) {
	settings, err := config.Load(app.global.configPath)
	if err != nil {
		return config.Config{}, err
	}
	if app.global.market != "" {
		settings.Spotify.Market = strings.ToUpper(strings.TrimSpace(app.global.market))
	}
	if app.global.matchThreshold >= 0 {
		settings.Matching.MatchThreshold = app.global.matchThreshold
	} else if app.global.matchThreshold < -1 {
		return config.Config{}, errors.New("--match-threshold must be between 1 and 100")
	}
	if err := settings.Validate(); err != nil {
		return config.Config{}, err
	}
	return settings, nil
}

func (app *App) secureStore() (credentials.Store, error) {
	store, err := app.credentialStore()
	if err != nil {
		return nil, fmt.Errorf("open secure credential storage: %w", err)
	}
	return store, nil
}

func newHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 8
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: transport, Timeout: timeout}
}

func (app *App) debugf(format string, arguments ...any) {
	if !app.global.debug {
		return
	}
	message := credentials.RedactSecrets(fmt.Sprintf(format, arguments...))
	fmt.Fprintf(app.errOut, "debug: %s\n", message)
}

func writeJSON(writer io.Writer, value any) error {
	return encodeJSON(writer, value)
}
