package guide

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func fakeGuideAPI(t *testing.T, now int64, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	h := func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("DeviceAuth") == "" {
			http.Error(w, "missing DeviceAuth", 400)
			return
		}
		ch := r.URL.Query().Get("Channel")
		start := r.URL.Query().Get("Start")
		if ch == "5.1" && start != "" {
			// Extended window: two more hours from the requested start.
			s, _ := strconv.ParseInt(start, 10, 64)
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"GuideNumber": "5.1", "GuideName": "FIVE",
				"Guide": []map[string]any{
					{"StartTime": s, "EndTime": s + 3600, "Title": "Later One"},
					{"StartTime": s + 3600, "EndTime": s + 7200, "Title": "Later Two"},
				},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"GuideNumber": "5.1", "GuideName": "FIVE", "Affiliate": "Net", "ImageURL": "http://img/5.png",
				"Guide": []map[string]any{
					{"StartTime": now - 1800, "EndTime": now + 1800, "Title": "Now Show", "EpisodeNumber": "S1E2", "First": 1},
					{"StartTime": now + 1800, "EndTime": now + 5400, "Title": "Next Show"},
				},
			},
			{
				"GuideNumber": "11.1", "GuideName": "ELEVEN",
				"Guide": []map[string]any{
					{"StartTime": now + 600, "EndTime": now + 4200, "Title": "Soon"},
				},
			},
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(srv.Close)
	return srv
}

func TestRefreshNowNextAndWindow(t *testing.T) {
	now := time.Now().Unix()
	var calls atomic.Int32
	api := fakeGuideAPI(t, now, &calls)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(api.URL, func() []string { return []string{"AUTH"} }, "", log)

	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	nn := s.NowNext(now)
	if nn["5.1"].Now == nil || nn["5.1"].Now.Title != "Now Show" || !nn["5.1"].Now.New {
		t.Fatalf("5.1 now = %+v", nn["5.1"].Now)
	}
	if nn["5.1"].Next == nil || nn["5.1"].Next.Title != "Next Show" {
		t.Fatalf("5.1 next = %+v", nn["5.1"].Next)
	}
	if nn["11.1"].Now != nil || nn["11.1"].Next == nil || nn["11.1"].Next.Title != "Soon" {
		t.Fatalf("11.1 = %+v", nn["11.1"])
	}

	win := s.Window(now, now+900)
	if len(win) != 2 || win[0].GuideNumber != "5.1" || len(win[0].Programs) != 1 || len(win[1].Programs) != 1 {
		t.Fatalf("window = %+v", win)
	}
	st := s.Status()
	if st.Channels != 2 || st.Programs != 3 || st.LastRefresh.IsZero() {
		t.Fatalf("status = %+v", st)
	}
}

func TestRefreshReplacesChangedListings(t *testing.T) {
	now := time.Now().Unix()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New("http://unused", func() []string { return nil }, "", log)
	s.merge([]wireChannel{{GuideNumber: "5.1", Guide: []wireProgram{
		{StartTime: now, EndTime: now + 3600, Title: "Old"},
		{StartTime: now + 3600, EndTime: now + 7200, Title: "Stays"},
	}}})
	// A later fetch replaces the entry at the same start time; entries at
	// other start times are kept.
	s.merge([]wireChannel{{GuideNumber: "5.1", Guide: []wireProgram{
		{StartTime: now, EndTime: now + 3600, Title: "New"},
	}}})
	c, _ := s.Channel("5.1")
	if len(c.Programs) != 2 || c.Programs[0].Title != "New" || c.Programs[1].Title != "Stays" {
		t.Fatalf("programs = %+v", c.Programs)
	}
	// A schedule change that makes the first programme overlap the second
	// drops the overlapped one rather than drawing two blocks on top of
	// each other.
	s.merge([]wireChannel{{GuideNumber: "5.1", Guide: []wireProgram{
		{StartTime: now, EndTime: now + 5400, Title: "Longer"},
	}}})
	c, _ = s.Channel("5.1")
	for i := 1; i < len(c.Programs); i++ {
		if c.Programs[i-1].End > c.Programs[i].Start {
			t.Fatalf("overlap not resolved: %+v", c.Programs)
		}
	}
}

func TestEnsureWindowExtendsAndCoolsDown(t *testing.T) {
	now := time.Now().Unix()
	var calls atomic.Int32
	api := fakeGuideAPI(t, now, &calls)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	s := New(api.URL, func() []string { return []string{"AUTH"} }, dir, log)
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	s.EnsureWindow(context.Background(), now+6*3600)
	c, _ := s.Channel("5.1")
	if len(c.Programs) != 4 || c.Programs[3].Title != "Later Two" {
		t.Fatalf("5.1 after extend = %+v", c.Programs)
	}
	// Both channels were short, so two requests; a second call within the
	// cooldown makes none.
	if got := calls.Load() - before; got != 2 {
		t.Fatalf("extend requests = %d, want 2", got)
	}
	s.EnsureWindow(context.Background(), now+6*3600)
	if got := calls.Load() - before; got != 2 {
		t.Fatalf("cooldown ignored: requests = %d", got)
	}
	// Asking for a further window than last time is not a repeat and
	// fetches again (the fake returns the same two hours, so 5.1 still
	// ends short of the target and is retried; 11.1 likewise).
	s.EnsureWindow(context.Background(), now+12*3600)
	if got := calls.Load() - before; got != 4 {
		t.Fatalf("further window should fetch: requests = %d", got)
	}

	// Persistence: a fresh service on the same directory starts populated.
	s2 := New(api.URL, func() []string { return nil }, dir, log)
	if st := s2.Status(); st.Channels != 2 || st.Programs < 4 {
		t.Fatalf("reloaded status = %+v", st)
	}
}
