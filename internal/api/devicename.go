package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Device names. A viewer is shown as the account that signed in plus the
// device it watches on, and the device's name is found without asking:
//
//  1. a name the person chose for this device in their profile;
//  2. the name the device gives itself (the TV app sends Android's device
//     name, e.g. "Living Room Fire TV");
//  3. on the home network, the name the router knows the computer or phone
//     by, from reverse DNS (e.g. "DEN-PC"), looked up in the background;
//  4. the browser and system the web app reports (e.g. "Chrome on Windows",
//     "Pixel 8 · Chrome"), since web pages cannot read a device's name;
//  5. the same worked out from the User-Agent header.

// watchNames are the naming fields a client sends with /api/watch.
type watchNames struct {
	Name       string // chosen by the person in their profile
	DeviceName string // the device's own name (TV app)
	Client     string // browser and system (web app)
}

// clip tidies a name and keeps it to 40 characters.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return s
}

// viewerName picks the name to show now, and reports whether a reverse DNS
// lookup might still find a better one.
func (s *Server) viewerName(r *http.Request, ip string, n watchNames) (name string, lookup bool) {
	if v := clip(n.Name); v != "" {
		return v, false
	}
	if v := clip(n.DeviceName); v != "" {
		return v, false
	}
	if host, known := s.hostNames.get(ip); known {
		if host != "" {
			return host, false
		}
	} else if privateIP(ip) {
		lookup = true
	}
	if v := clip(n.Client); v != "" {
		return v, lookup
	}
	return clientLabel(r.UserAgent()), lookup
}

func privateIP(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.IsPrivate()
}

// hostCache remembers reverse DNS answers for home-network addresses,
// "no name" included, for half an hour.
type hostCache struct {
	mu      sync.Mutex
	entries map[string]hostEntry
	waiting map[string][]func(string)
	// lookup is net.DefaultResolver.LookupAddr unless a test replaces it.
	lookup func(ctx context.Context, ip string) ([]string, error)
}

type hostEntry struct {
	name string
	at   time.Time
}

const hostTTL = 30 * time.Minute

func (c *hostCache) get(ip string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[ip]
	if !ok || time.Since(e.at) > hostTTL {
		return "", false
	}
	return e.name, true
}

// resolve looks an address up in the background and calls done with the
// name if one is found. Requests for an address already being looked up
// wait for that answer.
func (c *hostCache) resolve(ip string, done func(name string)) {
	c.mu.Lock()
	if c.waiting == nil {
		c.waiting = map[string][]func(string){}
		c.entries = map[string]hostEntry{}
	}
	pending := len(c.waiting[ip]) > 0
	c.waiting[ip] = append(c.waiting[ip], done)
	lookup := c.lookup
	c.mu.Unlock()
	if pending {
		return
	}
	if lookup == nil {
		lookup = net.DefaultResolver.LookupAddr
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		names, _ := lookup(ctx, ip)
		cancel()
		name := ""
		for _, n := range names {
			if name = hostLabel(n, ip); name != "" {
				break
			}
		}
		c.mu.Lock()
		c.entries[ip] = hostEntry{name: name, at: time.Now()}
		callbacks := c.waiting[ip]
		delete(c.waiting, ip)
		c.mu.Unlock()
		if name != "" {
			for _, f := range callbacks {
				f(name)
			}
		}
	}()
}

// hostLabel turns a reverse DNS answer into a device name: the first label,
// unless it is only the address spelled out.
func hostLabel(ptr, ip string) string {
	h := strings.TrimSuffix(strings.TrimSpace(ptr), ".")
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	low := strings.ToLower(h)
	if h == "" || low == "localhost" || net.ParseIP(h) != nil ||
		strings.Contains(low, strings.ReplaceAll(ip, ".", "-")) || strings.Contains(low, strings.ReplaceAll(ip, ":", "-")) {
		return ""
	}
	return clip(h)
}

// clientLabel describes a browser or app from its User-Agent.
func clientLabel(ua string) string {
	if ua == "" {
		return "Unknown device"
	}
	if strings.HasPrefix(ua, "opendvr-tv/") {
		return "TV app"
	}
	system := ""
	switch {
	case strings.Contains(ua, "iPhone"):
		system = "iPhone"
	case strings.Contains(ua, "iPad"):
		system = "iPad"
	case strings.Contains(ua, "Android"):
		system = "Android"
	case strings.Contains(ua, "CrOS"):
		system = "ChromeOS"
	case strings.Contains(ua, "Windows"):
		system = "Windows"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS X"):
		system = "Mac"
	case strings.Contains(ua, "Linux"):
		system = "Linux"
	}
	browser := ""
	switch {
	case strings.Contains(ua, "Edg/"), strings.Contains(ua, "EdgA/"), strings.Contains(ua, "EdgiOS/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"):
		browser = "Opera"
	case strings.Contains(ua, "SamsungBrowser/"):
		browser = "Samsung Internet"
	case strings.Contains(ua, "Firefox/"), strings.Contains(ua, "FxiOS/"):
		browser = "Firefox"
	case strings.Contains(ua, "CriOS/"), strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case system != "":
		return system
	}
	if i := strings.IndexAny(ua, "/ "); i > 0 {
		return clip(ua[:i])
	}
	return clip(ua)
}
