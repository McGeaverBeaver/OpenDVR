package auth

// TV sign-in: a device flow hosted by this server.
//
// The TV asks for a code and shows it next to the /tv address and a QR of
// /tv?code=…. The viewer opens that on a phone, signs in the normal way
// (which is the only place the identity provider appears), and approves
// the TV on a page served here. The TV polls and is handed its session.
// Nothing about this is configured in the provider.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/McGeaverBeaver/opendvr/internal/audit"
)

type deviceLogin struct {
	UserCode string
	Created  time.Time
	Expires  time.Time
	user     *User // set on approval
	denied   bool
}

const (
	deviceCodeTTL      = 10 * time.Minute
	devicePollInterval = 3
	userCodeAlphabet   = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O, 1/I
)

func newUserCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	out := make([]byte, 0, 9)
	for i, x := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, userCodeAlphabet[int(x)%len(userCodeAlphabet)])
	}
	return string(out)
}

// normalizeUserCode accepts "abcd efgh", "ABCDEFGH" or "ABCD-EFGH".
func normalizeUserCode(s string) string {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(s)))
	if len(s) != 8 {
		return ""
	}
	return s[:4] + "-" + s[4:]
}

func (s *Service) sweepDevices(now time.Time) {
	s.devices.Range(func(k, v any) bool {
		if now.After(v.(*deviceLogin).Expires) {
			s.devices.Delete(k)
		}
		return true
	})
}

func (s *Service) deviceByCode(code string) (string, *deviceLogin) {
	var handle string
	var found *deviceLogin
	if code == "" {
		return "", nil
	}
	s.devices.Range(func(k, v any) bool {
		d := v.(*deviceLogin)
		if d.UserCode == code && time.Now().Before(d.Expires) {
			handle, found = k.(string), d
			return false
		}
		return true
	})
	return handle, found
}

type deviceStartResponse struct {
	Handle                  string `json:"handle"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
	QR                      string `json:"qr"` // data: URL of a PNG
}

func (s *Service) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	s.sweepDevices(now)
	handle := randomString(24)
	code := newUserCode()
	s.devices.Store(handle, &deviceLogin{UserCode: code, Created: now, Expires: now.Add(deviceCodeTTL)})
	short := s.publicURL(r) + "/tv"
	complete := short + "?code=" + url.QueryEscape(code)
	png, err := qrcode.Encode(complete, qrcode.Medium, 320)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "qr: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, deviceStartResponse{
		Handle: handle, UserCode: code, VerificationURI: short, VerificationURIComplete: complete,
		ExpiresIn: int(deviceCodeTTL.Seconds()), Interval: devicePollInterval,
		QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	})
}

func (s *Service) handleDevicePoll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Handle string `json:"handle"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in)
	v, ok := s.devices.Load(in.Handle)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"status": "error", "error": "unknown or expired sign-in; start again"})
		return
	}
	d := v.(*deviceLogin)
	switch {
	case time.Now().After(d.Expires):
		s.devices.Delete(in.Handle)
		writeJSON(w, http.StatusOK, map[string]string{"status": "error", "error": "the code expired; start again"})
	case d.denied:
		s.devices.Delete(in.Handle)
		writeJSON(w, http.StatusOK, map[string]string{"status": "error", "error": "the sign-in was declined on the phone"})
	case d.user == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
	default:
		s.devices.Delete(in.Handle)
		s.setSession(w, r, *d.user)
		s.log.Info("tv signed in", "user", d.user.Subject, "name", d.user.Name)
		s.record(r, audit.Event{Kind: "tv.signed_in", User: d.user.Subject, Name: displayName(*d.user), Detail: "TV signed in with code " + d.UserCode})
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "user": *d.user})
	}
}

// tvToken binds the approval form to the signed-in user for a few minutes.
type tvTokenClaims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
}

func (s *Service) tvToken(u User) string {
	p, _ := json.Marshal(tvTokenClaims{Sub: u.Subject, Exp: time.Now().Add(10 * time.Minute).Unix()})
	return s.sign(p)
}

func (s *Service) tvTokenOK(tok string, u User) bool {
	p, ok := s.open(tok)
	if !ok {
		return false
	}
	var cl tvTokenClaims
	return json.Unmarshal(p, &cl) == nil && cl.Sub == u.Subject && cl.Exp > time.Now().Unix()
}

func displayName(u User) string {
	switch {
	case u.Name != "":
		return u.Name
	case u.Email != "":
		return u.Email
	}
	return u.Subject
}

const codeInputStyle = "width:100%;box-sizing:border-box;font-size:28px;letter-spacing:.2em;text-align:center;padding:10px;border-radius:8px;border:1px solid #2c323f;background:#0f1115;color:#e6e9f0"

// handleTVForm is the page a phone opens to sign a TV in. It needs the
// normal session; without one it goes through sign-in and comes back.
func (s *Service) handleTVForm(w http.ResponseWriter, r *http.Request) {
	u, ok := s.UserFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	code := normalizeUserCode(r.URL.Query().Get("code"))
	s.consentPage(w, 200, "<h2>Sign in a TV</h2>"+
		"<div class='row'>Signed in as <b>"+html.EscapeString(displayName(u))+"</b></div>"+
		"<p class='muted'>Enter the code shown on the TV. The TV is then signed in as you, with your access, until it signs out.</p>"+
		"<form method='post' action='/tv'><input type='hidden' name='t' value='"+html.EscapeString(s.tvToken(u))+"'>"+
		"<input name='code' value='"+html.EscapeString(code)+"' placeholder='ABCD-EFGH' autocomplete='off' autocapitalize='characters' spellcheck='false' style='"+codeInputStyle+"'>"+
		"<div class='actions'><button type='submit' name='decision' value='deny'>Deny</button><button type='submit' name='decision' value='approve' class='primary'>Approve this TV</button></div></form>")
}

func (s *Service) handleTVSubmit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.UserFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/auth/login?next=/tv", http.StatusFound)
		return
	}
	_ = r.ParseForm()
	if !s.tvTokenOK(r.Form.Get("t"), u) {
		s.consentPage(w, 400, "<h2>This page expired</h2><p class='muted'>Open the address on the TV again.</p>")
		return
	}
	code := normalizeUserCode(r.Form.Get("code"))
	handle, d := s.deviceByCode(code)
	if d == nil {
		s.consentPage(w, 404, "<h2 class='err'>Code not recognised</h2><p class='muted'>Check the code on the TV; codes last ten minutes. <a href='/tv' style='color:#5eead4'>Try again</a></p>")
		return
	}
	if r.Form.Get("decision") != "approve" {
		d.denied = true
		s.log.Info("tv sign-in declined", "user", u.Subject, "handle", handle)
		s.record(r, audit.Event{Kind: "tv.denied", User: u.Subject, Name: displayName(u), Detail: "declined TV code " + code})
		s.consentPage(w, 200, "<h2>Declined</h2><p class='muted'>The TV was not signed in.</p>")
		return
	}
	uu := u
	d.user = &uu
	s.log.Info("tv sign-in approved", "user", u.Subject, "handle", handle)
	s.record(r, audit.Event{Kind: "tv.approved", User: u.Subject, Name: displayName(u), Detail: "approved TV code " + code})
	s.consentPage(w, 200, "<h2>TV signed in</h2><p class='muted'>The TV is now signed in as <b>"+html.EscapeString(displayName(u))+"</b>. You can put the phone down; it picks the session up within a few seconds.</p>")
}
