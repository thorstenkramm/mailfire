package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/gmail"
	"github.com/thorstenkramm/mailfire/internal/message"
)

// auth.go implements "mailfire auth" (task 30): Gmail login, status,
// forced refresh, and logout. Only the [gmail] settings are loaded
// (config.LoadGmail), so authorization works before the rest of the
// configuration exists; a configuration file that exists must still be
// valid TOML.
//
// login is the only interactive command and the only one that may open a
// browser. refresh performs a real refresh-token exchange even when the
// cached access token is valid and never contacts the send endpoint, the
// mailbox, the deferred queue, or the deduplication state.
//
// Exit codes: 0 success; 1 usage; 2 configuration (unreadable file, no
// OAuth client in this build); 3 not authorized (not logged in, revoked or
// expired grant, consent denied); 4 transient token-endpoint failure or
// timeout (retry later); 5 other failures (e.g. writing the credentials).

// loginWait bounds how long login waits for the browser callback.
const loginWait = 10 * time.Minute

// callbackPath is the loopback redirect path registered for login.
const callbackPath = "/oauth2/callback"

type authOptions struct {
	sub       string
	provider  string
	config    string
	noBrowser bool
	manual    bool
	port      int
	timeout   time.Duration
	json      bool
}

// authResult is the JSON document of auth status, refresh, and logout.
type authResult struct {
	envelope
	Provider       string `json:"provider"`
	CredentialFile string `json:"credential_file,omitempty"`
	LoggedIn       *bool  `json:"logged_in,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
}

func parseAuthFlags(args []string) (authOptions, error) {
	var o authOptions
	if len(args) == 0 {
		return o, errors.New("missing subcommand: login, status, refresh, or logout")
	}
	o.sub = args[0]
	switch o.sub {
	case "login", "status", "refresh", "logout":
	default:
		return o, fmt.Errorf("unknown subcommand %q: use login, status, refresh, or logout", o.sub)
	}
	var configShort, port, timeout string
	fs := flag.NewFlagSet("auth "+o.sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.provider, "provider", "", "")
	fs.StringVar(&o.config, "config", "", "")
	fs.StringVar(&configShort, "c", "", "")
	fs.BoolVar(&o.noBrowser, "no-browser", false, "")
	fs.BoolVar(&o.manual, "manual", false, "")
	fs.StringVar(&port, "port", "", "")
	fs.StringVar(&timeout, "timeout", "", "")
	fs.BoolVar(&o.json, "json", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		o.json = scanJSON(fs, args[1:], false)
		return o, err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return o, fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
	}
	if o.provider != config.ProviderGmail {
		return o, errors.New("--provider gmail is required (Gmail is the only provider that needs a login)")
	}
	if configShort != "" {
		if o.config != "" && o.config != configShort {
			return o, errors.New("conflicting config paths: use either -c or --config, not both")
		}
		o.config = configShort
	}
	d, err := parseTimeoutOption(fs, timeout)
	if err != nil {
		return o, err
	}
	o.timeout = d
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return o, fmt.Errorf("invalid --port %q: must be a number between 1 and 65535", port)
		}
		o.port = n
	}
	if o.sub != "login" && (o.noBrowser || o.manual || port != "") {
		return o, errors.New("--no-browser, --manual, and --port apply only to auth login")
	}
	if o.manual && port == "" {
		return o, errors.New("--manual requires --port: the pasted callback URL must name a known loopback port")
	}
	if o.sub == "login" && o.json {
		return o, errors.New("--json is not available for auth login: login is interactive")
	}
	return o, nil
}

func runAuth(args []string, rc *runContext, stdout, stderr io.Writer) int {
	if len(args) == 0 || wantsHelp(args) {
		printOutput(stdout, authUsage)
		return ExitOK
	}
	o, err := parseAuthFlags(args)
	if err != nil {
		printfOutput(stderr, "mailfire: auth: %v\n\n", err)
		printOutput(stderr, authUsage)
		return jsonFail(stdout, o.json, "auth", errUsage, err, ExitUsage)
	}
	cmd := "auth " + o.sub
	cfg, err := config.LoadGmail(o.config, stderr, config.Overrides{Timeout: o.timeout > 0})
	if err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
		return jsonFail(stdout, o.json, cmd, errConfig, err, ExitConfig)
	}
	store := gmailStore(cfg)
	oauth := gmailOAuth(cfg, o.timeout, rc)
	res := &authResult{envelope: envelope{Command: cmd}, Provider: config.ProviderGmail, CredentialFile: store.Path}
	out := humanOut(stdout, o.json)
	var code int
	switch o.sub {
	case "status":
		code = authStatus(store, res, out, stderr)
	case "logout":
		code = authLogout(store, res, out, stderr)
	case "refresh":
		code = authRefresh(store, oauth, res, out, stderr)
	default:
		return gmailLogin(o, store, oauth, rc, stdout, stderr)
	}
	if o.json {
		return emitJSON(stdout, res, code)
	}
	return code
}

// authFailure maps an authorization error to an exit code and JSON code.
func authFailure(err error) (int, string) {
	switch {
	case message.IsAuth(err):
		return ExitAuth, errAuth
	case message.IsDeferrable(err):
		return ExitDeferred, errTemporary
	case message.IsPermanent(err):
		return ExitSendFail, errSendFailed
	default:
		// No OAuth client in this build, unknown or unreadable
		// credential location.
		return ExitConfig, errConfig
	}
}

func authStatus(store *gmail.Store, res *authResult, stdout, stderr io.Writer) int {
	c, err := store.Read()
	loggedIn := err == nil
	res.LoggedIn = &loggedIn
	if err != nil {
		code, jcode := authFailure(err)
		printfOutput(stdout, "gmail: not logged in\n")
		printfOutput(stderr, "mailfire: auth status: %v\n", err)
		res.setError(jcode, err)
		return code
	}
	res.ExpiresAt = rfc3339(c.Expiry)
	printfOutput(stdout, "gmail: logged in (%s); access token expires %s (renewed automatically)\n",
		store.Path, c.Expiry.UTC().Format(time.RFC3339))
	return ExitOK
}

func authLogout(store *gmail.Store, res *authResult, stdout, stderr io.Writer) int {
	if err := store.Delete(); err != nil {
		code, jcode := authFailure(err)
		if code == ExitConfig && store.Path != "" {
			code, jcode = ExitSendFail, errSendFailed
		}
		printfOutput(stderr, "mailfire: auth logout: %v\n", err)
		res.setError(jcode, err)
		return code
	}
	printfOutput(stdout, "gmail: local credentials removed (%s). Google's authorization was NOT revoked; "+
		"revoke it at https://myaccount.google.com/permissions\n", store.Path)
	return ExitOK
}

func authRefresh(store *gmail.Store, oauth gmail.OAuthClient, res *authResult, stdout, stderr io.Writer) int {
	c, err := store.AccessToken(context.Background(), oauth, true)
	if err != nil {
		code, jcode := authFailure(err)
		printfOutput(stderr, "mailfire: auth refresh: %v\n", err)
		res.setError(jcode, err)
		return code
	}
	res.ExpiresAt = rfc3339(c.Expiry)
	printfOutput(stdout, "gmail: authorization refreshed; access token expires %s\n", c.Expiry.UTC().Format(time.RFC3339))
	return ExitOK
}

// loginCallback is what Google's redirect delivered.
type loginCallback struct {
	code    string
	errCode string // e.g. "access_denied"
}

func gmailLogin(o authOptions, store *gmail.Store, oauth gmail.OAuthClient, rc *runContext, stdout, stderr io.Writer) int {
	verifier, challenge, state, err := gmail.PKCE()
	if err != nil {
		printfOutput(stderr, "mailfire: auth login: %v\n", err)
		return ExitSendFail
	}
	if oauth.ClientID == "" || oauth.ClientID == gmail.BuiltinClientID {
		printfOutput(stderr, "mailfire: auth login: %v\n", gmail.ErrClientNotConfigured)
		return ExitConfig
	}
	if o.manual {
		redirect := fmt.Sprintf("http://127.0.0.1:%d%s", o.port, callbackPath)
		printfOutput(stdout, "Open this URL in a browser and approve access:\n%s\n\n", gmail.AuthorizationURL(oauth, redirect, challenge, state))
		printlnOutput(stdout, "The browser then fails to load the callback page. Copy the complete URL from its address bar and paste it here:")
		cb, err := readManualCallback(rc.stdin, redirect, state)
		if err != nil {
			printfOutput(stderr, "mailfire: auth login: %v\n", err)
			return ExitAuth
		}
		return finishLogin(store, oauth, cb, redirect, verifier, stdout, stderr)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", o.port))
	if err != nil {
		printfOutput(stderr, "mailfire: auth login: listening for the OAuth callback: %v\n", err)
		return ExitSendFail
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		closeReported(ln, stderr)
		printfOutput(stderr, "mailfire: auth login: unexpected listener address %v\n", ln.Addr())
		return ExitSendFail
	}
	port := addr.Port
	redirect := fmt.Sprintf("http://127.0.0.1:%d%s", port, callbackPath)
	consent := gmail.AuthorizationURL(oauth, redirect, challenge, state)
	printfOutput(stdout, "Open this URL in a browser and approve access:\n%s\n", consent)
	if o.noBrowser {
		printfOutput(stdout, "\nOn a remote host, first forward the callback port from the computer with the browser:\n"+
			"  ssh -L %d:127.0.0.1:%d <user>@<this-host>\n", port, port)
	} else if err := rc.openBrowser(consent); err != nil {
		printfOutput(stderr, "mailfire: auth login: could not open a browser (%v); open the URL above manually\n", err)
	}
	printfOutput(stdout, "Waiting for authorization (up to %s)...\n", loginWait)

	result := make(chan loginCallback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// A request without the expected state is not our redirect (a
		// stale tab, a probe, or forgery): refuse it and keep waiting.
		if q.Get("state") != state {
			http.Error(w, "invalid or missing state", http.StatusBadRequest)
			return
		}
		cb := loginCallback{code: q.Get("code"), errCode: q.Get("error")}
		if cb.code == "" && cb.errCode == "" {
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			return
		}
		page := "Authorization received. Return to the terminal to see whether it completed."
		if cb.errCode != "" {
			page = "Mailfire was not authorized (" + cb.errCode + "). You may close this page."
		}
		// Answer the browser before handing over the result, so the page
		// is delivered even when login finishes at once. A browser that
		// went away cannot be told anything; the terminal reports the
		// outcome either way.
		_, werr := io.WriteString(w, page)
		if f, ok := w.(http.Flusher); ok && werr == nil {
			f.Flush()
		}
		select {
		case result <- cb:
		default: // a duplicate delivery; the first one counts
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	defer func() {
		// Graceful: lets a response in flight finish before closing.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			printfOutput(stderr, "mailfire: auth login: stopping the callback server: %v\n", err)
		}
	}()

	select {
	case cb := <-result:
		return finishLogin(store, oauth, cb, redirect, verifier, stdout, stderr)
	case err := <-served:
		printfOutput(stderr, "mailfire: auth login: the callback server stopped: %v\n", err)
		return ExitSendFail
	case <-time.After(loginWait):
		printfOutput(stderr, "mailfire: auth login: no authorization within %s; run login again\n", loginWait)
		return ExitAuth
	}
}

// readManualCallback reads the pasted callback URL and checks that it is
// the expected loopback redirect carrying our state.
func readManualCallback(input io.Reader, redirect, state string) (loginCallback, error) {
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return loginCallback{}, fmt.Errorf("reading the callback URL: %w", err)
	}
	want, err := url.Parse(redirect)
	if err != nil {
		return loginCallback{}, fmt.Errorf("invalid redirect %q: %w", redirect, err)
	}
	u, err := url.Parse(strings.TrimSpace(line))
	if err != nil || u.Scheme != want.Scheme || u.Host != want.Host || u.Path != want.Path {
		return loginCallback{}, errors.New("the pasted URL is not the expected callback " + redirect)
	}
	q := u.Query()
	if q.Get("state") != state {
		return loginCallback{}, errors.New("the pasted URL belongs to a different login attempt (state mismatch)")
	}
	cb := loginCallback{code: q.Get("code"), errCode: q.Get("error")}
	if cb.code == "" && cb.errCode == "" {
		return loginCallback{}, errors.New("the pasted URL contains no authorization code")
	}
	return cb, nil
}

// finishLogin redeems the code and saves the credentials.
func finishLogin(store *gmail.Store, oauth gmail.OAuthClient, cb loginCallback, redirect, verifier string, stdout, stderr io.Writer) int {
	if cb.errCode != "" {
		printfOutput(stderr, "mailfire: auth login: Google did not grant access (%s)\n", cb.errCode)
		return ExitAuth
	}
	c, err := oauth.Exchange(context.Background(), cb.code, redirect, verifier)
	if err != nil {
		code, _ := authFailure(err)
		if code == ExitSendFail {
			code = ExitAuth // a rejected code: authorization was not obtained
		}
		printfOutput(stderr, "mailfire: auth login: %v\n", err)
		return code
	}
	previous, perr := store.Read()
	if err := store.Write(c); err != nil {
		printfOutput(stderr, "mailfire: auth login: %v\n", err)
		return ExitSendFail
	}
	printfOutput(stdout, "gmail: authorization saved to %s\n", store.Path)
	if perr == nil && previous.Account != "" && c.Account != "" && previous.Account != c.Account {
		printfOutput(stdout, "note: this is a different Google account than before; messages deferred for the "+
			"previous account are not resent from this one\n")
	}
	return ExitOK
}

// openBrowser opens url in the desktop browser without waiting for it.
func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
