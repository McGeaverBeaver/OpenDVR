package stream

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/tuner"
)

// Config tunes the manager.
type Config struct {
	FFmpegPath     string
	BufferDir      string
	Encoder        Encoder // concrete
	RenderDevice   string
	SegmentSeconds int
	WindowSegments int
	IdleTimeout    time.Duration
	ReadyTimeout   time.Duration
	// ViewerTimeout is how long a viewer handle survives without a
	// heartbeat before it is dropped (and its session released if it was
	// the last one). Zero disables expiry.
	ViewerTimeout time.Duration
	VideoBitrateK int
	AudioBitrateK int
	Deinterlace   bool
	// FFmpegLogLevel is passed to -loglevel; "info" shows stream mapping and
	// encoder initialisation, which is what you need when a session stalls.
	FFmpegLogLevel string
}

// SourceResolver maps a guide number to a tuner stream. active lists the
// channels the manager is already streaming, so the resolver can tell a
// tuner it should share from one that is genuinely busy.
type SourceResolver interface {
	ResolveSource(ctx context.Context, guideNumber string, active map[string]bool) (tuner.Source, error)
}

// Viewer is one player watching a channel, identified by a handle the
// client keeps. Heartbeats keep it alive; the session is released when
// its last viewer leaves or expires.
type Viewer struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Channel string `json:"channel"`
	Addr    string `json:"addr,omitempty"`
	// User and UserName are the signed-in account, when auth is on.
	User     string `json:"user,omitempty"`
	UserName string `json:"userName,omitempty"`
	// Place is filled in for administrators from the GeoIP database.
	Place    string    `json:"place,omitempty"`
	Started  time.Time `json:"started"`
	LastSeen time.Time `json:"lastSeen"`
	Paused   bool      `json:"paused"`
}

// Session is one running ffmpeg for one channel. Any number of viewers
// read from its directory.
type Session struct {
	Channel string
	Source  tuner.Source
	Encoder Encoder
	Dir     string
	Started time.Time

	lastAccess atomic.Int64
	cmd        *exec.Cmd
	args       []string
	done       chan struct{}
	exitErr    error
	logs       *ringLog
	// stopping is set before we signal ffmpeg, so its exit (status 255 on
	// SIGINT) is not mistaken for a crash.
	stopping atomic.Bool
}

// Touch records viewer activity.
func (s *Session) Touch() { s.lastAccess.Store(time.Now().UnixNano()) }

// LastAccess is the time of the most recent viewer request.
func (s *Session) LastAccess() time.Time { return time.Unix(0, s.lastAccess.Load()) }

// Done reports whether ffmpeg has exited.
func (s *Session) Done() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// PlaylistPath is the absolute path of the session playlist.
func (s *Session) PlaylistPath() string { return filepath.Join(s.Dir, PlaylistName) }

// Status is the JSON view of a session.
type Status struct {
	Channel    string    `json:"channel"`
	DeviceID   string    `json:"deviceId"`
	Encoder    Encoder   `json:"encoder"`
	Started    time.Time `json:"started"`
	LastAccess time.Time `json:"lastAccess"`
	Segments   int       `json:"segments"`
	Running    bool      `json:"running"`
	Error      string    `json:"error,omitempty"`
	LogTail    []string  `json:"logTail,omitempty"`
	Viewers    []Viewer  `json:"viewers"`
}

// Failure records why a session died, for the Status page.
type Failure struct {
	Channel string    `json:"channel"`
	Time    time.Time `json:"time"`
	Encoder Encoder   `json:"encoder"`
	Error   string    `json:"error"`
	LogTail []string  `json:"logTail"`
	Command string    `json:"command"`
}

// Manager owns all sessions.
type Manager struct {
	cfg     Config
	resolve SourceResolver
	log     *slog.Logger
	// ExternalActive, when set, lists channels other parts of the server
	// hold a tuner for (recordings, exported streams) so the resolver
	// counts those tuners as taken rather than just released.
	ExternalActive func() []string

	mu       sync.Mutex
	sessions map[string]*Session
	viewers  map[string]*Viewer
	failures []Failure
	// degraded flips on after a hardware encoder failure; later sessions go
	// straight to software instead of paying the failure again.
	degraded atomic.Bool
}

// NewManager builds a manager and prepares the buffer directory.
func NewManager(cfg Config, resolve SourceResolver, log *slog.Logger) (*Manager, error) {
	if err := os.MkdirAll(cfg.BufferDir, 0o755); err != nil {
		return nil, fmt.Errorf("buffer dir: %w", err)
	}
	// Anything left over from a previous run is garbage.
	entries, _ := os.ReadDir(cfg.BufferDir)
	for _, e := range entries {
		if e.IsDir() {
			_ = os.RemoveAll(filepath.Join(cfg.BufferDir, e.Name()))
		}
	}
	return &Manager{cfg: cfg, resolve: resolve, log: log, sessions: map[string]*Session{}, viewers: map[string]*Viewer{}}, nil
}

// Degraded reports whether hardware encoding has been abandoned.
func (m *Manager) Degraded() bool { return m.degraded.Load() }

// startRetries is how many times a fresh session is restarted when ffmpeg
// exits before producing output. The dominant cause on a multi-tuner box is
// a tuner the HDHomeRun has not released yet from a session we just stopped
// (rapid channel surfing); it frees within a second or two, so a short
// backoff recovers cleanly instead of showing the viewer an error.
const startRetries = 3

const startBackoff = 1200 * time.Millisecond

// Acquire returns a ready session for the channel, starting one if needed.
//
// Recovery, in order:
//   - An existing live session is reused and returned as soon as it is ready.
//   - A fresh session that dies before producing output is retried a few
//     times (the tuner was still locked by a just-stopped session).
//   - A fresh hardware session that keeps failing falls back to software.
func (m *Manager) Acquire(ctx context.Context, channel string) (*Session, error) {
	s, _, err := m.get(ctx, channel)
	if err != nil {
		return nil, err
	}
	err = m.waitReady(ctx, s)
	if err == nil {
		return s, nil
	}
	if ctx.Err() != nil {
		// The viewer went away; leave the session to the reaper.
		return nil, err
	}
	// Whether ffmpeg exited or is hung, a session that cannot produce
	// output is unhealthy. Record it, kill it, and apply the retry policy.
	m.recordFailure(s, err)
	m.Stop(channel)
	if looksLikeNoSignal(time.Since(s.Started), err) {
		return nil, fmt.Errorf("the tuner could not lock channel %s (no video data): the signal is too weak or the channel is off air", channel)
	}

	// An input problem (tuner still locked from a session we just stopped,
	// connection reset) is retried on the same encoder after a short
	// backoff. Anything else on a hardware encoder, including a silent
	// stall, is not worth another 20-second wait: drop to software for this
	// and later sessions and surface the failure on the Status page.
	lastErr := err
	for attempt := 1; attempt <= startRetries; attempt++ {
		if s.Encoder.IsHardware() && !looksLikeInputError(lastErr) {
			m.degraded.Store(true)
			m.log.Warn("hardware encoder failed, falling back to software", "channel", channel, "err", lastErr)
		} else {
			m.log.Info("session start failed, retrying", "channel", channel, "attempt", attempt, "err", lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(startBackoff):
		}
		s, _, err = m.get(ctx, channel)
		if err != nil {
			return nil, err
		}
		if err := m.waitReady(ctx, s); err == nil {
			return s, nil
		} else {
			lastErr = err
			m.recordFailure(s, err)
			m.Stop(channel)
			if looksLikeNoSignal(time.Since(s.Started), err) {
				return nil, fmt.Errorf("the tuner could not lock channel %s (no video data): the signal is too weak or the channel is off air", channel)
			}
		}
	}
	return nil, fmt.Errorf("could not start %s after %d attempts: %w", channel, startRetries, lastErr)
}

// noSignalAfter separates the two reasons an HDHomeRun answers 503. With
// every tuner in use it refuses at once; with a free tuner it tries to
// lock the channel for about ten seconds and only then gives up
// (X-HDHomeRun-Error 807, "No Video Data"). ffmpeg does not surface the
// header, but the time it took tells the two apart, and retrying a
// channel with no signal would only make the viewer wait three times
// as long.
const noSignalAfter = 6 * time.Second

func looksLikeNoSignal(elapsed time.Duration, err error) bool {
	return err != nil && strings.Contains(err.Error(), "503") && elapsed >= noSignalAfter
}

// looksLikeInputError recognises a failure on the tuner side (busy tuner,
// dropped connection, no signal) that is worth retrying on the same
// encoder. Anything else is treated as a pipeline problem.
func looksLikeInputError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{
		"503", "server returned", "connection refused", "connection reset",
		"no route to host", "network is unreachable", "input/output error",
		"end of file", "invalid data found when processing input",
	} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func (m *Manager) recordFailure(s *Session, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures = append(m.failures, Failure{
		Channel: s.Channel, Time: time.Now(), Encoder: s.Encoder, Error: err.Error(),
		LogTail: s.logs.Tail(40), Command: m.cfg.FFmpegPath + " " + strings.Join(s.args, " "),
	})
	if len(m.failures) > 10 {
		m.failures = m.failures[len(m.failures)-10:]
	}
}

// Failures returns the most recent session failures, newest first.
func (m *Manager) Failures() []Failure {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Failure, len(m.failures))
	for i, f := range m.failures {
		out[len(m.failures)-1-i] = f
	}
	return out
}

// releaseGrace is how long a released session survives with no viewer
// activity. Other viewers poll the playlist every segment, so they keep
// a shared session alive through this window.
const releaseGrace = 4 * time.Second

// Release tells the manager a viewer has left the channel. The session is
// not stopped on the spot, because another viewer may be on it; instead
// its idle clock is wound forward so the reaper frees the tuner within
// releaseGrace unless someone else touches it.
func (m *Manager) Release(channel string) {
	m.mu.Lock()
	s, ok := m.sessions[channel]
	m.mu.Unlock()
	if !ok || s.Done() {
		return
	}
	cutoff := time.Now().Add(releaseGrace - m.cfg.IdleTimeout)
	if cutoff.Before(s.LastAccess()) {
		s.lastAccess.Store(cutoff.UnixNano())
	}
}

// Session returns a live session without starting one.
func (m *Manager) Session(channel string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[channel]
	if !ok || s.Done() {
		return nil, false
	}
	return s, true
}

// live returns the running session for a channel under m.mu, clearing a
// dead one out of the way.
func (m *Manager) live(channel string) (*Session, bool) {
	if s, ok := m.sessions[channel]; ok {
		if !s.Done() {
			return s, true
		}
		delete(m.sessions, channel)
		_ = os.RemoveAll(s.Dir)
	}
	return nil, false
}

func (m *Manager) get(ctx context.Context, channel string) (*Session, bool, error) {
	m.mu.Lock()
	if s, ok := m.live(channel); ok {
		s.Touch()
		m.mu.Unlock()
		return s, false, nil
	}
	active := map[string]bool{}
	for ch := range m.sessions {
		active[ch] = true
	}
	m.mu.Unlock()
	if m.ExternalActive != nil {
		for _, ch := range m.ExternalActive() {
			active[ch] = true
		}
	}

	// Resolving asks the tuners for their status, which is a network
	// round trip; do it without holding the lock so the Status page and
	// other channels are not stalled by a slow device.
	src, err := m.resolve.ResolveSource(ctx, channel, active)
	if err != nil {
		return nil, false, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.live(channel); ok {
		// Someone else started it while we were asking the tuner.
		s.Touch()
		return s, false, nil
	}
	enc := m.cfg.Encoder
	if m.degraded.Load() {
		enc = EncoderSoftware
	}
	s, err := m.start(channel, src, enc)
	if err != nil {
		return nil, false, err
	}
	m.sessions[channel] = s
	return s, true, nil
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func (m *Manager) start(channel string, src tuner.Source, enc Encoder) (*Session, error) {
	dir := filepath.Join(m.cfg.BufferDir, unsafeChars.ReplaceAllString(channel, "_"))
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("session dir: %w", err)
	}
	args := BuildArgs(TranscodeOptions{
		Input:          src.URL,
		OutDir:         dir,
		Encoder:        enc,
		RenderDevice:   m.cfg.RenderDevice,
		SegmentSeconds: m.cfg.SegmentSeconds,
		WindowSegments: m.cfg.WindowSegments,
		VideoBitrateK:  m.cfg.VideoBitrateK,
		AudioBitrateK:  m.cfg.AudioBitrateK,
		Deinterlace:    m.cfg.Deinterlace,
		LogLevel:       m.cfg.FFmpegLogLevel,
	})
	cmd := exec.Command(m.cfg.FFmpegPath, args...)
	cmd.Stdin = nil
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	s := &Session{
		Channel: channel,
		Source:  src,
		Encoder: enc,
		Dir:     dir,
		Started: time.Now(),
		cmd:     cmd,
		args:    args,
		done:    make(chan struct{}),
		logs:    newRingLog(120),
	}
	s.Touch()
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	m.log.Info("session started", "channel", channel, "device", src.DeviceID, "encoder", enc, "pid", cmd.Process.Pid)
	m.log.Debug("ffmpeg command", "args", strings.Join(args, " "))

	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			line := sc.Text()
			s.logs.Add(line)
			m.log.Debug("ffmpeg", "channel", channel, "line", line)
		}
	}()
	go func() {
		s.exitErr = cmd.Wait()
		close(s.done)
		if s.stopping.Load() {
			m.log.Debug("session exited on request", "channel", channel)
		} else if s.exitErr != nil {
			m.log.Warn("session exited", "channel", channel, "err", s.exitErr, "tail", s.logs.Tail(5))
			if countSegments(s.PlaylistPath()) >= minSegmentsReady {
				// Died after it was serving; readiness failures are recorded by Acquire.
				m.recordFailure(s, fmt.Errorf("exited while streaming: %w", s.exitErr))
			}
		} else {
			m.log.Info("session exited", "channel", channel)
		}
	}()
	return s, nil
}

// minSegmentsReady is how many segments must exist before a playlist is
// handed to a viewer. With B-frames disabled a single segment is a clean,
// forward-only run that the player can render at once, so one is enough;
// waiting for a second costs a full segment length on every channel
// change, and on a live source that time cannot be bought back.
const minSegmentsReady = 1

// waitReady blocks until the playlist lists enough segments to start.
func (m *Manager) waitReady(ctx context.Context, s *Session) error {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.ReadyTimeout)
	defer cancel()
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		if n := countSegments(s.PlaylistPath()); n >= minSegmentsReady {
			return nil
		}
		select {
		case <-s.done:
			return fmt.Errorf("ffmpeg exited before producing output: %w (%s)", s.exitErr, strings.Join(s.logs.Tail(6), " | "))
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for stream to start; ffmpeg says: %s", strings.Join(s.logs.Tail(6), " | "))
		case <-t.C:
		}
	}
}

func countSegments(playlist string) int {
	data, err := os.ReadFile(playlist)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "#EXTINF")
}

// Stop ends a session and deletes its buffer.
func (m *Manager) Stop(channel string) {
	m.mu.Lock()
	s, ok := m.sessions[channel]
	if ok {
		delete(m.sessions, channel)
	}
	m.mu.Unlock()
	if ok {
		m.stop(s)
	}
}

func (m *Manager) stop(s *Session) {
	s.stopping.Store(true)
	if !s.Done() && s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-s.done:
		case <-time.After(3 * time.Second):
			_ = s.cmd.Process.Kill()
			<-s.done
		}
	}
	_ = os.RemoveAll(s.Dir)
	m.log.Info("session stopped", "channel", s.Channel)
}

// StopAll ends every session; used on shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			m.stop(s)
		}(s)
	}
	wg.Wait()
}

// Reap stops sessions nobody has touched within the idle timeout, and
// clears out sessions whose ffmpeg died. Run it in a goroutine.
func (m *Manager) Reap(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.expireViewers()
			var idle []*Session
			m.mu.Lock()
			for ch, s := range m.sessions {
				if s.Done() || time.Since(s.LastAccess()) > m.cfg.IdleTimeout {
					delete(m.sessions, ch)
					idle = append(idle, s)
				}
			}
			m.mu.Unlock()
			for _, s := range idle {
				m.stop(s)
			}
		}
	}
}

// Statuses returns a snapshot of all sessions.
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.sessions))
	for _, s := range m.sessions {
		st := Status{
			Channel:    s.Channel,
			DeviceID:   s.Source.DeviceID,
			Encoder:    s.Encoder,
			Started:    s.Started,
			LastAccess: s.LastAccess(),
			Segments:   countSegments(s.PlaylistPath()),
			Running:    !s.Done(),
			LogTail:    s.logs.Tail(5),
			Viewers:    m.viewersOf(s.Channel),
		}
		if s.Done() && s.exitErr != nil {
			st.Error = s.exitErr.Error()
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Channel < out[j].Channel })
	return out
}

// viewersOf lists the viewers on a channel, oldest first. Caller holds m.mu.
func (m *Manager) viewersOf(channel string) []Viewer {
	out := []Viewer{}
	for _, v := range m.viewers {
		if v.Channel == channel {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// Join acquires the channel's session for a viewer. A viewer that was on
// another channel is moved, releasing the old channel if it was alone
// there. An empty id allocates a new handle.
func (m *Manager) Join(ctx context.Context, channel, id, name, addr, user, userName string) (*Session, *Viewer, error) {
	s, err := m.Acquire(ctx, channel)
	if err != nil {
		return nil, nil, err
	}
	if id == "" {
		id = newViewerID()
	}
	now := time.Now()
	var release string
	m.mu.Lock()
	v, ok := m.viewers[id]
	if !ok {
		v = &Viewer{ID: id, Started: now}
		m.viewers[id] = v
	} else if v.Channel != channel {
		v.Started = now
		if m.countViewersLocked(v.Channel) == 1 {
			release = v.Channel
		}
	}
	v.Channel = channel
	v.Addr = addr
	v.User, v.UserName = user, userName
	v.LastSeen = now
	v.Paused = false
	if name != "" {
		v.Name = name
	}
	out := *v
	m.mu.Unlock()
	if release != "" {
		m.Release(release)
	}
	s.Touch()
	return s, &out, nil
}

// RenameViewer gives a viewer a better name found after it joined (its
// computer's name from the home network), as long as it still has the
// name it joined with.
func (m *Manager) RenameViewer(id, from, to string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.viewers[id]
	if !ok || v.Name != from {
		return false
	}
	v.Name = to
	return true
}

// Heartbeat records that a viewer is still there and keeps its session
// alive, paused or not. It reports false for an unknown handle, which
// tells the client to join again.
func (m *Manager) Heartbeat(id string, paused bool) bool {
	m.mu.Lock()
	v, ok := m.viewers[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	v.LastSeen = time.Now()
	v.Paused = paused
	s, live := m.live(v.Channel)
	m.mu.Unlock()
	if live {
		s.Touch()
	}
	return true
}

// Leave drops a viewer handle and releases its channel if nobody else is
// on it.
func (m *Manager) Leave(id string) {
	m.mu.Lock()
	v, ok := m.viewers[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.viewers, id)
	alone := m.countViewersLocked(v.Channel) == 0
	m.mu.Unlock()
	if alone {
		m.Release(v.Channel)
	}
}

// Viewers returns every viewer handle, oldest first.
func (m *Manager) Viewers() []Viewer {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Viewer, 0, len(m.viewers))
	for _, v := range m.viewers {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

func (m *Manager) countViewersLocked(channel string) int {
	n := 0
	for _, v := range m.viewers {
		if v.Channel == channel {
			n++
		}
	}
	return n
}

// expireViewers drops handles whose heartbeats stopped.
func (m *Manager) expireViewers() {
	if m.cfg.ViewerTimeout <= 0 {
		return
	}
	var stale []string
	m.mu.Lock()
	for id, v := range m.viewers {
		if time.Since(v.LastSeen) > m.cfg.ViewerTimeout {
			stale = append(stale, id)
		}
	}
	m.mu.Unlock()
	for _, id := range stale {
		m.log.Info("viewer expired", "viewer", id)
		m.Leave(id)
	}
}

func newViewerID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// Args returns the ffmpeg argument list a session would use for src,
// for diagnostics.
func (m *Manager) Args(src tuner.Source, outDir string, logLevel string) []string {
	return BuildArgs(TranscodeOptions{
		Input:          src.URL,
		OutDir:         outDir,
		Encoder:        m.cfg.Encoder,
		RenderDevice:   m.cfg.RenderDevice,
		SegmentSeconds: m.cfg.SegmentSeconds,
		WindowSegments: m.cfg.WindowSegments,
		VideoBitrateK:  m.cfg.VideoBitrateK,
		AudioBitrateK:  m.cfg.AudioBitrateK,
		Deinterlace:    m.cfg.Deinterlace,
		LogLevel:       logLevel,
	})
}

// ErrNoSession is returned when a segment is requested for an idle channel.
var ErrNoSession = errors.New("no active session")

// ringLog keeps the last N lines of ffmpeg output for diagnostics.
type ringLog struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newRingLog(max int) *ringLog { return &ringLog{max: max} }

func (r *ringLog) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

func (r *ringLog) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out
}
