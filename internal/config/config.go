// Package config loads runtime configuration from environment variables.
//
// Every setting has a sensible default so the server runs with no
// configuration at all on a LAN with an HDHomeRun. In the Unraid container
// the same variables are exposed as template fields.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// ListenAddr is the HTTP listen address, e.g. ":8089".
	ListenAddr string

	// HDHomeRunHosts is the list of HDHomeRun IPs or hostnames to use.
	// When empty and AutoDiscover is true, UDP broadcast discovery runs.
	HDHomeRunHosts []string
	// AutoDiscover enables UDP broadcast discovery on the local subnet.
	AutoDiscover bool
	// RefreshInterval controls how often device lineups are re-read.
	RefreshInterval time.Duration

	// DataDir holds persistent state (database, settings).
	DataDir string
	// BufferDir holds the live HLS ring buffers. Should be fast storage.
	BufferDir string
	// WebDir is the built frontend to serve. Empty disables static serving.
	WebDir string
	// APKDir holds app packages baked into the image, served at /downloads.
	APKDir string
	// RecordingsDir is where the DVR keeps its files.
	RecordingsDir string
	// ComskipPath is the comskip binary for commercial detection; empty
	// means look on PATH, "off" disables it.
	ComskipPath string
	// DVRPadBefore and DVRPadAfter are the default minutes around an airing.
	DVRPadBefore int
	DVRPadAfter  int
	// DVRLookahead is how far ahead passes are matched against the guide.
	DVRLookahead time.Duration
	// DVRMaxGB caps the recordings folder; 0 is unlimited.
	DVRMaxGB int
	// DVRKeepOriginalDays keeps broadcast files after processing so
	// commercials can be found again.
	DVRKeepOriginalDays int
	// Captions extracts closed captions from recordings to WebVTT.
	Captions bool
	// HDHREmulate serves an HDHomeRun-compatible tuner for Plex/Jellyfin.
	HDHREmulate bool
	// Notifications.
	NotifyWebhook string
	NotifyOnReady bool
	SMTPHost      string
	SMTPPort      int
	SMTPUser      string
	SMTPPass      string
	SMTPFrom      string
	NotifyEmail   []string

	// FFmpegPath is the ffmpeg binary. Empty means auto-detect.
	FFmpegPath string
	// Encoder selects the video encoder: auto, qsv, vaapi or software.
	Encoder string
	// RenderDevice is the DRM render node used for hardware encoding.
	RenderDevice string
	// Deinterlace applies a deinterlacer to the video (broadcast 1080i).
	Deinterlace bool
	// FFmpegLogLevel is ffmpeg's -loglevel for sessions.
	FFmpegLogLevel string

	// SegmentSeconds is the HLS segment duration.
	SegmentSeconds int
	// BufferMinutes is how much live history each channel session keeps.
	BufferMinutes int
	// IdleTimeout is how long a session lives after its last viewer request.
	IdleTimeout time.Duration
	// ReadyTimeout bounds how long a viewer waits for the first segments.
	ReadyTimeout time.Duration
	// ViewerTimeout is how long a viewer handle lives without a heartbeat.
	ViewerTimeout time.Duration
	// GuideRefresh is how often programme listings are re-fetched.
	GuideRefresh time.Duration
	// GuideURL overrides the guide API endpoint (tests, proxies).
	GuideURL string

	// Auth: set AuthIssuer to put the server behind an OpenID Connect
	// provider (Authentik). Empty leaves everything open on the LAN.
	AuthIssuer       string
	AuthClientID     string
	AuthClientSecret string
	AuthPublicURL    string
	AuthSessionKey   string
	AuthSessionTTL   time.Duration
	AuthStreamTTL    time.Duration
	// MCPToken is the bearer token /mcp accepts when auth is on.
	MCPToken string
	// AuthGroup gates sign-in; MCPGroup gates /mcp; AdminGroup gates the
	// audit log, viewer addresses and administrative actions.
	AuthGroup  string
	MCPGroup   string
	AdminGroup string
	// TrustedProxies lists networks whose X-Forwarded-For is believed.
	TrustedProxies []string
	// VideoBitrateK and AudioBitrateK are transcode target bitrates in kbit/s.
	VideoBitrateK int
	AudioBitrateK int

	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	dataDir := envString("DATA_DIR", "./data")
	cfg := Config{
		ListenAddr:          envString("LISTEN_ADDR", ":8089"),
		HDHomeRunHosts:      envList("HDHR_HOSTS"),
		RefreshInterval:     envDuration("REFRESH_INTERVAL", 15*time.Minute),
		DataDir:             dataDir,
		BufferDir:           envString("BUFFER_DIR", filepath.Join(dataDir, "buffer")),
		WebDir:              envString("WEB_DIR", "./web/dist"),
		APKDir:              envString("APK_DIR", ""),
		RecordingsDir:       envString("RECORDINGS_DIR", filepath.Join(dataDir, "recordings")),
		ComskipPath:         envString("COMSKIP_PATH", ""),
		DVRPadBefore:        envInt("DVR_PAD_BEFORE", 1),
		DVRPadAfter:         envInt("DVR_PAD_AFTER", 3),
		DVRLookahead:        envDuration("DVR_LOOKAHEAD", 24*time.Hour),
		DVRMaxGB:            envInt("DVR_MAX_GB", 0),
		DVRKeepOriginalDays: envInt("DVR_KEEP_ORIGINAL_DAYS", 7),
		Captions:            envBool("CAPTIONS", true),
		HDHREmulate:         envBool("HDHR_EMULATE", false),
		NotifyWebhook:       envString("NOTIFY_WEBHOOK", ""),
		NotifyOnReady:       envBool("NOTIFY_ON_READY", false),
		SMTPHost:            envString("SMTP_HOST", ""),
		SMTPPort:            envInt("SMTP_PORT", 587),
		SMTPUser:            envString("SMTP_USER", ""),
		SMTPPass:            envString("SMTP_PASS", ""),
		SMTPFrom:            envString("SMTP_FROM", ""),
		NotifyEmail:         envList("NOTIFY_EMAIL"),
		FFmpegPath:          envString("FFMPEG_PATH", ""),
		Encoder:             strings.ToLower(envString("ENCODER", "auto")),
		RenderDevice:        envString("RENDER_DEVICE", "/dev/dri/renderD128"),
		Deinterlace:         envBool("DEINTERLACE", true),
		FFmpegLogLevel:      strings.ToLower(envString("FFMPEG_LOGLEVEL", "info")),
		SegmentSeconds:      envInt("SEGMENT_SECONDS", 2),
		BufferMinutes:       envInt("BUFFER_MINUTES", 30),
		IdleTimeout:         envDuration("IDLE_TIMEOUT", 45*time.Second),
		ReadyTimeout:        envDuration("READY_TIMEOUT", 20*time.Second),
		ViewerTimeout:       envDuration("VIEWER_TIMEOUT", 20*time.Second),
		GuideRefresh:        envDuration("GUIDE_REFRESH", 30*time.Minute),
		GuideURL:            envString("GUIDE_URL", ""),
		AuthIssuer:          envString("AUTH_ISSUER", ""),
		AuthClientID:        envString("AUTH_CLIENT_ID", ""),
		AuthClientSecret:    envString("AUTH_CLIENT_SECRET", ""),
		AuthPublicURL:       envString("AUTH_PUBLIC_URL", ""),
		AuthSessionKey:      envString("AUTH_SESSION_SECRET", ""),
		AuthSessionTTL:      envDuration("AUTH_SESSION_TTL", 30*24*time.Hour),
		AuthStreamTTL:       envDuration("AUTH_STREAM_TTL", 12*time.Hour),
		MCPToken:            envString("MCP_TOKEN", ""),
		AuthGroup:           envString("AUTH_GROUP", ""),
		MCPGroup:            envString("MCP_GROUP", ""),
		AdminGroup:          envString("ADMIN_GROUP", ""),
		TrustedProxies:      envList("TRUSTED_PROXIES"),
		VideoBitrateK:       envInt("VIDEO_BITRATE_K", 6000),
		AudioBitrateK:       envInt("AUDIO_BITRATE_K", 192),
		LogLevel:            strings.ToLower(envString("LOG_LEVEL", "info")),
	}
	// Discovery defaults on only when no hosts are pinned.
	cfg.AutoDiscover = envBool("HDHR_DISCOVER", len(cfg.HDHomeRunHosts) == 0)

	switch cfg.Encoder {
	case "auto", "qsv", "vaapi", "software":
	default:
		return cfg, fmt.Errorf("ENCODER must be auto, qsv, vaapi or software (got %q)", cfg.Encoder)
	}
	if cfg.SegmentSeconds < 1 || cfg.SegmentSeconds > 10 {
		return cfg, fmt.Errorf("SEGMENT_SECONDS must be between 1 and 10 (got %d)", cfg.SegmentSeconds)
	}
	if cfg.BufferMinutes < 1 {
		return cfg, fmt.Errorf("BUFFER_MINUTES must be at least 1 (got %d)", cfg.BufferMinutes)
	}
	return cfg, nil
}

// WindowSegments is the number of HLS segments retained per session.
func (c Config) WindowSegments() int {
	return c.BufferMinutes * 60 / c.SegmentSeconds
}

func envString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envList(key string) []string {
	raw := envString(key, "")
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envInt(key string, def int) int {
	raw := envString(key, "")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	raw := strings.ToLower(envString(key, ""))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	raw := envString(key, "")
	if raw == "" {
		return def
	}
	// Accept bare seconds as well as Go durations.
	if n, err := strconv.Atoi(raw); err == nil {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return d
}
