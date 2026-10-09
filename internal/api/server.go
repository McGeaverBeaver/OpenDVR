// Package api exposes the REST and HLS endpoints and serves the web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/apksign"
	"github.com/McGeaverBeaver/opendvr/internal/audit"
	"github.com/McGeaverBeaver/opendvr/internal/auth"
	"github.com/McGeaverBeaver/opendvr/internal/dvr"
	"github.com/McGeaverBeaver/opendvr/internal/guide"
	"github.com/McGeaverBeaver/opendvr/internal/hdhomerun"
	"github.com/McGeaverBeaver/opendvr/internal/logbuf"
	"github.com/McGeaverBeaver/opendvr/internal/mcpserver"
	"github.com/McGeaverBeaver/opendvr/internal/netdiag"
	"github.com/McGeaverBeaver/opendvr/internal/stream"
	"github.com/McGeaverBeaver/opendvr/internal/tuner"
	"github.com/McGeaverBeaver/opendvr/internal/users"
)

func init() {
	// Go's table lacks the PWA manifest type; Chrome refuses to install
	// a PWA whose manifest is served as text/plain.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// Deps are everything the HTTP layer serves.
type Deps struct {
	Registry *tuner.Registry
	Streams  *stream.Manager
	Client   *hdhomerun.Client
	Caps     stream.Capabilities
	Guide    *guide.Service
	DVR      *dvr.Service
	Auth     *auth.Service
	Audit    *audit.Log
	Geo      audit.Locator
	Users    *users.Store
	// APKSigner re-signs the built-in TV app with this server's key.
	APKSigner *apksign.Signer
	// HDHREmulate serves discover.json, lineup.json and /auto/v{ch}.
	HDHREmulate bool
	// Notify names the configured notification transports, for the summary.
	Notify  []string
	Hosts   []string
	WebDir  string
	Version string
	DataDir string
	APKDir  string
	Logs    *logbuf.Buffer
	Log     *slog.Logger
}

// Server wires the registry and stream manager to HTTP.
type Server struct {
	reg       *tuner.Registry
	streams   *stream.Manager
	client    *hdhomerun.Client
	caps      stream.Capabilities
	guide     *guide.Service
	dvr       *dvr.Service
	auth      *auth.Service
	audit     *audit.Log
	geo       audit.Locator
	users     *users.Store
	apkSigner *apksign.Signer
	signed    signedCache
	hostNames hostCache
	hdhr      bool
	exports   exports
	notify    []string
	hosts     []string
	webDir    string
	logs      *logbuf.Buffer
	log       *slog.Logger
	dataDir   string
	apkDir    string
	started   time.Time
	version   string
}

// New builds the HTTP handler.
func New(d Deps) *Server {
	return &Server{
		reg:       d.Registry,
		streams:   d.Streams,
		client:    d.Client,
		caps:      d.Caps,
		guide:     d.Guide,
		dvr:       d.DVR,
		auth:      d.Auth,
		audit:     d.Audit,
		geo:       d.Geo,
		users:     d.Users,
		apkSigner: d.APKSigner,
		hdhr:      d.HDHREmulate,
		notify:    d.Notify,
		hosts:     d.Hosts,
		webDir:    d.WebDir,
		logs:      d.Logs,
		dataDir:   d.DataDir,
		apkDir:    d.APKDir,
		log:       d.Log,
		started:   time.Now(),
		version:   d.Version,
	}
}

// Handler returns the routed http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/me", s.handleMe)
	if s.auth.Enabled() {
		s.auth.Mount(mux)
	}
	mux.HandleFunc("GET /api/system", s.handleSystem)
	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("POST /api/devices/refresh", s.handleRefresh)
	mux.HandleFunc("GET /api/devices/{id}/status", s.handleDeviceStatus)
	mux.HandleFunc("GET /api/channels", s.handleChannels)
	mux.HandleFunc("GET /api/channels/{number}/stream/"+stream.PlaylistName, s.handlePlaylist)
	mux.HandleFunc("GET /api/channels/{number}/stream/{file}", s.handleSegment)
	mux.HandleFunc("POST /api/channels/{number}/stream/release", s.handleRelease)
	mux.HandleFunc("POST /api/watch", s.handleWatch)
	mux.HandleFunc("POST /api/watch/{id}/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("POST /api/watch/{id}/leave", s.handleLeave)
	mux.HandleFunc("GET /api/viewers", s.handleViewers)
	mux.HandleFunc("GET /api/guide", s.handleGuide)
	mux.HandleFunc("GET /api/guide/now", s.handleGuideNow)
	mux.HandleFunc("GET /api/guide/{number}", s.handleGuideChannel)
	s.mountDVR(mux)
	s.mountAdmin(mux)
	s.mountHDHR(mux)
	mux.HandleFunc("GET /downloads/{file}", s.handleDownload)
	mux.HandleFunc("GET /api/downloads", s.handleDownloads)
	mux.HandleFunc("GET /api/audit", s.handleAudit)
	mux.HandleFunc("GET /api/mcp/info", s.handleMCPInfo)
	mux.HandleFunc("GET /api/mcp/clients", s.handleMCPClients)
	mux.HandleFunc("DELETE /api/mcp/clients/{id}", s.handleMCPClientDelete)
	mux.HandleFunc("DELETE /api/mcp/sessions/{id}", s.handleMCPSessionDelete)
	mux.HandleFunc("GET /api/streams", s.handleStreams)
	mux.HandleFunc("GET /api/streams/failures", s.handleStreamFailures)
	mux.HandleFunc("GET /api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	// Model Context Protocol endpoint for Claude Desktop and other agents.
	mux.Handle("/mcp", mcpserver.Handler(mcpserver.New(mcpserver.Deps{
		Registry: s.reg, Streams: s.streams, Client: s.client, Caps: s.caps, Guide: s.guide, DVR: s.dvr,
		Hosts: s.hosts, Logs: s.logs, Version: s.version,
	})))
	mux.HandleFunc("DELETE /api/streams/{number}", s.handleStopStream)
	mux.Handle("/", s.staticHandler())
	return secureHeaders(logRequests(s.log, s.auth.Protect(mux)))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uptime": time.Since(s.started).Round(time.Second).String()})
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":      s.version,
		"uptime":       time.Since(s.started).Round(time.Second).String(),
		"ffmpeg":       s.caps,
		"degraded":     s.streams.Degraded(),
		"hosts":        s.hosts,
		"lastRefresh":  s.reg.LastRefresh(),
		"deviceCount":  len(s.reg.Devices()),
		"channelCount": len(s.reg.Channels()),
		"guide":        s.guideStatus(),
	})
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.reg.Devices())
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.reg.Refresh(ctx); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"devices":  s.reg.Devices(),
		"channels": len(s.reg.Channels()),
	})
}

func (s *Server) handleDeviceStatus(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.reg.Device(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown device")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st, err := s.client.Status(ctx, &dev)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	chans := s.reg.Channels()
	if chans == nil {
		chans = []tuner.Channel{}
	}
	if s.restricted(r) {
		kept := []tuner.Channel{}
		for _, c := range chans {
			if s.mayWatch(r, c.GuideNumber) {
				kept = append(kept, c)
			}
		}
		chans = kept
	}
	writeJSON(w, http.StatusOK, chans)
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	sess, err := s.streams.Acquire(r.Context(), number)
	if err != nil {
		s.writeStreamError(w, number, err)
		return
	}
	sess.Touch()
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	if tok := s.streamToken(r, number); tok != "" {
		// Segment URLs in the playlist must carry the token too: players
		// resolve them relative to the playlist without its query string.
		data, err := os.ReadFile(sess.PlaylistPath())
		if err != nil {
			writeError(w, http.StatusNotFound, "playlist not ready")
			return
		}
		_, _ = w.Write(tokenizePlaylist(data, tok))
		return
	}
	http.ServeFile(w, r, sess.PlaylistPath())
}

// streamToken returns the token to embed in a playlist: the one the request
// came with, or a fresh one for the signed-in user. Empty when auth is off.
func (s *Server) streamToken(r *http.Request, channel string) string {
	if !s.auth.Enabled() {
		return ""
	}
	if t := r.URL.Query().Get("t"); t != "" {
		return t
	}
	if u, ok := s.auth.UserFromRequest(r); ok {
		return s.auth.StreamToken(u, channel)
	}
	return ""
}

// tokenizePlaylist appends ?t=token to every segment line.
func tokenizePlaylist(data []byte, token string) []byte {
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if strings.HasSuffix(strings.TrimSpace(l), ".ts") {
			lines[i] = strings.TrimSpace(l) + "?t=" + url.QueryEscape(token)
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

var segmentName = regexp.MustCompile(`^seg_\d{6}\.ts$`)

func (s *Server) handleSegment(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	file := r.PathValue("file")
	if !segmentName.MatchString(file) {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	sess, ok := s.streams.Session(number)
	if !ok {
		writeError(w, http.StatusNotFound, stream.ErrNoSession.Error())
		return
	}
	sess.Touch()
	w.Header().Set("Content-Type", "video/mp2t")
	// Segments are immutable once written; the playlist controls freshness.
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, filepath.Join(sess.Dir, file))
}

// handleDiagnostics probes a host from inside the container so routing and
// firewall problems can be seen without a shell.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host != "" && !hostName.MatchString(host) {
		writeError(w, http.StatusBadRequest, "host must be an IP address or hostname")
		return
	}
	if host == "" && len(s.hosts) > 0 {
		host = s.hosts[0]
	}
	if host == "" {
		writeError(w, http.StatusBadRequest, "host query parameter required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, netdiag.Run(ctx, host))
}

// handleRelease is called by the player when it leaves a channel, so the
// tuner is freed in seconds rather than after the idle timeout.
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	s.streams.Release(r.PathValue("number"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	n := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			n = i
		}
	}
	if n > 1000 {
		n = 1000
	}
	lines := []string{}
	if s.logs != nil {
		lines = s.logs.Lines(n)
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// handleStreams lists sessions. Who is watching, and from where, is
// shown to administrators only; others see the count and the names.
func (s *Server) handleStreams(w http.ResponseWriter, r *http.Request) {
	st := s.streams.Statuses()
	admin := s.isAdmin(r)
	for i := range st {
		for j := range st[i].Viewers {
			s.decorateViewer(&st[i].Viewers[j], admin)
		}
		if !admin {
			st[i].LogTail = nil
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// decorateViewer adds the place for administrators and hides the
// address and account from everyone else.
func (s *Server) decorateViewer(v *stream.Viewer, admin bool) {
	if !admin {
		v.Addr, v.User = "", ""
		return
	}
	if s.geo != nil && v.Addr != "" {
		if p, ok := s.geo.Lookup(v.Addr); ok {
			v.Place = p.Country
			if p.City != "" {
				v.Place = p.City + ", " + p.Country
			}
		}
	}
}

func (s *Server) handleStreamFailures(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	f := s.streams.Failures()
	if f == nil {
		f = []stream.Failure{}
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handleStopStream(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	s.streams.Stop(r.PathValue("number"))
	s.record(r, audit.Event{Kind: "admin.stop_stream", Detail: "stopped channel " + r.PathValue("number")})
	w.WriteHeader(http.StatusNoContent)
}

var hostName = regexp.MustCompile(`^[A-Za-z0-9.:\-]{1,253}$`)

// isAdmin reports whether the request holds the administrator role.
func (s *Server) isAdmin(r *http.Request) bool { return s.auth.IsAdmin(r) }

// requireAdmin answers 403 and returns false for non-administrators.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.isAdmin(r) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrators only", "code": "forbidden"})
	return false
}

// userOf is the signed-in account for audit entries; empty with auth off.
func (s *Server) userOf(r *http.Request) (sub, name string) {
	if !s.auth.Enabled() {
		return "", ""
	}
	u, ok := s.auth.UserFromRequest(r)
	if !ok {
		return "", ""
	}
	name = u.Name
	if name == "" {
		name = u.Email
	}
	return u.Subject, name
}

// record writes an audit event for the request's user and address.
func (s *Server) record(r *http.Request, e audit.Event) {
	if s.audit == nil {
		return
	}
	if e.User == "" {
		e.User, e.Name = s.userOf(r)
	}
	e.IP = audit.ClientIP(r)
	e.Agent = audit.Agent(r)
	s.audit.Record(e)
}

// handleAudit returns the audit log, newest first, for administrators.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var events []audit.Event
	if s.audit != nil {
		events = s.audit.Query(r.URL.Query().Get("kind"), r.URL.Query().Get("q"), limit)
	}
	if events == nil {
		events = []audit.Event{}
	}
	geoFile := ""
	if g, ok := s.geo.(interface{ Path() string }); ok {
		geoFile = g.Path()
	}
	writeJSON(w, http.StatusOK, map[string]any{"geoip": geoFile != "", "geoipFile": geoFile, "adminGroup": s.auth.AdminGroup(), "events": events})
}

// staticHandler serves the built SPA with index.html fallback for client
// side routes. If the web directory is missing it explains why.
func (s *Server) staticHandler() http.Handler {
	index := filepath.Join(s.webDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		s.log.Warn("web UI not found; API only", "webDir", s.webDir)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("opendvr API is running. Web UI not built (see WEB_DIR).\n"))
		})
	}
	fs := http.FileServer(http.Dir(s.webDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		if clean == "/" {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, index)
			return
		}
		full := filepath.Join(s.webDir, filepath.FromSlash(clean))
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			if strings.HasPrefix(clean, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fs.ServeHTTP(w, r)
			return
		}
		// Anything that looks like an asset is a real 404. Everything else
		// (including "/watch/5.1", whose dot is not an extension) is a client
		// side route and gets the SPA shell.
		if strings.HasPrefix(clean, "/api/") || strings.HasPrefix(clean, "/assets/") || assetExt[path.Ext(clean)] {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

var assetExt = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".map": true, ".json": true, ".webmanifest": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".ico": true, ".webp": true,
	".woff": true, ".woff2": true, ".ttf": true, ".txt": true, ".xml": true,
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// secureHeaders adds the browser protections every response should
// carry: no MIME sniffing, no framing by other sites, no referrer leaks,
// and a content security policy that confines scripts to this origin.
// Programme art comes from SiliconDust's image host; QR codes are data
// URLs; hls.js plays through blob: URLs.
func secureHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: https://img.hdhomerun.com; media-src 'self' blob: data:; connect-src 'self'; " +
		"worker-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self' https:"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Segment, playlist and the Status page's polling GETs are very chatty;
		// keep them at debug so the log buffer holds what matters: sign-ins,
		// session starts, failures and anything that was not a 2xx.
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		lvl := slog.LevelInfo
		quietGet := r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/") && sw.status < 400 && !strings.HasPrefix(r.URL.Path, "/api/watch")
		if strings.Contains(r.URL.Path, "/stream/") || quietGet || (!strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/auth/") && !strings.HasPrefix(r.URL.Path, "/mcp") && r.URL.Path != "/tv") {
			lvl = slog.LevelDebug
		}
		if sw.status >= 500 {
			lvl = slog.LevelWarn
		}
		log.Log(r.Context(), lvl, "http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "dur", time.Since(start).Round(time.Millisecond))
	})
}

// IsNotFound helps callers classify registry errors.
func IsNotFound(err error) bool {
	return err != nil && (errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "unknown"))
}

// writeStreamError maps a session start failure to a status code. A busy
// tuner set is a 409 carrying what each tuner is locked to, so the player
// can offer those channels instead of a dead end.
func (s *Server) writeStreamError(w http.ResponseWriter, channel string, err error) {
	var busy *tuner.ErrTunersBusy
	if errors.As(err, &busy) {
		s.log.Info("tuners busy", "channel", channel, "tuners", len(busy.Tuners))
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "All tuners are busy",
			"code":    "tuners_busy",
			"channel": channel,
			"tuners":  busy.Tuners,
		})
		return
	}
	status := http.StatusBadGateway
	if strings.Contains(err.Error(), "unknown channel") {
		status = http.StatusNotFound
	}
	s.log.Warn("playlist unavailable", "channel", channel, "err", err)
	writeError(w, status, err.Error())
}

// handleWatch registers a viewer on a channel, starting or joining its
// session. The response carries the viewer handle the client must keep
// for heartbeats and leave.
func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Channel  string `json:"channel"`
		ViewerID string `json:"viewerId"`
		// Name is one the person chose; DeviceName the device's own (TV
		// app); Client the browser and system (web app). See devicename.go.
		Name       string `json:"name"`
		DeviceName string `json:"deviceName"`
		Client     string `json:"client"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	in.Channel = strings.TrimSpace(in.Channel)
	if in.Channel == "" {
		writeError(w, http.StatusBadRequest, "channel is required")
		return
	}
	if !s.mayWatch(r, in.Channel) {
		writeError(w, http.StatusForbidden, "this channel is not allowed on your profile")
		return
	}
	ip := audit.ClientIP(r)
	name, lookup := s.viewerName(r, ip, watchNames{Name: in.Name, DeviceName: in.DeviceName, Client: in.Client})
	sub, uname := s.userOf(r)
	sess, v, err := s.streams.Join(r.Context(), in.Channel, in.ViewerID, name, ip, sub, uname)
	if err != nil {
		s.writeStreamError(w, in.Channel, err)
		return
	}
	if lookup {
		// The router may know this computer or phone by name; if so, use it.
		id := v.ID
		s.hostNames.resolve(ip, func(host string) { s.streams.RenameViewer(id, name, host) })
	}
	s.record(r, audit.Event{Kind: "stream.watch", Detail: "watching " + sess.Channel, Fields: map[string]string{"channel": sess.Channel, "device": name}})
	writeJSON(w, http.StatusOK, map[string]any{
		"viewerId": v.ID,
		"channel":  sess.Channel,
		"deviceId": sess.Source.DeviceID,
		"encoder":  sess.Encoder,
		"playlist": s.playlistURL(r, sess.Channel),
	})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in)
	if !s.streams.Heartbeat(r.PathValue("id"), in.Paused) {
		writeError(w, http.StatusNotFound, "unknown viewer; join again")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	s.streams.Leave(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleViewers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	vs := s.streams.Viewers()
	for i := range vs {
		s.decorateViewer(&vs[i], true)
	}
	writeJSON(w, http.StatusOK, vs)
}

// clientAddr is the viewer's address, honouring X-Forwarded-For only
// from a trusted proxy.
func clientAddr(r *http.Request) string { return audit.ClientIP(r) }

func (s *Server) guideStatus() any {
	if s.guide == nil {
		return nil
	}
	return s.guide.Status()
}

// guideWindow parses from/to (unix seconds) with a default of one hour
// back to six hours ahead, capped at two days.
func guideWindow(r *http.Request) (int64, int64) {
	now := time.Now().Unix()
	from, to := now-3600, now+6*3600
	if v, err := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64); err == nil && v > 0 {
		from = v
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64); err == nil && v > from {
		to = v
	}
	if to-from > 48*3600 {
		to = from + 48*3600
	}
	return from, to
}

// handleGuide returns every channel's programmes in a window, extending
// the cache first when the window reaches past what is held.
func (s *Server) handleGuide(w http.ResponseWriter, r *http.Request) {
	if s.guide == nil {
		writeJSON(w, http.StatusOK, []guide.Channel{})
		return
	}
	from, to := guideWindow(r)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	s.guide.EnsureWindow(ctx, to)
	w.Header().Set("Cache-Control", "private, max-age=60")
	chans := s.guide.Window(from, to)
	if s.restricted(r) {
		kept := chans[:0]
		for _, c := range chans {
			if s.mayWatch(r, c.GuideNumber) {
				kept = append(kept, c)
			}
		}
		chans = kept
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":     from,
		"to":       to,
		"channels": chans,
	})
}

func (s *Server) handleGuideNow(w http.ResponseWriter, r *http.Request) {
	if s.guide == nil {
		writeJSON(w, http.StatusOK, map[string]guide.NowNext{})
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=30")
	writeJSON(w, http.StatusOK, s.guide.NowNext(time.Now().Unix()))
}

func (s *Server) handleGuideChannel(w http.ResponseWriter, r *http.Request) {
	if s.guide == nil {
		writeError(w, http.StatusNotFound, "guide not available")
		return
	}
	number := r.PathValue("number")
	_, to := guideWindow(r)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	s.guide.EnsureWindow(ctx, to)
	c, ok := s.guide.Channel(number)
	if !ok {
		writeError(w, http.StatusNotFound, "no listings for channel "+number)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// playlistURL is the playlist path for a channel, with a stream token
// when auth is on so the player's requests carry it.
func (s *Server) playlistURL(r *http.Request, channel string) string {
	p := "/api/channels/" + channel + "/stream/" + stream.PlaylistName
	if tok := s.streamToken(r, channel); tok != "" {
		p += "?t=" + url.QueryEscape(tok)
	}
	return p
}

// handleMe tells the client whether auth is on and who is signed in.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.auth.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "admin": true, "favorites": []string{}})
		return
	}
	u, ok := s.auth.UserFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"enabled": true, "error": "sign in required", "code": "unauthenticated"})
		return
	}
	var p users.Profile
	if s.users != nil {
		p = s.users.Touch(u.Subject, u.Name, u.Email)
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "user": u, "admin": s.auth.IsAdmin(r), "restricted": p.Restricted, "favorites": p.Favorites})
}

var downloadName = regexp.MustCompile(`^[A-Za-z0-9._-]+\.apk$`)

// downloadDirs are where app packages are looked for: an APK dropped into
// the data directory wins over the one baked into the image.
func (s *Server) downloadDirs() []string {
	var dirs []string
	for _, d := range []string{s.dataDir, s.apkDir} {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func (s *Server) findDownload(name string) string {
	if !downloadName.MatchString(name) {
		return ""
	}
	for _, d := range s.downloadDirs() {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// ---- MCP connector administration ----------------------------------------------

func (s *Server) mcpAdmin(w http.ResponseWriter, r *http.Request) bool {
	return s.requireAdmin(w, r)
}

func (s *Server) handleMCPInfo(w http.ResponseWriter, r *http.Request) {
	base := "http://" + r.Host
	if s.auth.Enabled() {
		base = s.auth.PublicURL(r)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mcpUrl":    base + "/mcp",
		"oauth":     s.auth.Enabled(),
		"admin":     s.auth.IsMCPAdmin(r),
		"mcpGroup":  s.auth.MCPGroup(),
		"toolCount": mcpserver.ToolCount(),
	})
}

func (s *Server) handleMCPClients(w http.ResponseWriter, r *http.Request) {
	if !s.mcpAdmin(w, r) {
		return
	}
	if !s.auth.Enabled() {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.auth.MCPClients())
}

func (s *Server) handleMCPClientDelete(w http.ResponseWriter, r *http.Request) {
	if !s.mcpAdmin(w, r) {
		return
	}
	s.record(r, audit.Event{Kind: "mcp.revoked", Detail: "removed assistant " + r.PathValue("id")})
	if !s.auth.Enabled() || !s.auth.DeleteMCPClient(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "unknown client")
		return
	}
	s.log.Info("mcp client removed", "clientId", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMCPSessionDelete(w http.ResponseWriter, r *http.Request) {
	if !s.mcpAdmin(w, r) {
		return
	}
	s.record(r, audit.Event{Kind: "mcp.revoked", Detail: "revoked assistant session " + r.PathValue("id")})
	if !s.auth.Enabled() || !s.auth.RevokeMCPSession(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "unknown session")
		return
	}
	s.log.Info("mcp session revoked", "sessionId", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}
