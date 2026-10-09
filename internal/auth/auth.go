// Package auth puts the server behind an OpenID Connect provider
// (Authentik). The backend is the OIDC client: it runs the authorization
// code flow with PKCE, holds the session in an HMAC-signed cookie, signs
// short-lived stream tokens for players that fetch media without cookies,
// and wraps the OAuth 2.0 device authorization grant (RFC 8628) so a TV
// can sign in by showing a QR code that a phone approves.
//
// With no issuer configured the package is inert: every request passes
// and the client is told auth is off.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/McGeaverBeaver/opendvr/internal/audit"
)

// Config is read from the environment by the caller.
type Config struct {
	// Issuer is the OIDC issuer URL. Empty disables auth entirely.
	Issuer       string
	ClientID     string
	ClientSecret string
	// PublicURL is how browsers reach this server (scheme and host), used
	// for the redirect URI. Empty derives it from each request.
	PublicURL string
	// SessionSecret signs cookies and stream tokens. Empty loads or
	// creates DATA_DIR/session.key.
	SessionSecret string
	SessionTTL    time.Duration
	// StreamTokenTTL bounds a signed playlist/segment URL.
	StreamTokenTTL time.Duration
	// MCPToken is a static bearer token the /mcp endpoint accepts, for
	// bridges that cannot do OAuth. Optional.
	MCPToken string
	// AllowedGroup, when set, is the group a user must be in to sign in at
	// all. MCPGroup is the group required to use /mcp. Groups come from
	// the provider's "groups" claim (Authentik sends it with the profile
	// scope) and, for access tokens, from the userinfo endpoint.
	AllowedGroup string
	MCPGroup     string
	// AdminGroup is the group that may see the audit log, viewer
	// addresses, server logs and manage connectors and streams. Empty
	// falls back to MCPGroup; with both empty every signed-in user is an
	// administrator (a LAN install).
	AdminGroup string
	DataDir    string
	// Audit receives sign-in and connector events. Optional.
	Audit audit.Recorder
	// HDHREmulate opens the HDHomeRun-compatible JSON files and lets the
	// exported streams in with a long-lived token.
	HDHREmulate bool
}

// User is who a session belongs to.
type User struct {
	Subject string   `json:"sub"`
	Name    string   `json:"name,omitempty"`
	Email   string   `json:"email,omitempty"`
	Groups  []string `json:"groups,omitempty"`
}

// ErrNotAllowed is returned when a signed-in user is not in a required group.
var ErrNotAllowed = errors.New("your account is not in a group allowed to use this server")

func (u User) inGroup(g string) bool {
	if g == "" {
		return true
	}
	for _, x := range u.Groups {
		if x == g {
			return true
		}
	}
	return false
}

// Service is the auth layer. A nil *Service means auth is disabled.
type Service struct {
	cfg    Config
	secret []byte
	log    *slog.Logger

	// The provider is discovered lazily so the server still starts when
	// Authentik is down; requests that need it get a clear 503.
	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	endpoint oauth2.Endpoint
	extra    providerExtra
	initErr  error
	initAt   time.Time

	devices sync.Map // handle -> *deviceLogin

	// OAuth authorization server for MCP clients (mcpoauth.go).
	mcp  *mcpStore
	flow mcpFlow

	// lastRefused is the subject of the most recent refused sign-in, for
	// the audit entry written by the callback.
	lastRefused string
}

type providerExtra struct {
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	EndSessionEndpoint          string `json:"end_session_endpoint"`
}

// New builds the service. It returns (nil, nil) when no issuer is set.
func New(cfg Config, log *slog.Logger) (*Service, error) {
	if cfg.Issuer == "" {
		return nil, nil
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("AUTH_CLIENT_ID and AUTH_CLIENT_SECRET are required when AUTH_ISSUER is set")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	if cfg.StreamTokenTTL <= 0 {
		cfg.StreamTokenTTL = 12 * time.Hour
	}
	secret, err := loadSecret(cfg)
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, secret: secret, log: log, mcp: newMCPStore(cfg.DataDir), flow: mcpFlow{pending: map[string]*pendingAuthz{}, codes: map[string]*authCode{}}}, nil
}

func loadSecret(cfg Config) ([]byte, error) {
	if cfg.SessionSecret != "" {
		return []byte(cfg.SessionSecret), nil
	}
	if cfg.DataDir == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		return b, nil
	}
	path := filepath.Join(cfg.DataDir, "session.key")
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(b)
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write session key: %w", err)
	}
	return []byte(key), nil
}

// Enabled reports whether auth is on. Safe on a nil receiver.
func (s *Service) Enabled() bool { return s != nil }

// ready discovers the provider on first use and retries at most every
// thirty seconds after a failure.
func (s *Service) ready(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil {
		return nil
	}
	if s.initErr != nil && time.Since(s.initAt) < 30*time.Second {
		return s.initErr
	}
	s.initAt = time.Now()
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(dctx, s.cfg.Issuer)
	if err != nil {
		s.initErr = fmt.Errorf("OIDC discovery failed for %s: %w", s.cfg.Issuer, err)
		s.log.Warn("auth provider unavailable", "err", err)
		return s.initErr
	}
	var extra providerExtra
	_ = p.Claims(&extra)
	s.provider = p
	s.extra = extra
	s.endpoint = p.Endpoint()
	s.verifier = p.Verifier(&oidc.Config{ClientID: s.cfg.ClientID})
	s.initErr = nil
	s.log.Info("auth provider ready", "issuer", s.cfg.Issuer, "deviceFlow", extra.DeviceAuthorizationEndpoint != "")
	return nil
}

func (s *Service) oauth(r *http.Request) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     s.cfg.ClientID,
		ClientSecret: s.cfg.ClientSecret,
		Endpoint:     s.endpoint,
		RedirectURL:  s.publicURL(r) + "/auth/callback",
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

func (s *Service) publicURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

func isSecure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// ---- signing -------------------------------------------------------------

func (s *Service) sign(payload []byte) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) open(token string) ([]byte, bool) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, false
	}
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	if subtle.ConstantTimeCompare(mac.Sum(nil), want) != 1 {
		return nil, false
	}
	return payload, true
}

// ---- sessions ------------------------------------------------------------

const sessionCookie = "opendvr_session"

type sessionClaims struct {
	User
	Exp int64 `json:"exp"`
	Iat int64 `json:"iat"`
}

func (s *Service) setSession(w http.ResponseWriter, r *http.Request, u User) {
	now := time.Now()
	payload, _ := json.Marshal(sessionClaims{User: u, Exp: now.Add(s.cfg.SessionTTL).Unix(), Iat: now.Unix()})
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.sign(payload), Path: "/", HttpOnly: true,
		Secure: isSecure(r), SameSite: http.SameSiteLaxMode, Expires: now.Add(s.cfg.SessionTTL),
	})
}

func clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: isSecure(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// UserFromRequest returns the signed-in user, if any.
func (s *Service) UserFromRequest(r *http.Request) (User, bool) {
	if s == nil {
		return User{}, false
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return User{}, false
	}
	payload, ok := s.open(c.Value)
	if !ok {
		return User{}, false
	}
	var cl sessionClaims
	if json.Unmarshal(payload, &cl) != nil || cl.Exp < time.Now().Unix() || cl.Subject == "" {
		return User{}, false
	}
	return cl.User, true
}

// ---- stream tokens -------------------------------------------------------

type streamClaims struct {
	Sub     string `json:"sub"`
	Channel string `json:"ch"`
	Exp     int64  `json:"exp"`
}

// StreamToken signs a token that lets a cookie-less player fetch one
// channel's playlist and segments.
func (s *Service) StreamToken(u User, channel string) string {
	payload, _ := json.Marshal(streamClaims{Sub: u.Subject, Channel: channel, Exp: time.Now().Add(s.cfg.StreamTokenTTL).Unix()})
	return s.sign(payload)
}

// ExportToken is the long-lived token that lets Plex or Jellyfin open
// the exported tuner streams; it is bound to "hdhr", not a channel.
func (s *Service) ExportToken() string {
	if s == nil {
		return ""
	}
	payload, _ := json.Marshal(streamClaims{Sub: "hdhr", Channel: "hdhr", Exp: time.Now().Add(10 * 365 * 24 * time.Hour).Unix()})
	return s.sign(payload)
}

// CheckStreamToken validates a token for a channel.
func (s *Service) CheckStreamToken(token, channel string) bool {
	payload, ok := s.open(token)
	if !ok {
		return false
	}
	var cl streamClaims
	if json.Unmarshal(payload, &cl) != nil || cl.Exp < time.Now().Unix() {
		return false
	}
	return cl.Channel == channel
}

// ---- HTTP handlers ---------------------------------------------------------

// Mount registers the /auth/* handlers.
func (s *Service) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", s.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /auth/logout", s.handleLogout)
	mux.HandleFunc("POST /auth/device/start", s.handleDeviceStart)
	mux.HandleFunc("POST /auth/device/poll", s.handleDevicePoll)
	mux.HandleFunc("GET /tv", s.handleTVForm)
	mux.HandleFunc("POST /tv", s.handleTVSubmit)
	s.mountMCPOAuth(mux)
}

const stateCookie = "opendvr_login"

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"next"`
	Exp      int64  `json:"exp"`
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// safeNext keeps redirects on this site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := s.ready(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// The provider must have exactly this redirect URI registered; log it
	// so a "mismatching redirect_uri" error can be fixed by copying.
	s.log.Info("starting sign-in", "redirectUri", s.oauth(r).RedirectURL)
	st := loginState{State: randomString(16), Nonce: randomString(16), Verifier: oauth2.GenerateVerifier(), Next: safeNext(r.URL.Query().Get("next")), Exp: time.Now().Add(10 * time.Minute).Unix()}
	payload, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: s.sign(payload), Path: "/auth", HttpOnly: true, Secure: isSecure(r), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	u := s.oauth(r).AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier))
	http.Redirect(w, r, u, http.StatusFound)
}

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	if err := s.ready(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	c, err := r.Cookie(stateCookie)
	if err != nil {
		http.Error(w, "login session missing or expired; start again", http.StatusBadRequest)
		return
	}
	payload, ok := s.open(c.Value)
	var st loginState
	if !ok || json.Unmarshal(payload, &st) != nil || st.Exp < time.Now().Unix() {
		http.Error(w, "login session invalid; start again", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/auth", MaxAge: -1})
	if r.URL.Query().Get("state") != st.State {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		http.Error(w, "sign-in refused: "+e+" "+r.URL.Query().Get("error_description"), http.StatusForbidden)
		return
	}
	tok, err := s.oauth(r).Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		s.log.Warn("token exchange failed", "err", err)
		s.record(r, audit.Event{Kind: "auth.failed", Detail: "token exchange failed: " + err.Error()})
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	u, err := s.userFromToken(r.Context(), tok, st.Nonce)
	if errors.Is(err, ErrNotAllowed) {
		s.record(r, audit.Event{Kind: "auth.refused", User: s.lastRefused, Detail: err.Error()})
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err != nil {
		s.record(r, audit.Event{Kind: "auth.failed", Detail: err.Error()})
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.setSession(w, r, u)
	s.log.Info("signed in", "user", u.Subject, "name", u.Name)
	s.record(r, audit.Event{Kind: "auth.login", User: u.Subject, Name: displayName(u), Detail: "signed in"})
	http.Redirect(w, r, st.Next, http.StatusFound)
}

// record writes an audit event with the request's address and agent.
func (s *Service) record(r *http.Request, e audit.Event) {
	if s == nil || s.cfg.Audit == nil {
		return
	}
	if e.IP == "" {
		e.IP = audit.ClientIP(r)
	}
	if e.Agent == "" {
		e.Agent = audit.Agent(r)
	}
	s.cfg.Audit.Record(e)
}

// isAdmin reports whether a user holds the administrator role.
func (s *Service) isAdmin(u User) bool {
	g := s.cfg.AdminGroup
	if g == "" {
		g = s.cfg.MCPGroup
	}
	return u.inGroup(g)
}

// IsAdmin reports whether the request's user is an administrator. With
// auth off everyone on the LAN is.
func (s *Service) IsAdmin(r *http.Request) bool {
	if s == nil {
		return true
	}
	u, ok := s.UserFromRequest(r)
	return ok && s.isAdmin(u)
}

// AdminGroup is the group that confers the administrator role, for display.
func (s *Service) AdminGroup() string {
	if s == nil {
		return ""
	}
	if s.cfg.AdminGroup != "" {
		return s.cfg.AdminGroup
	}
	return s.cfg.MCPGroup
}

func (s *Service) userFromToken(ctx context.Context, tok *oauth2.Token, nonce string) (User, error) {
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return User{}, errors.New("no id_token in token response")
	}
	idt, err := s.verifier.Verify(ctx, raw)
	if err != nil {
		return User{}, fmt.Errorf("id_token invalid: %w%s", err, signingHint(err))
	}
	if nonce != "" && idt.Nonce != nonce {
		return User{}, errors.New("id_token nonce mismatch")
	}
	var cl struct {
		Name              string   `json:"name"`
		PreferredUsername string   `json:"preferred_username"`
		Email             string   `json:"email"`
		Groups            []string `json:"groups"`
	}
	_ = idt.Claims(&cl)
	name := cl.Name
	if name == "" {
		name = cl.PreferredUsername
	}
	u := User{Subject: idt.Subject, Name: name, Email: cl.Email, Groups: cl.Groups}
	if !u.inGroup(s.cfg.AllowedGroup) {
		s.log.Warn("sign-in refused: not in allowed group", "user", u.Subject, "groups", u.Groups, "required", s.cfg.AllowedGroup)
		s.lastRefused = u.Subject
		return User{}, ErrNotAllowed
	}
	return u, nil
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.UserFromRequest(r); ok {
		s.record(r, audit.Event{Kind: "auth.logout", User: u.Subject, Name: displayName(u), Detail: "signed out"})
	}
	clearSession(w, r)
	s.mu.Lock()
	end := s.extra.EndSessionEndpoint
	s.mu.Unlock()
	if end != "" {
		q := url.Values{"post_logout_redirect_uri": {s.publicURL(r) + "/"}, "client_id": {s.cfg.ClientID}}
		http.Redirect(w, r, end+"?"+q.Encode(), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// postForm posts a form and decodes JSON from any status; OAuth error
// bodies are JSON too and the caller reads the error field.
func (s *Service) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// signingHint explains the most common provider misconfiguration: with no
// signing key selected, Authentik signs tokens with HS256 using the client
// secret, which cannot be verified against its published keys.
func signingHint(err error) string {
	if err != nil && strings.Contains(err.Error(), "HS256") {
		return " (the provider is signing tokens with HS256: in Authentik open the OAuth2 provider and select a Signing Key, for example the authentik Self-signed Certificate, so tokens are RS256)"
	}
	return ""
}

// PublicURL is the origin browsers reach this server on. Safe on nil.
func (s *Service) PublicURL(r *http.Request) string {
	if s == nil {
		scheme := "http"
		if isSecure(r) {
			scheme = "https"
		}
		return scheme + "://" + r.Host
	}
	return s.publicURL(r)
}

// MCPGroup is the group required to connect assistants. Safe on nil.
func (s *Service) MCPGroup() string {
	if s == nil {
		return ""
	}
	return s.cfg.MCPGroup
}
