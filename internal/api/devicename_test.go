package api

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestClientLabel(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36":                         "Chrome on Windows",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 Edg/129.0.0.0":           "Edge on Windows",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (Linux; Android 14; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36":                         "Chrome on Android",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14.6; rv:131.0) Gecko/20100101 Firefox/131.0":                                                     "Firefox on Mac",
		"opendvr-tv/0.3.0":         "TV app",
		"":                         "Unknown device",
		"VLC/3.0.20 LibVLC/3.0.20": "VLC",
	}
	for ua, want := range cases {
		if got := clientLabel(ua); got != want {
			t.Errorf("clientLabel(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestHostLabel(t *testing.T) {
	cases := []struct{ ptr, ip, want string }{
		{"DEN-PC.lan.", "192.168.1.40", "DEN-PC"},
		{"Alexs-iPhone.localdomain", "192.168.1.41", "Alexs-iPhone"},
		{"192-168-1-40.example.net.", "192.168.1.40", ""},
		{"localhost.", "192.168.1.40", ""},
		{"", "192.168.1.40", ""},
	}
	for _, c := range cases {
		if got := hostLabel(c.ptr, c.ip); got != c.want {
			t.Errorf("hostLabel(%q) = %q, want %q", c.ptr, got, c.want)
		}
	}
}

func TestViewerNameOrder(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest("POST", "/api/watch", nil)
	r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/129.0.0.0 Safari/537.36")

	if n, look := s.viewerName(r, "192.168.1.5", watchNames{Name: "  Den  ", DeviceName: "Fire TV", Client: "Chrome on Windows"}); n != "Den" || look {
		t.Fatalf("chosen name = %q %v", n, look)
	}
	if n, look := s.viewerName(r, "192.168.1.5", watchNames{DeviceName: "Living Room Fire TV"}); n != "Living Room Fire TV" || look {
		t.Fatalf("device name = %q %v", n, look)
	}
	// Home network, nothing known yet: the browser description for now and a lookup.
	if n, look := s.viewerName(r, "192.168.1.5", watchNames{Client: "Pixel 8 · Chrome"}); n != "Pixel 8 · Chrome" || !look {
		t.Fatalf("client = %q %v", n, look)
	}
	// Public address: never looked up; nothing sent falls back to the User-Agent.
	if n, look := s.viewerName(r, "203.0.113.9", watchNames{}); n != "Chrome on Windows" || look {
		t.Fatalf("user agent = %q %v", n, look)
	}

	// A lookup that finds a name: later joins use it straight away.
	var mu sync.Mutex
	calls := 0
	s.hostNames.lookup = func(ctx context.Context, ip string) ([]string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		return []string{"DEN-PC.lan."}, nil
	}
	got := make(chan string, 2)
	s.hostNames.resolve("192.168.1.5", func(n string) { got <- n })
	s.hostNames.resolve("192.168.1.5", func(n string) { got <- n }) // shares the first lookup
	for i := 0; i < 2; i++ {
		select {
		case n := <-got:
			if n != "DEN-PC" {
				t.Fatalf("resolved %q", n)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("lookup never answered")
		}
	}
	mu.Lock()
	if calls != 1 {
		t.Fatalf("lookups = %d, want 1", calls)
	}
	mu.Unlock()
	if n, look := s.viewerName(r, "192.168.1.5", watchNames{Client: "Chrome on Windows"}); n != "DEN-PC" || look {
		t.Fatalf("after lookup = %q %v", n, look)
	}
}
