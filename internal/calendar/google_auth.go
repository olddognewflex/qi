package calendar

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
	googleoauth "golang.org/x/oauth2/google"
)

func GoogleOAuthConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     googleoauth.Endpoint,
		Scopes:       []string{GoogleCalendarScope, "https://www.googleapis.com/auth/userinfo.email"},
	}
}

type AuthResult struct {
	Token   *oauth2.Token
	Account string
}

func RunOAuthFlow(ctx context.Context, clientID, clientSecret string, openBrowser bool) (*AuthResult, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind loopback: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	oauthCfg := GoogleOAuthConfig(clientID, clientSecret, redirectURL)
	state, err := randomState()
	if err != nil {
		return nil, err
	}

	authURL := oauthCfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)

	type callbackResult struct {
		code string
		err  error
	}
	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errStr := q.Get("error"); errStr != "" {
			fmt.Fprintf(w, "OAuth error: %s. You may close this tab.", errStr)
			resultCh <- callbackResult{err: fmt.Errorf("oauth error: %s", errStr)}
			return
		}
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resultCh <- callbackResult{err: fmt.Errorf("state mismatch")}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resultCh <- callbackResult{err: fmt.Errorf("missing code")}
			return
		}
		fmt.Fprintln(w, "Authorization received. You may close this tab and return to the terminal.")
		resultCh <- callbackResult{code: code}
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Shutdown(context.Background())

	fmt.Println("Open this URL in your browser to authorize qi:")
	fmt.Println()
	fmt.Println("  " + authURL)
	fmt.Println()

	if openBrowser {
		_ = openInBrowser(authURL)
	}

	var code string
	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		code = res.code
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("oauth flow timed out after 5 minutes")
	}

	tok, err := oauthCfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}

	email, err := fetchAccountEmail(ctx, oauthCfg, tok)
	if err != nil {
		return nil, fmt.Errorf("fetch account email: %w", err)
	}

	return &AuthResult{Token: tok, Account: email}, nil
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func openInBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

func fetchAccountEmail(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (string, error) {
	client := cfg.Client(ctx, tok)
	req, err := http.NewRequestWithContext(ctx, "GET", "https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("userinfo status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	email := extractJSONField(string(body), "email")
	if email == "" {
		return "", fmt.Errorf("email not present in userinfo response")
	}
	return email, nil
}

func extractJSONField(body, field string) string {
	needle := fmt.Sprintf("%q:", field)
	idx := strings.Index(body, needle)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(needle):]
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	v, err := url.QueryUnescape(rest[:end])
	if err != nil {
		return rest[:end]
	}
	return v
}
