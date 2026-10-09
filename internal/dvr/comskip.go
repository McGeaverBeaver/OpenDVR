package dvr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Commercial detection settings.
//
// comskip reads an .ini file. The server keeps one default
// (DATA_DIR/comskip.ini) and optional per-channel files
// (DATA_DIR/comskip/<channel>.ini) that replace the default for that
// channel. A channel file containing `opendvr_disable=1` turns detection
// off there (for channels without commercials). The lines the server
// itself depends on (EDL output) are appended at run time, so a settings
// file only has to hold detection settings.

// defaultComskipINI is tuned for North American over-the-air broadcasts,
// where many stations, Canadian ones especially, run ads without the black
// frames between them that comskip looks for by default. Logo, silence and
// scene changes are used as well, and blocks are joined across the logo.
const defaultComskipINI = `; OpenDVR commercial detection defaults (opendvr-defaults: 2).
; Edit freely; per-channel files in comskip/<channel>.ini replace this one.
; detect_method: 1 black frames, 2 logo, 4 scene change, 8 resolution,
; 32 aspect ratio, 64 silence. 111 = all of those.
detect_method=111
validate_silence=1
validate_uniform=1
validate_scenechange=1
max_brightness=60
test_brightness=40
max_avg_brightness=25
maxbright=1
non_uniformity=500
max_volume=500
min_silence=12
noise_level=5
logo_threshold=0.75
logo_percentage_threshold=0.25
punish_no_logo=1
connect_blocks_with_logo=1
min_commercialbreak=25
max_commercialbreak=600
min_commercial_size=4
max_commercial_size=125
min_show_segment_length=125
always_keep_first_seconds=0
always_keep_last_seconds=0
delete_show_after_last_commercial=0
lowres=1
thread_count=2
`

// requiredComskipLines are appended to every run; later lines win.
const requiredComskipLines = "\n; added by OpenDVR\noutput_edl=1\noutput_default=0\noutput_srt=0\noutput_vdr=0\noutput_txt=0\nverbose=0\n"

var unsafeChannel = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func (s *Service) defaultINIPath() string { return filepath.Join(s.cfg.DataDir, "comskip.ini") }

func (s *Service) channelINIPath(channel string) string {
	return filepath.Join(s.cfg.DataDir, "comskip", unsafeChannel.ReplaceAllString(channel, "_")+".ini")
}

// ensureDefaultINI writes the defaults, or replaces the near-empty file
// earlier versions wrote.
func (s *Service) ensureDefaultINI() {
	p := s.defaultINIPath()
	b, err := os.ReadFile(p)
	if err == nil && (strings.Contains(string(b), "detect_method") || !strings.Contains(string(b), "comskip settings used by")) {
		return
	}
	_ = os.WriteFile(p, []byte(defaultComskipINI), 0o644)
}

// ComskipSettings is the default and every per-channel override.
type ComskipSettings struct {
	Default  string            `json:"default"`
	Channels map[string]string `json:"channels"`
}

// ComskipSettings reads the settings files.
func (s *Service) ComskipSettings() ComskipSettings {
	s.ensureDefaultINI()
	out := ComskipSettings{Channels: map[string]string{}}
	if b, err := os.ReadFile(s.defaultINIPath()); err == nil {
		out.Default = string(b)
	}
	entries, _ := os.ReadDir(filepath.Join(s.cfg.DataDir, "comskip"))
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".ini"); ok && !e.IsDir() {
			if b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "comskip", e.Name())); err == nil {
				out.Channels[name] = string(b)
			}
		}
	}
	return out
}

// SaveComskipSettings stores settings for a channel, or the default when
// channel is empty. Empty settings for a channel remove its override.
func (s *Service) SaveComskipSettings(channel, settings string) error {
	if len(settings) > 64<<10 {
		return errors.New("settings too long")
	}
	if channel == "" {
		if strings.TrimSpace(settings) == "" {
			settings = defaultComskipINI
		}
		return os.WriteFile(s.defaultINIPath(), []byte(settings), 0o644)
	}
	p := s.channelINIPath(channel)
	if strings.TrimSpace(settings) == "" {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(settings), 0o644)
}

// iniFor returns the settings a channel's recordings are scanned with, and
// whether detection is turned off for it.
func (s *Service) iniFor(channel string) (string, bool) {
	s.ensureDefaultINI()
	text := ""
	if b, err := os.ReadFile(s.channelINIPath(channel)); err == nil {
		text = string(b)
	} else if b, err := os.ReadFile(s.defaultINIPath()); err == nil {
		text = string(b)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.ReplaceAll(strings.TrimSpace(line), " ", "") == "opendvr_disable=1" {
			return text, true
		}
	}
	return text, false
}

// runComskip scans a file with the given settings, writing its output
// into outDir, and returns the commercial breaks found and the tail of
// comskip's own output.
func (s *Service) runComskip(ctx context.Context, ts, settings, outDir string) ([]Span, string, error) {
	if s.cfg.ComskipPath == "" {
		return nil, "", errors.New("comskip is not installed")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, "", err
	}
	ini := filepath.Join(outDir, fmt.Sprintf("opendvr-%d.ini", time.Now().UnixNano()))
	if err := os.WriteFile(ini, []byte(settings+requiredComskipLines), 0o644); err != nil {
		return nil, "", err
	}
	defer os.Remove(ini)
	cmd := exec.CommandContext(ctx, s.cfg.ComskipPath, "--ini="+ini, "--output="+outDir, ts)
	cmd.Dir = outDir
	out, err := cmd.CombinedOutput()
	tail := strings.TrimSpace(string(out))
	if len(tail) > 1500 {
		tail = tail[len(tail)-1500:]
	}
	if ctx.Err() != nil {
		return nil, tail, ctx.Err()
	}
	base := strings.TrimSuffix(filepath.Base(ts), filepath.Ext(ts))
	edl := filepath.Join(outDir, base+".edl")
	f, ferr := os.Open(edl)
	if ferr != nil {
		if err != nil {
			return nil, tail, fmt.Errorf("comskip: %v", err)
		}
		return nil, tail, errors.New("comskip wrote no EDL")
	}
	spans := parseEDL(f)
	f.Close()
	for _, ext := range []string{".edl", ".log", ".logo.txt", ".txt", ".vdr"} {
		_ = os.Remove(filepath.Join(outDir, base+ext))
	}
	return spans, tail, nil
}

// originalPath is where a recording's broadcast file is, if it was kept.
func (s *Service) originalPath(r Recording) string {
	if r.Original != "" {
		return filepath.Join(s.cfg.RecordingsDir, filepath.FromSlash(r.Original))
	}
	if strings.HasSuffix(r.File, ".ts") {
		return s.Path(r)
	}
	return ""
}

// Redetect runs post-processing again from the kept broadcast file, so a
// change to the detection settings or to the recording's commercial
// handling takes effect without recording again.
func (s *Service) Redetect(id string, commercials Commercials) (Recording, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	if !ok {
		return Recording{}, ErrNotFound
	}
	if r.Status != StatusReady && r.Status != StatusFailed {
		return Recording{}, errors.New("the recording is still being made or processed")
	}
	p := s.originalPath(*r)
	if p == "" {
		return Recording{}, errors.New("the original broadcast file was not kept for this recording")
	}
	if _, err := os.Stat(p); err != nil {
		return Recording{}, errors.New("the original broadcast file is gone")
	}
	switch commercials {
	case "":
	case CommercialsSkip, CommercialsRemove, CommercialsNone:
		r.Commercials = commercials
	default:
		return Recording{}, errors.New("commercials must be skip, remove or none")
	}
	r.Status = StatusProcessing
	r.Error = ""
	s.saveLocked()
	s.enqueuePost(id)
	s.log.Info("finding commercials again", "id", id, "title", r.Title, "commercials", r.Commercials)
	return *r, nil
}

// expireOriginalsLocked deletes kept broadcast files past their date.
func (s *Service) expireOriginalsLocked(now time.Time) {
	for _, r := range s.recs {
		if r.Original == "" || r.OriginalUntil.IsZero() || now.Before(r.OriginalUntil) || r.Status == StatusProcessing {
			continue
		}
		_ = os.Remove(filepath.Join(s.cfg.RecordingsDir, filepath.FromSlash(r.Original)))
		s.log.Info("original broadcast file expired", "id", r.ID, "title", r.Title)
		r.Original = ""
		r.OriginalUntil = time.Time{}
	}
}

// ---- trial runs ------------------------------------------------------------

// ComskipTest is a trial scan of a kept recording with given settings; it
// changes nothing.
type ComskipTest struct {
	ID          string    `json:"id"`
	RecordingID string    `json:"recordingId"`
	Title       string    `json:"title"`
	Channel     string    `json:"channel"`
	Started     time.Time `json:"started"`
	Done        bool      `json:"done"`
	Error       string    `json:"error,omitempty"`
	DurationSec float64   `json:"durationSec,omitempty"`
	Breaks      []Span    `json:"breaks,omitempty"`
	// CommercialSec is the total length of the breaks found.
	CommercialSec float64 `json:"commercialSec,omitempty"`
	// Percent is the share of the recording judged to be commercials.
	// Network TV in North America runs about 25-30%; public TV about 0.
	Percent float64 `json:"percent,omitempty"`
	Output  string  `json:"output,omitempty"`
}

type comskipTests struct {
	mu    sync.Mutex
	items map[string]*ComskipTest
}

// TestComskip starts a trial scan in the background. Empty settings use
// what the recording's channel would use now.
func (s *Service) TestComskip(id, settings string) (ComskipTest, error) {
	rec, ok := s.Recording(id)
	if !ok {
		return ComskipTest{}, ErrNotFound
	}
	p := s.originalPath(rec)
	if p == "" {
		return ComskipTest{}, errors.New("the original broadcast file was not kept for this recording")
	}
	if _, err := os.Stat(p); err != nil {
		return ComskipTest{}, errors.New("the original broadcast file is gone")
	}
	if strings.TrimSpace(settings) == "" {
		settings, _ = s.iniFor(rec.Channel)
	}
	t := &ComskipTest{ID: newID(), RecordingID: id, Title: rec.Title, Channel: rec.Channel, Started: time.Now()}
	s.tests.mu.Lock()
	if s.tests.items == nil {
		s.tests.items = map[string]*ComskipTest{}
	}
	for k, v := range s.tests.items {
		if time.Since(v.Started) > 6*time.Hour {
			delete(s.tests.items, k)
		}
	}
	s.tests.items[t.ID] = t
	s.tests.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		dur := s.duration(ctx, p)
		spans, out, err := s.runComskip(ctx, p, settings, filepath.Join(s.cfg.DataDir, "comskip-tests", t.ID))
		_ = os.RemoveAll(filepath.Join(s.cfg.DataDir, "comskip-tests", t.ID))
		s.tests.mu.Lock()
		defer s.tests.mu.Unlock()
		t.Done = true
		t.Output = out
		t.DurationSec = dur
		if err != nil {
			t.Error = err.Error()
			return
		}
		t.Breaks = spans
		for _, sp := range spans {
			t.CommercialSec += sp.End - sp.Start
		}
		if dur > 0 {
			t.Percent = 100 * t.CommercialSec / dur
		}
	}()
	return *t, nil
}

// ComskipTestResult reports a trial scan, finished or not.
func (s *Service) ComskipTestResult(id string) (ComskipTest, bool) {
	s.tests.mu.Lock()
	defer s.tests.mu.Unlock()
	t, ok := s.tests.items[id]
	if !ok {
		return ComskipTest{}, false
	}
	out := *t
	out.Breaks = append([]Span(nil), t.Breaks...)
	return out, true
}

// KeptOriginals lists recordings whose broadcast file is still there,
// newest first, for choosing what to test on.
func (s *Service) KeptOriginals() []Recording {
	var out []Recording
	for _, r := range s.Recordings("") {
		if p := s.originalPath(r); p != "" {
			if _, err := os.Stat(p); err == nil {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Airing > out[j].Airing })
	return out
}
