package spotify

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LoopbackReceiver is a one-shot local OAuth callback server. It binds only to
// an explicit loopback IP address, checks state in constant time, and never
// receives or stores a client secret.
type LoopbackReceiver struct {
	redirectURI   string
	expectedState string
	listener      net.Listener
	server        *http.Server
	results       chan callbackResult
	closeOnce     sync.Once
}

type callbackResult struct {
	code string
	err  error
}

// NewLoopbackReceiver starts listening. A port of 0 chooses an available port;
// use RedirectURI when constructing the authorization URL so the exchange uses
// the exact same value.
func NewLoopbackReceiver(redirectURI, expectedState string) (*LoopbackReceiver, error) {
	if expectedState == "" {
		return nil, errors.New("Spotify OAuth state is required")
	}
	parsed, err := parseLoopbackRedirect(redirectURI)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(parsed.Hostname(), parsed.Port()))
	if err != nil {
		return nil, fmt.Errorf("listen for Spotify OAuth callback: %w", err)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	parsed.Host = net.JoinHostPort(parsed.Hostname(), strconv.Itoa(actualPort))

	receiver := &LoopbackReceiver{
		redirectURI:   parsed.String(),
		expectedState: expectedState,
		listener:      listener,
		results:       make(chan callbackResult, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(parsed.Path, receiver.handleCallback)
	receiver.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if serveErr := receiver.server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
			receiver.publish(callbackResult{err: fmt.Errorf("serve Spotify OAuth callback: %w", serveErr)})
		}
	}()
	return receiver, nil
}

// RedirectURI is the exact bound callback URL, including an allocated port.
func (r *LoopbackReceiver) RedirectURI() string { return r.redirectURI }

// Wait waits for one valid-state callback and returns its authorization code.
func (r *LoopbackReceiver) Wait(ctx context.Context) (string, error) {
	select {
	case result := <-r.results:
		_ = r.Close()
		return result.code, result.err
	case <-ctx.Done():
		_ = r.Close()
		return "", ctx.Err()
	}
}

// Close stops the loopback listener. It is safe to call repeatedly.
func (r *LoopbackReceiver) Close() error {
	var closeErr error
	r.closeOnce.Do(func() {
		if r.server != nil {
			closeErr = r.server.Close()
		} else if r.listener != nil {
			closeErr = r.listener.Close()
		}
	})
	if errors.Is(closeErr, http.ErrServerClosed) || errors.Is(closeErr, net.ErrClosed) {
		return nil
	}
	return closeErr
}

func (r *LoopbackReceiver) handleCallback(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state := request.URL.Query().Get("state")
	if subtle.ConstantTimeCompare([]byte(state), []byte(r.expectedState)) != 1 {
		http.Error(writer, "authorization state mismatch; return to bpbridge and try again", http.StatusBadRequest)
		return
	}
	if code := request.URL.Query().Get("error"); code != "" {
		description := request.URL.Query().Get("error_description")
		r.publish(callbackResult{err: &CallbackError{Code: code, Description: description}})
		writeCallbackPage(writer, "Spotify authorization was not completed. You can close this window.")
		return
	}
	code := request.URL.Query().Get("code")
	if code == "" {
		http.Error(writer, "authorization code is missing", http.StatusBadRequest)
		return
	}
	r.publish(callbackResult{code: code})
	writeCallbackPage(writer, "Spotify authorization complete. You can close this window and return to bpbridge.")
}

func (r *LoopbackReceiver) publish(result callbackResult) {
	select {
	case r.results <- result:
	default:
	}
}

func writeCallbackPage(writer http.ResponseWriter, message string) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(message))
}

func validateRedirectURI(raw string) error {
	_, err := parseLoopbackRedirect(raw)
	return err
}

func parseLoopbackRedirect(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("Spotify redirect URI is malformed")
	}
	if parsed.Scheme != "http" {
		return nil, errors.New("Spotify desktop redirect URI must use HTTP on a loopback IP")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Spotify redirect URI must not include user info, query parameters, or a fragment")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("Spotify redirect URI must use an explicit loopback IP such as 127.0.0.1 (not localhost)")
	}
	port := parsed.Port()
	if port == "" {
		return nil, errors.New("Spotify loopback redirect URI must include a port")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber > 65535 {
		return nil, errors.New("Spotify loopback redirect URI has an invalid port")
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	if !strings.HasPrefix(parsed.Path, "/") || strings.Contains(parsed.Path, "//") {
		return nil, errors.New("Spotify loopback redirect URI has an invalid path")
	}
	return parsed, nil
}
