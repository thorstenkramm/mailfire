package gmail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/thorstenkramm/mailfire/internal/filelock"
	"github.com/thorstenkramm/mailfire/internal/fsutil"
	"github.com/thorstenkramm/mailfire/internal/message"
)

// Scopes requested at login. gmail.send is the only Gmail scope: it allows
// sending but not reading or modifying the mailbox. openid adds a stable,
// opaque account identifier ("sub") to the token response so a deferred
// message is never resent from a different Google account after a new
// login (task 30). It grants no mailbox access and no profile data.
const (
	ScopeSend   = "https://www.googleapis.com/auth/gmail.send"
	ScopeOpenID = "openid"
)

const (
	tokenURL = "https://oauth2.googleapis.com/token"
	authURL  = "https://accounts.google.com/o/oauth2/v2/auth"
)

// refreshMargin is how long before expiry an access token is refreshed.
const refreshMargin = 5 * time.Minute

// BuiltinClientID is Mailfire's registered Google OAuth client (type
// Desktop app, project "mailfire"). Installed-app client metadata is not
// confidential; refresh tokens are the credentials that must be guarded.
const BuiltinClientID = "607295572237-5taqu4r757e7ilgt0u60kc7hctdgv28e.apps.googleusercontent.com"

// builtinClientSecret is the secret of BuiltinClientID. Google does not
// treat Desktop client secrets as confidential either, but it is kept out
// of the repository: release builds set it with
// -ldflags "-X github.com/thorstenkramm/mailfire/internal/gmail.builtinClientSecret=..."
// (see the Makefile and .goreleaser.yaml).
var builtinClientSecret string

// ErrClientNotConfigured reports that this build has no usable OAuth
// client. It is a configuration problem, not an authorization failure.
var ErrClientNotConfigured = errors.New("gmail: sign-in is not available in this build: it was built without Mailfire's Google client secret (set MAILFIRE_GMAIL_CLIENT_SECRET, or use a release build)")

// Credentials is the persisted authorization. AccessToken and RefreshToken
// are secrets; Account (Google's opaque subject ID) is not.
type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	Account      string    `json:"account,omitempty"`
}

// OAuthClient talks to Google's OAuth endpoints.
type OAuthClient struct {
	ClientID, ClientSecret string
	HTTP                   *http.Client
	TokenURL               string
	AuthURL                string
}

// DefaultOAuthClient returns the built-in client (or the developer
// override from MAILFIRE_GMAIL_CLIENT_ID / MAILFIRE_GMAIL_CLIENT_SECRET)
// with the given request timeout (non-positive: DefaultTimeout). The
// built-in secret is only ever paired with the built-in client ID.
func DefaultOAuthClient(timeout time.Duration) OAuthClient {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	id := env("MAILFIRE_GMAIL_CLIENT_ID", BuiltinClientID)
	secret := strings.TrimSpace(os.Getenv("MAILFIRE_GMAIL_CLIENT_SECRET"))
	if secret == "" && id == BuiltinClientID {
		secret = builtinClientSecret
	}
	return OAuthClient{
		ClientID:     id,
		ClientSecret: secret,
		HTTP:         &http.Client{Timeout: timeout},
		TokenURL:     tokenURL,
		AuthURL:      authURL,
	}
}

func env(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}

// DefaultCredentialPath is the per-user credential file:
// %APPDATA%\mailfire\gmail-credentials.json on Windows and
// $HOME/.config/mailfire/gmail-credentials.json elsewhere (next to the
// user configuration file). It returns "" when neither directory is
// known; a relative fallback would make unattended runs depend on their
// working directory.
func DefaultCredentialPath() string {
	return defaultCredentialPath(runtime.GOOS, os.Getenv)
}

func defaultCredentialPath(goos string, getenv func(string) string) string {
	const name = "gmail-credentials.json"
	if goos == "windows" {
		if d := getenv("APPDATA"); d != "" {
			return filepath.Join(d, "mailfire", name)
		}
		return ""
	}
	if h := getenv("HOME"); h != "" {
		return filepath.Join(h, ".config", "mailfire", name)
	}
	return ""
}

// Store is a single-account, owner-only credential file. A sibling
// advisory lock serializes every write and refresh across processes; an
// atomic rename makes interrupted writes safe.
type Store struct {
	Path string
}

func (s *Store) lockPath() string { return s.Path + ".lock" }

func (s *Store) checkPath() error {
	if s.Path == "" {
		return errors.New("gmail: no credential file location (home directory unknown): set [gmail] credentials or MAILFIRE_GMAIL_CREDENTIALS")
	}
	return nil
}

// notLoggedIn is the authorization failure for missing or unusable
// credentials: the fix is a new browser login.
func notLoggedIn(detail string) error {
	return newError("read credentials", 0, message.KindAuth, detail+"; run 'mailfire auth login --provider gmail'")
}

// Read returns the stored credentials without refreshing them.
func (s *Store) Read() (Credentials, error) {
	if err := s.checkPath(); err != nil {
		return Credentials{}, err
	}
	return s.read()
}

func (s *Store) read() (Credentials, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, notLoggedIn("not logged in: no credentials at " + s.Path)
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("gmail: reading credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return Credentials{}, notLoggedIn("credentials file " + s.Path + " is unreadable")
	}
	if c.RefreshToken == "" {
		return Credentials{}, notLoggedIn("credentials contain no refresh token")
	}
	return c, nil
}

// lock takes the credential lock, creating the directory owner-only.
func (s *Store) lock() (func() error, error) {
	if err := s.checkPath(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return nil, fmt.Errorf("gmail: creating credential directory: %w", err)
	}
	unlock, err := filelock.Lock(s.lockPath())
	if err != nil {
		return nil, fmt.Errorf("gmail: credential lock: %w", err)
	}
	return unlock, nil
}

// Write replaces the stored credentials atomically, under the lock.
func (s *Store) Write(c Credentials) (err error) {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	return s.write(c)
}

func (s *Store) write(c Credentials) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// A unique temporary file (0600) in the target directory, synced and
	// renamed into place: a crash leaves either the old or the new file.
	f, err := os.CreateTemp(filepath.Dir(s.Path), filepath.Base(s.Path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("gmail: writing credentials: %w", err)
	}
	tmp := f.Name()
	if err := fsutil.WriteSyncClose(f, append(b, '\n')); err != nil {
		return errors.Join(fmt.Errorf("gmail: writing credentials: %w", err), removeTemp(tmp))
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		return errors.Join(fmt.Errorf("gmail: saving credentials: %w", err), removeTemp(tmp))
	}
	return nil
}

// removeTemp deletes a temporary credential file after a failed write.
func removeTemp(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gmail: removing %s: %w", path, err)
	}
	return nil
}

// Delete removes the local credentials (it does not revoke Google's grant).
func (s *Store) Delete() (err error) {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gmail: removing credentials: %w", err)
	}
	return nil
}

// AccessToken returns a usable token, refreshing it when it expires within
// refreshMargin or when force is set (auth refresh: a real refresh-token
// exchange even with a valid cached token). The file lock makes concurrent
// cron/send invocations safe: a waiting process re-reads the credentials
// another one just refreshed instead of refreshing again.
func (s *Store) AccessToken(ctx context.Context, o OAuthClient, force bool) (_ Credentials, err error) {
	unlock, err := s.lock()
	if err != nil {
		return Credentials{}, err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	c, err := s.read()
	if err != nil {
		return Credentials{}, err
	}
	if !force && c.AccessToken != "" && time.Until(c.Expiry) > refreshMargin {
		return c, nil
	}
	fresh, err := o.Refresh(ctx, c.RefreshToken)
	if err != nil {
		return Credentials{}, err
	}
	// Google usually omits the refresh token (and the ID token) from
	// refresh responses: keep the stored ones. A replacement is persisted.
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = c.RefreshToken
	}
	if fresh.Account == "" {
		fresh.Account = c.Account
	}
	if err := s.write(fresh); err != nil {
		return Credentials{}, err
	}
	return fresh, nil
}

// Refresh exchanges a refresh token for a new access token.
func (o OAuthClient) Refresh(ctx context.Context, refresh string) (Credentials, error) {
	return o.exchange(ctx, "refresh token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
}

// Exchange redeems an authorization code (PKCE) for credentials.
func (o OAuthClient) Exchange(ctx context.Context, code, redirect, verifier string) (Credentials, error) {
	return o.exchange(ctx, "authorize", url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirect}, "code_verifier": {verifier}})
}

// checkGranted rejects a token without the send permission. Google's
// consent screen lets the user grant each scope separately, so a sign-in
// can succeed with only "openid"; such a token could never send. Google
// lists the granted scopes in every token response (an absent list is
// accepted, as it says nothing).
func checkGranted(op, scope string) error {
	if scope == "" || slices.Contains(strings.Fields(scope), ScopeSend) {
		return nil
	}
	return newError(op, 0, message.KindAuth, "Google did not grant permission to send email (granted: "+scope+
		"); run 'mailfire auth login --provider gmail' again and allow \"Send email on your behalf\"")
}

// Configured reports ErrClientNotConfigured when the client cannot sign
// in: no client ID, or the built-in client without its secret (a build
// without the release ldflags), which Google rejects.
func (o OAuthClient) Configured() error {
	if o.ClientID == "" || (o.ClientID == BuiltinClientID && o.ClientSecret == "") {
		return ErrClientNotConfigured
	}
	return nil
}

func (o OAuthClient) exchange(ctx context.Context, op string, v url.Values) (Credentials, error) {
	if err := o.Configured(); err != nil {
		return Credentials{}, err
	}
	v.Set("client_id", o.ClientID)
	if o.ClientSecret != "" {
		v.Set("client_secret", o.ClientSecret)
	}
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	u := o.TokenURL
	if u == "" {
		u = tokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(v.Encode()))
	if err != nil {
		return Credentials{}, permanent(op, 0, err.Error())
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return Credentials{}, transportError(op, err, hc.Timeout)
	}
	defer closeBody(resp.Body)
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Credentials{}, transportError(op, err, hc.Timeout)
	}
	var r struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		IDToken          string `json:"id_token"`
		ExpiresIn        int64  `json:"expires_in"`
		Scope            string `json:"scope"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	// Error bodies may be empty or not JSON; only a success needs to parse.
	parseErr := json.Unmarshal(b, &r)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Token-endpoint bodies never echo credentials, but keep only the
		// structured fields rather than a raw body.
		msg := r.ErrorDescription
		if msg == "" {
			msg = r.Error
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		switch {
		case r.Error == "invalid_grant" || r.Error == "invalid_client" || r.Error == "unauthorized_client" ||
			resp.StatusCode == http.StatusUnauthorized:
			return Credentials{}, newError(op, resp.StatusCode, message.KindAuth,
				"authorization is no longer valid: "+msg+"; run 'mailfire auth login --provider gmail'")
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			return Credentials{}, temporary(op, resp.StatusCode, msg)
		default:
			return Credentials{}, permanent(op, resp.StatusCode, msg)
		}
	}
	if parseErr != nil || r.AccessToken == "" {
		return Credentials{}, temporary(op, resp.StatusCode, "invalid token response")
	}
	if err := checkGranted(op, r.Scope); err != nil {
		return Credentials{}, err
	}
	return Credentials{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(r.ExpiresIn) * time.Second),
		Account:      subject(r.IDToken),
	}, nil
}

// subject extracts the "sub" claim from an ID token. The token was
// received directly from Google's token endpoint over a verified TLS
// connection, so OpenID Connect permits using it without checking the
// signature (OIDC Core 3.1.3.7). Malformed tokens yield "".
func subject(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Sub
}

// PKCE returns a code verifier, its S256 challenge, and a random state.
func PKCE() (verifier, challenge, state string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	b = make([]byte, 24)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", err
	}
	return verifier, challenge, base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthorizationURL builds the consent URL for the installed-app loopback
// flow with offline access, PKCE, and state.
func AuthorizationURL(o OAuthClient, redirect, challenge, state string) string {
	q := url.Values{
		"client_id":             {o.ClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {ScopeOpenID + " " + ScopeSend},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	u := o.AuthURL
	if u == "" {
		u = authURL
	}
	return u + "?" + q.Encode()
}
