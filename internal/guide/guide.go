// Package guide keeps programme listings for the lineup, fetched from
// SiliconDust's guide API with each tuner's DeviceAuth. The free tier
// returns the next several hours for every channel in one call and a day
// or so per channel on request; a subscription extends both. Listings are
// cached in memory, persisted to the data directory so a restart does not
// start blank, and extended on demand when a client asks for a window
// beyond what is cached.
package guide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// DefaultBaseURL is SiliconDust's guide endpoint.
const DefaultBaseURL = "https://api.hdhomerun.com/api/guide"

// Program is one listing.
type Program struct {
	Start           int64    `json:"start"`
	End             int64    `json:"end"`
	Title           string   `json:"title"`
	EpisodeTitle    string   `json:"episodeTitle,omitempty"`
	EpisodeNumber   string   `json:"episodeNumber,omitempty"`
	Synopsis        string   `json:"synopsis,omitempty"`
	ImageURL        string   `json:"imageUrl,omitempty"`
	SeriesID        string   `json:"seriesId,omitempty"`
	OriginalAirdate int64    `json:"originalAirdate,omitempty"`
	New             bool     `json:"new,omitempty"`
	Filter          []string `json:"filter,omitempty"`
}

// Channel is a channel's listings, keyed by guide number so the web
// client can join it to the tuner lineup.
type Channel struct {
	GuideNumber string    `json:"guideNumber"`
	GuideName   string    `json:"guideName"`
	Affiliate   string    `json:"affiliate,omitempty"`
	ImageURL    string    `json:"imageUrl,omitempty"`
	Programs    []Program `json:"programs"`
}

// NowNext is what is on a channel at a moment and what follows.
type NowNext struct {
	Now  *Program `json:"now,omitempty"`
	Next *Program `json:"next,omitempty"`
}

// Status describes the cache for the Status page.
type Status struct {
	LastRefresh time.Time `json:"lastRefresh"`
	LastError   string    `json:"lastError,omitempty"`
	Channels    int       `json:"channels"`
	Programs    int       `json:"programs"`
	// Until is when the earliest-ending channel's listings run out.
	Until time.Time `json:"until"`
}

// AuthSource yields the DeviceAuth strings of the known tuners.
type AuthSource func() []string

// Service owns the cache.
type Service struct {
	// UserAgent is sent with every guide request. The API rejects Go's
	// default, so it must be set to something that names this software.
	UserAgent string

	base   string
	auths  AuthSource
	http   *http.Client
	file   string
	log    *slog.Logger
	extend time.Duration

	mu          sync.RWMutex
	channels    map[string]*Channel
	lastRefresh time.Time
	lastError   string
	// extended records when each channel was last extended on demand and
	// how far it was asked to reach, so a client scrolling back and forth
	// does not hammer the API but a request for a further window still
	// fetches.
	extended map[string]extendRec
}

type extendRec struct {
	at time.Time
	to int64
}

// New builds a service. dataDir may be empty to disable persistence.
func New(base string, auths AuthSource, dataDir string, log *slog.Logger) *Service {
	if base == "" {
		base = DefaultBaseURL
	}
	s := &Service{
		UserAgent: "opendvr",
		base:      base,
		auths:     auths,
		http:      &http.Client{Timeout: 30 * time.Second},
		log:       log,
		extend:    30 * time.Minute,
		channels:  map[string]*Channel{},
		extended:  map[string]extendRec{},
	}
	if dataDir != "" {
		s.file = filepath.Join(dataDir, "guide.json")
		s.load()
	}
	return s
}

// wire is the API's JSON shape.
type wireChannel struct {
	GuideNumber string        `json:"GuideNumber"`
	GuideName   string        `json:"GuideName"`
	Affiliate   string        `json:"Affiliate"`
	ImageURL    string        `json:"ImageURL"`
	Guide       []wireProgram `json:"Guide"`
}

type wireProgram struct {
	StartTime       int64    `json:"StartTime"`
	EndTime         int64    `json:"EndTime"`
	Title           string   `json:"Title"`
	EpisodeTitle    string   `json:"EpisodeTitle"`
	EpisodeNumber   string   `json:"EpisodeNumber"`
	Synopsis        string   `json:"Synopsis"`
	ImageURL        string   `json:"ImageURL"`
	SeriesID        string   `json:"SeriesID"`
	OriginalAirdate int64    `json:"OriginalAirdate"`
	First           int      `json:"First"`
	Filter          []string `json:"Filter"`
}

func (s *Service) fetch(ctx context.Context, auth, channel string, start int64) ([]wireChannel, error) {
	q := url.Values{"DeviceAuth": {auth}}
	if channel != "" {
		q.Set("Channel", channel)
	}
	if start > 0 {
		q.Set("Start", strconv.FormatInt(start, 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	// SiliconDust's CDN answers 403 to Go's default User-Agent.
	req.Header.Set("User-Agent", s.UserAgent)
	res, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		return nil, fmt.Errorf("guide API: %s: %s", res.Status, string(body))
	}
	var out []wireChannel
	if err := json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("guide API: decode: %w", err)
	}
	return out, nil
}

// Refresh fetches the base window for every tuner and merges it in.
func (s *Service) Refresh(ctx context.Context) error {
	auths := s.auths()
	if len(auths) == 0 {
		return errors.New("no tuners with a DeviceAuth yet")
	}
	var firstErr error
	fetched := 0
	for _, a := range auths {
		chans, err := s.fetch(ctx, a, "", 0)
		if err != nil {
			s.log.Warn("guide refresh failed", "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		s.merge(chans)
		fetched++
	}
	s.mu.Lock()
	if fetched > 0 {
		s.lastRefresh = time.Now()
		s.lastError = ""
	} else if firstErr != nil {
		s.lastError = firstErr.Error()
	}
	s.prune(time.Now())
	s.mu.Unlock()
	if fetched > 0 {
		s.save()
		return nil
	}
	return firstErr
}

// Run refreshes on an interval until ctx is cancelled.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Refresh(ctx); err != nil {
				s.log.Warn("guide refresh failed", "err", err)
			}
		}
	}
}

// merge folds fetched listings into the cache. Programs are keyed by start
// time per channel; a fetched entry replaces a cached one at the same
// start, and cached entries outside the fetched range are kept.
func (s *Service) merge(chans []wireChannel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, wc := range chans {
		if wc.GuideNumber == "" {
			continue
		}
		c := s.channels[wc.GuideNumber]
		if c == nil {
			c = &Channel{GuideNumber: wc.GuideNumber}
			s.channels[wc.GuideNumber] = c
		}
		c.GuideName = wc.GuideName
		c.Affiliate = wc.Affiliate
		c.ImageURL = wc.ImageURL
		byStart := map[int64]Program{}
		for _, p := range c.Programs {
			byStart[p.Start] = p
		}
		for _, g := range wc.Guide {
			if g.EndTime <= g.StartTime {
				continue
			}
			byStart[g.StartTime] = Program{
				Start: g.StartTime, End: g.EndTime, Title: g.Title, EpisodeTitle: g.EpisodeTitle,
				EpisodeNumber: g.EpisodeNumber, Synopsis: g.Synopsis, ImageURL: g.ImageURL,
				SeriesID: g.SeriesID, OriginalAirdate: g.OriginalAirdate, New: g.First == 1, Filter: g.Filter,
			}
		}
		c.Programs = c.Programs[:0]
		for _, p := range byStart {
			c.Programs = append(c.Programs, p)
		}
		sort.Slice(c.Programs, func(i, j int) bool { return c.Programs[i].Start < c.Programs[j].Start })
		// Drop overlaps left by schedule changes: keep the later-fetched
		// entry, which is the one at the later start when two overlap.
		kept := c.Programs[:0]
		for _, p := range c.Programs {
			if n := len(kept); n > 0 && kept[n-1].End > p.Start {
				kept[n-1] = p
				continue
			}
			kept = append(kept, p)
		}
		c.Programs = kept
	}
}

// prune drops programmes that ended more than two hours ago. Caller holds
// s.mu.
func (s *Service) prune(now time.Time) {
	cutoff := now.Add(-2 * time.Hour).Unix()
	for _, c := range s.channels {
		i := 0
		for i < len(c.Programs) && c.Programs[i].End < cutoff {
			i++
		}
		c.Programs = append([]Program(nil), c.Programs[i:]...)
	}
}

// Window returns every channel's programmes overlapping [from, to].
// Channels with nothing in the window are returned with no programmes so
// the client can still draw a row for them.
func (s *Service) Window(from, to int64) []Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Channel, 0, len(s.channels))
	for _, c := range s.channels {
		cc := Channel{GuideNumber: c.GuideNumber, GuideName: c.GuideName, Affiliate: c.Affiliate, ImageURL: c.ImageURL, Programs: []Program{}}
		for _, p := range c.Programs {
			if p.End > from && p.Start < to {
				cc.Programs = append(cc.Programs, p)
			}
		}
		out = append(out, cc)
	}
	sort.Slice(out, func(i, j int) bool { return lessGuideNumber(out[i].GuideNumber, out[j].GuideNumber) })
	return out
}

// NowNext reports what is on every channel at the given time.
func (s *Service) NowNext(at int64) map[string]NowNext {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]NowNext, len(s.channels))
	for num, c := range s.channels {
		var nn NowNext
		for i := range c.Programs {
			p := &c.Programs[i]
			if p.Start <= at && at < p.End {
				cp := *p
				nn.Now = &cp
				if i+1 < len(c.Programs) {
					np := c.Programs[i+1]
					nn.Next = &np
				}
				break
			}
			if p.Start > at {
				np := *p
				nn.Next = &np
				break
			}
		}
		if nn.Now != nil || nn.Next != nil {
			out[num] = nn
		}
	}
	return out
}

// Channel returns one channel's cached listings.
func (s *Service) Channel(guideNumber string) (Channel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.channels[guideNumber]
	if !ok {
		return Channel{}, false
	}
	return Channel{GuideNumber: c.GuideNumber, GuideName: c.GuideName, Affiliate: c.Affiliate, ImageURL: c.ImageURL, Programs: append([]Program(nil), c.Programs...)}, true
}

// EnsureWindow extends channels whose cached listings end before `to`,
// fetching per channel from where the cache runs out. At most four
// requests run at once and a channel is not re-extended within the
// cooldown. Errors are logged, not returned: a partial guide is still a
// guide.
func (s *Service) EnsureWindow(ctx context.Context, to int64) {
	auths := s.auths()
	if len(auths) == 0 {
		return
	}
	type job struct {
		channel string
		from    int64
	}
	var jobs []job
	now := time.Now()
	s.mu.Lock()
	for num, c := range s.channels {
		var end int64
		if n := len(c.Programs); n > 0 {
			end = c.Programs[n-1].End
		}
		if end >= to {
			continue
		}
		if rec, ok := s.extended[num]; ok && rec.to >= to && now.Sub(rec.at) < s.extend {
			continue
		}
		s.extended[num] = extendRec{at: now, to: to}
		if end == 0 {
			end = now.Unix()
		}
		jobs = append(jobs, job{channel: num, from: end})
	}
	s.mu.Unlock()
	if len(jobs) == 0 {
		return
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			chans, err := s.fetch(ctx, auths[0], j.channel, j.from)
			if err != nil {
				s.log.Debug("guide extend failed", "channel", j.channel, "err", err)
				return
			}
			s.merge(chans)
		}(j)
	}
	wg.Wait()
	s.save()
}

// Status summarises the cache.
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{LastRefresh: s.lastRefresh, LastError: s.lastError, Channels: len(s.channels)}
	var until int64
	for _, c := range s.channels {
		st.Programs += len(c.Programs)
		if n := len(c.Programs); n > 0 {
			if e := c.Programs[n-1].End; until == 0 || e < until {
				until = e
			}
		}
	}
	if until > 0 {
		st.Until = time.Unix(until, 0)
	}
	return st
}

type persisted struct {
	LastRefresh time.Time  `json:"lastRefresh"`
	Channels    []*Channel `json:"channels"`
}

func (s *Service) save() {
	if s.file == "" {
		return
	}
	s.mu.RLock()
	p := persisted{LastRefresh: s.lastRefresh}
	for _, c := range s.channels {
		p.Channels = append(p.Channels, c)
	}
	data, err := json.Marshal(p)
	s.mu.RUnlock()
	if err != nil {
		return
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		s.log.Warn("guide save failed", "err", err)
		return
	}
	_ = os.Rename(tmp, s.file)
}

func (s *Service) load() {
	data, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range p.Channels {
		if c != nil && c.GuideNumber != "" {
			s.channels[c.GuideNumber] = c
		}
	}
	s.lastRefresh = p.LastRefresh
	s.prune(time.Now())
	s.log.Info("guide loaded from disk", "channels", len(s.channels), "asOf", p.LastRefresh)
}

func lessGuideNumber(a, b string) bool {
	am, as := split(a)
	bm, bs := split(b)
	if am != bm {
		return am < bm
	}
	if as != bs {
		return as < bs
	}
	return a < b
}

func split(s string) (float64, float64) {
	var major, minor float64
	if i := indexByte(s, '.'); i >= 0 {
		major, _ = strconv.ParseFloat(s[:i], 64)
		minor, _ = strconv.ParseFloat(s[i+1:], 64)
	} else {
		major, _ = strconv.ParseFloat(s, 64)
	}
	return major, minor
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
