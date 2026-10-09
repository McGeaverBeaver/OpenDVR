// Package dvr records programmes. Series passes (rules) are matched
// against the guide to schedule recordings; a scheduler starts them on
// time, capturing the tuner's transport stream unchanged; a post-processor
// then marks or removes commercials with comskip and transcodes to an
// MP4 that every player can seek through. State lives in DATA_DIR/dvr.json
// and the files under RECORDINGS_DIR.
package dvr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/guide"
	"github.com/McGeaverBeaver/opendvr/internal/stream"
	"github.com/McGeaverBeaver/opendvr/internal/tuner"
)

// Commercials says what to do about adverts in a recording.
type Commercials string

const (
	// CommercialsSkip detects adverts and marks them; players jump over them.
	CommercialsSkip Commercials = "skip"
	// CommercialsRemove detects adverts and cuts them out of the file.
	CommercialsRemove Commercials = "remove"
	// CommercialsNone leaves the recording as broadcast.
	CommercialsNone Commercials = "none"
)

// Rule is a series pass: which programmes to record and how.
type Rule struct {
	ID string `json:"id"`
	// Title is matched case-insensitively against programme titles.
	Title string `json:"title"`
	// SeriesID, when set, is matched instead of the title.
	SeriesID string `json:"seriesId,omitempty"`
	// Channel restricts the pass to one channel; empty means any.
	Channel string `json:"channel,omitempty"`
	// NewOnly records only first airings.
	NewOnly bool `json:"newOnly"`
	// Season, when non-zero, records only that season (from SnnEnn numbers).
	Season int `json:"season,omitempty"`
	// Keep is how many recordings to retain; oldest are deleted. 0 keeps all.
	Keep int `json:"keep"`
	// PadBefore and PadAfter are minutes added around the airing.
	PadBefore   int         `json:"padBefore"`
	PadAfter    int         `json:"padAfter"`
	Commercials Commercials `json:"commercials"`
	// Priority orders passes when tuners are short; lower wins.
	Priority int       `json:"priority"`
	Paused   bool      `json:"paused"`
	Created  time.Time `json:"created"`
	// DeleteWatched removes recordings a day after they were watched.
	DeleteWatched bool `json:"deleteWatched,omitempty"`
	// Kind is "series" (match the guide) or "manual" (a time slot).
	Kind string `json:"kind,omitempty"`
	// Manual rules: the time of day (HH:MM), length, and either the days
	// of the week (0 = Sunday) or one date (YYYY-MM-DD).
	TimeOfDay   string `json:"timeOfDay,omitempty"`
	DurationMin int    `json:"durationMin,omitempty"`
	Days        []int  `json:"days,omitempty"`
	Date        string `json:"date,omitempty"`
}

// Status is where a recording is in its life.
type Status string

const (
	StatusScheduled  Status = "scheduled"
	StatusConflict   Status = "conflict" // scheduled, but no tuner will be free
	StatusRecording  Status = "recording"
	StatusProcessing Status = "processing"
	StatusReady      Status = "ready"
	StatusFailed     Status = "failed"
)

// Span is a stretch of a recording, in seconds from its start.
type Span struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Recording is one airing, scheduled, in progress or done.
type Recording struct {
	ID            string `json:"id"`
	RuleID        string `json:"ruleId,omitempty"`
	Channel       string `json:"channel"`
	ChannelName   string `json:"channelName,omitempty"`
	Title         string `json:"title"`
	EpisodeTitle  string `json:"episodeTitle,omitempty"`
	EpisodeNumber string `json:"episodeNumber,omitempty"`
	Synopsis      string `json:"synopsis,omitempty"`
	SeriesID      string `json:"seriesId,omitempty"`
	ImageURL      string `json:"imageUrl,omitempty"`
	// Airing and AiringEnd are the programme's listed times; Start and End
	// include padding and are when the tuner is actually used.
	Airing    int64 `json:"airing"`
	AiringEnd int64 `json:"airingEnd"`
	Start     int64 `json:"start"`
	End       int64 `json:"end"`
	// Manual recordings were asked for directly rather than by a pass.
	Manual bool   `json:"manual,omitempty"`
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
	// File is relative to the recordings directory.
	File        string      `json:"file,omitempty"`
	SizeBytes   int64       `json:"sizeBytes,omitempty"`
	DurationSec float64     `json:"durationSec,omitempty"`
	Commercials Commercials `json:"commercials"`
	// Skips are the detected adverts (empty once they have been cut out).
	Skips []Span `json:"skips,omitempty"`
	Cut   bool   `json:"cut,omitempty"`
	// Position is where playback last stopped, for resume.
	Position float64   `json:"position,omitempty"`
	Created  time.Time `json:"created"`
	Finished time.Time `json:"finished,omitempty"`
	// Watched is set when playback reached the end.
	Watched   bool      `json:"watched,omitempty"`
	WatchedAt time.Time `json:"watchedAt,omitempty"`
	// Captions is true when a WebVTT sidecar was extracted.
	Captions bool `json:"captions,omitempty"`
	// Notified remembers which messages went out for this recording.
	Notified []string `json:"notified,omitempty"`
	// Original is the broadcast file kept after processing (relative to
	// the recordings folder), until OriginalUntil, so commercials can be
	// found again with other settings.
	Original      string    `json:"original,omitempty"`
	OriginalUntil time.Time `json:"originalUntil,omitempty"`
}

// Done reports whether the recording no longer needs a tuner.
func (r *Recording) Done() bool {
	return r.Status == StatusReady || r.Status == StatusFailed || r.Status == StatusProcessing
}

// Config sets up the service.
type Config struct {
	DataDir       string
	RecordingsDir string
	FFmpegPath    string
	// ComskipPath is the comskip binary; empty disables commercial detection.
	ComskipPath   string
	Encoder       stream.Encoder
	RenderDevice  string
	VideoBitrateK int
	AudioBitrateK int
	Deinterlace   bool
	// PadBefore and PadAfter are the default minutes around an airing.
	PadBefore int
	PadAfter  int
	// Lookahead is how far into the guide passes are matched.
	Lookahead time.Duration
	// MaxBytes caps the recordings folder; 0 means no cap.
	MaxBytes int64
	// KeepOriginalDays keeps each recording's broadcast file this many
	// days after processing; 0 deletes it straight away.
	KeepOriginalDays int
	// Captions extracts closed captions to WebVTT sidecars.
	Captions bool
	// Notify, when set, is told about failures, conflicts and (with
	// ready true) finished recordings.
	Notify func(kind, title, body string)
}

// Tuners is what the recorder needs from the tuner registry.
type Tuners interface {
	ResolveSource(ctx context.Context, guideNumber string, active map[string]bool) (tuner.Source, error)
	Devices() []tuner.DeviceState
	Channel(guideNumber string) (tuner.Channel, bool)
}

// Guide is what the planner needs from the listings.
type Guide interface {
	Window(from, to int64) []guide.Channel
	NowNext(at int64) map[string]guide.NowNext
	EnsureWindow(ctx context.Context, to int64)
}

// Live is what the recorder needs from the live stream manager.
type Live interface {
	Statuses() []stream.Status
	Degraded() bool
}

// Service is the DVR.
type Service struct {
	cfg    Config
	tuners Tuners
	epg    Guide
	live   Live
	log    *slog.Logger
	file   string
	probe  string // ffprobe, if found

	mu         sync.Mutex
	rules      map[string]*Rule
	recs       map[string]*Recording
	jobs       map[string]context.CancelFunc // recording id -> cancel
	retryAt    map[string]time.Time          // recording id -> next tuner attempt
	planReq    chan struct{}
	postReq    chan string
	postCancel map[string]context.CancelFunc
	tests      comskipTests
	lastPlan   time.Time
	planErr    string
}

// New loads state and prepares the directories.
func New(cfg Config, tuners Tuners, epg Guide, live Live, log *slog.Logger) (*Service, error) {
	if cfg.RecordingsDir == "" {
		cfg.RecordingsDir = filepath.Join(cfg.DataDir, "recordings")
	}
	if err := os.MkdirAll(cfg.RecordingsDir, 0o755); err != nil {
		return nil, fmt.Errorf("recordings dir: %w", err)
	}
	if cfg.PadBefore == 0 && cfg.PadAfter == 0 {
		cfg.PadBefore, cfg.PadAfter = 1, 3
	}
	if cfg.Lookahead == 0 {
		cfg.Lookahead = 24 * time.Hour
	}
	if cfg.ComskipPath == "" {
		if p, err := exec.LookPath("comskip"); err == nil {
			cfg.ComskipPath = p
		}
	} else if _, err := os.Stat(cfg.ComskipPath); err != nil {
		log.Warn("comskip not found; commercial detection off", "path", cfg.ComskipPath)
		cfg.ComskipPath = ""
	}
	s := &Service{
		cfg: cfg, tuners: tuners, epg: epg, live: live, log: log,
		rules: map[string]*Rule{}, recs: map[string]*Recording{},
		jobs: map[string]context.CancelFunc{}, retryAt: map[string]time.Time{},
		planReq: make(chan struct{}, 1), postReq: make(chan string, 64),
		postCancel: map[string]context.CancelFunc{},
	}
	s.probe = findProbe(cfg.FFmpegPath)
	if cfg.DataDir != "" {
		s.file = filepath.Join(cfg.DataDir, "dvr.json")
		s.load()
	}
	return s, nil
}

// ComskipAvailable reports whether commercial detection can run.
func (s *Service) ComskipAvailable() bool { return s.cfg.ComskipPath != "" }

// RecordingsDir is where files are kept.
func (s *Service) RecordingsDir() string { return s.cfg.RecordingsDir }

// DefaultCommercials is what a new pass gets when nothing is chosen.
func (s *Service) DefaultCommercials() Commercials {
	if s.cfg.ComskipPath != "" {
		return CommercialsSkip
	}
	return CommercialsNone
}

// Summary describes the DVR for the Status page and assistants.
type Summary struct {
	RecordingsDir  string         `json:"recordingsDir"`
	DiskFreeBytes  uint64         `json:"diskFreeBytes"`
	DiskTotalBytes uint64         `json:"diskTotalBytes"`
	UsedBytes      int64          `json:"usedBytes"`
	Comskip        bool           `json:"comskip"`
	TunerCount     int            `json:"tunerCount"`
	Rules          int            `json:"rules"`
	Counts         map[Status]int `json:"counts"`
	Encoder        stream.Encoder `json:"encoder"`
	LookaheadHours int            `json:"lookaheadHours"`
	LastPlan       time.Time      `json:"lastPlan"`
	PlanError      string         `json:"planError,omitempty"`
	MaxBytes       int64          `json:"maxBytes"`
	Captions       bool           `json:"captions"`
}

// Summary builds the overview.
func (s *Service) Summary() Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Summary{
		RecordingsDir: s.cfg.RecordingsDir, Comskip: s.cfg.ComskipPath != "",
		TunerCount: s.tunerCount(), Rules: len(s.rules), Counts: map[Status]int{},
		Encoder: s.encoder(), LookaheadHours: int(s.cfg.Lookahead.Hours()),
		LastPlan: s.lastPlan, PlanError: s.planErr,
	}
	for _, r := range s.recs {
		out.Counts[r.Status]++
		out.UsedBytes += r.SizeBytes
	}
	out.DiskFreeBytes, out.DiskTotalBytes = diskSpace(s.cfg.RecordingsDir)
	out.MaxBytes = s.cfg.MaxBytes
	out.Captions = s.cfg.Captions
	return out
}

func (s *Service) encoder() stream.Encoder {
	if s.live != nil && s.live.Degraded() {
		return stream.EncoderSoftware
	}
	if s.cfg.Encoder == "" || s.cfg.Encoder == stream.EncoderAuto {
		return stream.EncoderSoftware
	}
	return s.cfg.Encoder
}

// tunerCount sums the tuners of every known device; unknown counts as two.
func (s *Service) tunerCount() int {
	n := 0
	for _, d := range s.tuners.Devices() {
		if d.Device.TunerCount > 0 {
			n += d.Device.TunerCount
		} else {
			n += 2
		}
	}
	if n == 0 {
		n = 2
	}
	return n
}

// ---- rules -----------------------------------------------------------------

// Rules lists the passes, highest priority first.
func (s *Service) Rules() []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rule, 0, len(s.rules))
	for _, r := range s.rules {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].Created.Before(out[j].Created)
	})
	return out
}

// Rule fetches one pass.
func (s *Service) Rule(id string) (Rule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rules[id]
	if !ok {
		return Rule{}, false
	}
	return *r, true
}

// ErrNotFound is returned for unknown ids.
var ErrNotFound = errors.New("not found")

// SaveRule creates or updates a pass. Missing options take defaults; a
// new rule goes to the back of the priority order. Planning follows.
func (s *Service) SaveRule(in Rule) (Rule, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Kind == "" {
		in.Kind = RuleKindSeries
	}
	switch in.Kind {
	case RuleKindSeries:
		if in.Title == "" && in.SeriesID == "" {
			return Rule{}, errors.New("a title or series id is required")
		}
	case RuleKindManual:
		if err := s.validateManual(&in); err != nil {
			return Rule{}, err
		}
	default:
		return Rule{}, errors.New("kind must be series or manual")
	}
	switch in.Commercials {
	case CommercialsSkip, CommercialsRemove, CommercialsNone:
	case "":
		in.Commercials = s.DefaultCommercials()
	default:
		return Rule{}, fmt.Errorf("commercials must be skip, remove or none")
	}
	if in.Commercials != CommercialsNone && s.cfg.ComskipPath == "" {
		in.Commercials = CommercialsNone
	}
	if in.PadBefore < 0 || in.PadAfter < 0 || in.PadBefore > 120 || in.PadAfter > 180 {
		return Rule{}, errors.New("padding must be between 0 and 120 minutes")
	}
	if in.Keep < 0 {
		in.Keep = 0
	}
	s.mu.Lock()
	if in.ID == "" {
		in.ID = newID()
		in.Created = time.Now()
		if in.PadBefore == 0 && in.PadAfter == 0 && in.Kind == RuleKindSeries {
			in.PadBefore, in.PadAfter = s.cfg.PadBefore, s.cfg.PadAfter
		}
		if in.Priority == 0 {
			for _, r := range s.rules {
				if r.Priority >= in.Priority {
					in.Priority = r.Priority + 1
				}
			}
			if in.Priority == 0 {
				in.Priority = 1
			}
		}
	} else {
		old, ok := s.rules[in.ID]
		if !ok {
			s.mu.Unlock()
			return Rule{}, ErrNotFound
		}
		in.Created = old.Created
		if in.Priority == 0 {
			in.Priority = old.Priority
		}
	}
	r := in
	s.rules[r.ID] = &r
	s.saveLocked()
	s.mu.Unlock()
	s.RequestPlan()
	return r, nil
}

// DeleteRule removes a pass and whatever it had scheduled. Recordings in
// progress or finished are kept.
func (s *Service) DeleteRule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rules[id]; !ok {
		return ErrNotFound
	}
	delete(s.rules, id)
	for rid, r := range s.recs {
		if r.RuleID == id && (r.Status == StatusScheduled || r.Status == StatusConflict) {
			delete(s.recs, rid)
		}
	}
	s.saveLocked()
	return nil
}

// ---- recordings ------------------------------------------------------------

// Recordings lists recordings, newest airing first. With a status it is
// filtered; "upcoming" means scheduled, conflict and recording.
func (s *Service) Recordings(filter string) []Recording {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Recording, 0, len(s.recs))
	for _, r := range s.recs {
		switch filter {
		case "", "all":
		case "upcoming":
			if r.Status != StatusScheduled && r.Status != StatusConflict && r.Status != StatusRecording {
				continue
			}
		case "library":
			if r.Status != StatusReady && r.Status != StatusProcessing && r.Status != StatusFailed && r.Status != StatusRecording {
				continue
			}
		default:
			if string(r.Status) != filter {
				continue
			}
		}
		out = append(out, *r)
	}
	if filter == "upcoming" {
		sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].Start > out[j].Start })
	}
	return out
}

// Recording fetches one.
func (s *Service) Recording(id string) (Recording, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	if !ok {
		return Recording{}, false
	}
	return *r, true
}

// Path is the absolute path of a recording's file.
func (s *Service) Path(r Recording) string {
	if r.File == "" {
		return ""
	}
	return filepath.Join(s.cfg.RecordingsDir, filepath.FromSlash(r.File))
}

// Options for a one-off recording; zero values take defaults.
type Options struct {
	PadBefore   *int        `json:"padBefore,omitempty"`
	PadAfter    *int        `json:"padAfter,omitempty"`
	Commercials Commercials `json:"commercials,omitempty"`
}

func (s *Service) applyOptions(r *Recording, o Options) {
	r.Start = r.Airing - int64(s.cfg.PadBefore)*60
	r.End = r.AiringEnd + int64(s.cfg.PadAfter)*60
	if o.PadBefore != nil && *o.PadBefore >= 0 {
		r.Start = r.Airing - int64(*o.PadBefore)*60
	}
	if o.PadAfter != nil && *o.PadAfter >= 0 {
		r.End = r.AiringEnd + int64(*o.PadAfter)*60
	}
	r.Commercials = o.Commercials
	if r.Commercials == "" || s.cfg.ComskipPath == "" {
		r.Commercials = s.DefaultCommercials()
	}
}

// RecordAiring schedules one programme from the guide. If it is already
// on, recording starts within seconds. Asking twice returns the same
// recording.
func (s *Service) RecordAiring(channel string, airing int64, o Options) (Recording, error) {
	var prog *guide.Program
	var chName string
	for _, c := range s.epg.Window(airing, airing+1) {
		if c.GuideNumber != channel {
			continue
		}
		chName = c.GuideName
		for i := range c.Programs {
			if c.Programs[i].Start == airing {
				prog = &c.Programs[i]
			}
		}
	}
	if prog == nil {
		return Recording{}, fmt.Errorf("no programme on %s at that time", channel)
	}
	if prog.End <= time.Now().Unix() {
		return Recording{}, errors.New("that programme has already finished")
	}
	if _, ok := s.tuners.Channel(channel); !ok {
		return Recording{}, fmt.Errorf("unknown channel %s", channel)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r.Channel == channel && r.Airing == airing && r.Status != StatusFailed {
			return *r, nil
		}
	}
	r := s.fromProgram(channel, chName, *prog)
	r.Manual = true
	s.applyOptions(r, o)
	if r.Start < time.Now().Unix() {
		r.Start = time.Now().Unix()
	}
	s.recs[r.ID] = r
	s.markConflictsLocked()
	s.saveLocked()
	s.log.Info("recording scheduled", "id", r.ID, "channel", channel, "title", r.Title, "start", time.Unix(r.Start, 0).Format(time.RFC3339))
	return *r, nil
}

// RecordNow starts recording a channel immediately: the current
// programme to its end when the guide knows it, else for minutes.
func (s *Service) RecordNow(channel string, minutes int, o Options) (Recording, error) {
	ch, ok := s.tuners.Channel(channel)
	if !ok {
		return Recording{}, fmt.Errorf("unknown channel %s", channel)
	}
	now := time.Now().Unix()
	var r *Recording
	if nn := s.epg.NowNext(now)[channel]; nn.Now != nil {
		r = s.fromProgram(channel, ch.GuideName, *nn.Now)
	} else {
		if minutes <= 0 {
			minutes = 60
		}
		r = &Recording{
			ID: newID(), Channel: channel, ChannelName: ch.GuideName,
			Title:  ch.GuideName + " " + time.Unix(now, 0).Format("2006-01-02 15:04"),
			Airing: now, AiringEnd: now + int64(minutes)*60, Created: time.Now(), Status: StatusScheduled,
		}
	}
	r.Manual = true
	s.applyOptions(r, o)
	r.Start = now
	if minutes > 0 {
		r.End = now + int64(minutes)*60
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.recs {
		if x.Channel == channel && x.Status == StatusRecording {
			return *x, nil
		}
	}
	s.recs[r.ID] = r
	s.saveLocked()
	s.log.Info("recording now", "id", r.ID, "channel", channel, "title", r.Title, "until", time.Unix(r.End, 0).Format(time.RFC3339))
	return *r, nil
}

func (s *Service) fromProgram(channel, chName string, p guide.Program) *Recording {
	return &Recording{
		ID: newID(), Channel: channel, ChannelName: chName,
		Title: p.Title, EpisodeTitle: p.EpisodeTitle, EpisodeNumber: p.EpisodeNumber, Synopsis: p.Synopsis,
		SeriesID: p.SeriesID, ImageURL: p.ImageURL,
		Airing: p.Start, AiringEnd: p.End, Created: time.Now(), Status: StatusScheduled,
	}
}

// Delete cancels a scheduled or running recording, or removes a finished
// one and its file.
func (s *Service) Delete(id string) error {
	s.mu.Lock()
	r, ok := s.recs[id]
	if !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	delete(s.recs, id)
	if cancel := s.jobs[id]; cancel != nil {
		cancel()
		delete(s.jobs, id)
	}
	if cancel := s.postCancel[id]; cancel != nil {
		cancel()
		delete(s.postCancel, id)
	}
	paths := s.filesOf(*r)
	s.saveLocked()
	s.mu.Unlock()
	for _, p := range paths {
		_ = os.Remove(p)
	}
	s.pruneDirs()
	s.log.Info("recording deleted", "id", id, "title", r.Title, "status", r.Status)
	return nil
}

// SetPosition remembers where playback stopped.
// ErrNotEditable is returned for recordings that have already finished.
var ErrNotEditable = errors.New("only scheduled or running recordings can be changed")

// Edit changes a recording that has not finished: how early it starts,
// how late it runs and what happens to commercials. A recording already
// running can only have its end moved and its commercial handling
// changed; the capture picks up the new end time as it goes.
func (s *Service) Edit(id string, o Options) (Recording, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	if !ok {
		return Recording{}, ErrNotFound
	}
	switch r.Status {
	case StatusScheduled, StatusConflict, StatusRecording:
	default:
		return Recording{}, ErrNotEditable
	}
	now := time.Now().Unix()
	if o.PadBefore != nil {
		if *o.PadBefore < 0 || *o.PadBefore > 120 {
			return Recording{}, errors.New("start early must be between 0 and 120 minutes")
		}
		if r.Status != StatusRecording {
			r.Start = r.Airing - int64(*o.PadBefore)*60
			if r.Start < now {
				r.Start = now
			}
		}
	}
	if o.PadAfter != nil {
		if *o.PadAfter < 0 || *o.PadAfter > 180 {
			return Recording{}, errors.New("run late must be between 0 and 180 minutes")
		}
		end := r.AiringEnd + int64(*o.PadAfter)*60
		if end <= now+30 {
			return Recording{}, errors.New("that would end the recording in the past")
		}
		r.End = end
	}
	switch o.Commercials {
	case "":
	case CommercialsSkip, CommercialsRemove, CommercialsNone:
		r.Commercials = o.Commercials
		if s.cfg.ComskipPath == "" {
			r.Commercials = CommercialsNone
		}
	default:
		return Recording{}, errors.New("commercials must be skip, remove or none")
	}
	s.markConflictsLocked()
	s.saveLocked()
	s.log.Info("recording edited", "id", id, "title", r.Title, "start", time.Unix(r.Start, 0).Format(time.RFC3339), "end", time.Unix(r.End, 0).Format(time.RFC3339), "commercials", r.Commercials)
	return *r, nil
}

func (s *Service) SetPosition(id string, pos float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	if !ok {
		return ErrNotFound
	}
	if pos < 0 {
		pos = 0
	}
	r.Position = pos
	s.saveLocked()
	return nil
}

// filesOf lists everything on disk for a recording: the file itself and
// comskip's side files.
func (s *Service) filesOf(r Recording) []string {
	if r.File == "" {
		return nil
	}
	p := s.Path(r)
	base := strings.TrimSuffix(p, filepath.Ext(p))
	out := []string{p, base + ".ts", base + ".mp4", base + ".vtt"}
	for _, ext := range []string{".edl", ".log", ".logo.txt", ".txt", ".vdr", ".srt"} {
		out = append(out, base+ext)
	}
	return out
}

// pruneDirs removes empty show folders.
func (s *Service) pruneDirs() {
	entries, _ := os.ReadDir(s.cfg.RecordingsDir)
	for _, e := range entries {
		if e.IsDir() {
			_ = os.Remove(filepath.Join(s.cfg.RecordingsDir, e.Name())) // fails unless empty
		}
	}
}

var unsafeName = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]+`)

func safeName(s string) string {
	s = unsafeName.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, ". ")
	if len(s) > 100 {
		s = s[:100]
	}
	if s == "" {
		s = "Recording"
	}
	return s
}

// relPathFor picks "Show/Show - S01E02 - Episode.ts" style names.
func (s *Service) relPathFor(r *Recording) string {
	show := safeName(r.Title)
	parts := []string{show}
	if r.EpisodeNumber != "" {
		parts = append(parts, safeName(r.EpisodeNumber))
	} else {
		parts = append(parts, time.Unix(r.Airing, 0).Format("2006-01-02 1504"))
	}
	if r.EpisodeTitle != "" {
		parts = append(parts, safeName(r.EpisodeTitle))
	}
	name := strings.Join(parts, " - ")
	rel := filepath.ToSlash(filepath.Join(show, name+".ts"))
	// Avoid clobbering an earlier recording with the same name.
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(s.cfg.RecordingsDir, filepath.FromSlash(rel))); err != nil {
			return rel
		}
		rel = filepath.ToSlash(filepath.Join(show, fmt.Sprintf("%s (%d).ts", name, i)))
	}
}

// ---- persistence -------------------------------------------------------

type persisted struct {
	Rules      []*Rule      `json:"rules"`
	Recordings []*Recording `json:"recordings"`
}

func (s *Service) saveLocked() {
	if s.file == "" {
		return
	}
	p := persisted{Rules: []*Rule{}, Recordings: []*Recording{}}
	for _, r := range s.rules {
		p.Rules = append(p.Rules, r)
	}
	for _, r := range s.recs {
		p.Recordings = append(p.Recordings, r)
	}
	sort.Slice(p.Rules, func(i, j int) bool { return p.Rules[i].ID < p.Rules[j].ID })
	sort.Slice(p.Recordings, func(i, j int) bool { return p.Recordings[i].ID < p.Recordings[j].ID })
	b, err := json.MarshalIndent(p, "", " ")
	if err != nil {
		return
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		s.log.Warn("dvr save", "err", err)
		return
	}
	if err := os.Rename(tmp, s.file); err != nil {
		s.log.Warn("dvr save", "err", err)
	}
}

func (s *Service) load() {
	b, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		s.log.Warn("dvr state unreadable; starting empty", "err", err)
		return
	}
	for _, r := range p.Rules {
		s.rules[r.ID] = r
	}
	for _, r := range p.Recordings {
		s.recs[r.ID] = r
	}
}

func newID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = alphabet[int(randByte())%len(alphabet)]
	}
	return string(b)
}

func findProbe(ffmpeg string) string {
	if ffmpeg != "" {
		p := filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		if _, err := os.Stat(p + ".exe"); err == nil {
			return p + ".exe"
		}
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p
	}
	return ""
}
