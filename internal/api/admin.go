package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/audit"
	"github.com/McGeaverBeaver/opendvr/internal/users"
)

// backupFiles are the state worth carrying to another server. The
// session key is included so sign-ins and connector tokens survive a
// move; keep the archive private.
var backupFiles = []string{"dvr.json", "users.json", "mcp_oauth.json", "comskip.ini", "session.key", "audit.jsonl", "apk-signing.pem"}

func (s *Server) mountAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me/prefs", s.handlePrefs)
	mux.HandleFunc("PUT /api/me/prefs", s.handlePrefsSave)
	mux.HandleFunc("GET /api/users", s.handleUsers)
	mux.HandleFunc("PUT /api/users/{sub}", s.handleUserSave)
	mux.HandleFunc("GET /api/admin/backup", s.handleBackup)
	mux.HandleFunc("POST /api/admin/restore", s.handleRestore)
}

// profileOf is the caller's stored profile (empty with auth off).
func (s *Server) profileOf(r *http.Request) users.Profile {
	sub, _ := s.userOf(r)
	if sub == "" || s.users == nil {
		return users.Profile{Favorites: []string{}}
	}
	p, _ := s.users.Get(sub)
	return p
}

// restricted reports whether the caller is a kids profile.
func (s *Server) restricted(r *http.Request) bool {
	return s.profileOf(r).Restricted
}

// mayWatch reports whether the caller may use a channel.
func (s *Server) mayWatch(r *http.Request, channel string) bool {
	sub, _ := s.userOf(r)
	return s.users == nil || s.users.MayWatch(sub, channel)
}

func (s *Server) handlePrefs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.profileOf(r))
}

func (s *Server) handlePrefsSave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Favorites []string `json:"favorites"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	sub, _ := s.userOf(r)
	if sub == "" || s.users == nil {
		writeError(w, http.StatusBadRequest, "favourites are stored per account; sign in first")
		return
	}
	if len(in.Favorites) > 500 {
		in.Favorites = in.Favorites[:500]
	}
	s.users.SetFavorites(sub, in.Favorites)
	writeJSON(w, http.StatusOK, s.profileOf(r))
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.users == nil {
		writeJSON(w, http.StatusOK, []users.Profile{})
		return
	}
	writeJSON(w, http.StatusOK, s.users.All())
}

func (s *Server) handleUserSave(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in struct {
		Restricted bool     `json:"restricted"`
		Allowed    []string `json:"allowed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if s.users == nil || !s.users.SetRestriction(r.PathValue("sub"), in.Restricted, in.Allowed) {
		writeError(w, http.StatusNotFound, "no such user")
		return
	}
	mode := "unrestricted"
	if in.Restricted {
		mode = "kids mode with " + string(rune('0'+min(len(in.Allowed), 9))) + " channels"
		if len(in.Allowed) > 9 {
			mode = "kids mode"
		}
	}
	s.record(r, audit.Event{Kind: "admin.user", Detail: "set " + r.PathValue("sub") + " to " + mode})
	p, _ := s.users.Get(r.PathValue("sub"))
	writeJSON(w, http.StatusOK, p)
}

// handleBackup streams a zip of the server's state files.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	n := 0
	for _, name := range backupFiles {
		b, err := os.ReadFile(filepath.Join(s.dataDir, name))
		if err != nil {
			continue
		}
		f, err := zw.Create(name)
		if err != nil {
			continue
		}
		_, _ = f.Write(b)
		n++
	}
	_ = zw.Close()
	s.record(r, audit.Event{Kind: "admin.backup", Detail: "downloaded a backup"})
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="opendvr-backup-`+time.Now().Format("2006-01-02")+`.zip"`)
	_, _ = w.Write(buf.Bytes())
	_ = n
}

// handleRestore writes the files from an uploaded backup zip into the
// data directory and reloads what can be reloaded live. Connector
// tokens and the session key take effect on the next start.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "upload too large or unreadable")
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		writeError(w, http.StatusBadRequest, "not a zip file")
		return
	}
	allowed := map[string]bool{}
	for _, n := range backupFiles {
		allowed[n] = true
	}
	var restored []string
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if !allowed[name] || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(rc, 32<<20))
		rc.Close()
		if err != nil {
			continue
		}
		if strings.HasSuffix(name, ".json") && !json.Valid(b) {
			continue
		}
		mode := os.FileMode(0o644)
		if name == "session.key" || name == "mcp_oauth.json" || name == "audit.jsonl" || name == "users.json" || name == "apk-signing.pem" {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(s.dataDir, name), b, mode); err != nil {
			continue
		}
		restored = append(restored, name)
	}
	if s.dvr != nil {
		s.dvr.Reload()
		s.dvr.RequestPlan()
	}
	if s.users != nil {
		s.users.Reload()
	}
	if s.audit != nil {
		s.audit.Record(audit.Event{Kind: "admin.restore", Detail: "restored " + strings.Join(restored, ", "), IP: audit.ClientIP(r)})
	}
	if restored == nil {
		restored = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"restored": restored,
		"note":     "recordings, passes and profiles are live; restart the container for the session key, connector tokens and the TV app signing key",
	})
}
