package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bpbridge/bpbridge/internal/beatport"
	"github.com/bpbridge/bpbridge/internal/config"
	"github.com/bpbridge/bpbridge/internal/credentials"
	"github.com/bpbridge/bpbridge/internal/spotify"
	"github.com/bpbridge/bpbridge/internal/ui"
	"github.com/spf13/cobra"
)

const spotifyRedirectURI = "http://127.0.0.1:8000/callback"

type beatportStore struct {
	credentials.JSONStore[beatport.Token]
}

func (store beatportStore) Load(ctx context.Context) (beatport.Token, error) {
	token, err := store.JSONStore.Load(ctx)
	if errors.Is(err, credentials.ErrNotFound) {
		return beatport.Token{}, beatport.ErrNotAuthenticated
	}
	return token, err
}

func (app *App) beatportServices(settings config.Config) (*beatport.AuthManager, *beatport.Client, error) {
	store, err := app.secureStore()
	if err != nil {
		return nil, nil, err
	}
	tokenStore := beatportStore{credentials.JSONStore[beatport.Token]{Store: store, Key: credentials.KeyBeatportToken}}
	authHTTP := newHTTPClient(settings.Network.RequestTimeout.Value())
	authHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	auth := beatport.NewAuthManager(
		tokenStore,
		beatport.WithAuthHTTPClient(authHTTP),
		beatport.WithAuthBaseURL(settings.Beatport.APIBaseURL),
		beatport.WithClientID(settings.Beatport.ClientID),
	)
	client := beatport.NewClient(
		auth,
		beatport.WithHTTPClient(newHTTPClient(settings.Network.RequestTimeout.Value())),
		beatport.WithBaseURLs(settings.Beatport.APIBaseURL, beatport.DefaultSearchBase),
		beatport.WithMaxRetries(settings.Network.MaxRetries),
	)
	return auth, client, nil
}

type spotifySession struct {
	client       *spotify.Client
	manager      *spotify.TokenManager
	refresh      *spotify.RefreshTokenSource
	store        credentials.Store
	userToken    bool
	credentialBy string
}

func (session *spotifySession) persist(ctx context.Context) error {
	if session == nil || !session.userToken || session.store == nil || session.manager == nil {
		return nil
	}
	token := session.manager.Snapshot()
	if session.refresh != nil {
		refreshed := session.refresh.Snapshot()
		if refreshed.AccessToken != "" || refreshed.RefreshToken != "" {
			token = refreshed
		}
	}
	if token.RefreshToken == "" {
		return nil
	}
	return credentials.JSONStore[spotify.Token]{Store: session.store, Key: credentials.KeySpotifyToken}.Save(ctx, token)
}

func (app *App) spotifyServices(ctx context.Context, settings config.Config) (*spotifySession, error) {
	if settings.Spotify.ClientID == "" {
		return nil, errors.New("Spotify client ID is not configured; run 'bpbridge setup'")
	}
	store, err := app.secureStore()
	if err != nil {
		return nil, err
	}
	httpClient := newHTTPClient(settings.Network.RequestTimeout.Value())
	stored, loadErr := credentials.JSONStore[spotify.Token]{Store: store, Key: credentials.KeySpotifyToken}.Load(ctx)
	var manager *spotify.TokenManager
	session := &spotifySession{store: store}
	if loadErr == nil && stored.RefreshToken != "" {
		oauth, oauthErr := spotify.NewOAuthClient(spotify.OAuthConfig{ClientID: settings.Spotify.ClientID, HTTPClient: httpClient})
		if oauthErr != nil {
			return nil, oauthErr
		}
		refresh := spotify.NewRefreshTokenSource(oauth, stored)
		manager = spotify.NewTokenManager(refresh, stored)
		session.refresh = refresh
		session.userToken = true
		session.credentialBy = "Spotify user OAuth (PKCE)"
	} else if loadErr != nil && !errors.Is(loadErr, credentials.ErrNotFound) {
		return nil, fmt.Errorf("load Spotify OAuth token: %w", loadErr)
	} else {
		secret, origin, secretErr := loadSecret(ctx, store, "SPOTIFY_CLIENT_SECRET", credentials.KeySpotifyClientSecret)
		if secretErr != nil {
			return nil, secretErr
		}
		if secret == "" {
			return nil, errors.New("Spotify client secret is not configured; run 'bpbridge setup', set SPOTIFY_CLIENT_SECRET, or run 'bpbridge auth spotify --user'")
		}
		source := spotify.NewClientCredentialsSource(settings.Spotify.ClientID, secret, httpClient)
		manager = spotify.NewTokenManager(source)
		session.credentialBy = "Spotify Client Credentials (secret from " + origin + ")"
	}
	session.manager = manager
	session.client = spotify.NewClient(
		manager,
		spotify.WithHTTPClient(httpClient),
		spotify.WithMarket(settings.Spotify.Market),
		spotify.WithRetryPolicy(settings.Network.MaxRetries, 250*time.Millisecond, 8*time.Second),
	)
	app.debugf("Spotify authentication mode: %s", session.credentialBy)
	return session, nil
}

func loadSecret(ctx context.Context, store credentials.Store, environmentName, key string) (string, string, error) {
	if value, present := os.LookupEnv(environmentName); present && value != "" {
		return value, "environment", nil
	}
	value, err := store.Get(ctx, key)
	if errors.Is(err, credentials.ErrNotFound) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("load secure credential %q: %w", key, err)
	}
	return string(value), "Windows Credential Manager", nil
}

func (app *App) newSetupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Configure non-secret defaults and securely store a Spotify client secret",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			settings, err := app.loadConfig()
			if err != nil {
				return err
			}
			if app.global.nonInteractive {
				if settings.Spotify.ClientID == "" {
					return errors.New("SPOTIFY_CLIENT_ID is required for non-interactive setup")
				}
				if err := config.Save(app.global.configPath, settings); err != nil {
					return err
				}
				return app.printSetupResult(settings, false)
			}

			fmt.Fprintln(app.errOut, "BPBridge setup (secrets are stored in Windows Credential Manager)")
			clientID, err := ui.Prompt(app.reader, app.errOut, "Spotify client ID", settings.Spotify.ClientID)
			if err != nil {
				return err
			}
			settings.Spotify.ClientID = clientID
			market, err := ui.Prompt(app.reader, app.errOut, "Spotify market (optional, e.g. IT)", settings.Spotify.Market)
			if err != nil {
				return err
			}
			settings.Spotify.Market = strings.ToUpper(strings.TrimSpace(market))
			beatportID, err := ui.Prompt(app.reader, app.errOut, "Beatport public client ID", settings.Beatport.ClientID)
			if err != nil {
				return err
			}
			settings.Beatport.ClientID = beatportID
			if err := config.Save(app.global.configPath, settings); err != nil {
				return err
			}

			store, err := app.secureStore()
			if err != nil {
				return err
			}
			_, _, existingErr := loadSecret(command.Context(), store, "SPOTIFY_CLIENT_SECRET", credentials.KeySpotifyClientSecret)
			if existingErr != nil {
				return existingErr
			}
			secret, err := app.readSecret("Spotify client secret (blank keeps existing/env value): ")
			if err != nil {
				return err
			}
			storedSecret := false
			if secret != "" {
				if err := store.Set(command.Context(), credentials.KeySpotifyClientSecret, []byte(secret)); err != nil {
					return err
				}
				storedSecret = true
			}
			return app.printSetupResult(settings, storedSecret)
		},
	}
}

func (app *App) printSetupResult(settings config.Config, storedSecret bool) error {
	path := app.global.configPath
	if path == "" {
		path, _ = config.DefaultPath()
	}
	if app.global.json {
		return writeJSON(app.out, map[string]any{
			"configured": true, "config_path": path, "spotify_client_id_configured": settings.Spotify.ClientID != "",
			"spotify_secret_stored": storedSecret, "beatport_client_id_configured": settings.Beatport.ClientID != "",
		})
	}
	fmt.Fprintf(app.out, "Configuration saved to %s\n", path)
	if storedSecret {
		fmt.Fprintln(app.out, "Spotify client secret saved in Windows Credential Manager.")
	}
	fmt.Fprintln(app.out, "Next: bpbridge auth spotify  and  bpbridge auth beatport")
	return nil
}

func (app *App) newAuthCommand() *cobra.Command {
	command := &cobra.Command{Use: "auth", Short: "Configure and inspect provider authentication", Args: cobra.NoArgs}
	command.AddCommand(app.newSpotifyAuthCommand(), app.newBeatportAuthCommand(), app.newAuthStatusCommand())
	return command
}

func (app *App) newSpotifyAuthCommand() *cobra.Command {
	var userOAuth bool
	var noBrowser bool
	var redirectURI string
	command := &cobra.Command{
		Use:   "spotify",
		Short: "Authenticate Spotify with Client Credentials or optional user PKCE",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !userOAuth && noBrowser {
				return errors.New("--no-browser is only valid together with --user")
			}
			settings, err := app.loadConfig()
			if err != nil {
				return err
			}
			if settings.Spotify.ClientID == "" {
				return errors.New("Spotify client ID is not configured; run 'bpbridge setup' or set SPOTIFY_CLIENT_ID")
			}
			store, err := app.secureStore()
			if err != nil {
				return err
			}
			if userOAuth {
				return app.authorizeSpotifyUser(command.Context(), settings, store, redirectURI, noBrowser)
			}
			secret, _, err := loadSecret(command.Context(), store, "SPOTIFY_CLIENT_SECRET", credentials.KeySpotifyClientSecret)
			if err != nil {
				return err
			}
			if secret == "" {
				secret, err = app.readSecret("Spotify client secret: ")
				if err != nil {
					return err
				}
				if secret == "" {
					return errors.New("Spotify client secret cannot be empty")
				}
				if err := store.Set(command.Context(), credentials.KeySpotifyClientSecret, []byte(secret)); err != nil {
					return err
				}
			}
			source := spotify.NewClientCredentialsSource(settings.Spotify.ClientID, secret, newHTTPClient(settings.Network.RequestTimeout.Value()))
			if _, err := source.Token(command.Context()); err != nil {
				return fmt.Errorf("test Spotify Client Credentials: %w", err)
			}
			return app.printAuthSuccess("spotify", "client_credentials")
		},
	}
	command.Flags().BoolVar(&userOAuth, "user", false, "authorize a Spotify user with PKCE for eligible playlist access")
	command.Flags().BoolVar(&noBrowser, "no-browser", false, "print the authorization URL without opening a browser")
	command.Flags().StringVar(&redirectURI, "redirect-uri", spotifyRedirectURI, "registered loopback redirect URI (default http://127.0.0.1:8000/callback)")
	return command
}

func (app *App) authorizeSpotifyUser(ctx context.Context, settings config.Config, store credentials.Store, redirectURI string, noBrowser bool) error {
	pair, err := spotify.GeneratePKCE()
	if err != nil {
		return err
	}
	state, err := spotify.GenerateState()
	if err != nil {
		return err
	}
	receiver, err := spotify.NewLoopbackReceiver(redirectURI, state)
	if err != nil {
		return err
	}
	defer receiver.Close()
	oauth, err := spotify.NewOAuthClient(spotify.OAuthConfig{
		ClientID: settings.Spotify.ClientID, HTTPClient: newHTTPClient(settings.Network.RequestTimeout.Value()),
	})
	if err != nil {
		return err
	}
	authorizationURL, err := oauth.AuthorizationURL(receiver.RedirectURI(), state, pair.Challenge, []string{
		"playlist-read-private", "playlist-read-collaborative",
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(app.errOut, "Open this Spotify authorization URL:\n%s\n", authorizationURL)
	if !noBrowser {
		if err := app.openBrowser(authorizationURL); err != nil {
			fmt.Fprintf(app.errOut, "Could not open a browser automatically: %v\n", err)
		}
	}
	waitContext, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	code, err := receiver.Wait(waitContext)
	if err != nil {
		return fmt.Errorf("wait for Spotify authorization: %w", err)
	}
	token, err := oauth.Exchange(ctx, receiver.RedirectURI(), code, pair.Verifier)
	if err != nil {
		return err
	}
	if err := (credentials.JSONStore[spotify.Token]{Store: store, Key: credentials.KeySpotifyToken}).Save(ctx, token); err != nil {
		return err
	}
	return app.printAuthSuccess("spotify", "authorization_code_pkce")
}

func (app *App) newBeatportAuthCommand() *cobra.Command {
	var tokenImport bool
	var username string
	command := &cobra.Command{
		Use:   "beatport",
		Short: "Authenticate Beatport or securely import token JSON",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			settings, err := app.loadConfig()
			if err != nil {
				return err
			}
			auth, client, err := app.beatportServices(settings)
			if err != nil {
				return err
			}
			if tokenImport {
				data, err := app.readTokenJSON()
				if err != nil {
					return err
				}
				if err := auth.ImportJSON(command.Context(), data); err != nil {
					return err
				}
			} else {
				if username == "" {
					username, err = ui.Prompt(app.reader, app.errOut, "Beatport username/email", "")
					if err != nil {
						return err
					}
				}
				password, err := app.readSecret("Beatport password (not stored): ")
				if err != nil {
					return err
				}
				if err := auth.Login(command.Context(), username, password); err != nil {
					return err
				}
			}
			if _, err := client.ListPlaylists(command.Context()); err != nil {
				return fmt.Errorf("Beatport token was saved but playlist access test failed: %w", err)
			}
			mode := "legacy_authorization_code"
			if tokenImport {
				mode = "token_import"
			}
			return app.printAuthSuccess("beatport", mode)
		},
	}
	command.Flags().BoolVar(&tokenImport, "token", false, "securely paste token JSON instead of entering a password")
	command.Flags().StringVar(&username, "username", "", "Beatport username/email (password is always prompted)")
	return command
}

func (app *App) printAuthSuccess(provider, mode string) error {
	if app.global.json {
		return writeJSON(app.out, map[string]any{"provider": provider, "authenticated": true, "mode": mode})
	}
	_, err := fmt.Fprintf(app.out, "%s authentication succeeded (%s).\n", providerName(provider), mode)
	return err
}

func (app *App) newAuthStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show credential status without revealing secrets",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			settings, err := app.loadConfig()
			if err != nil {
				return err
			}
			store, err := app.secureStore()
			if err != nil {
				return err
			}
			beatportStatus := map[string]any{"authenticated": false}
			auth, _, err := app.beatportServices(settings)
			if err != nil {
				return err
			}
			authenticated, expiresAt, err := auth.Status(command.Context())
			if err != nil {
				return err
			}
			beatportStatus["authenticated"] = authenticated
			if authenticated {
				beatportStatus["expires_at"] = expiresAt.UTC()
			}

			spotifyStatus := map[string]any{"client_id_configured": settings.Spotify.ClientID != "", "client_secret_configured": false, "user_oauth": false}
			if secret, _, secretErr := loadSecret(command.Context(), store, "SPOTIFY_CLIENT_SECRET", credentials.KeySpotifyClientSecret); secretErr != nil {
				return secretErr
			} else {
				spotifyStatus["client_secret_configured"] = secret != ""
			}
			if token, tokenErr := (credentials.JSONStore[spotify.Token]{Store: store, Key: credentials.KeySpotifyToken}).Load(command.Context()); tokenErr == nil {
				spotifyStatus["user_oauth"] = token.RefreshToken != ""
				if !token.ExpiresAt.IsZero() {
					spotifyStatus["expires_at"] = token.ExpiresAt.UTC()
				}
			} else if !errors.Is(tokenErr, credentials.ErrNotFound) {
				return tokenErr
			}

			result := map[string]any{"spotify": spotifyStatus, "beatport": beatportStatus}
			if app.global.json {
				return writeJSON(app.out, result)
			}
			fmt.Fprintf(app.out, "Spotify client ID:     %s\n", yesNo(settings.Spotify.ClientID != ""))
			fmt.Fprintf(app.out, "Spotify client secret: %s\n", yesNo(spotifyStatus["client_secret_configured"].(bool)))
			fmt.Fprintf(app.out, "Spotify user OAuth:    %s\n", yesNo(spotifyStatus["user_oauth"].(bool)))
			fmt.Fprintf(app.out, "Beatport token:        %s\n", yesNo(authenticated))
			if authenticated {
				fmt.Fprintf(app.out, "Beatport token expiry: %s\n", expiresAt.Local().Format(time.RFC3339))
			}
			return nil
		},
	}
}

func (app *App) newLogoutCommand() *cobra.Command {
	command := &cobra.Command{Use: "logout", Short: "Delete securely stored provider credentials", Args: cobra.NoArgs}
	command.AddCommand(app.newProviderLogoutCommand("spotify"), app.newProviderLogoutCommand("beatport"))
	return command
}

func (app *App) newProviderLogoutCommand(provider string) *cobra.Command {
	return &cobra.Command{
		Use:   provider,
		Short: "Delete stored " + provider + " credentials",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			store, err := app.secureStore()
			if err != nil {
				return err
			}
			keys := []string{credentials.KeyBeatportToken}
			if provider == "spotify" {
				keys = []string{credentials.KeySpotifyToken, credentials.KeySpotifyClientSecret}
			}
			for _, key := range keys {
				if err := store.Delete(command.Context(), key); err != nil {
					return err
				}
			}
			if app.global.json {
				return writeJSON(app.out, map[string]any{"provider": provider, "authenticated": false, "credentials_deleted": true})
			}
			fmt.Fprintf(app.out, "%s credentials removed from Windows Credential Manager.\n", providerName(provider))
			return nil
		},
	}
}

func (app *App) readSecret(prompt string) (string, error) {
	return ui.ReadPassword(app.inFile, app.reader, app.errOut, prompt)
}

func (app *App) readTokenJSON() ([]byte, error) {
	if app.inFile != nil && isTerminal(app.inFile) {
		value, err := app.readSecret("Paste one-line Beatport token JSON (input hidden): ")
		return []byte(value), err
	}
	fmt.Fprintln(app.errOut, "Reading Beatport token JSON from stdin...")
	data, err := io.ReadAll(io.LimitReader(app.reader, 1<<20))
	if err != nil {
		return nil, err
	}
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, errors.New("Beatport token JSON is empty")
	}
	return data, nil
}

func yesNo(value bool) string {
	if value {
		return "configured"
	}
	return "not configured"
}

func providerName(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func encodeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
