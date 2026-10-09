package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/audit"
	"github.com/McGeaverBeaver/opendvr/internal/tuner"
)

// HDHomeRun emulation: Plex, Jellyfin, Emby and Channels DVR itself can
// add this server as if it were a tuner. They read discover.json and
// lineup.json, then open /auto/v{channel} for an MPEG-TS stream, which
// is proxied straight from the real tuner (no transcode) through the
// same tuner selection live viewers use, so the tuner counts as taken.
//
// Those apps cannot sign in, so when auth is on the stream URLs in the
// lineup carry a long-lived token and the two JSON files are open; they
// reveal only channel numbers and names.

// exportHTTP streams for hours; only the connection and headers are bounded.
var exportHTTP = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 15 * time.Second}}

// exports tracks the channels being proxied, for tuner accounting.
type exports struct {
	mu    sync.Mutex
	count map[string]int
}

func (e *exports) add(ch string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.count == nil {
		e.count = map[string]int{}
	}
	e.count[ch]++
}

func (e *exports) remove(ch string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.count[ch] <= 1 {
		delete(e.count, ch)
	} else {
		e.count[ch]--
	}
}

// Active lists channels with an exported stream open.
func (e *exports) Active() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.count))
	for ch := range e.count {
		out = append(out, ch)
	}
	return out
}

func (s *Server) mountHDHR(mux *http.ServeMux) {
	if !s.hdhr {
		return
	}
	mux.HandleFunc("GET /discover.json", s.handleHDHRDiscover)
	mux.HandleFunc("GET /lineup_status.json", s.handleHDHRLineupStatus)
	mux.HandleFunc("GET /lineup.json", s.handleHDHRLineup)
	mux.HandleFunc("GET /auto/v{channel}", s.handleHDHRStream)
}

func (s *Server) hdhrBase(r *http.Request) string {
	return s.auth.PublicURL(r)
}

func (s *Server) handleHDHRDiscover(w http.ResponseWriter, r *http.Request) {
	count := 0
	for _, d := range s.reg.Devices() {
		if d.Device.TunerCount > 0 {
			count += d.Device.TunerCount
		} else {
			count += 2
		}
	}
	if count == 0 {
		count = 2
	}
	base := s.hdhrBase(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"FriendlyName":    "OpenDVR",
		"Manufacturer":    "OpenDVR",
		"ModelNumber":     "HDHR5-4US",
		"FirmwareName":    "hdhomerun5_atsc",
		"FirmwareVersion": "20240101",
		"DeviceID":        "0D5E0D57",
		"DeviceAuth":      "opendvr",
		"BaseURL":         base,
		"LineupURL":       base + "/lineup.json",
		"TunerCount":      count,
	})
}

func (s *Server) handleHDHRLineupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ScanInProgress": 0, "ScanPossible": 0, "Source": "Antenna", "SourceList": []string{"Antenna"}})
}

func (s *Server) handleHDHRLineup(w http.ResponseWriter, r *http.Request) {
	base := s.hdhrBase(r)
	tok := ""
	if s.auth.Enabled() {
		tok = "?t=" + s.auth.ExportToken()
	}
	type entry struct {
		GuideNumber string `json:"GuideNumber"`
		GuideName   string `json:"GuideName"`
		HD          int    `json:"HD,omitempty"`
		URL         string `json:"URL"`
	}
	out := []entry{}
	for _, c := range s.reg.Channels() {
		if c.DRM {
			continue
		}
		e := entry{GuideNumber: c.GuideNumber, GuideName: c.GuideName, URL: base + "/auto/v" + c.GuideNumber + tok}
		if c.HD {
			e.HD = 1
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleHDHRStream proxies the tuner's transport stream.
func (s *Server) handleHDHRStream(w http.ResponseWriter, r *http.Request) {
	channel := r.PathValue("channel")
	active := map[string]bool{}
	for _, st := range s.streams.Statuses() {
		if st.Running {
			active[st.Channel] = true
		}
	}
	for _, ch := range s.exports.Active() {
		active[ch] = true
	}
	if s.dvr != nil {
		for _, ch := range s.dvr.ActiveChannels() {
			active[ch] = true
		}
	}
	rctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	src, err := s.reg.ResolveSource(rctx, channel, active)
	cancel()
	if err != nil {
		if _, busy := err.(*tuner.ErrTunersBusy); busy {
			http.Error(w, "all tuners are busy", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, src.URL, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	res, err := exportHTTP.Do(req)
	if err != nil {
		http.Error(w, "tuner: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		http.Error(w, "tuner answered "+res.Status, http.StatusServiceUnavailable)
		return
	}
	s.exports.add(channel)
	defer s.exports.remove(channel)
	s.record(r, audit.Event{Kind: "stream.export", Detail: "tuner stream for " + channel + " (" + strings.TrimSpace(strings.Split(r.UserAgent(), "/")[0]) + ")"})
	s.log.Info("exported stream started", "channel", channel, "client", audit.ClientIP(r), "agent", r.UserAgent())
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 64*1024)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr != nil {
			if rerr != io.EOF && r.Context().Err() == nil {
				s.log.Warn("exported stream ended", "channel", channel, "err", rerr)
			}
			break
		}
	}
	s.log.Info("exported stream ended", "channel", channel)
}
