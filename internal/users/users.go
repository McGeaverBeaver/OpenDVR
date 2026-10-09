// Package users keeps what the server knows about each account beyond
// what the identity provider says: favourite channels, and for
// restricted ("kids") profiles the channels they may watch. Accounts are
// recorded as they are seen; nothing is created in the provider.
package users

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Profile is one account's settings.
type Profile struct {
	Subject   string    `json:"sub"`
	Name      string    `json:"name,omitempty"`
	Email     string    `json:"email,omitempty"`
	Favorites []string  `json:"favorites"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// Restricted profiles see only Allowed channels and cannot change the
	// DVR: a kids mode.
	Restricted bool     `json:"restricted"`
	Allowed    []string `json:"allowed,omitempty"`
}

// Store is the file-backed set of profiles.
type Store struct {
	file string
	mu   sync.Mutex
	all  map[string]*Profile
}

// New loads DATA_DIR/users.json (or starts empty).
func New(dataDir string) *Store {
	s := &Store{all: map[string]*Profile{}}
	if dataDir != "" {
		s.file = filepath.Join(dataDir, "users.json")
		s.load()
	}
	return s
}

// Reload re-reads the file (after a restore).
func (s *Store) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all = map[string]*Profile{}
	s.load()
}

// Touch records that an account was seen and returns its profile.
func (s *Store) Touch(sub, name, email string) Profile {
	if sub == "" {
		return Profile{Favorites: []string{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.all[sub]
	now := time.Now()
	if !ok {
		p = &Profile{Subject: sub, Favorites: []string{}, FirstSeen: now}
		s.all[sub] = p
	}
	changed := !ok || p.Name != name || p.Email != email || now.Sub(p.LastSeen) > 5*time.Minute
	p.Name, p.Email, p.LastSeen = name, email, now
	if changed {
		s.saveLocked()
	}
	return *p
}

// Get fetches a profile without recording a visit.
func (s *Store) Get(sub string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.all[sub]
	if !ok {
		return Profile{Favorites: []string{}}, false
	}
	return *p, true
}

// All lists profiles, most recently seen first.
func (s *Store) All() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Profile, 0, len(s.all))
	for _, p := range s.all {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// SetFavorites replaces an account's favourite channels.
func (s *Store) SetFavorites(sub string, favs []string) {
	if sub == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.all[sub]
	if !ok {
		p = &Profile{Subject: sub, FirstSeen: time.Now(), LastSeen: time.Now()}
		s.all[sub] = p
	}
	if favs == nil {
		favs = []string{}
	}
	p.Favorites = favs
	s.saveLocked()
}

// SetRestriction turns kids mode on or off with the allowed channels.
func (s *Store) SetRestriction(sub string, restricted bool, allowed []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.all[sub]
	if !ok {
		return false
	}
	p.Restricted = restricted
	if !restricted {
		allowed = nil
	}
	p.Allowed = allowed
	s.saveLocked()
	return true
}

// MayWatch reports whether an account may use a channel.
func (s *Store) MayWatch(sub, channel string) bool {
	if sub == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.all[sub]
	if !ok || !p.Restricted {
		return true
	}
	for _, c := range p.Allowed {
		if c == channel {
			return true
		}
	}
	return false
}

func (s *Store) saveLocked() {
	if s.file == "" {
		return
	}
	list := make([]*Profile, 0, len(s.all))
	for _, p := range s.all {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Subject < list[j].Subject })
	b, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return
	}
	tmp := s.file + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.file)
	}
}

func (s *Store) load() {
	b, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	var list []*Profile
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, p := range list {
		if p.Favorites == nil {
			p.Favorites = []string{}
		}
		s.all[p.Subject] = p
	}
}
