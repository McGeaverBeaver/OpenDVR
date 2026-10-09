package dvr

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/stream"
)

// postWorker processes finished recordings one at a time, so a batch of
// overnight recordings does not fight live viewers for the encoder.
func (s *Service) postWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.postReq:
			s.mu.Lock()
			r, ok := s.recs[id]
			if !ok || r.Status != StatusProcessing {
				s.mu.Unlock()
				continue
			}
			rec := *r
			pctx, cancel := context.WithCancel(ctx)
			s.postCancel[id] = cancel
			s.mu.Unlock()

			err := s.process(pctx, &rec)

			s.mu.Lock()
			delete(s.postCancel, id)
			cancel()
			if cur, ok := s.recs[id]; ok {
				if err != nil && ctx.Err() == nil && pctx.Err() == nil {
					cur.Status = StatusFailed
					cur.Error = err.Error()
					s.log.Warn("post-processing failed", "id", id, "title", cur.Title, "err", err)
					s.notifyLocked(cur, "failed", "Recording failed: "+cur.Title, cur.Title+" on "+cur.Channel+": "+err.Error())
				} else if err == nil {
					cur.File, cur.SizeBytes, cur.DurationSec = rec.File, rec.SizeBytes, rec.DurationSec
					cur.Skips, cur.Cut, cur.Captions = rec.Skips, rec.Cut, rec.Captions
					cur.Original, cur.OriginalUntil = rec.Original, rec.OriginalUntil
					cur.Status = StatusReady
					cur.Error = ""
					s.log.Info("recording ready", "id", id, "title", cur.Title, "file", cur.File, "size", cur.SizeBytes, "skips", len(cur.Skips), "cut", cur.Cut, "captions", cur.Captions)
					s.notifyLocked(cur, "ready", "Recorded: "+cur.Title, fmt.Sprintf("%s on %s is ready (%d min, %d MB).", cur.Title, cur.Channel, int(cur.DurationSec/60), cur.SizeBytes>>20))
				}
				cur.Finished = time.Now()
				s.retainLocked(cur.RuleID)
			}
			s.saveLocked()
			s.mu.Unlock()
		}
	}
}

// process detects commercials and transcodes the capture to MP4.
func (s *Service) process(ctx context.Context, r *Recording) error {
	ts := s.originalPath(*r)
	if ts == "" {
		ts = s.Path(*r)
	}
	if _, err := os.Stat(ts); err != nil {
		return fmt.Errorf("capture missing: %w", err)
	}
	dur := s.duration(ctx, ts)
	if dur <= 0 {
		dur = float64(r.End - r.Start)
	}
	var skips []Span
	if r.Commercials != CommercialsNone && s.cfg.ComskipPath != "" {
		if settings, off := s.iniFor(r.Channel); off {
			s.log.Info("commercial detection is off for this channel", "id", r.ID, "channel", r.Channel)
		} else {
			var err error
			skips, _, err = s.runComskip(ctx, ts, settings, filepath.Dir(ts))
			if err != nil {
				s.log.Warn("comskip failed; keeping commercials", "id", r.ID, "err", err)
				skips = nil
			}
		}
	}
	mp4 := strings.TrimSuffix(ts, filepath.Ext(ts)) + ".mp4"
	cut := r.Commercials == CommercialsRemove && len(skips) > 0
	args := s.transcodeArgs(ts, mp4, dur, skips, cut)
	if err := s.runFFmpeg(ctx, args); err != nil {
		_ = os.Remove(mp4)
		return err
	}
	st, err := os.Stat(mp4)
	if err != nil || st.Size() == 0 {
		return fmt.Errorf("transcode produced no file")
	}
	if s.cfg.Captions {
		var keepSpans []Span
		if cut {
			keepSpans = keep(dur, skips)
		}
		r.Captions = s.extractCaptions(ctx, ts, strings.TrimSuffix(ts, filepath.Ext(ts))+".vtt", keepSpans)
	}
	// The MP4 replaces the capture, which is kept for a while if asked so
	// commercials can be found again with other settings.
	if s.cfg.KeepOriginalDays > 0 {
		rel, _ := filepath.Rel(s.cfg.RecordingsDir, ts)
		r.Original = filepath.ToSlash(rel)
		if r.OriginalUntil.IsZero() {
			r.OriginalUntil = time.Now().AddDate(0, 0, s.cfg.KeepOriginalDays)
		}
	} else {
		_ = os.Remove(ts)
		r.Original, r.OriginalUntil = "", time.Time{}
	}
	rel, _ := filepath.Rel(s.cfg.RecordingsDir, mp4)
	r.File = filepath.ToSlash(rel)
	r.SizeBytes = st.Size()
	r.DurationSec = s.duration(ctx, mp4)
	r.Cut = cut
	if cut {
		r.Skips = nil
	} else {
		r.Skips = skips
	}
	return nil
}

// duration asks ffprobe for a file's length in seconds; 0 when unknown.
func (s *Service) duration(ctx context.Context, path string) float64 {
	if s.probe == "" {
		return 0
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, s.probe, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return d
}

// parseEDL reads "start<TAB>end<TAB>action" lines; action 0 is a cut.
func parseEDL(f *os.File) []Span {
	var out []Span
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		a, err1 := strconv.ParseFloat(fields[0], 64)
		b, err2 := strconv.ParseFloat(fields[1], 64)
		if err1 != nil || err2 != nil || b <= a {
			continue
		}
		if len(fields) >= 3 && fields[2] != "0" {
			continue
		}
		out = append(out, Span{Start: a, End: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// keep returns the stretches of a file that are not commercials.
func keep(dur float64, skips []Span) []Span {
	var out []Span
	pos := 0.0
	for _, sk := range skips {
		if sk.Start > pos+0.5 {
			out = append(out, Span{Start: pos, End: sk.Start})
		}
		if sk.End > pos {
			pos = sk.End
		}
	}
	if dur-pos > 0.5 {
		out = append(out, Span{Start: pos, End: dur})
	}
	return out
}

// transcodeArgs builds the ffmpeg command that turns the capture into an
// H.264/AAC MP4, optionally with the commercials cut out. Decoding and
// deinterlacing are in software like the live path; only the encode uses
// the GPU.
func (s *Service) transcodeArgs(in, out string, dur float64, skips []Span, cut bool) []string {
	enc := s.encoder()
	args := []string{
		"-hide_banner", "-nostats", "-loglevel", "error", "-y",
		"-fflags", "+genpts+discardcorrupt",
	}
	args = append(args, stream.HWDeviceArgs(enc, s.cfg.RenderDevice)...)
	args = append(args, "-i", in)

	var chain []string
	if s.cfg.Deinterlace {
		chain = append(chain, "yadif=mode=send_frame:deint=interlaced")
	}
	switch enc {
	case stream.EncoderQSV:
		chain = append(chain, "format=nv12", "hwupload=extra_hw_frames=64")
	case stream.EncoderVAAPI:
		chain = append(chain, "format=nv12", "hwupload")
	}

	if cut {
		parts := keep(dur, skips)
		var fc strings.Builder
		var labels strings.Builder
		for i, p := range parts {
			fmt.Fprintf(&fc, "[0:v:0]trim=start=%.3f:end=%.3f,setpts=PTS-STARTPTS[v%d];[0:a:0]atrim=start=%.3f:end=%.3f,asetpts=PTS-STARTPTS[a%d];", p.Start, p.End, i, p.Start, p.End, i)
			fmt.Fprintf(&labels, "[v%d][a%d]", i, i)
		}
		fmt.Fprintf(&fc, "%sconcat=n=%d:v=1:a=1[vc][ac]", labels.String(), len(parts))
		if len(chain) > 0 {
			fmt.Fprintf(&fc, ";[vc]%s[vout]", strings.Join(chain, ","))
		} else {
			fc.WriteString(";[vc]null[vout]")
		}
		args = append(args, "-filter_complex", fc.String(), "-map", "[vout]", "-map", "[ac]")
	} else {
		args = append(args, "-map", "0:v:0", "-map", "0:a:0?")
		if len(chain) > 0 {
			args = append(args, "-vf", strings.Join(chain, ","))
		}
	}

	vb := strconv.Itoa(s.cfg.VideoBitrateK)
	switch enc {
	case stream.EncoderQSV:
		args = append(args, "-c:v", "h264_qsv", "-preset", "medium", "-profile:v", "high", "-look_ahead", "0", "-a53cc", "1", "-b:v", vb+"k", "-maxrate", strconv.Itoa(s.cfg.VideoBitrateK*3/2)+"k", "-bufsize", strconv.Itoa(s.cfg.VideoBitrateK*3)+"k", "-g", "60")
	case stream.EncoderVAAPI:
		args = append(args, "-c:v", "h264_vaapi", "-profile:v", "high", "-b:v", vb+"k", "-maxrate", strconv.Itoa(s.cfg.VideoBitrateK*3/2)+"k", "-bufsize", strconv.Itoa(s.cfg.VideoBitrateK*3)+"k", "-g", "60")
	default:
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-profile:v", "high", "-pix_fmt", "yuv420p", "-crf", "21", "-a53cc", "1", "-maxrate", strconv.Itoa(s.cfg.VideoBitrateK*3/2)+"k", "-bufsize", strconv.Itoa(s.cfg.VideoBitrateK*3)+"k", "-g", "60")
	}
	ab := s.cfg.AudioBitrateK
	if ab <= 0 {
		ab = 192
	}
	args = append(args,
		"-c:a", "aac", "-b:a", strconv.Itoa(ab)+"k", "-ac", "2",
		"-movflags", "+faststart",
		"-f", "mp4", out,
	)
	return args
}

func (s *Service) runFFmpeg(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, s.cfg.FFmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(string(out))
		if len(msg) > 600 {
			msg = msg[len(msg)-600:]
		}
		return fmt.Errorf("ffmpeg: %v: %s", err, msg)
	}
	return nil
}

// notifyLocked sends a message about a recording once per kind.
func (s *Service) notifyLocked(r *Recording, kind, title, body string) {
	if s.cfg.Notify == nil || hasNote(r, kind) {
		return
	}
	r.Notified = append(r.Notified, kind)
	s.cfg.Notify(kind, title, body)
}

// retainLocked enforces a pass's keep count by deleting its oldest
// finished recordings.
func (s *Service) retainLocked(ruleID string) {
	rule, ok := s.rules[ruleID]
	if !ok || rule.Keep <= 0 {
		return
	}
	var done []*Recording
	for _, r := range s.recs {
		if r.RuleID == ruleID && r.Status == StatusReady {
			done = append(done, r)
		}
	}
	if len(done) <= rule.Keep {
		return
	}
	sort.Slice(done, func(i, j int) bool { return done[i].Airing < done[j].Airing })
	for _, r := range done[:len(done)-rule.Keep] {
		for _, p := range s.filesOf(*r) {
			_ = os.Remove(p)
		}
		delete(s.recs, r.ID)
		s.log.Info("recording expired by keep count", "id", r.ID, "title", r.Title, "keep", rule.Keep)
	}
}
