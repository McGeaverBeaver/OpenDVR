// Package authtest is a minimal OpenID Connect provider for tests: discovery,
// JWKS, an authorization endpoint that approves at once, a token endpoint
// for the code and device grants, and a device authorization endpoint
// whose codes are approved by calling Approve.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// IDP is the fake provider.
type IDP struct {
	*httptest.Server
	ClientID string
	key      *rsa.PrivateKey

	mu      sync.Mutex
	codes   map[string]codeRec   // auth code -> nonce/user
	devices map[string]deviceRec // device_code -> state
	// User is what every sign-in produces.
	User struct {
		Sub, Name, Email string
		Groups           []string
	}
}

type codeRec struct {
	nonce     string
	challenge string
}

type deviceRec struct {
	userCode string
	approved bool
	denied   bool
	polls    int
}

// New starts the provider.
func New(clientID string) *IDP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	p := &IDP{ClientID: clientID, key: key, codes: map[string]codeRec{}, devices: map[string]deviceRec{}}
	p.User.Sub, p.User.Name, p.User.Email = "user-1", "Test User", "user@example.com"
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("/jwks", p.jwks)
	mux.HandleFunc("/userinfo", p.userinfo)
	mux.HandleFunc("/authorize", p.authorize)
	mux.HandleFunc("/token", p.token)
	mux.HandleFunc("/device", p.device)
	mux.HandleFunc("/device/verify", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body style="font-family:sans-serif"><h1>Fake IdP</h1><p>Code: %s</p><form method="post" action="/device/approve"><button>Approve</button></form></body></html>`, r.URL.Query().Get("code"))
	})
	mux.HandleFunc("/device/approve", func(w http.ResponseWriter, r *http.Request) {
		p.Approve(true)
		fmt.Fprint(w, "approved; go back to the TV")
	})
	p.Server = httptest.NewServer(mux)
	return p
}

func (p *IDP) discovery(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                p.URL,
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
		"jwks_uri":                              p.URL + "/jwks",
		"userinfo_endpoint":                     p.URL + "/userinfo",
		"device_authorization_endpoint":         p.URL + "/device",
		"end_session_endpoint":                  p.URL + "/end-session",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *IDP) jwks(w http.ResponseWriter, r *http.Request) {
	pub := p.key.PublicKey
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// authorize approves immediately and redirects back with a code.
func (p *IDP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" {
		http.Error(w, "bad client", 400)
		return
	}
	code := fmt.Sprintf("code-%d", time.Now().UnixNano())
	p.mu.Lock()
	p.codes[code] = codeRec{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	p.mu.Unlock()
	u, _ := url.Parse(q.Get("redirect_uri"))
	rq := u.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// AccessToken mints a JWT access token for a client id, as the provider
// would for a third-party OAuth client such as the Claude connector.
func (p *IDP) AccessToken(clientID string) string {
	claims := jwt.MapClaims{
		"iss": p.URL, "aud": clientID, "azp": clientID, "sub": p.User.Sub, "name": p.User.Name,
		"jti": fmt.Sprintf("%d", time.Now().UnixNano()),
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	t.Header["kid"] = "test"
	s, err := t.SignedString(p.key)
	if err != nil {
		panic(err)
	}
	return s
}

func (p *IDP) idToken(nonce string) string {
	claims := jwt.MapClaims{
		"iss": p.URL, "aud": p.ClientID, "sub": p.User.Sub, "name": p.User.Name, "email": p.User.Email, "groups": p.User.Groups,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	t.Header["kid"] = "test"
	s, err := t.SignedString(p.key)
	if err != nil {
		panic(err)
	}
	return s
}

func (p *IDP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	w.Header().Set("Content-Type", "application/json")
	fail := func(code, desc string) {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
	}
	clientID, secret := r.Form.Get("client_id"), r.Form.Get("client_secret")
	if u, pw, ok := r.BasicAuth(); ok {
		clientID, secret = u, pw
	}
	if clientID != p.ClientID || secret != "secret" {
		fail("invalid_client", "bad credentials")
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		p.mu.Lock()
		rec, ok := p.codes[r.Form.Get("code")]
		delete(p.codes, r.Form.Get("code"))
		p.mu.Unlock()
		if !ok {
			fail("invalid_grant", "unknown code")
			return
		}
		if rec.challenge != "" && r.Form.Get("code_verifier") == "" {
			fail("invalid_grant", "PKCE verifier missing")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": p.idToken(rec.nonce)})
	case "urn:ietf:params:oauth:grant-type:device_code":
		p.mu.Lock()
		rec, ok := p.devices[r.Form.Get("device_code")]
		if ok {
			rec.polls++
			p.devices[r.Form.Get("device_code")] = rec
		}
		p.mu.Unlock()
		switch {
		case !ok:
			fail("expired_token", "unknown device code")
		case rec.denied:
			fail("access_denied", "user refused")
		case !rec.approved:
			fail("authorization_pending", "waiting for the user")
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": p.idToken("")})
		}
	default:
		fail("unsupported_grant_type", r.Form.Get("grant_type"))
	}
}

func (p *IDP) device(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if r.Form.Get("client_id") != p.ClientID {
		http.Error(w, `{"error":"invalid_client"}`, 400)
		return
	}
	dc := fmt.Sprintf("dev-%d", time.Now().UnixNano())
	uc := "ABCD-EFGH"
	p.mu.Lock()
	p.devices[dc] = deviceRec{userCode: uc}
	p.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"device_code": dc, "user_code": uc, "verification_uri": p.URL + "/device/verify",
		"verification_uri_complete": p.URL + "/device/verify?code=" + uc, "expires_in": 600, "interval": 1,
	})
}

// Approve marks every pending device code as approved (or denied).
func (p *IDP) Approve(approved bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, rec := range p.devices {
		if approved {
			rec.approved = true
		} else {
			rec.denied = true
		}
		p.devices[k] = rec
	}
}

// DevicePolls reports how many token polls the provider has seen.
func (p *IDP) DevicePolls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, rec := range p.devices {
		n += rec.polls
	}
	return n
}

// userinfo answers any bearer token the token endpoint handed out (they
// are all "at" or a signed JWT here) with the user's claims.
func (p *IDP) userinfo(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		http.Error(w, "no token", 401)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sub": p.User.Sub, "name": p.User.Name, "email": p.User.Email, "groups": p.User.Groups,
	})
}
