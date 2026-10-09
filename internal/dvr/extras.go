package dvr

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- time-slot recordings ----------------------------------------------------

// RuleKindSeries matches programmes in the guide; RuleKindManual records
// a channel at a time of day regardless of what is on.
const (
	RuleKindSeries = "series"
	RuleKindManual = "manual"
)

var timeOfDayRe = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)$`)

// validateManual checks a time-slot rule and fills its defaults.
func (s *Service) validateManual(r *Rule) error {
	if r.Channel == "" {
		return fmt.Errorf("a time-slot recording needs a channel")
	}
	if _, ok := s.tuners.Channel(r.Channel); !ok {
		return fmt.Errorf("unknown channel %s", r.Channel)
	}
	if !timeOfDayRe.MatchString(r.TimeOfDay) {
		return fmt.Errorf("time must be HH:MM")
	}
	if r.DurationMin <= 0 || r.DurationMin > 24*60 {
		return fmt.Errorf("duration must be between 1 and 1440 minutes")
	}
	if r.Date != "" {
		if _, err := time.ParseInLocation("2006-01-02", r.Date, time.Local); err != nil {
			return fmt.Errorf("date must be YYYY-MM-DD")
		}
		r.Days = nil
	} else if len(r.Days) == 0 {
		return fmt.Errorf("pick the days of the week, or a single date")
	}
	for _, d := range r.Days {
		if d < 0 || d > 6 {
			return fmt.Errorf("days are 0 (Sunday) to 6 (Saturday)")
		}
	}
	if r.Title == "" {
		ch, _ := s.tuners.Channel(r.Channel)
		r.Title = ch.GuideName + " " + r.TimeOfDay
	}
	return nil
}

// occurrences lists the slots a manual rule has between from and to.
func (r *Rule) occurrences(from, to int64) []int64 {
	m := timeOfDayRe.FindStringSubmatch(r.TimeOfDay)
	if m == nil {
		return nil
	}
	hh, _ := strconv.Atoi(m[1])
	mm, _ := strconv.Atoi(m[2])
	var out []int64
	if r.Date != "" {
		d, err := time.ParseInLocation("2006-01-02", r.Date, time.Local)
		if err != nil {
			return nil
		}
		t := time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, time.Local).Unix()
		if t+int64(r.DurationMin)*60 > from && t < to {
			out = append(out, t)
		}
		return out
	}
	days := map[int]bool{}
	for _, d := range r.Days {
		days[d] = true
	}
	start := time.Unix(from, 0).In(time.Local)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	for day.Unix() < to {
		if days[int(day.Weekday())] {
			t := time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, time.Local).Unix()
			if t+int64(r.DurationMin)*60 > from && t < to {
				out = append(out, t)
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return out
}

// manualRecording builds the recording for one slot of a manual rule,
// titled after what the guide says is on if it knows.
func (s *Service) manualRecording(r *Rule, at int64) *Recording {
	ch, _ := s.tuners.Channel(r.Channel)
	rec := &Recording{
		ID: newID(), RuleID: r.ID, Channel: r.Channel, ChannelName: ch.GuideName,
		Title: r.Title, Airing: at, AiringEnd: at + int64(r.DurationMin)*60,
		Created: time.Now(), Status: StatusScheduled,
	}
	if nn, ok := s.epg.NowNext(at)[r.Channel]; ok && nn.Now != nil {
		rec.EpisodeTitle = nn.Now.Title
		rec.Synopsis = nn.Now.Synopsis
		rec.ImageURL = nn.Now.ImageURL
	}
	return rec
}

// ---- watched and storage -------------------------------------------------------

// MarkWatched records that a recording has been watched to the end.
func (s *Service) MarkWatched(id string, watched bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	if !ok {
		return ErrNotFound
	}
	r.Watched = watched
	if watched {
		r.WatchedAt = time.Now()
		r.Position = 0
	} else {
		r.WatchedAt = time.Time{}
	}
	s.saveLocked()
	return nil
}

// sweepStorageLocked applies the keep-space rule and deletes watched
// recordings whose pass asked for that, oldest first. Called from plan.
func (s *Service) sweepStorageLocked() {
	now := time.Now()
	s.expireOriginalsLocked(now)
	var ready []*Recording
	var used int64
	for _, r := range s.recs {
		if r.Status == StatusReady {
			ready = append(ready, r)
		}
		used += r.SizeBytes
		if r.Original != "" {
			if st, err := os.Stat(filepath.Join(s.cfg.RecordingsDir, filepath.FromSlash(r.Original))); err == nil {
				used += st.Size()
			}
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].Airing < ready[j].Airing })

	// Watched recordings a pass wants gone, a day after they were watched.
	for _, r := range ready {
		rule, ok := s.rules[r.RuleID]
		if ok && rule.DeleteWatched && r.Watched && now.Sub(r.WatchedAt) > 24*time.Hour {
			s.removeLocked(r, "watched")
			used -= r.SizeBytes
		}
	}
	if s.cfg.MaxBytes <= 0 || used <= s.cfg.MaxBytes {
		return
	}
	// Over the cap: watched ones first, then the oldest.
	order := append([]*Recording(nil), ready...)
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].Watched != order[j].Watched {
			return order[i].Watched
		}
		return order[i].Airing < order[j].Airing
	})
	for _, r := range order {
		if used <= s.cfg.MaxBytes {
			break
		}
		if _, still := s.recs[r.ID]; !still {
			continue
		}
		s.removeLocked(r, "over the storage cap")
		used -= r.SizeBytes
	}
}

func (s *Service) removeLocked(r *Recording, why string) {
	for _, p := range s.filesOf(*r) {
		_ = os.Remove(p)
	}
	delete(s.recs, r.ID)
	s.log.Info("recording removed", "id", r.ID, "title", r.Title, "why", why)
}

// ---- closed captions -----------------------------------------------------

// extractCaptions pulls the broadcast's CEA-608 captions out of the
// capture as WebVTT next to the final file. When commercials were cut,
// cue times are shifted so they still line up.
func (s *Service) extractCaptions(ctx context.Context, ts, vtt string, keepSpans []Span) bool {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	src := strings.ReplaceAll(filepath.ToSlash(ts), ":", "\\:")
	args := []string{
		"-hide_banner", "-nostats", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "movie=" + src + "[out0+subcc]",
		"-map", "0:s:0", "-c:s", "webvtt", vtt,
	}
	out, err := exec.CommandContext(cctx, s.cfg.FFmpegPath, args...).CombinedOutput()
	if err != nil {
		s.log.Debug("caption extraction failed", "err", err, "out", strings.TrimSpace(string(out)))
		_ = os.Remove(vtt)
		return false
	}
	st, err := os.Stat(vtt)
	if err != nil || st.Size() < 20 {
		_ = os.Remove(vtt)
		return false
	}
	if len(keepSpans) > 0 {
		if err := remapVTT(vtt, keepSpans); err != nil {
			s.log.Warn("caption remap failed", "err", err)
		}
	}
	return true
}

var vttTime = regexp.MustCompile(`(\d{2}):(\d{2}):(\d{2})\.(\d{3})`)

func parseVTTTime(m []string) float64 {
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	sec, _ := strconv.Atoi(m[3])
	ms, _ := strconv.Atoi(m[4])
	return float64(h*3600+mi*60+sec) + float64(ms)/1000
}

func fmtVTTTime(t float64) string {
	if t < 0 {
		t = 0
	}
	ms := int(t*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// remapVTT rewrites cue times after commercials were cut: a time inside
// a kept span moves back by the total length removed before it; cues
// inside removed spans are dropped.
func remapVTT(path string, keep []Span) error {
	in, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	mapTime := func(t float64) (float64, bool) {
		removed := 0.0
		prevEnd := 0.0
		for _, k := range keep {
			removed += k.Start - prevEnd
			if t >= k.Start && t < k.End {
				return t - removed, true
			}
			prevEnd = k.End
		}
		return 0, false
	}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(string(in)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	skipCue := false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "-->") {
			times := vttTime.FindAllStringSubmatch(line, 2)
			if len(times) == 2 {
				a, okA := mapTime(parseVTTTime(times[0]))
				b, okB := mapTime(parseVTTTime(times[1]))
				if !okA && !okB {
					skipCue = true
					continue
				}
				if !okA {
					a = b - 0.5
				}
				if !okB {
					b = a + 2
				}
				line = vttTime.ReplaceAllStringFunc(line, func(string) string { return "" })
				line = fmtVTTTime(a) + " --> " + fmtVTTTime(b) + strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-->"))
				skipCue = false
			}
		} else if line == "" {
			skipCue = false
		} else if skipCue {
			continue
		}
		out = append(out, line)
	}
	return os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// CaptionsPath is where a recording's WebVTT lives, if extracted.
func (s *Service) CaptionsPath(r Recording) string {
	if r.File == "" || !r.Captions {
		return ""
	}
	p := s.Path(r)
	return strings.TrimSuffix(p, filepath.Ext(p)) + ".vtt"
}

// ActiveChannels lists channels with a recording in progress, so live
// sessions can count those tuners as taken.
func (s *Service) ActiveChannels() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for id := range s.jobs {
		if r, ok := s.recs[id]; ok {
			out = append(out, r.Channel)
		}
	}
	return out
}

// Reload re-reads dvr.json (after a restore).
func (s *Service) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = map[string]*Rule{}
	s.recs = map[string]*Recording{}
	s.load()
}
