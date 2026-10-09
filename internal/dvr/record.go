package dvr

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/tuner"
)

func randByte() byte {
	var b [1]byte
	_, _ = rand.Read(b[:])
	return b[0]
}

// tick starts recordings whose time has come and finishes ones whose
// time has passed without a tuner.
func (s *Service) tick(ctx context.Context) {
	now := time.Now()
	s.mu.Lock()
	var start []*Recording
	for id, r := range s.recs {
		if r.Status != StatusScheduled && r.Status != StatusConflict {
			continue
		}
		if r.End <= now.Unix() {
			// Never got a tuner.
			if r.Start <= now.Unix() {
				r.Status = StatusFailed
				r.Error = "no tuner was free"
				r.Finished = now
				s.log.Warn("recording missed", "id", id, "title", r.Title, "reason", r.Error)
			}
			continue
		}
		if r.Start > now.Unix() {
			continue
		}
		if _, running := s.jobs[id]; running {
			continue
		}
		if t, ok := s.retryAt[id]; ok && now.Before(t) {
			continue
		}
		start = append(start, r)
	}
	for _, r := range start {
		jctx, cancel := context.WithCancel(ctx)
		s.jobs[r.ID] = cancel
		go s.runJob(jctx, r.ID)
	}
	s.mu.Unlock()
}

// active lists channels this server is using a tuner for: live sessions
// and recordings in progress. The resolver counts those tuners as used.
func (s *Service) activeLocked() map[string]bool {
	out := map[string]bool{}
	if s.live != nil {
		for _, st := range s.live.Statuses() {
			if st.Running {
				out[st.Channel] = true
			}
		}
	}
	for id := range s.jobs {
		if r, ok := s.recs[id]; ok {
			out[r.Channel] = true
		}
	}
	return out
}

// runJob records one programme: it takes a tuner, copies the transport
// stream to disk until the end time, restarting ffmpeg if the stream
// drops, then hands the file to post-processing.
func (s *Service) runJob(ctx context.Context, id string) {
	s.mu.Lock()
	r, ok := s.recs[id]
	if !ok {
		delete(s.jobs, id)
		s.mu.Unlock()
		return
	}
	if r.File == "" {
		r.File = s.relPathFor(r)
	}
	path := s.Path(*r)
	channel, end, title := r.Channel, r.End, r.Title
	active := s.activeLocked()
	s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.finishJob(id, fmt.Errorf("create folder: %w", err))
		return
	}

	// A tuner that is busy now may free up in a few minutes (a live viewer
	// changing channel); keep asking until the programme is mostly gone.
	var src tuner.Source
	for {
		var err error
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		src, err = s.tuners.ResolveSource(rctx, channel, active)
		cancel()
		if err == nil {
			break
		}
		var busy *tuner.ErrTunersBusy
		if !errors.As(err, &busy) {
			s.finishJob(id, err)
			return
		}
		s.mu.Lock()
		if x, ok := s.recs[id]; ok && x.Status != StatusConflict {
			x.Status = StatusConflict
			x.Error = "waiting for a free tuner"
		}
		delete(s.jobs, id)
		s.retryAt[id] = time.Now().Add(30 * time.Second)
		s.mu.Unlock()
		s.log.Info("recording waiting for a tuner", "id", id, "title", title, "busy", busy.Error())
		return // tick retries
	}

	s.mu.Lock()
	if x, ok := s.recs[id]; ok {
		x.Status = StatusRecording
		x.Error = ""
		s.saveLocked()
	}
	s.mu.Unlock()
	s.log.Info("recording started", "id", id, "channel", channel, "title", title, "file", path, "device", src.DeviceID)

	// Capture until the end time. ffmpeg is restarted whenever it exits
	// early: the tuner refusing the channel (503: no signal on that
	// channel, or every tuner taken by a live viewer) clears up when the
	// signal returns or a viewer moves on, so keep trying for the whole
	// slot rather than giving up after a few minutes.
	var lastErr error
	attempts := 0
	for {
		// The end time is read afresh each round, so an edit that makes a
		// running recording run later takes effect.
		s.mu.Lock()
		if cur, ok := s.recs[id]; ok {
			end = cur.End
		}
		s.mu.Unlock()
		remaining := time.Until(time.Unix(end, 0))
		if remaining < 3*time.Second {
			break
		}
		attempts++
		started := time.Now()
		err := s.capture(ctx, src.URL, path, remaining)
		if ctx.Err() != nil {
			s.mu.Lock()
			delete(s.jobs, id)
			s.mu.Unlock()
			return // cancelled: Delete has already removed the entry
		}
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = explainCapture(err, time.Since(started))
		if attempts == 1 || attempts%10 == 0 {
			s.log.Warn("capture interrupted; retrying until the end time", "id", id, "title", title, "attempt", attempts, "err", lastErr)
		}
		wait := 5 * time.Second
		if attempts > 12 {
			wait = 15 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	st, statErr := os.Stat(path)
	if statErr != nil || st.Size() < 512*1024 {
		if lastErr == nil {
			lastErr = errors.New("the tuner produced no video")
		}
		s.finishJob(id, lastErr)
		return
	}
	if lastErr != nil {
		s.log.Warn("recording ended with gaps", "id", id, "title", title, "lastErr", lastErr)
	}
	s.finishJob(id, nil)
}

// explainCapture turns ffmpeg's view of a tuner refusal into something a
// person can act on. The HDHomeRun answers 503 at once when no tuner is
// free and after about ten seconds when it cannot lock the channel.
func explainCapture(err error, took time.Duration) error {
	msg := err.Error()
	if !strings.Contains(msg, "503") && !strings.Contains(msg, "5XX") {
		return err
	}
	if took > 6*time.Second {
		return fmt.Errorf("the HDHomeRun could not lock this channel (no signal); it answered 503 to the stream request")
	}
	return fmt.Errorf("every HDHomeRun tuner was in use when the stream was requested (503); live viewers were holding them")
}

// capture runs one ffmpeg that copies the stream to the file, appending.
func (s *Service) capture(ctx context.Context, url, path string, d time.Duration) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	args := []string{
		"-hide_banner", "-nostats", "-loglevel", "warning",
		"-fflags", "+genpts+discardcorrupt",
		"-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "2",
		"-i", url,
		"-t", strconv.FormatFloat(d.Seconds(), 'f', 0, 64),
		"-map", "0:v:0", "-map", "0:a?",
		"-c", "copy",
		"-f", "mpegts", "pipe:1",
	}
	cmd := exec.CommandContext(ctx, s.cfg.FFmpegPath, args...)
	cmd.Stdout = f
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var tail []string
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		tail = append(tail, sc.Text())
		if len(tail) > 8 {
			tail = tail[1:]
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, strings.Join(tail, " | "))
	}
	return nil
}

// finishJob moves a recording to processing or failed.
func (s *Service) finishJob(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	r, ok := s.recs[id]
	if !ok {
		return
	}
	if err != nil {
		r.Status = StatusFailed
		r.Error = err.Error()
		r.Finished = time.Now()
		s.log.Warn("recording failed", "id", id, "title", r.Title, "err", err)
		s.notifyLocked(r, "failed", "Recording failed: "+r.Title, r.Title+" on "+r.Channel+": "+err.Error())
		if p := s.Path(*r); p != "" {
			if st, e := os.Stat(p); e == nil {
				r.SizeBytes = st.Size()
			}
		}
	} else {
		r.Status = StatusProcessing
		r.Error = ""
		s.log.Info("recording finished; processing", "id", id, "title", r.Title)
		s.enqueuePost(id)
	}
	s.saveLocked()
}
