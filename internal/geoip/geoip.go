// Package geoip resolves addresses to a country (and city, when the
// database has one) with a MaxMind GeoLite2 or GeoIP2 database dropped
// into the data directory. Nothing is downloaded: MaxMind's licence asks
// each user to fetch their own copy, so the file is simply picked up when
// present and re-read when it changes.
package geoip

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"

	"github.com/McGeaverBeaver/opendvr/internal/audit"
)

// Names are the database file names looked for, in order.
var Names = []string{"GeoLite2-City.mmdb", "GeoIP2-City.mmdb", "GeoLite2-Country.mmdb", "GeoIP2-Country.mmdb", "geoip.mmdb"}

// Resolver looks addresses up and watches the file.
type Resolver struct {
	dir string
	log *slog.Logger

	mu      sync.RWMutex
	db      *maxminddb.Reader
	path    string
	modTime time.Time
	checked time.Time
}

// New builds a resolver over dataDir. It is safe to use with no file.
func New(dataDir string, log *slog.Logger) *Resolver {
	r := &Resolver{dir: dataDir, log: log}
	r.refresh()
	return r
}

// Path is the database in use, or empty.
func (r *Resolver) Path() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.path
}

// Available reports whether lookups can succeed.
func (r *Resolver) Available() bool { return r.Path() != "" }

// refresh opens the newest database file if it changed; at most every
// minute so lookups stay cheap.
func (r *Resolver) refresh() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.checked) < time.Minute {
		return
	}
	r.checked = time.Now()
	for _, n := range Names {
		p := filepath.Join(r.dir, n)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if r.db != nil && r.path == p && st.ModTime().Equal(r.modTime) {
			return
		}
		db, err := maxminddb.Open(p)
		if err != nil {
			r.log.Warn("geoip database unreadable", "path", p, "err", err)
			continue
		}
		if r.db != nil {
			_ = r.db.Close()
		}
		r.db, r.path, r.modTime = db, p, st.ModTime()
		r.log.Info("geoip database loaded", "path", p, "type", db.Metadata.DatabaseType)
		return
	}
	if r.db != nil {
		_ = r.db.Close()
		r.db, r.path = nil, ""
		r.log.Info("geoip database removed")
	}
}

type record struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
}

// Lookup resolves an address. Private and loopback addresses answer
// "LAN" so the admin page can tell local viewers from remote ones.
func (r *Resolver) Lookup(ip string) (audit.Place, bool) {
	addr := net.ParseIP(ip)
	if addr == nil {
		return audit.Place{}, false
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return audit.Place{Country: "LAN"}, true
	}
	r.refresh()
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.db == nil {
		return audit.Place{}, false
	}
	var rec record
	if err := r.db.Lookup(addr, &rec); err != nil || rec.Country.ISOCode == "" {
		return audit.Place{}, false
	}
	return audit.Place{Country: rec.Country.ISOCode, City: rec.City.Names["en"]}, true
}
