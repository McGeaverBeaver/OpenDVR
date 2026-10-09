package auth

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/McGeaverBeaver/opendvr/internal/auth/authtest"
)

func newStack(t *testing.T) (*authtest.IDP, *Service, *httptest.Server) {
	t.Helper()
	idp := authtest.New("cc")
	t.Cleanup(idp.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(Config{
		Issuer: idp.URL, ClientID: "cc", ClientSecret: "secret",
		SessionSecret: strings.Repeat("k", 32), MCPToken: "mcp-secret",
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	ok := func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }
	mux.HandleFunc("GET /api/channels", ok)
	mux.HandleFunc("GET /api/channels/{n}/stream/index.m3u8", ok)
	mux.HandleFunc("GET /api/health", ok)
	mux.HandleFunc("POST /mcp", ok)
	mux.HandleFunc("GET /", ok)
	srv := httptest.NewServer(s.Protect(mux))
	t.Cleanup(srv.Close)
	return idp, s, srv
}

func client(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func get(t *testing.T, c *http.Client, url string) (*http.Response, string) {
	t.Helper()
	res, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(b)
}

func TestDisabledIsNoop(t *testing.T) {
	s, err := New(Config{}, slog.Default())
	if err != nil || s != nil || s.Enabled() {
		t.Fatalf("no issuer should disable auth: %v %v", s, err)
	}
	h := s.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/channels", nil))
	if rr.Code != 204 {
		t.Fatalf("disabled auth must pass requests: %d", rr.Code)
	}
}

func TestLoginFlowAndProtection(t *testing.T) {
	_, s, srv := newStack(t)
	c := client(t)

	// Unauthenticated API calls are refused; the shell and health are not.
	if res, body := get(t, c, srv.URL+"/api/channels"); res.StatusCode != 401 || !strings.Contains(body, "unauthenticated") {
		t.Fatalf("api without session = %d %s", res.StatusCode, body)
	}
	if res, _ := get(t, c, srv.URL+"/"); res.StatusCode != 200 {
		t.Fatalf("shell should be open: %d", res.StatusCode)
	}
	if res, _ := get(t, c, srv.URL+"/api/health"); res.StatusCode != 200 {
		t.Fatalf("health should be open: %d", res.StatusCode)
	}

	// Login round-trips through the provider and lands on next.
	res, _ := get(t, c, srv.URL+"/auth/login?next=/watch/5.1")
	if res.StatusCode != 200 || res.Request.URL.Path != "/watch/5.1" {
		t.Fatalf("after login: %d at %s", res.StatusCode, res.Request.URL)
	}
	if res, _ := get(t, c, srv.URL+"/api/channels"); res.StatusCode != 200 {
		t.Fatalf("api with session = %d", res.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/channels", nil)
	for _, ck := range c.Jar.Cookies(req.URL) {
		if ck.Name == sessionCookie {
			req.AddCookie(ck)
		}
	}
	u, ok := s.UserFromRequest(req)
	if !ok || u.Subject != "user-1" || u.Name != "Test User" || u.Email != "user@example.com" {
		t.Fatalf("session user = %+v ok=%v", u, ok)
	}

	// A tampered cookie is rejected.
	bad := client(t)
	req2, _ := http.NewRequest("GET", srv.URL+"/api/channels", nil)
	req2.AddCookie(&http.Cookie{Name: sessionCookie, Value: "eyJzdWIiOiJ4In0.AAAA"})
	res2, _ := bad.Do(req2)
	res2.Body.Close()
	if res2.StatusCode != 401 {
		t.Fatalf("forged cookie accepted: %d", res2.StatusCode)
	}

	// Logout clears the session and redirects to the provider's end-session.
	noRedirect := &http.Client{Jar: c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res3, _ := noRedirect.Get(srv.URL + "/auth/logout")
	res3.Body.Close()
	if res3.StatusCode != 302 || !strings.Contains(res3.Header.Get("Location"), "/end-session") {
		t.Fatalf("logout = %d %s", res3.StatusCode, res3.Header.Get("Location"))
	}
	if res, _ := get(t, c, srv.URL+"/api/channels"); res.StatusCode != 401 {
		t.Fatalf("api after logout = %d", res.StatusCode)
	}
}

func TestStreamTokensAndMCPBearer(t *testing.T) {
	_, s, srv := newStack(t)
	anon := &http.Client{}
	tok := s.StreamToken(User{Subject: "user-1"}, "5.1")

	if res, _ := get(t, anon, srv.URL+"/api/channels/5.1/stream/index.m3u8?t="+tok); res.StatusCode != 200 {
		t.Fatalf("stream with token = %d", res.StatusCode)
	}
	if res, _ := get(t, anon, srv.URL+"/api/channels/7.1/stream/index.m3u8?t="+tok); res.StatusCode != 401 {
		t.Fatalf("token for another channel accepted: %d", res.StatusCode)
	}
	if res, _ := get(t, anon, srv.URL+"/api/channels/5.1/stream/index.m3u8"); res.StatusCode != 401 {
		t.Fatalf("stream without token = %d", res.StatusCode)
	}
	if !s.CheckStreamToken(tok, "5.1") || s.CheckStreamToken(tok+"x", "5.1") {
		t.Fatal("CheckStreamToken wrong")
	}

	req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(nil))
	res, _ := anon.Do(req)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("mcp without bearer = %d", res.StatusCode)
	}
	req.Header.Set("Authorization", "Bearer mcp-secret")
	res, _ = anon.Do(req)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("mcp with bearer = %d", res.StatusCode)
	}
}
