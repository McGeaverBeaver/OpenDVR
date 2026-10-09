package dvr

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestComskipSettings(t *testing.T) {
	s := newService(t, &fakeTuners{}, &fakeGuide{})
	// The near-empty file earlier versions wrote is replaced by the defaults.
	old := "; comskip settings used by opendvr. Edit freely; see the comskip\noutput_edl=1\n"
	_ = os.WriteFile(s.defaultINIPath(), []byte(old), 0o644)
	text, off := s.iniFor("5.1")
	if off || !strings.Contains(text, "detect_method=111") {
		t.Fatalf("defaults not installed: %q", text)
	}
	// A settings file someone edited is left alone.
	_ = os.WriteFile(s.defaultINIPath(), []byte("detect_method=43\n"), 0o644)
	if text, _ := s.iniFor("5.1"); text != "detect_method=43\n" {
		t.Fatalf("edited defaults overwritten: %q", text)
	}
	// Per-channel override and the off switch.
	if err := s.SaveComskipSettings("17.1", "opendvr_disable=1\n"); err != nil {
		t.Fatal(err)
	}
	if _, off := s.iniFor("17.1"); !off {
		t.Fatal("detection should be off for 17.1")
	}
	if _, off := s.iniFor("5.1"); off {
		t.Fatal("other channels must keep detection")
	}
	if got := s.ComskipSettings(); got.Channels["17.1"] == "" || got.Default != "detect_method=43\n" {
		t.Fatalf("settings = %+v", got)
	}
	if err := s.SaveComskipSettings("17.1", ""); err != nil {
		t.Fatal(err)
	}
	if _, off := s.iniFor("17.1"); off {
		t.Fatal("removing the override should restore the default")
	}
}

func TestOriginalExpires(t *testing.T) {
	s := newService(t, &fakeTuners{}, &fakeGuide{})
	if err := os.MkdirAll(s.cfg.RecordingsDir+"/Show", 0o755); err != nil {
		t.Fatal(err)
	}
	ts := s.cfg.RecordingsDir + "/Show/a.ts"
	_ = os.WriteFile(ts, []byte("x"), 0o644)
	s.mu.Lock()
	s.recs["a"] = &Recording{ID: "a", Status: StatusReady, File: "Show/a.mp4", Original: "Show/a.ts", OriginalUntil: time.Now().Add(-time.Minute)}
	s.recs["b"] = &Recording{ID: "b", Status: StatusReady, File: "Show/b.mp4", Original: "Show/b.ts", OriginalUntil: time.Now().Add(time.Hour)}
	s.expireOriginalsLocked(time.Now())
	s.mu.Unlock()
	if _, err := os.Stat(ts); err == nil {
		t.Fatal("expired original kept")
	}
	if r, _ := s.Recording("a"); r.Original != "" {
		t.Fatal("expired original still listed")
	}
	if r, _ := s.Recording("b"); r.Original == "" {
		t.Fatal("unexpired original dropped")
	}
	if _, err := s.Redetect("a", ""); err == nil {
		t.Fatal("redetect without an original accepted")
	}
}
