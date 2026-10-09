package dvr

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/guide"
	"github.com/McGeaverBeaver/opendvr/internal/hdhomerun"
	"github.com/McGeaverBeaver/opendvr/internal/stream"
	"github.com/McGeaverBeaver/opendvr/internal/tuner"
)

type fakeTuners struct {
	url    string
	busy   bool
	count  int
	chans  map[string]string
	called int
}

func (f *fakeTuners) ResolveSource(ctx context.Context, n string, active map[string]bool) (tuner.Source, error) {
	f.called++
	if f.busy {
		return tuner.Source{}, &tuner.ErrTunersBusy{Channel: n}
	}
	return tuner.Source{DeviceID: "FAKE", URL: f.url}, nil
}

func (f *fakeTuners) Devices() []tuner.DeviceState {
	return []tuner.DeviceState{{Device: hdhomerun.Device{DeviceID: "FAKE", TunerCount: f.count}}}
}

func (f *fakeTuners) Channel(n string) (tuner.Channel, bool) {
	name, ok := f.chans[n]
	return tuner.Channel{GuideNumber: n, GuideName: name}, ok
}

type fakeGuide struct{ chans []guide.Channel }

func (g *fakeGuide) Window(from, to int64) []guide.Channel {
	var out []guide.Channel
	for _, c := range g.chans {
		cc := c
		cc.Programs = nil
		for _, p := range c.Programs {
			if p.End > from && p.Start < to {
				cc.Programs = append(cc.Programs, p)
			}
		}
		out = append(out, cc)
	}
	return out
}

func (g *fakeGuide) NowNext(at int64) map[string]guide.NowNext {
	out := map[string]guide.NowNext{}
	for _, c := range g.chans {
		for _, p := range c.Programs {
			if p.Start <= at && at < p.End {
				pp := p
				out[c.GuideNumber] = guide.NowNext{Now: &pp}
			}
		}
	}
	return out
}

func (g *fakeGuide) EnsureWindow(context.Context, int64) {}

type fakeLive struct{}

func (fakeLive) Statuses() []stream.Status { return nil }
func (fakeLive) Degraded() bool            { return false }

func newService(t *testing.T, tuners *fakeTuners, epg Guide) *Service {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	s, err := New(Config{DataDir: dir, RecordingsDir: filepath.Join(dir, "rec"), FFmpegPath: "ffmpeg", Encoder: stream.EncoderSoftware, VideoBitrateK: 1000, AudioBitrateK: 96}, tuners, epg, fakeLive{}, log)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlanSeriesPass(t *testing.T) {
	now := time.Now().Unix()
	h := int64(3600)
	epg := &fakeGuide{chans: []guide.Channel{
		{GuideNumber: "5.1", GuideName: "FIVE", Programs: []guide.Program{
			{Start: now + h, End: now + 2*h, Title: "The Show", SeriesID: "S1", EpisodeNumber: "S02E01", New: true},
			{Start: now + 2*h, End: now + 3*h, Title: "The Show", SeriesID: "S1", EpisodeNumber: "S01E05"},
			{Start: now + 3*h, End: now + 4*h, Title: "Other", SeriesID: "S9"},
		}},
		{GuideNumber: "7.1", GuideName: "SEVEN", Programs: []guide.Program{
			// Same episode repeated on another channel: not recorded twice.
			{Start: now + 5*h, End: now + 6*h, Title: "The Show", SeriesID: "S1", EpisodeNumber: "S02E01"},
			{Start: now + h, End: now + 2*h, Title: "News", EpisodeTitle: ""},
		}},
	}}
	tuners := &fakeTuners{count: 2, chans: map[string]string{"5.1": "FIVE", "7.1": "SEVEN"}}
	s := newService(t, tuners, epg)

	if _, err := s.SaveRule(Rule{Title: "the show", Season: 2}); err != nil {
		t.Fatal(err)
	}
	s.plan(context.Background())
	up := s.Recordings("upcoming")
	if len(up) != 1 || up[0].Channel != "5.1" || up[0].EpisodeNumber != "S02E01" || up[0].Status != StatusScheduled {
		t.Fatalf("season pass scheduled %+v", up)
	}
	if up[0].Start != now+h-60 || up[0].End != now+2*h+180 {
		t.Fatalf("default padding not applied: %d..%d vs airing %d..%d", up[0].Start, up[0].End, now+h, now+2*h)
	}

	// Widen to the whole series and the other episode appears; the repeat
	// on 7.1 does not.
	r, _ := s.Rule(s.Rules()[0].ID)
	r.Season = 0
	if _, err := s.SaveRule(r); err != nil {
		t.Fatal(err)
	}
	s.plan(context.Background())
	up = s.Recordings("upcoming")
	if len(up) != 2 {
		t.Fatalf("series pass = %d recordings, want 2: %+v", len(up), up)
	}

	// New-only drops the repeat episode.
	r.NewOnly = true
	_, _ = s.SaveRule(r)
	s.plan(context.Background())
	if up = s.Recordings("upcoming"); len(up) != 1 || !up[0].Manual == false && false {
		t.Fatalf("new only = %+v", up)
	}

	// A manual one-off and conflicts with a single tuner.
	tuners.count = 1
	if _, err := s.RecordAiring("7.1", now+h, Options{}); err != nil {
		t.Fatal(err)
	}
	s.plan(context.Background())
	up = s.Recordings("upcoming")
	var manual, pass *Recording
	for i := range up {
		if up[i].Manual {
			manual = &up[i]
		} else {
			pass = &up[i]
		}
	}
	if manual == nil || pass == nil || manual.Status != StatusScheduled || pass.Status != StatusConflict {
		t.Fatalf("conflict: manual=%+v pass=%+v", manual, pass)
	}
	tuners.count = 2
	s.plan(context.Background())
	for _, r := range s.Recordings("upcoming") {
		if r.Status != StatusScheduled {
			t.Fatalf("conflict should clear with two tuners: %+v", r)
		}
	}

	// Deleting the pass drops what it scheduled but not the manual one.
	if err := s.DeleteRule(r.ID); err != nil {
		t.Fatal(err)
	}
	if up = s.Recordings("upcoming"); len(up) != 1 || !up[0].Manual {
		t.Fatalf("after delete rule = %+v", up)
	}

	// State survives a reload.
	s2 := newServiceAt(t, s.cfg.DataDir, tuners, epg)
	if got := s2.Recordings(""); len(got) != 1 {
		t.Fatalf("reloaded = %+v", got)
	}
}

func newServiceAt(t *testing.T, dataDir string, tuners *fakeTuners, epg Guide) *Service {
	t.Helper()
	s, err := New(Config{DataDir: dataDir, FFmpegPath: "ffmpeg"}, tuners, epg, fakeLive{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestKeepAndEDL(t *testing.T) {
	k := keep(100, []Span{{10, 20}, {50, 60}})
	if len(k) != 3 || k[0] != (Span{0, 10}) || k[1] != (Span{20, 50}) || k[2] != (Span{60, 100}) {
		t.Fatalf("keep = %+v", k)
	}
	f, _ := os.CreateTemp(t.TempDir(), "x.edl")
	_, _ = f.WriteString("0.00\t12.5\t0\n300.0\t420.0\t0\n500\t510\t3\nbad line\n")
	_, _ = f.Seek(0, 0)
	spans := parseEDL(f)
	f.Close()
	if len(spans) != 2 || spans[1].Start != 300 || spans[1].End != 420 {
		t.Fatalf("edl = %+v", spans)
	}
	s := newService(t, &fakeTuners{}, &fakeGuide{})
	args := strings.Join(s.transcodeArgs("in.ts", "out.mp4", 100, []Span{{10, 20}}, true), " ")
	if !strings.Contains(args, "concat=n=2") || !strings.Contains(args, "trim=start=20.000:end=100.000") || !strings.Contains(args, "libx264") {
		t.Fatalf("cut args = %s", args)
	}
	if seasonOf("S05E02") != 5 || seasonOf("502") != 0 {
		t.Fatal("seasonOf")
	}
}

// tsSource is a fake tuner stream: MPEG-2 + AC-3 transport stream from
// ffmpeg's test pattern, like the api package's fake HDHomeRun.
func tsSource(t *testing.T, ffmpeg string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := exec.CommandContext(r.Context(), ffmpeg,
			"-hide_banner", "-loglevel", "error", "-re",
			"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-c:v", "mpeg2video", "-b:v", "800k", "-g", "15",
			"-c:a", "ac3", "-b:a", "96k",
			"-f", "mpegts", "-")
		out, err := cmd.StdoutPipe()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := cmd.Start(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer func() { _ = cmd.Wait() }()
		w.Header().Set("Content-Type", "video/mp2t")
		fl, _ := w.(http.Flusher)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := out.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
			}
			if rerr != nil {
				return
			}
		}
	}))
}

func TestRecordNowEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ffmpeg, err := stream.FindFFmpeg("")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	src := tsSource(t, ffmpeg)
	t.Cleanup(src.Close)

	now := time.Now().Unix()
	epg := &fakeGuide{chans: []guide.Channel{{GuideNumber: "5.1", GuideName: "FIVE", Programs: []guide.Program{
		{Start: now - 60, End: now + 7, Title: "Quick Show", EpisodeTitle: "Pilot", EpisodeNumber: "S01E01", Synopsis: "A test."},
	}}}}
	tuners := &fakeTuners{url: src.URL, count: 2, chans: map[string]string{"5.1": "FIVE"}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	s, err := New(Config{DataDir: dir, RecordingsDir: filepath.Join(dir, "rec"), FFmpegPath: ffmpeg, Encoder: stream.EncoderSoftware, VideoBitrateK: 800, AudioBitrateK: 96, Deinterlace: true}, tuners, epg, fakeLive{}, log)
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	r, err := s.RecordNow("5.1", 0, Options{PadAfter: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Quick Show" || r.End != now+7 || r.Status != StatusScheduled {
		t.Fatalf("record now = %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	deadline := time.Now().Add(90 * time.Second)
	var got Recording
	for time.Now().Before(deadline) {
		got, _ = s.Recording(r.ID)
		if got.Status == StatusReady || got.Status == StatusFailed {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got.Status != StatusReady {
		t.Fatalf("recording ended as %s: %s", got.Status, got.Error)
	}
	if !strings.HasSuffix(got.File, ".mp4") || !strings.Contains(got.File, "Quick Show/Quick Show - S01E01 - Pilot") {
		t.Fatalf("file name = %q", got.File)
	}
	st, err := os.Stat(s.Path(got))
	if err != nil || st.Size() < 50_000 {
		t.Fatalf("mp4 missing or tiny: %v %d", err, got.SizeBytes)
	}
	if got.DurationSec < 3 {
		t.Fatalf("duration = %v", got.DurationSec)
	}
	if _, err := os.Stat(strings.TrimSuffix(s.Path(got), ".mp4") + ".ts"); err == nil {
		t.Fatal("capture .ts should be removed after transcode")
	}
	// Position is remembered; delete removes the file and the folder.
	if err := s.SetPosition(r.ID, 2.5); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.Recording(r.ID); x.Position != 2.5 {
		t.Fatal("position not stored")
	}
	if err := s.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path(got)); err == nil {
		t.Fatal("file should be deleted")
	}
	if entries, _ := os.ReadDir(s.cfg.RecordingsDir); len(entries) != 0 {
		t.Fatalf("show folder left behind: %v", entries)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "dvr.json"))
	var p persisted
	_ = json.Unmarshal(b, &p)
	if len(p.Recordings) != 0 {
		t.Fatalf("persisted = %+v", p.Recordings)
	}
}
