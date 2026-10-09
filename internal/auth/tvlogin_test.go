package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestTVSignIn(t *testing.T) {
	_, s, srv := newStack(t)
	tv := client(t) // the TV: no session
	post := func(c *http.Client, path string, body any) (int, map[string]any) {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		res, err := c.Post(srv.URL+path, "application/json", &buf)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		res.Body.Close()
		return res.StatusCode, out
	}

	// The TV gets a code and this server's own addresses, never the provider's.
	code, start := post(tv, "/auth/device/start", nil)
	userCode, _ := start["userCode"].(string)
	if code != 200 || !regexp.MustCompile(`^[A-Z2-9]{4}-[A-Z2-9]{4}$`).MatchString(userCode) {
		t.Fatalf("device start = %d %+v", code, start)
	}
	if start["verificationUri"] != srv.URL+"/tv" || start["verificationUriComplete"] != srv.URL+"/tv?code="+url.QueryEscape(userCode) {
		t.Fatalf("addresses = %+v", start)
	}
	if !strings.HasPrefix(start["qr"].(string), "data:image/png;base64,") {
		t.Fatal("no QR image")
	}
	handle := start["handle"].(string)
	if _, out := post(tv, "/auth/device/poll", map[string]string{"handle": handle}); out["status"] != "pending" {
		t.Fatalf("first poll = %+v", out)
	}

	// The phone opens /tv without a session: it is sent through sign-in and
	// back to the same page.
	phone := client(t)
	noRedirect := &http.Client{Jar: phone.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := noRedirect.Get(srv.URL + "/tv?code=" + userCode)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 302 || !strings.HasPrefix(res.Header.Get("Location"), "/auth/login?next=%2Ftv%3Fcode%3D") {
		t.Fatalf("anonymous /tv = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res, body := get(t, phone, srv.URL+"/tv?code="+userCode)
	if res.StatusCode != 200 || !strings.Contains(body, "Approve this TV") || !strings.Contains(body, userCode) || !strings.Contains(body, "Test User") {
		t.Fatalf("approval page after sign-in = %d at %s: %.300s", res.StatusCode, res.Request.URL, body)
	}
	tok := regexp.MustCompile(`name='t' value='([^']+)'`).FindStringSubmatch(body)
	if tok == nil {
		t.Fatal("no form token on the approval page")
	}

	// A wrong code is refused; the right one (typed loosely) approves the TV.
	res, body = postFormClient(t, phone, srv.URL+"/tv", url.Values{"t": {tok[1]}, "code": {"ZZZZ-ZZZZ"}, "decision": {"approve"}})
	if res.StatusCode != 404 || !strings.Contains(body, "not recognised") {
		t.Fatalf("wrong code = %d %.200s", res.StatusCode, body)
	}
	loose := strings.ToLower(strings.ReplaceAll(userCode, "-", " "))
	res, body = postFormClient(t, phone, srv.URL+"/tv", url.Values{"t": {tok[1]}, "code": {loose}, "decision": {"approve"}})
	if res.StatusCode != 200 || !strings.Contains(body, "TV signed in") {
		t.Fatalf("approve = %d %.200s", res.StatusCode, body)
	}

	// The TV's next poll signs it in; the handle is then gone.
	if _, out := post(tv, "/auth/device/poll", map[string]string{"handle": handle}); out["status"] != "ok" {
		t.Fatalf("poll after approval = %+v", out)
	}
	if res, _ := get(t, tv, srv.URL+"/api/channels"); res.StatusCode != 200 {
		t.Fatalf("tv api after sign-in = %d", res.StatusCode)
	}
	if code, _ := post(tv, "/auth/device/poll", map[string]string{"handle": handle}); code != 404 {
		t.Fatalf("reused handle = %d", code)
	}

	// Deny path and a forged form token.
	_, start2 := post(client(t), "/auth/device/start", nil)
	res, body = postFormClient(t, phone, srv.URL+"/tv", url.Values{"t": {"forged.token"}, "code": {start2["userCode"].(string)}, "decision": {"approve"}})
	if res.StatusCode != 400 {
		t.Fatalf("forged token = %d %.200s", res.StatusCode, body)
	}
	res, body = postFormClient(t, phone, srv.URL+"/tv", url.Values{"t": {tok[1]}, "code": {start2["userCode"].(string)}, "decision": {"deny"}})
	if res.StatusCode != 200 || !strings.Contains(body, "Declined") {
		t.Fatalf("deny = %d %.200s", res.StatusCode, body)
	}
	if _, out := post(client(t), "/auth/device/poll", map[string]string{"handle": start2["handle"].(string)}); out["status"] != "error" {
		t.Fatalf("poll after deny = %+v", out)
	}
	_ = s
}

func postFormClient(t *testing.T, c *http.Client, url string, form url.Values) (*http.Response, string) {
	t.Helper()
	res, err := c.PostForm(url, form)
	if err != nil {
		t.Fatal(err)
	}
	b := new(bytes.Buffer)
	_, _ = b.ReadFrom(res.Body)
	res.Body.Close()
	return res, b.String()
}
