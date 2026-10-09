package dvr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/guide"
)

func TestManualOccurrences(t *testing.T) {
	loc := time.Local
	// A Wednesday 2026-10-07 at noon.
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, loc)
	r := Rule{Kind: RuleKindManual, TimeOfDay: "18:30", DurationMin: 60, Days: []int{3, 5}} // Wed, Fri
	got := r.occurrences(base.Unix(), base.Add(7*24*time.Hour).Unix())
	if len(got) != 2 {
		t.Fatalf("occurrences = %d, want 2 (Wed, Fri): %v", len(got), got)
	}
	if time.Unix(got[0], 0).In(loc).Format("Mon 15:04") != "Wed 18:30" || time.Unix(got[1], 0).In(loc).Format("Mon 15:04") != "Fri 18:30" {
		t.Fatalf("wrong slots: %s %s", time.Unix(got[0], 0).In(loc), time.Unix(got[1], 0).In(loc))
	}
	// A slot already under way is still wanted; one that ended is not.
	r2 := Rule{Kind: RuleKindManual, TimeOfDay: "11:30", DurationMin: 60, Days: []int{3}}
	if got := r2.occurrences(base.Unix(), base.Add(time.Hour).Unix()); len(got) != 1 {
		t.Fatalf("running slot = %v", got)
	}
	r3 := Rule{Kind: RuleKindManual, TimeOfDay: "09:00", DurationMin: 60, Date: "2026-10-07"}
	if got := r3.occurrences(base.Unix(), base.Add(24*time.Hour).Unix()); len(got) != 0 {
		t.Fatalf("finished slot = %v", got)
	}
	r4 := Rule{Kind: RuleKindManual, TimeOfDay: "21:00", DurationMin: 30, Date: "2026-10-08"}
	if got := r4.occurrences(base.Unix(), base.Add(48*time.Hour).Unix()); len(got) != 1 {
		t.Fatalf("dated slot = %v", got)
	}
}

func TestManualRulePlans(t *testing.T) {
	tuners := &fakeTuners{count: 2, chans: map[string]string{"5.1": "FIVE"}}
	s := newService(t, tuners, &fakeGuide{})
	now := time.Now()
	slot := now.Add(2 * time.Hour)
	r, err := s.SaveRule(Rule{Kind: RuleKindManual, Channel: "5.1", TimeOfDay: slot.Format("15:04"), DurationMin: 45, Date: slot.Format("2006-01-02"), Title: "Evening slot"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Evening slot" || r.Kind != RuleKindManual {
		t.Fatalf("rule = %+v", r)
	}
	if _, err := s.SaveRule(Rule{Kind: RuleKindManual, Channel: "5.1", TimeOfDay: "25:00", DurationMin: 10, Days: []int{1}}); err == nil {
		t.Fatal("bad time accepted")
	}
	s.plan(t.Context())
	up := s.Recordings("upcoming")
	if len(up) != 1 || up[0].RuleID != r.ID || up[0].AiringEnd-up[0].Airing != 45*60 || up[0].ChannelName != "FIVE" {
		t.Fatalf("planned = %+v", up)
	}
	// Watched bookkeeping.
	rec := up[0]
	rec.Status = StatusReady
	s.mu.Lock()
	s.recs[rec.ID].Status = StatusReady
	s.mu.Unlock()
	if err := s.MarkWatched(rec.ID, true); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.Recording(rec.ID); !x.Watched || x.WatchedAt.IsZero() {
		t.Fatalf("watched = %+v", x)
	}
}

func TestStorageCap(t *testing.T) {
	tuners := &fakeTuners{count: 2, chans: map[string]string{"5.1": "FIVE"}}
	s := newService(t, tuners, &fakeGuide{})
	s.cfg.MaxBytes = 250
	s.mu.Lock()
	for i, size := range []int64{100, 100, 100} {
		id := "r" + string(rune('a'+i))
		s.recs[id] = &Recording{ID: id, Title: "T", Status: StatusReady, SizeBytes: size, Airing: int64(1000 + i), Watched: i == 2}
	}
	s.sweepStorageLocked()
	s.mu.Unlock()
	left := s.Recordings("")
	if len(left) != 2 {
		t.Fatalf("cap left %d recordings", len(left))
	}
	for _, r := range left {
		if r.ID == "rc" {
			t.Fatal("the watched recording should go first")
		}
	}
}

func TestRemapVTT(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.vtt")
	src := "WEBVTT\n\n00:00:05.000 --> 00:00:08.000\nhello\n\n00:00:12.000 --> 00:00:14.000\nad\n\n00:00:25.000 --> 00:00:27.000\nback\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// Commercials 10..20 removed: keep 0..10 and 20..100.
	if err := remapVTT(p, []Span{{0, 10}, {20, 100}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	out := string(b)
	if !strings.Contains(out, "00:00:05.000 --> 00:00:08.000\nhello") {
		t.Fatalf("first cue changed: %s", out)
	}
	if strings.Contains(out, "ad") {
		t.Fatalf("cue inside the cut survived: %s", out)
	}
	if !strings.Contains(out, "00:00:15.000 --> 00:00:17.000\nback") {
		t.Fatalf("cue after the cut not shifted: %s", out)
	}
}

func TestEditScheduled(t *testing.T) {
	now := time.Now().Unix()
	epg := &fakeGuide{chans: []guide.Channel{{GuideNumber: "5.1", GuideName: "FIVE", Programs: []guide.Program{
		{Start: now + 3600, End: now + 7200, Title: "Show"},
	}}}}
	s := newService(t, &fakeTuners{count: 2, chans: map[string]string{"5.1": "FIVE"}}, epg)
	r, err := s.RecordAiring("5.1", now+3600, Options{})
	if err != nil {
		t.Fatal(err)
	}
	five, thirty := 5, 30
	got, err := s.Edit(r.ID, Options{PadBefore: &five, PadAfter: &thirty, Commercials: CommercialsNone})
	if err != nil {
		t.Fatal(err)
	}
	if got.Start != now+3600-300 || got.End != now+7200+1800 || got.Commercials != CommercialsNone {
		t.Fatalf("edited = %+v", got)
	}
	bad := -1
	if _, err := s.Edit(r.ID, Options{PadAfter: &bad}); err == nil {
		t.Fatal("negative padding accepted")
	}
	s.mu.Lock()
	s.recs[r.ID].Status = StatusReady
	s.mu.Unlock()
	if _, err := s.Edit(r.ID, Options{PadAfter: &five}); err != ErrNotEditable {
		t.Fatalf("finished recording edited: %v", err)
	}
}
