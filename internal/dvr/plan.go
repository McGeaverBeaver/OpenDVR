package dvr

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/guide"
)

// RequestPlan asks the scheduler to match passes against the guide soon.
func (s *Service) RequestPlan() {
	select {
	case s.planReq <- struct{}{}:
	default:
	}
}

// Run drives the DVR until ctx ends: planning, starting recordings on
// time and post-processing. Anything interrupted by a restart is picked
// up again.
func (s *Service) Run(ctx context.Context) {
	s.recover(ctx)
	go s.postWorker(ctx)
	plan := time.NewTicker(time.Minute)
	tick := time.NewTicker(5 * time.Second)
	defer plan.Stop()
	defer tick.Stop()
	s.plan(ctx)
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.planReq:
			s.plan(ctx)
			s.tick(ctx)
		case <-plan.C:
			s.plan(ctx)
		case <-tick.C:
			s.tick(ctx)
		}
	}
}

// recover resumes work left over from a previous run.
func (s *Service) recover(ctx context.Context) {
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		switch r.Status {
		case StatusRecording:
			if r.End > now+30 {
				r.Status = StatusScheduled // tick restarts it, appending to the file
			} else {
				r.Status = StatusProcessing
				s.enqueuePost(r.ID)
			}
		case StatusProcessing:
			s.enqueuePost(r.ID)
		}
	}
}

func (s *Service) enqueuePost(id string) {
	select {
	case s.postReq <- id:
	default:
		s.log.Warn("post-processing queue full", "id", id)
	}
}

// candidate is an airing a pass wants.
type candidate struct {
	rule    *Rule
	channel guide.Channel
	prog    guide.Program
}

// episodeKey identifies an episode across channels and airings; empty
// when the listing has nothing to go on.
func episodeKey(p guide.Program) string {
	switch {
	case p.SeriesID != "" && p.EpisodeNumber != "":
		return p.SeriesID + "|" + p.EpisodeNumber
	case p.EpisodeNumber != "":
		return strings.ToLower(p.Title) + "|" + p.EpisodeNumber
	case p.EpisodeTitle != "":
		return strings.ToLower(p.Title) + "|" + strings.ToLower(p.EpisodeTitle)
	}
	return ""
}

var seasonRe = regexp.MustCompile(`(?i)^S(\d+)E\d+`)

// seasonOf parses "S05E02"; 0 when unknown.
func seasonOf(episodeNumber string) int {
	m := seasonRe.FindStringSubmatch(episodeNumber)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// isNew is the guide's first-airing flag, or an original airdate on the
// same day for guides that do not set it.
func isNew(p guide.Program) bool {
	if p.New {
		return true
	}
	if p.OriginalAirdate > 0 {
		return p.Start-p.OriginalAirdate >= 0 && p.Start-p.OriginalAirdate < 36*3600
	}
	return false
}

func (r *Rule) matches(c guide.Channel, p guide.Program) bool {
	if r.Paused {
		return false
	}
	if r.Channel != "" && r.Channel != c.GuideNumber {
		return false
	}
	if r.SeriesID != "" {
		if p.SeriesID != r.SeriesID {
			return false
		}
	} else if !strings.EqualFold(strings.TrimSpace(p.Title), r.Title) {
		return false
	}
	if r.NewOnly && !isNew(p) {
		return false
	}
	if r.Season > 0 && seasonOf(p.EpisodeNumber) != r.Season {
		return false
	}
	return true
}

// plan matches passes against the guide window and reconciles the
// scheduled recordings with it, then recomputes conflicts.
func (s *Service) plan(ctx context.Context) {
	now := time.Now()
	s.mu.Lock()
	rules := make([]*Rule, 0, len(s.rules))
	for _, r := range s.rules {
		if !r.Paused {
			rules = append(rules, r)
		}
	}
	s.mu.Unlock()

	to := now.Add(s.cfg.Lookahead).Unix()
	if len(rules) > 0 {
		ectx, cancel := context.WithTimeout(ctx, 90*time.Second)
		s.epg.EnsureWindow(ectx, to)
		cancel()
	}
	window := s.epg.Window(now.Unix(), to)

	// Collect every airing a pass wants, earliest first, so the first
	// airing of an episode wins when it is shown more than once.
	var cands []candidate
	var slots []*Recording // time-slot recordings wanted in the window
	for _, r := range rules {
		if r.Kind != RuleKindManual {
			continue
		}
		for _, at := range r.occurrences(now.Unix(), to) {
			slots = append(slots, s.manualRecording(r, at))
		}
	}
	for _, c := range window {
		if _, ok := s.tuners.Channel(c.GuideNumber); !ok {
			continue
		}
		for _, p := range c.Programs {
			if p.End <= now.Unix() {
				continue
			}
			for _, r := range rules {
				if r.Kind == RuleKindManual {
					continue
				}
				if r.matches(c, p) {
					cands = append(cands, candidate{rule: r, channel: c, prog: p})
					break
				}
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].prog.Start < cands[j].prog.Start })

	s.mu.Lock()
	defer s.mu.Unlock()
	have := map[string]bool{}   // episode keys already covered
	airing := map[string]bool{} // channel@start already scheduled
	wanted := map[string]bool{} // scheduled ids the guide still wants
	for _, r := range s.recs {
		if r.Status == StatusFailed {
			continue
		}
		airing[r.Channel+"@"+strconv.FormatInt(r.Airing, 10)] = true
		if r.RuleID != "" || r.Manual {
			if k := episodeKeyOf(r); k != "" {
				have[k] = true
			}
		}
	}
	added := 0
	for _, rec := range slots {
		key := rec.Channel + "@" + strconv.FormatInt(rec.Airing, 10)
		if airing[key] {
			for _, r := range s.recs {
				if r.Channel == rec.Channel && r.Airing == rec.Airing {
					wanted[r.ID] = true
				}
			}
			continue
		}
		rule := s.rules[rec.RuleID]
		pb, pa := rule.PadBefore, rule.PadAfter
		s.applyOptions(rec, Options{PadBefore: &pb, PadAfter: &pa, Commercials: rule.Commercials})
		if rec.End <= now.Unix() {
			continue
		}
		if rec.Start < now.Unix() {
			rec.Start = now.Unix()
		}
		s.recs[rec.ID] = rec
		wanted[rec.ID] = true
		airing[key] = true
		added++
	}
	for _, c := range cands {
		key := c.channel.GuideNumber + "@" + strconv.FormatInt(c.prog.Start, 10)
		if airing[key] {
			for _, r := range s.recs {
				if r.Channel == c.channel.GuideNumber && r.Airing == c.prog.Start {
					wanted[r.ID] = true
				}
			}
			continue
		}
		if ek := episodeKey(c.prog); ek != "" && have[ek] {
			continue
		}
		r := s.fromProgram(c.channel.GuideNumber, c.channel.GuideName, c.prog)
		r.RuleID = c.rule.ID
		pb, pa := c.rule.PadBefore, c.rule.PadAfter
		r.Commercials = c.rule.Commercials
		s.applyOptions(r, Options{PadBefore: &pb, PadAfter: &pa, Commercials: c.rule.Commercials})
		if r.Start < now.Unix() {
			r.Start = now.Unix()
		}
		s.recs[r.ID] = r
		wanted[r.ID] = true
		airing[key] = true
		if ek := episodeKey(c.prog); ek != "" {
			have[ek] = true
		}
		added++
	}
	// Scheduled recordings a pass no longer wants (the listing changed, the
	// pass was edited or paused) are dropped before they start.
	dropped := 0
	for id, r := range s.recs {
		if r.RuleID == "" || wanted[id] {
			continue
		}
		if r.Status == StatusScheduled || r.Status == StatusConflict {
			if _, ok := s.rules[r.RuleID]; !ok || r.Start > now.Unix() {
				delete(s.recs, id)
				dropped++
			}
		}
	}
	s.markConflictsLocked()
	s.sweepStorageLocked()
	s.notifyConflictsLocked()
	s.lastPlan = now
	if added > 0 || dropped > 0 {
		s.log.Info("dvr planned", "added", added, "dropped", dropped, "rules", len(rules))
	}
	s.saveLocked()
}

// notifyConflictsLocked sends one message per recording that will be
// missed for lack of a tuner.
func (s *Service) notifyConflictsLocked() {
	if s.cfg.Notify == nil {
		return
	}
	for _, r := range s.recs {
		if r.Status != StatusConflict || hasNote(r, "conflict") {
			continue
		}
		r.Notified = append(r.Notified, "conflict")
		s.cfg.Notify("conflict", "Recording conflict: "+r.Title,
			fmt.Sprintf("%s on %s at %s needs a tuner that will be busy. Lower another pass's priority or cancel something.", r.Title, r.Channel, time.Unix(r.Start, 0).Format("Mon Jan 2 15:04")))
	}
}

func hasNote(r *Recording, kind string) bool {
	for _, n := range r.Notified {
		if n == kind {
			return true
		}
	}
	return false
}

func episodeKeyOf(r *Recording) string {
	return episodeKey(guide.Program{Title: r.Title, SeriesID: r.SeriesID, EpisodeNumber: r.EpisodeNumber, EpisodeTitle: r.EpisodeTitle})
}

// markConflictsLocked flags scheduled recordings that will find no tuner,
// taking passes in priority order and recordings already running as fixed.
func (s *Service) markConflictsLocked() {
	tuners := s.tunerCount()
	type item struct {
		r   *Recording
		pri int
	}
	var items []item
	for _, r := range s.recs {
		switch r.Status {
		case StatusScheduled, StatusConflict:
			pri := 1 << 20 // manual recordings outrank passes
			if r.RuleID != "" {
				pri = 1 << 21
				if rule, ok := s.rules[r.RuleID]; ok {
					pri = rule.Priority
				}
			}
			if r.Manual {
				pri = -1
			}
			items = append(items, item{r, pri})
		case StatusRecording:
			items = append(items, item{r, -2})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].pri != items[j].pri {
			return items[i].pri < items[j].pri
		}
		return items[i].r.Start < items[j].r.Start
	})
	var accepted []*Recording
	for _, it := range items {
		overlap := 0
		for _, a := range accepted {
			if a.Start < it.r.End && it.r.Start < a.End {
				overlap++
			}
		}
		if it.r.Status == StatusRecording || overlap < tuners {
			accepted = append(accepted, it.r)
			if it.r.Status == StatusConflict {
				it.r.Status = StatusScheduled
			}
		} else if it.r.Status == StatusScheduled {
			it.r.Status = StatusConflict
		}
	}
}

// Search finds programmes in the cached guide whose title, episode title
// or synopsis contain the words, soonest first. The guide is extended to
// the lookahead first so a search covers what a pass would.
func (s *Service) Search(ctx context.Context, q string, limit int) []SearchHit {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return []SearchHit{}
	}
	now := time.Now().Unix()
	to := now + int64(s.cfg.Lookahead.Seconds())
	ectx, cancel := context.WithTimeout(ctx, 60*time.Second)
	s.epg.EnsureWindow(ectx, to)
	cancel()
	var out []SearchHit
	for _, c := range s.epg.Window(now, to) {
		if ch, ok := s.tuners.Channel(c.GuideNumber); !ok || ch.DRM {
			continue
		}
		for _, p := range c.Programs {
			hay := strings.ToLower(p.Title + " " + p.EpisodeTitle + " " + p.Synopsis)
			ok := true
			for _, w := range words {
				if !strings.Contains(hay, w) {
					ok = false
					break
				}
			}
			if ok {
				out = append(out, SearchHit{Channel: c.GuideNumber, ChannelName: c.GuideName, Program: p})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Program.Start < out[j].Program.Start })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []SearchHit{}
	}
	return out
}

// SearchHit is one matching airing.
type SearchHit struct {
	Channel     string        `json:"channel"`
	ChannelName string        `json:"channelName"`
	Program     guide.Program `json:"program"`
}
