package stream

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/tuner"
)

// A real tuner hands us a transport stream from wherever the broadcast
// happens to be, usually in the middle of a GOP with no sequence header in
// sight for up to half a second. This fake does the same: it generates a
// long-GOP MPEG-2 stream, then serves it from a byte offset mid-GOP at
// real-time pace.
func midGopTuner(t *testing.T, ffmpeg string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src.ts")
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "20",
		"-c:v", "mpeg2video", "-b:v", "4000k", "-g", "300", "-flags", "+ilme+ildct",
		"-c:a", "ac3", "-b:a", "192k",
		"-f", "mpegts", src)
	gen.Stderr = os.Stderr
	if err := gen.Run(); err != nil {
		t.Fatalf("generate source: %v", err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	// Start 35% in, aligned to a TS packet, so the first picture the
	// decoder sees has no preceding sequence header.
	offset := (len(data) * 35 / 100) / 188 * 188
	payload := data[offset:]
	bytesPerSec := len(data) / 20

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		fl, _ := w.(http.Flusher)
		const chunk = 64 * 1024
		for i := 0; i < len(payload); i += chunk {
			end := i + chunk
			if end > len(payload) {
				end = len(payload)
			}
			if _, err := w.Write(payload[i:end]); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(time.Duration(float64(end-i) / float64(bytesPerSec) * float64(time.Second)))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/auto/v5.1"
}

type staticSource struct{ url string }

func (s staticSource) ResolveSource(context.Context, string, map[string]bool) (tuner.Source, error) {
	return tuner.Source{DeviceID: "FAKE", URL: s.url}, nil
}

func TestSessionStartsFromMidGop(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ffmpeg, err := FindFFmpeg("")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	url := midGopTuner(t, ffmpeg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := NewManager(Config{
		FFmpegPath:     ffmpeg,
		BufferDir:      t.TempDir(),
		Encoder:        EncoderSoftware,
		SegmentSeconds: 2,
		WindowSegments: 30,
		IdleTimeout:    time.Minute,
		ReadyTimeout:   15 * time.Second,
		VideoBitrateK:  1500,
		AudioBitrateK:  96,
		Deinterlace:    true,
	}, staticSource{url}, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)

	start := time.Now()
	s, err := m.Acquire(context.Background(), "5.1")
	if err != nil {
		for _, f := range m.Failures() {
			t.Logf("failure: %s\n%s", f.Error, joinLines(f.LogTail))
		}
		t.Fatalf("acquire: %v", err)
	}
	t.Logf("ready after %s with %d segments", time.Since(start).Round(time.Millisecond), countSegments(s.PlaylistPath()))
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += "  " + l + "\n"
	}
	return out
}
