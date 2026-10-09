package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/McGeaverBeaver/opendvr/internal/audit"
	"github.com/McGeaverBeaver/opendvr/internal/dvr"
)

// mountDVR adds the recording endpoints.
func (s *Server) mountDVR(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dvr", s.handleDVRSummary)
	mux.HandleFunc("GET /api/dvr/rules", s.handleDVRRules)
	mux.HandleFunc("POST /api/dvr/rules", s.handleDVRRuleSave)
	mux.HandleFunc("PUT /api/dvr/rules/{id}", s.handleDVRRuleSave)
	mux.HandleFunc("DELETE /api/dvr/rules/{id}", s.handleDVRRuleDelete)
	mux.HandleFunc("GET /api/dvr/recordings", s.handleDVRRecordings)
	mux.HandleFunc("POST /api/dvr/recordings", s.handleDVRRecordAiring)
	mux.HandleFunc("POST /api/dvr/record-now", s.handleDVRRecordNow)
	mux.HandleFunc("GET /api/dvr/recordings/{id}", s.handleDVRRecording)
	mux.HandleFunc("DELETE /api/dvr/recordings/{id}", s.handleDVRRecordingDelete)
	mux.HandleFunc("PATCH /api/dvr/recordings/{id}", s.handleDVRRecordingEdit)
	mux.HandleFunc("POST /api/dvr/recordings/{id}/redetect", s.handleDVRRedetect)
	mux.HandleFunc("PUT /api/dvr/recordings/{id}/position", s.handleDVRPosition)
	mux.HandleFunc("GET /api/dvr/recordings/{id}/video", s.handleDVRVideo)
	mux.HandleFunc("GET /api/dvr/recordings/{id}/captions.vtt", s.handleDVRCaptions)
	mux.HandleFunc("GET /api/guide/search", s.handleGuideSearch)
}

func (s *Server) dvrReady(w http.ResponseWriter) bool {
	if s.dvr == nil {
		writeError(w, http.StatusNotFound, "the DVR is not available")
		return false
	}
	return true
}

// dvrWriter is dvrReady plus a check that the caller may change the DVR
// (kids profiles may only watch).
func (s *Server) dvrWriter(w http.ResponseWriter, r *http.Request) bool {
	if !s.dvrReady(w) {
		return false
	}
	if s.restricted(r) {
		writeError(w, http.StatusForbidden, "your profile cannot change recordings")
		return false
	}
	return true
}

// handleDVRCaptions serves the WebVTT sidecar.
func (s *Server) handleDVRCaptions(w http.ResponseWriter, r *http.Request) {
	if !s.dvrReady(w) {
		return
	}
	rec, ok := s.dvr.Recording(r.PathValue("id"))
	if !ok || !rec.Captions {
		writeError(w, http.StatusNotFound, "no captions")
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, s.dvr.CaptionsPath(rec))
}

// RecordingView is a recording plus the URL a player can open.
type RecordingView struct {
	dvr.Recording
	PlayURL     string `json:"playUrl,omitempty"`
	CaptionsURL string `json:"captionsUrl,omitempty"`
	RuleTitle   string `json:"ruleTitle,omitempty"`
}

func (s *Server) recordingView(r *http.Request, rec dvr.Recording) RecordingView {
	v := RecordingView{Recording: rec}
	if rec.Status == dvr.StatusReady {
		v.PlayURL = "/api/dvr/recordings/" + rec.ID + "/video"
		tok := s.streamToken(r, "rec:"+rec.ID)
		if tok != "" {
			v.PlayURL += "?t=" + tok
		}
		if rec.Captions {
			v.CaptionsURL = "/api/dvr/recordings/" + rec.ID + "/captions.vtt"
			if tok != "" {
				v.CaptionsURL += "?t=" + tok
			}
		}
	}
	if rec.RuleID != "" {
		if rule, ok := s.dvr.Rule(rec.RuleID); ok {
			v.RuleTitle = rule.Title
		}
	}
	return v
}

func (s *Server) handleDVRSummary(w http.ResponseWriter, r *http.Request) {
	if !s.dvrReady(w) {
		return
	}
	sum := s.dvr.Summary()
	writeJSON(w, http.StatusOK, map[string]any{
		"recordingsDir": sum.RecordingsDir, "diskFreeBytes": sum.DiskFreeBytes, "diskTotalBytes": sum.DiskTotalBytes,
		"usedBytes": sum.UsedBytes, "comskip": sum.Comskip, "tunerCount": sum.TunerCount, "rules": sum.Rules,
		"counts": sum.Counts, "encoder": sum.Encoder, "lookaheadHours": sum.LookaheadHours, "lastPlan": sum.LastPlan,
		"planError": sum.PlanError, "maxBytes": sum.MaxBytes, "captions": sum.Captions,
		"notify": s.notify, "hdhrEmulate": s.hdhr,
	})
}

func (s *Server) handleDVRRules(w http.ResponseWriter, r *http.Request) {
	if !s.dvrReady(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.dvr.Rules())
}

func (s *Server) handleDVRRuleSave(w http.ResponseWriter, r *http.Request) {
	if !s.dvrWriter(w, r) {
		return
	}
	var in dvr.Rule
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if id := r.PathValue("id"); id != "" {
		in.ID = id
	}
	out, err := s.dvr.SaveRule(in)
	if err != nil {
		if errors.Is(err, dvr.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such pass")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("dvr pass saved", "id", out.ID, "title", out.Title, "channel", out.Channel, "newOnly", out.NewOnly, "season", out.Season, "commercials", out.Commercials)
	s.record(r, audit.Event{Kind: "dvr.pass", Detail: "saved pass " + out.Title, Fields: map[string]string{"ruleId": out.ID}})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDVRRuleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.dvrWriter(w, r) {
		return
	}
	rule, _ := s.dvr.Rule(r.PathValue("id"))
	if err := s.dvr.DeleteRule(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "no such pass")
		return
	}
	s.record(r, audit.Event{Kind: "dvr.pass_delete", Detail: "deleted pass " + rule.Title})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDVRRecordings(w http.ResponseWriter, r *http.Request) {
	if !s.dvrReady(w) {
		return
	}
	recs := s.dvr.Recordings(r.URL.Query().Get("status"))
	out := make([]RecordingView, 0, len(recs))
	for _, rec := range recs {
		out = append(out, s.recordingView(r, rec))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDVRRecording(w http.ResponseWriter, r *http.Request) {
	if !s.dvrReady(w) {
		return
	}
	rec, ok := s.dvr.Recording(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such recording")
		return
	}
	writeJSON(w, http.StatusOK, s.recordingView(r, rec))
}

type recordAiringIn struct {
	Channel string `json:"channel"`
	Airing  int64  `json:"airing"`
	dvr.Options
}

func (s *Server) handleDVRRecordAiring(w http.ResponseWriter, r *http.Request) {
	if !s.dvrWriter(w, r) {
		return
	}
	var in recordAiringIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	rec, err := s.dvr.RecordAiring(in.Channel, in.Airing, in.Options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.record(r, audit.Event{Kind: "dvr.record", Detail: "scheduled " + rec.Title + " on " + rec.Channel, Fields: map[string]string{"recordingId": rec.ID}})
	writeJSON(w, http.StatusOK, s.recordingView(r, rec))
}

type recordNowIn struct {
	Channel string `json:"channel"`
	Minutes int    `json:"minutes,omitempty"`
	dvr.Options
}

func (s *Server) handleDVRRecordNow(w http.ResponseWriter, r *http.Request) {
	if !s.dvrWriter(w, r) {
		return
	}
	var in recordNowIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	rec, err := s.dvr.RecordNow(in.Channel, in.Minutes, in.Options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.record(r, audit.Event{Kind: "dvr.record", Detail: "recording now " + rec.Title + " on " + rec.Channel, Fields: map[string]string{"recordingId": rec.ID}})
	s.dvr.RequestPlan()
	writeJSON(w, http.StatusOK, s.recordingView(r, rec))
}

// handleDVRRecordingEdit changes a scheduled or running recording:
// {padBefore?, padAfter?, commercials?} in minutes around the listed times.
func (s *Server) handleDVRRecordingEdit(w http.ResponseWriter, r *http.Request) {
	if !s.dvrWriter(w, r) {
		return
	}
	var in dvr.Options
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	rec, err := s.dvr.Edit(r.PathValue("id"), in)
	switch {
	case errors.Is(err, dvr.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such recording")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.record(r, audit.Event{Kind: "dvr.edit", Detail: "changed recording " + rec.Title, Fields: map[string]string{"recordingId": rec.ID}})
	writeJSON(w, http.StatusOK, s.recordingView(r, rec))
}

// handleDVRRedetect finds commercials again from the kept broadcast file:
// {commercials?} to also change how they are handled.
func (s *Server) handleDVRRedetect(w http.ResponseWriter, r *http.Request) {
	if !s.dvrWriter(w, r) {
		return
	}
	var in struct {
		Commercials dvr.Commercials `json:"commercials"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in)
	rec, err := s.dvr.Redetect(r.PathValue("id"), in.Commercials)
	switch {
	case errors.Is(err, dvr.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such recording")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.record(r, audit.Event{Kind: "dvr.redetect", Detail: "finding commercials again in " + rec.Title})
	writeJSON(w, http.StatusOK, s.recordingView(r, rec))
}

func (s *Server) handleDVRRecordingDelete(w http.ResponseWriter, r *http.Request) {
	if !s.dvrWriter(w, r) {
		return
	}
	rec, _ := s.dvr.Recording(r.PathValue("id"))
	if err := s.dvr.Delete(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "no such recording")
		return
	}
	s.record(r, audit.Event{Kind: "dvr.delete", Detail: "deleted recording " + rec.Title + " (" + string(rec.Status) + ")"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDVRPosition(w http.ResponseWriter, r *http.Request) {
	if !s.dvrReady(w) {
		return
	}
	var in struct {
		Position float64 `json:"position"`
		Watched  *bool   `json:"watched,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if in.Watched != nil {
		if err := s.dvr.MarkWatched(r.PathValue("id"), *in.Watched); err != nil {
			writeError(w, http.StatusNotFound, "no such recording")
			return
		}
		if *in.Watched {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if err := s.dvr.SetPosition(r.PathValue("id"), in.Position); err != nil {
		writeError(w, http.StatusNotFound, "no such recording")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDVRVideo serves the MP4 with range support, so every player can
// seek. Access is a session or a stream token for "rec:<id>".
func (s *Server) handleDVRVideo(w http.ResponseWriter, r *http.Request) {
	if !s.dvrReady(w) {
		return
	}
	rec, ok := s.dvr.Recording(r.PathValue("id"))
	if !ok || rec.Status != dvr.StatusReady {
		writeError(w, http.StatusNotFound, "no such recording")
		return
	}
	f, err := os.Open(s.dvr.Path(rec))
	if err != nil {
		writeError(w, http.StatusNotFound, "the file is missing")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, strings.TrimSuffix(rec.File, "/")+".mp4", st.ModTime(), f)
}

func (s *Server) handleGuideSearch(w http.ResponseWriter, r *http.Request) {
	if !s.dvrReady(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	writeJSON(w, http.StatusOK, s.dvr.Search(r.Context(), r.URL.Query().Get("q"), limit))
}
