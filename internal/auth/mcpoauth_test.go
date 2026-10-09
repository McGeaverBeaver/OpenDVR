package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func postJSONClient(t *testing.T, c *http.Client, url string, body any) (*http.Response, string) {
	t.Helper()
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	res, err := c.Post(url, "application/json", &buf)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(b)
}

func mcpWith(t *testing.T, srv *httptest.Server, bearer string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(nil))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// mcpConnect runs the connector flow end to end: dynamic registration,
// authorize (through the OpenID sign-in), consent, and the code exchange.
func mcpConnect(t *testing.T, srv *httptest.Server, c *http.Client) (map[string]any, string) {
	t.Helper()
	reg, body := postJSONClient(t, c, srv.URL+"/mcp/oauth/register", map[string]any{
		"client_name": "Claude", "redirect_uris": []string{"https://claude.ai/api/mcp/auth_callback"},
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"},
	})
	if reg.StatusCode != 201 {
		t.Fatalf("register = %d %s", reg.StatusCode, body)
	}
	var client struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal([]byte(body), &client)

	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	// Follow the sign-in and consent redirects but stop at claude.ai so the
	// code can be read from the Location header.
	stopAtClaude := &http.Client{Jar: c.Jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Host == "claude.ai" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	authz := srv.URL + "/mcp/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {client.ClientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
		"state": {"xyz"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "scope": {"mcp"},
	}.Encode()
	res, err := stopAtClaude.Get(authz)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(page), "Allow this application") {
		t.Fatalf("consent page = %d at %s: %.200s", res.StatusCode, res.Request.URL, page)
	}
	q := res.Request.URL.Query()
	res, err = stopAtClaude.PostForm(srv.URL+"/mcp/oauth/consent", url.Values{"authz": {q.Get("authz")}, "n": {q.Get("n")}, "decision": {"allow"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != 302 || loc == nil || loc.Host != "claude.ai" || loc.Query().Get("state") != "xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("consent redirect = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	tokRes, err := http.PostForm(srv.URL+"/mcp/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "client_id": {client.ClientID},
		"redirect_uri": {"https://claude.ai/api/mcp/auth_callback"}, "code_verifier": {verifier},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(tokRes.Body).Decode(&tok)
	tokRes.Body.Close()
	if tokRes.StatusCode != 200 || tok["access_token"] == nil || tok["refresh_token"] == nil {
		t.Fatalf("token = %d %+v", tokRes.StatusCode, tok)
	}
	return tok, client.ClientID
}

func TestMCPOAuthFlow(t *testing.T) {
	_, s, srv := newStack(t)
	c := client(t)

	// Discovery: the challenge points at our own metadata, which names this
	// server as the authorization server with registration and PKCE.
	if code := mcpWith(t, srv, ""); code != 401 {
		t.Fatalf("mcp without token = %d", code)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(nil))
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if !strings.Contains(res.Header.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource/mcp") {
		t.Fatalf("challenge header = %q", res.Header.Get("WWW-Authenticate"))
	}
	if _, body := get(t, c, srv.URL+"/.well-known/oauth-protected-resource/mcp"); !strings.Contains(body, `"authorization_servers":["`+srv.URL+`"]`) {
		t.Fatalf("resource metadata = %s", body)
	}
	if _, body := get(t, c, srv.URL+"/.well-known/oauth-authorization-server"); !strings.Contains(body, "registration_endpoint") || !strings.Contains(body, `"S256"`) {
		t.Fatalf("AS metadata = %s", body)
	}

	tok, clientID := mcpConnect(t, srv, c)
	access := tok["access_token"].(string)
	refresh := tok["refresh_token"].(string)
	if mcpWith(t, srv, access) != 200 {
		t.Fatal("access token refused on /mcp")
	}
	if mcpWith(t, srv, refresh) != 401 {
		t.Fatal("refresh token must not work as a bearer")
	}
	if mcpWith(t, srv, access+"x") != 401 {
		t.Fatal("forged token accepted")
	}

	// The operator sees the client and its session with last activity.
	clients := s.MCPClients()
	if len(clients) != 1 || clients[0].Name != "Claude" || len(clients[0].Sessions) != 1 || clients[0].Sessions[0].User.Subject != "user-1" || clients[0].Sessions[0].LastUsedAt == nil {
		t.Fatalf("clients = %+v", clients)
	}

	// Refresh rotates: the old pair dies, the new access token works, and
	// the old refresh token cannot be used twice.
	tokRes, _ := http.PostForm(srv.URL+"/mcp/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	var tok2 map[string]any
	_ = json.NewDecoder(tokRes.Body).Decode(&tok2)
	tokRes.Body.Close()
	if tokRes.StatusCode != 200 || tok2["access_token"] == nil {
		t.Fatalf("refresh = %d %+v", tokRes.StatusCode, tok2)
	}
	if mcpWith(t, srv, access) != 401 {
		t.Fatal("old access token should die with its refresh token")
	}
	if mcpWith(t, srv, tok2["access_token"].(string)) != 200 {
		t.Fatal("new access token refused")
	}
	tokRes, _ = http.PostForm(srv.URL+"/mcp/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	tokRes.Body.Close()
	if tokRes.StatusCode != 400 {
		t.Fatalf("reused refresh token = %d, want 400", tokRes.StatusCode)
	}

	// Revoking the session from the admin side cuts the connector off;
	// removing the client forgets it entirely.
	if !s.RevokeMCPSession(s.MCPClients()[0].Sessions[0].ID) {
		t.Fatal("revoke session failed")
	}
	if mcpWith(t, srv, tok2["access_token"].(string)) != 401 {
		t.Fatal("revoked session still works")
	}
	if !s.DeleteMCPClient(clientID) || len(s.MCPClients()) != 0 {
		t.Fatal("delete client failed")
	}

	// Unknown clients and unregistered redirect URIs never get a redirect.
	res, body := get(t, c, srv.URL+"/mcp/oauth/authorize?response_type=code&client_id=nope&redirect_uri=https://evil.example/cb&code_challenge=x")
	if res.StatusCode != 400 || !strings.Contains(body, "invalid_client") {
		t.Fatalf("unknown client = %d %s", res.StatusCode, body)
	}
}

func TestMCPGroupGating(t *testing.T) {
	idp, s, srv := newStack(t)
	s.cfg.MCPGroup = "admin"

	// A plain viewer can sign in to the web app but is refused at consent
	// and cannot administer connectors.
	idp.User.Groups = []string{"tv"}
	c := client(t)
	if res, _ := get(t, c, srv.URL+"/auth/login?next=/"); res.StatusCode != 200 {
		t.Fatalf("sign-in = %d", res.StatusCode)
	}
	reg, body := postJSONClient(t, c, srv.URL+"/mcp/oauth/register", map[string]any{"client_name": "Claude", "redirect_uris": []string{"https://claude.ai/api/mcp/auth_callback"}})
	if reg.StatusCode != 201 {
		t.Fatalf("register = %d %s", reg.StatusCode, body)
	}
	var regd struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal([]byte(body), &regd)
	res, page := get(t, c, srv.URL+"/mcp/oauth/authorize?response_type=code&client_id="+regd.ClientID+"&redirect_uri=https://claude.ai/api/mcp/auth_callback&code_challenge="+strings.Repeat("a", 43))
	if res.StatusCode != 403 || !strings.Contains(page, "Not allowed") {
		t.Fatalf("outsider consent = %d %.200s", res.StatusCode, page)
	}
	if s.IsMCPAdmin(reqWithCookies(c, srv.URL)) {
		t.Fatal("tv member must not administer connectors")
	}

	// An admin gets through and can administer.
	idp.User.Groups = []string{"tv", "admin"}
	c2 := client(t)
	if _, _ = get(t, c2, srv.URL+"/auth/login?next=/"); !s.IsMCPAdmin(reqWithCookies(c2, srv.URL)) {
		t.Fatal("admin should administer connectors")
	}
	if tok, _ := mcpConnect(t, srv, c2); mcpWith(t, srv, tok["access_token"].(string)) != 200 {
		t.Fatal("admin token refused")
	}
}

func reqWithCookies(c *http.Client, base string) *http.Request {
	req, _ := http.NewRequest("GET", base+"/api/mcp/clients", nil)
	u, _ := url.Parse(base)
	for _, ck := range c.Jar.Cookies(u) {
		req.AddCookie(ck)
	}
	return req
}
