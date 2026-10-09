package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The TV app baked into the image is re-signed with this server's key
// before it is handed out (see internal/apksign), so every version a
// server serves installs over the one before. Packages someone drops into
// the config folder are served exactly as they are.

// signedCache holds re-signed built-in APKs, keyed by path and invalidated
// when the file changes.
type signedCache struct {
	mu    sync.Mutex
	items map[string]signedItem
}

type signedItem struct {
	mod  time.Time
	size int64
	data []byte
}

// builtIn reports whether a download path is in the image's APK folder.
func (s *Server) builtIn(path string) bool {
	return s.apkDir != "" && filepath.Clean(filepath.Dir(path)) == filepath.Clean(s.apkDir)
}

// signedAPK returns the re-signed bytes of a built-in APK, signing on first
// use. ok is false when there is no signer or signing failed, in which case
// the file is served unchanged.
func (s *Server) signedAPK(path string, st os.FileInfo) ([]byte, bool) {
	if s.apkSigner == nil || !s.builtIn(path) {
		return nil, false
	}
	s.signed.mu.Lock()
	defer s.signed.mu.Unlock()
	if it, ok := s.signed.items[path]; ok && it.mod.Equal(st.ModTime()) && it.size == st.Size() {
		return it.data, true
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	out, err := s.apkSigner.Sign(src)
	if err != nil {
		s.log.Warn("could not sign the TV app; serving it unchanged", "file", filepath.Base(path), "err", err)
		return nil, false
	}
	if s.signed.items == nil {
		s.signed.items = map[string]signedItem{}
	}
	s.signed.items[path] = signedItem{mod: st.ModTime(), size: st.Size(), data: out}
	s.log.Info("TV app signed with this server's key", "file", filepath.Base(path), "key", s.apkSigner.Fingerprint())
	return out, true
}

// handleDownloads lists the app packages on offer, built-in first.
func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Name     string    `json:"name"`
		URL      string    `json:"url"`
		Size     int64     `json:"size"`
		Modified time.Time `json:"modified"`
		// BuiltIn is true for the package that came with this server's
		// image; false for one added to the config folder.
		BuiltIn bool `json:"builtIn"`
		// SignedBy is the SHA-256 of the certificate the server signs the
		// built-in package with.
		SignedBy string `json:"signedBy,omitempty"`
		// VersionCode and VersionName come from the <name>.json file CI
		// writes next to the APK; the TV app compares them with its own.
		VersionCode int    `json:"versionCode,omitempty"`
		VersionName string `json:"versionName,omitempty"`
	}
	seen := map[string]bool{}
	out := []item{}
	for _, d := range s.downloadDirs() {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if e.IsDir() || !downloadName.MatchString(e.Name()) || seen[e.Name()] {
				continue
			}
			st, err := e.Info()
			if err != nil {
				continue
			}
			seen[e.Name()] = true
			path := filepath.Join(d, e.Name())
			it := item{Name: e.Name(), URL: "/downloads/" + e.Name(), Size: st.Size(), Modified: st.ModTime(), BuiltIn: s.builtIn(path)}
			if it.BuiltIn && s.apkSigner != nil {
				it.SignedBy = s.apkSigner.Fingerprint()
			}
			if b, err := os.ReadFile(strings.TrimSuffix(path, ".apk") + ".json"); err == nil {
				var v struct {
					VersionCode int    `json:"versionCode"`
					VersionName string `json:"versionName"`
				}
				if json.Unmarshal(b, &v) == nil {
					it.VersionCode, it.VersionName = v.VersionCode, v.VersionName
				}
			}
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].BuiltIn != out[j].BuiltIn {
			return out[i].BuiltIn
		}
		return out[i].Modified.After(out[j].Modified)
	})
	writeJSON(w, http.StatusOK, out)
}

// handleDownload serves an app package: the one baked into the image
// (re-signed with this server's key) or one dropped into the data
// directory, so a TV can install the app straight from the server (Fire
// TV's Downloader app or a browser). APKs are not secret, so this stays
// open even with auth on; the app itself still has to sign in.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	p := s.findDownload(name)
	if p == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	if st, err := os.Stat(p); err == nil {
		if data, ok := s.signedAPK(p, st); ok {
			http.ServeContent(w, r, name, st.ModTime(), bytes.NewReader(data))
			return
		}
	}
	http.ServeFile(w, r, p)
}
