package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Protect wraps the whole server. With auth disabled it is a no-op.
//
// What passes without a session:
//   - the SPA shell and its assets, so the login page can render;
//   - /auth/*;
//   - /api/health, for the container health check;
//   - /api/me, which reports the auth state itself;
//   - stream playlists and segments carrying a valid stream token (?t=);
//   - /mcp with an OAuth access token from the connector flow or the
//     configured static bearer token; /mcp/oauth/* and /.well-known/* are the
//     authorization server itself and stay open.
//
// Everything else under /api and /mcp gets 401 as JSON.
func (s *Service) Protect(next http.Handler) http.Handler {
	if s == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/auth/"), strings.HasPrefix(p, "/downloads/"), p == "/api/health", p == "/api/me":
			next.ServeHTTP(w, r)
			return
		case s.cfg.HDHREmulate && (p == "/discover.json" || p == "/lineup.json" || p == "/lineup_status.json"):
			next.ServeHTTP(w, r)
			return
		case strings.HasPrefix(p, "/auto/"):
			if s.cfg.HDHREmulate && s.CheckStreamToken(r.URL.Query().Get("t"), "hdhr") {
				next.ServeHTTP(w, r)
				return
			}
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "token required", "code": "unauthenticated"})
			return
		case p == mcpPath || p == mcpPath+"/":
			tok := bearer(r)
			if tok != "" && s.cfg.MCPToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.MCPToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := s.MCPBearer(tok); ok {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := s.UserFromRequest(r); ok {
				next.ServeHTTP(w, r)
				return
			}
			s.challenge(w, r)
			return
		case !strings.HasPrefix(p, "/api/"):
			// Static files and client-side routes.
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := s.UserFromRequest(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		if ch, ok := streamChannel(p); ok && s.CheckStreamToken(r.URL.Query().Get("t"), ch) {
			next.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in required", "code": "unauthenticated"})
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// streamChannel extracts the token subject from a media path: the channel
// of /api/channels/{n}/stream/... or "rec:{id}" of a recording's video.
func streamChannel(p string) (string, bool) {
	if rest, ok := strings.CutPrefix(p, "/api/dvr/recordings/"); ok {
		id, tail, ok := strings.Cut(rest, "/")
		if ok && id != "" && (tail == "video" || tail == "captions.vtt") {
			return "rec:" + id, true
		}
		return "", false
	}
	rest, ok := strings.CutPrefix(p, "/api/channels/")
	if !ok {
		return "", false
	}
	ch, tail, ok := strings.Cut(rest, "/stream/")
	if !ok || ch == "" || tail == "" {
		return "", false
	}
	return ch, true
}
