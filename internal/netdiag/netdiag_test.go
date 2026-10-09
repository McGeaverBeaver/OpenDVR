package netdiag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRunAgainstFakeTuner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discover.json" {
			w.Write([]byte(`{"DeviceID":"ABCD1234","TunerCount":2}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	host, _, _ := strings.Cut(u.Host, ":")

	// The fake listens on a random port, so the port-80 checks will fail;
	// we only assert the structure and that the hint logic runs.
	r := Run(context.Background(), host)
	if len(r.ResolvedIPs) == 0 {
		t.Fatalf("expected loopback to resolve")
	}
	if len(r.Checks) != 5 {
		t.Fatalf("expected 5 checks, got %d: %+v", len(r.Checks), r.Checks)
	}
	if r.DefaultGateway == "" {
		t.Logf("no default gateway in this environment (ok in sandbox)")
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"dial tcp: connect: connection refused": "refused",
		"dial tcp: i/o timeout":                 "timeout",
		"connect: no route to host":             "no route",
	}
	for in, want := range cases {
		if got := classify(errString(in)); !strings.Contains(got, want) {
			t.Errorf("classify(%q) = %q, want contains %q", in, got, want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
