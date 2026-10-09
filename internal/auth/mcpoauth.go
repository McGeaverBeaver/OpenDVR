package auth

// OAuth 2.1 authorization server for MCP clients.
//
// A remote MCP client such as a claude.ai custom connector discovers this
// server's authorization metadata, registers itself (RFC 7591), sends the
// user through an authorization-code + PKCE flow and exchanges the code
// for a bearer token. The server has no password login of its own: the
// authorization endpoint reuses the normal OpenID sign-in (Authentik),
// picks the user up on the way back, shows a consent page and mints its
// own opaque tokens. Nothing about the connector is configured in the
// identity provider.
//
//	client → GET  /mcp/oauth/authorize?client_id&redirect_uri&code_challenge…
//	       ← 302 /auth/login?next=/mcp/oauth/authorize?authz=<id>   (or straight
//	                                                                 to consent)
//	user   → GET  /mcp/oauth/consent?authz=<id>&n=<nonce>
//	       → POST /mcp/oauth/consent   allow / deny
//	       ← 302 <redirect_uri>?code=…&state=…
//	client → POST /mcp/oauth/token     code + code_verifier
//	       ← { access_token (cct_…), refresh_token (ccr_…), … }
//
// Clients and tokens live in DATA_DIR/mcp_oauth.json (hashed secrets);
// pending authorizations and codes are in memory with short TTLs.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/McGeaverBeaver/opendvr/internal/audit"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	mcpPath            = "/mcp"
	accessTokenPrefix  = "cct_"
	refreshTokenPrefix = "ccr_"
	accessTokenTTL     = time.Hour
	refreshTokenTTL    = 30 * 24 * time.Hour
	pendingAuthzTTL    = 10 * time.Minute
	authCodeTTL        = 5 * time.Minute
	lastUsedThrottle   = 5 * time.Minute
	mcpScope           = "mcp"
)

// MCPClient is a registered OAuth client (one per connector install).
type MCPClient struct {
	ID           string     `json:"clientId"`
	SecretHash   string     `json:"secretHash,omitempty"`
	Name         string     `json:"name"`
	RedirectURIs []string   `json:"redirectUris"`
	AuthMethod   string     `json:"tokenEndpointAuthMethod"`
	ClientURI    string     `json:"clientUri,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastUsedAt   *time.Time `json:"lastUsedAt,omitempty"`
}

// MCPToken is an access or refresh token. Only the hash is stored.
type MCPToken struct {
	ID         string     `json:"id"`
	Hash       string     `json:"hash"`
	Kind       string     `json:"kind"` // access | refresh
	ClientID   string     `json:"clientId"`
	ParentID   string     `json:"parentId,omitempty"` // access -> its refresh token
	User       User       `json:"user"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

func (t *MCPToken) usable(now time.Time) bool {
	return t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

type mcpStore struct {
	mu      sync.Mutex
	file    string
	Clients map[string]*MCPClient `json:"clients"`
	Tokens  map[string]*MCPToken  `json:"tokens"` // by hash
	dirty   bool
}

func newMCPStore(dataDir string) *mcpStore {
	s := &mcpStore{Clients: map[string]*MCPClient{}, Tokens: map[string]*MCPToken{}}
	if dataDir != "" {
		s.file = filepath.Join(dataDir, "mcp_oauth.json")
		if data, err := os.ReadFile(s.file); err == nil {
			_ = json.Unmarshal(data, s)
			if s.Clients == nil {
				s.Clients = map[string]*MCPClient{}
			}
			if s.Tokens == nil {
				s.Tokens = map[string]*MCPToken{}
			}
		}
	}
	return s
}

// save writes the store. Caller holds s.mu.
func (s *mcpStore) save() {
	if s.file == "" {
		return
	}
	data, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return
	}
	tmp := s.file + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, s.file)
	}
}

// purge drops tokens that expired or were revoked more than a day ago.
// Caller holds s.mu.
func (s *mcpStore) purge(now time.Time) {
	for h, t := range s.Tokens {
		if now.After(t.ExpiresAt.Add(24*time.Hour)) || (t.RevokedAt != nil && now.After(t.RevokedAt.Add(24*time.Hour))) {
			delete(s.Tokens, h)
		}
	}
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func randomToken(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// ---- short-lived state -----------------------------------------------------

type pendingAuthz struct {
	created       time.Time
	clientID      string
	clientName    string
	redirectURI   string
	state         string
	codeChallenge string
	user          *User
	nonce         string
}

type authCode struct {
	created       time.Time
	clientID      string
	redirectURI   string
	codeChallenge string
	user          User
}

type mcpFlow struct {
	mu      sync.Mutex
	pending map[string]*pendingAuthz
	codes   map[string]*authCode // by hash
}

func (f *mcpFlow) sweep(now time.Time) {
	for k, p := range f.pending {
		if now.Sub(p.created) > pendingAuthzTTL {
			delete(f.pending, k)
		}
	}
	for k, c := range f.codes {
		if now.Sub(c.created) > authCodeTTL {
			delete(f.codes, k)
		}
	}
}

// ---- metadata ------------------------------------------------------------

func (s *Service) mcpMetadata(r *http.Request) map[string]any {
	base := s.publicURL(r)
	return map[string]any{
		"issuer":                                     base,
		"authorization_endpoint":                     base + "/mcp/oauth/authorize",
		"token_endpoint":                             base + "/mcp/oauth/token",
		"registration_endpoint":                      base + "/mcp/oauth/register",
		"revocation_endpoint":                        base + "/mcp/oauth/revoke",
		"response_types_supported":                   []string{"code"},
		"response_modes_supported":                   []string{"query"},
		"grant_types_supported":                      []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":           []string{"S256"},
		"token_endpoint_auth_methods_supported":      []string{"none", "client_secret_post", "client_secret_basic"},
		"revocation_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                           []string{mcpScope},
	}
}

func (s *Service) handleASMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mcpMetadata(r))
}

func (s *Service) handleResourceMetadata(w http.ResponseWriter, r *http.Request) {
	base := s.publicURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 base + mcpPath,
		"authorization_servers":    []string{base},
		"scopes_supported":         []string{mcpScope},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "opendvr",
	})
}

// challenge tells an MCP client how to obtain a token.
func (s *Service) challenge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="opendvr", resource_metadata="`+s.publicURL(r)+resourceMetadataPath+mcpPath+`"`)
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in through the connector's OAuth flow, or use the MCP_TOKEN bearer"})
}

const resourceMetadataPath = "/.well-known/oauth-protected-resource"

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func redirectWith(w http.ResponseWriter, r *http.Request, to string, params url.Values) {
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	http.Redirect(w, r, to+sep+params.Encode(), http.StatusFound)
}

// ---- dynamic client registration (RFC 7591) ---------------------------------

func validRedirectURI(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return "redirect_uri is empty or too long"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "redirect_uri has no scheme"
	}
	if u.Fragment != "" {
		return "redirect_uri must not contain a fragment"
	}
	if u.Scheme == "http" {
		switch strings.ToLower(u.Hostname()) {
		case "localhost", "127.0.0.1", "::1":
		default:
			return "http:// redirect URIs are only allowed for localhost"
		}
	}
	return ""
}

func (s *Service) handleRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		ClientURI    string   `json:"client_uri"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
		GrantTypes   []string `json:"grant_types"`
		ResponseType []string `json:"response_types"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		oauthError(w, 400, "invalid_client_metadata", "malformed registration request: "+err.Error())
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 20 {
		oauthError(w, 400, "invalid_redirect_uri", "between 1 and 20 redirect_uris are required")
		return
	}
	for _, u := range in.RedirectURIs {
		if msg := validRedirectURI(u); msg != "" {
			oauthError(w, 400, "invalid_redirect_uri", msg)
			return
		}
	}
	method := strings.ToLower(strings.TrimSpace(in.AuthMethod))
	if method == "" {
		method = "none"
	}
	switch method {
	case "none", "client_secret_post", "client_secret_basic":
	default:
		oauthError(w, 400, "invalid_client_metadata", "unsupported token_endpoint_auth_method "+method)
		return
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, 400, "invalid_client_metadata", "unsupported grant_type "+g)
			return
		}
	}
	for _, rt := range in.ResponseType {
		if rt != "code" {
			oauthError(w, 400, "invalid_client_metadata", "only response_type=code is supported")
			return
		}
	}
	name := strings.TrimSpace(in.ClientName)
	if name == "" {
		name = "MCP client"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	c := &MCPClient{
		ID: "ccm_" + randomString(18), Name: name, AuthMethod: method, CreatedAt: time.Now(),
		ClientURI: strings.TrimSpace(in.ClientURI),
	}
	for _, u := range in.RedirectURIs {
		c.RedirectURIs = append(c.RedirectURIs, strings.TrimSpace(u))
	}
	var secret string
	if method != "none" {
		secret = randomString(32)
		c.SecretHash = hashToken(secret)
	}
	s.mcp.mu.Lock()
	s.mcp.Clients[c.ID] = c
	s.mcp.save()
	s.mcp.mu.Unlock()
	s.log.Info("mcp client registered", "clientId", c.ID, "name", c.Name, "redirects", c.RedirectURIs)
	s.record(r, audit.Event{Kind: "mcp.registered", Detail: "assistant registered: " + c.Name, Fields: map[string]string{"clientId": c.ID}})

	out := map[string]any{
		"client_id": c.ID, "client_id_issued_at": c.CreatedAt.Unix(), "client_name": c.Name,
		"redirect_uris": c.RedirectURIs, "grant_types": []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"}, "token_endpoint_auth_method": method, "scope": mcpScope,
	}
	if c.ClientURI != "" {
		out["client_uri"] = c.ClientURI
	}
	if secret != "" {
		out["client_secret"] = secret
		out["client_secret_expires_at"] = 0
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, out)
}

// ---- authorization endpoint -------------------------------------------------

func (s *Service) client(id string) *MCPClient {
	s.mcp.mu.Lock()
	defer s.mcp.mu.Unlock()
	return s.mcp.Clients[id]
}

// mcpAllowed reports whether a signed-in user may connect an MCP client.
func (s *Service) mcpAllowed(u User) bool {
	return u.inGroup(s.cfg.MCPGroup)
}

func (s *Service) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	s.flow.mu.Lock()
	s.flow.sweep(now)
	s.flow.mu.Unlock()
	q := r.URL.Query()

	// Resuming after the OpenID sign-in: bind the user and go to consent.
	if id := q.Get("authz"); id != "" {
		u, ok := s.UserFromRequest(r)
		if !ok {
			s.consentPage(w, 400, "<h2>Sign-in did not complete</h2><p class='muted'>Start the connection again from the application.</p>")
			return
		}
		s.bindAndConsent(w, r, id, u)
		return
	}

	c := s.client(q.Get("client_id"))
	if c == nil {
		oauthError(w, 400, "invalid_client", "unknown client_id; register the client first")
		return
	}
	redirectURI := q.Get("redirect_uri")
	registered := false
	for _, u := range c.RedirectURIs {
		if u == redirectURI {
			registered = true
		}
	}
	if !registered {
		// Never redirect to an unregistered URI.
		oauthError(w, 400, "invalid_request", "redirect_uri is missing or not registered for this client")
		return
	}
	state := q.Get("state")
	if q.Get("response_type") != "code" {
		redirectWith(w, r, redirectURI, url.Values{"error": {"unsupported_response_type"}, "state": {state}})
		return
	}
	challenge := q.Get("code_challenge")
	if challenge == "" || (q.Get("code_challenge_method") != "" && q.Get("code_challenge_method") != "S256") {
		redirectWith(w, r, redirectURI, url.Values{"error": {"invalid_request"}, "error_description": {"PKCE with S256 is required"}, "state": {state}})
		return
	}
	id := randomString(24)
	s.flow.mu.Lock()
	s.flow.pending[id] = &pendingAuthz{created: now, clientID: c.ID, clientName: c.Name, redirectURI: redirectURI, state: state, codeChallenge: challenge}
	s.flow.mu.Unlock()

	if u, ok := s.UserFromRequest(r); ok {
		s.bindAndConsent(w, r, id, u)
		return
	}
	next := "/mcp/oauth/authorize?authz=" + url.QueryEscape(id)
	http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(next), http.StatusFound)
}

func (s *Service) bindAndConsent(w http.ResponseWriter, r *http.Request, id string, u User) {
	s.flow.mu.Lock()
	p := s.flow.pending[id]
	if p != nil {
		p.user = &u
		p.nonce = randomString(24)
	}
	s.flow.mu.Unlock()
	if p == nil {
		s.consentPage(w, 400, "<h2>This sign-in request has expired</h2><p class='muted'>Start the connection again from the application; the window is ten minutes.</p>")
		return
	}
	http.Redirect(w, r, "/mcp/oauth/consent?authz="+url.QueryEscape(id)+"&n="+url.QueryEscape(p.nonce), http.StatusFound)
}

// ---- consent page -----------------------------------------------------------

const consentCSS = `:root{color-scheme:dark}body{margin:0;background:#0f1115;color:#e6e9f0;font:14px/1.5 -apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif}.wrap{max-width:520px;margin:8vh auto;padding:0 16px}.card{background:#171a21;border:1px solid #262b36;border-radius:12px;padding:28px}h1{font-size:13px;letter-spacing:.12em;text-transform:uppercase;color:#8b93a7;margin:0 0 6px}h2{font-size:20px;margin:0 0 18px}.muted{color:#8b93a7;font-size:12px}.row{margin:14px 0;padding:12px 14px;background:#0f1115;border:1px solid #262b36;border-radius:8px}.actions{display:flex;gap:10px;margin-top:20px}button{flex:1;padding:10px 14px;border-radius:8px;border:1px solid #2c323f;background:#1f232c;color:#e6e9f0;font-weight:600;cursor:pointer}button.primary{background:#5eead4;border-color:#5eead4;color:#0f1115}code{background:#0f1115;padding:1px 5px;border-radius:4px;font-size:12px}.err{color:#f87171}`

func (s *Service) consentPage(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte("<!doctype html><html lang='en'><head><meta charset='utf-8'><meta name='viewport' content='width=device-width,initial-scale=1'><title>opendvr · Connector access</title><style>" + consentCSS + "</style></head><body><div class='wrap'><div class='card'><h1>opendvr</h1>" + body + "</div></div></body></html>"))
}

func (s *Service) loadConsent(id, nonce string) *pendingAuthz {
	s.flow.mu.Lock()
	defer s.flow.mu.Unlock()
	s.flow.sweep(time.Now())
	p := s.flow.pending[id]
	if p == nil || p.user == nil || nonce == "" || subtle.ConstantTimeCompare([]byte(p.nonce), []byte(nonce)) != 1 {
		return nil
	}
	return p
}

func (s *Service) handleConsentForm(w http.ResponseWriter, r *http.Request) {
	id, nonce := r.URL.Query().Get("authz"), r.URL.Query().Get("n")
	p := s.loadConsent(id, nonce)
	if p == nil {
		s.consentPage(w, 400, "<h2>This sign-in request has expired</h2><p class='muted'>Start the connection again from the application; the window is ten minutes.</p>")
		return
	}
	if !s.mcpAllowed(*p.user) {
		s.log.Warn("mcp consent refused: not in group", "user", p.user.Subject, "required", s.cfg.MCPGroup)
		s.record(r, audit.Event{Kind: "mcp.refused", User: p.user.Subject, Name: displayName(*p.user), Detail: "not in group " + s.cfg.MCPGroup})
		s.consentPage(w, 403, "<h2 class='err'>Not allowed</h2><p class='muted'>Your account is not in the group that may connect assistants to this server (<code>"+html.EscapeString(s.cfg.MCPGroup)+"</code>).</p>")
		return
	}
	host := p.redirectURI
	if u, err := url.Parse(p.redirectURI); err == nil && u.Host != "" {
		host = u.Host
	}
	who := p.user.Name
	if who == "" {
		who = p.user.Email
	}
	if who == "" {
		who = p.user.Subject
	}
	s.consentPage(w, 200, "<h2>Allow this application to use opendvr?</h2>"+
		"<div class='row'><b>"+html.EscapeString(p.clientName)+"</b><br><span class='muted'>redirects to <code>"+html.EscapeString(host)+"</code></span></div>"+
		"<div class='row'>Signed in as <b>"+html.EscapeString(who)+"</b></div>"+
		"<p class='muted'>The assistant will be able to see the lineup, guide, tuners and viewers, and start or stop channels, as you. Access can be revoked any time from the Status page.</p>"+
		"<form method='post' action='/mcp/oauth/consent'><input type='hidden' name='authz' value='"+html.EscapeString(id)+"'><input type='hidden' name='n' value='"+html.EscapeString(nonce)+"'>"+
		"<div class='actions'><button type='submit' name='decision' value='deny'>Deny</button><button type='submit' name='decision' value='allow' class='primary'>Allow</button></div></form>")
}

func (s *Service) handleConsentSubmit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, nonce := r.Form.Get("authz"), r.Form.Get("n")
	p := s.loadConsent(id, nonce)
	if p == nil {
		s.consentPage(w, 400, "<h2>This sign-in request has expired</h2>")
		return
	}
	s.flow.mu.Lock()
	delete(s.flow.pending, id) // single use, whatever the decision
	s.flow.mu.Unlock()
	params := url.Values{}
	if p.state != "" {
		params.Set("state", p.state)
	}
	if r.Form.Get("decision") != "allow" || !s.mcpAllowed(*p.user) {
		params.Set("error", "access_denied")
		params.Set("error_description", "the user declined the request")
		redirectWith(w, r, p.redirectURI, params)
		return
	}
	code := randomString(32)
	s.flow.mu.Lock()
	s.flow.codes[hashToken(code)] = &authCode{created: time.Now(), clientID: p.clientID, redirectURI: p.redirectURI, codeChallenge: p.codeChallenge, user: *p.user}
	s.flow.mu.Unlock()
	s.log.Info("mcp consent granted", "user", p.user.Subject, "client", p.clientName)
	s.record(r, audit.Event{Kind: "mcp.consent", User: p.user.Subject, Name: displayName(*p.user), Detail: "allowed assistant " + p.clientName})
	params.Set("code", code)
	redirectWith(w, r, p.redirectURI, params)
}

// ---- token endpoint -----------------------------------------------------------

func formOrJSON(r *http.Request) url.Values {
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var m map[string]any
		_ = json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(&m)
		v := url.Values{}
		for k, x := range m {
			if str, ok := x.(string); ok {
				v.Set(k, str)
			}
		}
		return v
	}
	_ = r.ParseForm()
	return r.Form
}

func (s *Service) authenticateClient(w http.ResponseWriter, r *http.Request, p url.Values) *MCPClient {
	id, secret := p.Get("client_id"), p.Get("client_secret")
	if u, pw, ok := r.BasicAuth(); ok {
		if id == "" {
			id = u
		}
		if secret == "" {
			secret = pw
		}
	}
	c := s.client(id)
	if c == nil {
		oauthError(w, 401, "invalid_client", "unknown client_id")
		return nil
	}
	if c.SecretHash != "" && subtle.ConstantTimeCompare([]byte(hashToken(secret)), []byte(c.SecretHash)) != 1 {
		oauthError(w, 401, "invalid_client", "client authentication failed")
		return nil
	}
	return c
}

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// mint issues a refresh token and an access token for it. Caller holds no lock.
func (s *Service) mint(c *MCPClient, u User) map[string]any {
	now := time.Now()
	refreshRaw := randomToken(refreshTokenPrefix)
	accessRaw := randomToken(accessTokenPrefix)
	refresh := &MCPToken{ID: randomString(9), Hash: hashToken(refreshRaw), Kind: "refresh", ClientID: c.ID, User: u, CreatedAt: now, ExpiresAt: now.Add(refreshTokenTTL)}
	access := &MCPToken{ID: randomString(9), Hash: hashToken(accessRaw), Kind: "access", ClientID: c.ID, ParentID: refresh.ID, User: u, CreatedAt: now, ExpiresAt: now.Add(accessTokenTTL)}
	s.mcp.mu.Lock()
	s.mcp.Tokens[refresh.Hash] = refresh
	s.mcp.Tokens[access.Hash] = access
	if cc := s.mcp.Clients[c.ID]; cc != nil {
		cc.LastUsedAt = &now
	}
	s.mcp.purge(now)
	s.mcp.save()
	s.mcp.mu.Unlock()
	return map[string]any{
		"access_token": accessRaw, "token_type": "Bearer", "expires_in": int(accessTokenTTL.Seconds()),
		"refresh_token": refreshRaw, "scope": mcpScope,
	}
}

// revokeLocked revokes a token and, for a refresh token, everything minted
// from it. Caller holds s.mcp.mu.
func (s *mcpStore) revokeLocked(t *MCPToken, now time.Time) int {
	n := 0
	if t.RevokedAt == nil {
		t.RevokedAt = &now
		n++
	}
	if t.Kind == "refresh" {
		for _, o := range s.Tokens {
			if o.ParentID == t.ID && o.RevokedAt == nil {
				o.RevokedAt = &now
				n++
			}
		}
	}
	return n
}

func (s *Service) handleToken(w http.ResponseWriter, r *http.Request) {
	p := formOrJSON(r)
	c := s.authenticateClient(w, r, p)
	if c == nil {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch p.Get("grant_type") {
	case "authorization_code":
		s.flow.mu.Lock()
		s.flow.sweep(time.Now())
		code := s.flow.codes[hashToken(p.Get("code"))]
		delete(s.flow.codes, hashToken(p.Get("code")))
		s.flow.mu.Unlock()
		if code == nil || p.Get("code") == "" {
			oauthError(w, 400, "invalid_grant", "authorization code is invalid, expired or already used")
			return
		}
		if code.clientID != c.ID {
			oauthError(w, 400, "invalid_grant", "authorization code was issued to a different client")
			return
		}
		if ru := p.Get("redirect_uri"); ru != "" && ru != code.redirectURI {
			oauthError(w, 400, "invalid_grant", "redirect_uri does not match the authorization request")
			return
		}
		if !pkceOK(p.Get("code_verifier"), code.codeChallenge) {
			oauthError(w, 400, "invalid_grant", "PKCE verification failed")
			return
		}
		writeJSON(w, http.StatusOK, s.mint(c, code.user))
	case "refresh_token":
		raw := p.Get("refresh_token")
		now := time.Now()
		s.mcp.mu.Lock()
		t := s.mcp.Tokens[hashToken(raw)]
		ok := raw != "" && t != nil && t.Kind == "refresh" && t.usable(now) && t.ClientID == c.ID
		var user User
		if ok {
			user = t.User
			// Rotate: the old refresh token and everything minted from it die now.
			s.mcp.revokeLocked(t, now)
			s.mcp.save()
		}
		s.mcp.mu.Unlock()
		if !ok {
			oauthError(w, 400, "invalid_grant", "refresh token is invalid, expired or revoked")
			return
		}
		writeJSON(w, http.StatusOK, s.mint(c, user))
	default:
		oauthError(w, 400, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

// handleRevoke is RFC 7009: always 200 so a caller learns nothing.
func (s *Service) handleRevoke(w http.ResponseWriter, r *http.Request) {
	p := formOrJSON(r)
	if raw := p.Get("token"); raw != "" {
		now := time.Now()
		s.mcp.mu.Lock()
		if t := s.mcp.Tokens[hashToken(raw)]; t != nil {
			if t.Kind == "access" && t.ParentID != "" {
				for _, o := range s.mcp.Tokens {
					if o.ID == t.ParentID {
						s.mcp.revokeLocked(o, now)
					}
				}
			}
			s.mcp.revokeLocked(t, now)
			s.mcp.save()
		}
		s.mcp.mu.Unlock()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{})
}

// ---- bearer validation on /mcp -----------------------------------------------

// MCPBearer resolves an OAuth access token to its user, recording use.
func (s *Service) MCPBearer(raw string) (User, bool) {
	if s == nil || !strings.HasPrefix(raw, accessTokenPrefix) {
		return User{}, false
	}
	now := time.Now()
	s.mcp.mu.Lock()
	defer s.mcp.mu.Unlock()
	t := s.mcp.Tokens[hashToken(raw)]
	if t == nil || t.Kind != "access" || !t.usable(now) {
		return User{}, false
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > lastUsedThrottle {
		t.LastUsedAt = &now
		if p := s.parentLocked(t); p != nil {
			p.LastUsedAt = &now
		}
		if c := s.mcp.Clients[t.ClientID]; c != nil {
			c.LastUsedAt = &now
		}
		s.mcp.save()
	}
	return t.User, true
}

func (s *Service) parentLocked(t *MCPToken) *MCPToken {
	for _, o := range s.mcp.Tokens {
		if o.ID == t.ParentID {
			return o
		}
	}
	return nil
}

// ---- operator view -------------------------------------------------------------

// MCPSession is one connector sign-in (a refresh token family).
type MCPSession struct {
	ID         string     `json:"id"`
	ClientID   string     `json:"clientId"`
	ClientName string     `json:"clientName"`
	User       User       `json:"user"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// MCPClientView is a client with its live sessions.
type MCPClientView struct {
	MCPClient
	Sessions []MCPSession `json:"sessions"`
}

// MCPClients lists registered clients and their active sessions.
func (s *Service) MCPClients() []MCPClientView {
	now := time.Now()
	s.mcp.mu.Lock()
	defer s.mcp.mu.Unlock()
	out := make([]MCPClientView, 0, len(s.mcp.Clients))
	for _, c := range s.mcp.Clients {
		v := MCPClientView{MCPClient: *c, Sessions: []MCPSession{}}
		v.SecretHash = ""
		for _, t := range s.mcp.Tokens {
			if t.ClientID == c.ID && t.Kind == "refresh" && t.usable(now) {
				v.Sessions = append(v.Sessions, MCPSession{ID: t.ID, ClientID: c.ID, ClientName: c.Name, User: t.User, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt})
			}
		}
		sort.Slice(v.Sessions, func(i, j int) bool { return v.Sessions[i].CreatedAt.After(v.Sessions[j].CreatedAt) })
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// RevokeMCPSession revokes one sign-in (its refresh token and access tokens).
func (s *Service) RevokeMCPSession(id string) bool {
	now := time.Now()
	s.mcp.mu.Lock()
	defer s.mcp.mu.Unlock()
	for _, t := range s.mcp.Tokens {
		if t.ID == id && t.Kind == "refresh" {
			s.mcp.revokeLocked(t, now)
			s.mcp.save()
			return true
		}
	}
	return false
}

// DeleteMCPClient forgets a client and revokes everything it holds.
func (s *Service) DeleteMCPClient(id string) bool {
	now := time.Now()
	s.mcp.mu.Lock()
	defer s.mcp.mu.Unlock()
	if _, ok := s.mcp.Clients[id]; !ok {
		return false
	}
	for _, t := range s.mcp.Tokens {
		if t.ClientID == id {
			s.mcp.revokeLocked(t, now)
		}
	}
	delete(s.mcp.Clients, id)
	s.mcp.save()
	return true
}

// IsMCPAdmin reports whether a request's user may manage connectors.
func (s *Service) IsMCPAdmin(r *http.Request) bool {
	if s == nil {
		return true
	}
	u, ok := s.UserFromRequest(r)
	return ok && s.isAdmin(u)
}

// mountMCPOAuth registers the authorization server routes.
func (s *Service) mountMCPOAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleASMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/mcp", s.handleASMetadata)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.handleASMetadata)
	mux.HandleFunc("GET "+resourceMetadataPath, s.handleResourceMetadata)
	mux.HandleFunc("GET "+resourceMetadataPath+mcpPath, s.handleResourceMetadata)
	mux.HandleFunc("POST /mcp/oauth/register", s.handleRegister)
	mux.HandleFunc("GET /mcp/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("GET /mcp/oauth/consent", s.handleConsentForm)
	mux.HandleFunc("POST /mcp/oauth/consent", s.handleConsentSubmit)
	mux.HandleFunc("POST /mcp/oauth/token", s.handleToken)
	mux.HandleFunc("POST /mcp/oauth/revoke", s.handleRevoke)
}
