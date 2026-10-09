// Package audit keeps a record of who did what: sign-ins and failures,
// TVs approved, assistants connected, channels watched, recordings set up
// or removed, and administrative actions. Events are appended to
// DATA_DIR/audit.jsonl (rotated at a few megabytes) and the most recent
// ones are kept in memory for the admin page.
package audit

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Event is one audited action.
type Event struct {
	Time time.Time `json:"time"`
	// Kind is a short dotted name: auth.login, auth.refused, auth.logout,
	// tv.approved, tv.denied, tv.signed_in, mcp.registered, mcp.consent,
	// mcp.revoked, stream.watch, dvr.pass, dvr.record, dvr.delete,
	// admin.stop_stream and so on.
	Kind string `json:"kind"`
	// User is the account's subject; Name its display name.
	User    string `json:"user,omitempty"`
	Name    string `json:"name,omitempty"`
	IP      string `json:"ip,omitempty"`
	Country string `json:"country,omitempty"`
	City    string `json:"city,omitempty"`
	Agent   string `json:"agent,omitempty"`
	// Detail is a one-line description; Fields carry anything structured.
	Detail string            `json:"detail,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

// Place is what a GeoIP lookup adds to an address.
type Place struct {
	Country string
	City    string
}

// Locator turns an address into a place; nil means no database.
type Locator interface {
	Lookup(ip string) (Place, bool)
}

// Recorder is what the rest of the server logs to.
type Recorder interface {
	Record(e Event)
}

// Log is the file-backed recorder.
type Log struct {
	file    string
	geo     Locator
	mu      sync.Mutex
	recent  []Event
	max     int
	maxSize int64
}

// New opens (or creates) the log under dataDir. With an empty dataDir
// events are kept in memory only.
func New(dataDir string, geo Locator) *Log {
	l := &Log{geo: geo, max: 5000, maxSize: 8 << 20}
	if dataDir != "" {
		l.file = filepath.Join(dataDir, "audit.jsonl")
		l.load()
	}
	return l
}

// SetLocator installs or replaces the GeoIP lookup.
func (l *Log) SetLocator(geo Locator) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.geo = geo
}

// Record stores an event, filling in the time and the place of its IP.
func (l *Log) Record(e Event) {
	if l == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.IP != "" && e.Country == "" && l.geo != nil {
		if p, ok := l.geo.Lookup(e.IP); ok {
			e.Country, e.City = p.Country, p.City
		}
	}
	l.recent = append(l.recent, e)
	if len(l.recent) > l.max {
		l.recent = append([]Event(nil), l.recent[len(l.recent)-l.max:]...)
	}
	if l.file == "" {
		return
	}
	if st, err := os.Stat(l.file); err == nil && st.Size() > l.maxSize {
		_ = os.Rename(l.file, l.file+".1")
	}
	f, err := os.OpenFile(l.file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, _ = f.Write(append(b, '\n'))
}

// Query returns the newest events first, filtered by kind prefix and a
// case-insensitive word in user, name, IP, country or detail.
func (l *Log) Query(kind, q string, limit int) []Event {
	if l == nil {
		return []Event{}
	}
	kind = strings.ToLower(kind)
	q = strings.ToLower(strings.TrimSpace(q))
	if limit <= 0 || limit > l.max {
		limit = 200
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, 0, limit)
	for i := len(l.recent) - 1; i >= 0 && len(out) < limit; i-- {
		e := l.recent[i]
		if kind != "" && !strings.HasPrefix(e.Kind, kind) {
			continue
		}
		if q != "" {
			hay := strings.ToLower(e.User + " " + e.Name + " " + e.IP + " " + e.Country + " " + e.City + " " + e.Detail + " " + e.Kind)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// load reads the current file (and the rotated one before it) into memory.
func (l *Log) load() {
	var all []Event
	for _, p := range []string{l.file + ".1", l.file} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				all = append(all, e)
			}
		}
		f.Close()
	}
	if len(all) > l.max {
		all = all[len(all)-l.max:]
	}
	l.recent = all
}

// ---- client addresses ------------------------------------------------------

var trustedProxies []*net.IPNet

func init() {
	SetTrustedProxies(nil)
}

// SetTrustedProxies sets the networks whose X-Forwarded-For header is
// believed. Nil means loopback and the private ranges, which is where a
// reverse proxy on the LAN lives; anything else could forge the header.
func SetTrustedProxies(cidrs []string) {
	if cidrs == nil {
		cidrs = []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}
	}
	var nets []*net.IPNet
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	trustedProxies = nets
}

func trusted(ip string) bool {
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	for _, n := range trustedProxies {
		if n.Contains(p) {
			return true
		}
	}
	return false
}

// ClientIP is the address a request really came from: the first hop in
// X-Forwarded-For when the connection is from a trusted proxy, else the
// TCP peer.
func ClientIP(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if trusted(peer) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if net.ParseIP(first) != nil {
				return first
			}
		}
		if rip := strings.TrimSpace(r.Header.Get("X-Real-IP")); rip != "" && net.ParseIP(rip) != nil {
			return rip
		}
	}
	return peer
}

// Agent is a shortened User-Agent for the log.
func Agent(r *http.Request) string {
	ua := r.UserAgent()
	if len(ua) > 160 {
		ua = ua[:160]
	}
	return ua
}
