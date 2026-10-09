// Command server runs the opendvr backend: HDHomeRun discovery,
// channel lineup, the live stream broker and the web UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/api"
	"github.com/McGeaverBeaver/opendvr/internal/apksign"
	"github.com/McGeaverBeaver/opendvr/internal/audit"
	"github.com/McGeaverBeaver/opendvr/internal/auth"
	"github.com/McGeaverBeaver/opendvr/internal/config"
	"github.com/McGeaverBeaver/opendvr/internal/dvr"
	"github.com/McGeaverBeaver/opendvr/internal/geoip"
	"github.com/McGeaverBeaver/opendvr/internal/guide"
	"github.com/McGeaverBeaver/opendvr/internal/hdhomerun"
	"github.com/McGeaverBeaver/opendvr/internal/logbuf"
	"github.com/McGeaverBeaver/opendvr/internal/netdiag"
	"github.com/McGeaverBeaver/opendvr/internal/notify"
	"github.com/McGeaverBeaver/opendvr/internal/stream"
	"github.com/McGeaverBeaver/opendvr/internal/tuner"
	"github.com/McGeaverBeaver/opendvr/internal/users"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-healthcheck":
			os.Exit(healthcheck(cfg.ListenAddr))
		case "-encode-test":
			// Run the exact session pipeline against a channel for a few
			// seconds with verbose ffmpeg output:
			//   docker exec opendvr /app/opendvr -encode-test 57.1
			os.Exit(encodeTest(cfg, os.Args[2:]))
		case "-check":
			// Connectivity test from inside the container:
			//   docker exec opendvr /app/opendvr -check 192.168.1.50
			hosts := os.Args[2:]
			if len(hosts) == 0 {
				hosts = cfg.HDHomeRunHosts
			}
			if len(hosts) == 0 {
				fmt.Fprintln(os.Stderr, "usage: opendvr -check <host> (or set HDHR_HOSTS)")
				os.Exit(2)
			}
			exit := 0
			for _, h := range hosts {
				r := netdiag.Run(context.Background(), h)
				netdiag.Print(os.Stdout, r)
				fmt.Fprintln(os.Stdout)
				for _, c := range r.Checks {
					if c.Name == "http discover.json" && !c.OK {
						exit = 1
					}
				}
			}
			os.Exit(exit)
		}
	}
	logs := logbuf.New(1000)
	log := newLoggerTo(cfg.LogLevel, logs.Tee(os.Stdout))
	log.Info("opendvr starting", "version", version, "listen", cfg.ListenAddr)
	logNetwork(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Error("data dir", "err", err)
		os.Exit(1)
	}

	ffmpegPath, err := stream.FindFFmpeg(cfg.FFmpegPath)
	if err != nil {
		log.Error("ffmpeg", "err", err)
		os.Exit(1)
	}
	if dir, set := stream.ConfigureDriverPath(ffmpegPath); set {
		log.Info("using bundled Intel media driver", "LIBVA_DRIVERS_PATH", dir)
	}
	caps, err := stream.Probe(ctx, ffmpegPath, cfg.RenderDevice, stream.Encoder(cfg.Encoder))
	if err != nil {
		log.Error("ffmpeg probe", "err", err)
		os.Exit(1)
	}
	caps.DriverPath = os.Getenv("LIBVA_DRIVERS_PATH")
	log.Info("ffmpeg ready", "path", caps.FFmpegPath, "version", caps.Version, "encoder", caps.Encoder, "qsv", caps.HasQSV, "vaapi", caps.HasVAAPI, "render", caps.RenderPresent)
	if caps.Encoder.IsHardware() {
		if !caps.RenderPresent {
			log.Warn("hardware encoder requested but render device is missing; using software", "device", cfg.RenderDevice)
			caps.HardwareError = "render device " + cfg.RenderDevice + " not found; pass --device=/dev/dri:/dev/dri to the container"
			caps.Encoder = stream.EncoderSoftware
		} else if err := stream.SelfTest(ctx, ffmpegPath, caps.Encoder, cfg.RenderDevice); err != nil {
			log.Warn("hardware encoder self-test failed; using software", "encoder", caps.Encoder, "err", err)
			caps.HardwareError = err.Error()
			caps.Encoder = stream.EncoderSoftware
		} else {
			log.Info("hardware encoder self-test passed", "encoder", caps.Encoder)
		}
	}

	client := hdhomerun.NewClient(8 * time.Second)
	reg := tuner.NewRegistry(client, cfg.HDHomeRunHosts, cfg.AutoDiscover, log)
	if err := reg.Refresh(ctx); err != nil {
		log.Warn("initial device scan failed; will retry", "err", err, "hosts", cfg.HDHomeRunHosts, "discover", cfg.AutoDiscover)
		log.Warn("to troubleshoot: open Status in the web UI, or run: docker exec <container> /app/opendvr -check " + strings.Join(cfg.HDHomeRunHosts, " "))
	} else {
		log.Info("lineup loaded", "devices", len(reg.Devices()), "channels", len(reg.Channels()))
	}
	go reg.Run(ctx, cfg.RefreshInterval)
	reg.SetSelfAddrs(selfAddrs())

	epg := guide.New(cfg.GuideURL, func() []string {
		var auths []string
		for _, d := range reg.Devices() {
			if d.Device.DeviceAuth != "" {
				auths = append(auths, d.Device.DeviceAuth)
			}
		}
		return auths
	}, cfg.DataDir, log)
	epg.UserAgent = "opendvr/" + version

	geo := geoip.New(cfg.DataDir, log)
	if !geo.Available() {
		log.Info("no GeoIP database; drop GeoLite2-City.mmdb into the data directory to see countries on the Admin page")
	}
	if len(cfg.TrustedProxies) > 0 {
		audit.SetTrustedProxies(cfg.TrustedProxies)
	}
	auditLog := audit.New(cfg.DataDir, geo)

	authn, err := auth.New(auth.Config{
		Issuer: cfg.AuthIssuer, ClientID: cfg.AuthClientID, ClientSecret: cfg.AuthClientSecret,
		PublicURL: cfg.AuthPublicURL, SessionSecret: cfg.AuthSessionKey, SessionTTL: cfg.AuthSessionTTL,
		StreamTokenTTL: cfg.AuthStreamTTL, MCPToken: cfg.MCPToken, AllowedGroup: cfg.AuthGroup, MCPGroup: cfg.MCPGroup, AdminGroup: cfg.AdminGroup, DataDir: cfg.DataDir,
		Audit: auditLog, HDHREmulate: cfg.HDHREmulate,
	}, log)
	if err != nil {
		log.Error("auth configuration", "err", err)
		os.Exit(2)
	}
	if authn.Enabled() {
		log.Info("authentication enabled", "issuer", cfg.AuthIssuer, "mcpToken", cfg.MCPToken != "")
		if cfg.MCPToken == "" {
			log.Warn("MCP_TOKEN is not set; /mcp will only accept a browser session cookie")
		}
	} else {
		log.Warn("authentication is off; keep this server on the LAN (set AUTH_ISSUER to enable)")
	}
	go func() {
		if err := epg.Refresh(ctx); err != nil {
			log.Warn("initial guide fetch failed; will retry", "err", err)
		} else {
			st := epg.Status()
			log.Info("guide loaded", "channels", st.Channels, "programs", st.Programs, "until", st.Until.Format(time.RFC3339))
		}
		epg.Run(ctx, cfg.GuideRefresh)
	}()

	streams, err := stream.NewManager(stream.Config{
		FFmpegPath:     caps.FFmpegPath,
		BufferDir:      cfg.BufferDir,
		Encoder:        caps.Encoder,
		RenderDevice:   cfg.RenderDevice,
		SegmentSeconds: cfg.SegmentSeconds,
		WindowSegments: cfg.WindowSegments(),
		IdleTimeout:    cfg.IdleTimeout,
		ReadyTimeout:   cfg.ReadyTimeout,
		ViewerTimeout:  cfg.ViewerTimeout,
		VideoBitrateK:  cfg.VideoBitrateK,
		AudioBitrateK:  cfg.AudioBitrateK,
		Deinterlace:    cfg.Deinterlace,
		FFmpegLogLevel: cfg.FFmpegLogLevel,
	}, reg, log)
	if err != nil {
		log.Error("stream manager", "err", err)
		os.Exit(1)
	}
	go streams.Reap(ctx)

	comskip := cfg.ComskipPath
	if strings.EqualFold(comskip, "off") {
		comskip = "-"
	}
	notifier := notify.New(notify.Config{
		WebhookURL: cfg.NotifyWebhook, SMTPHost: cfg.SMTPHost, SMTPPort: cfg.SMTPPort, SMTPUser: cfg.SMTPUser,
		SMTPPass: cfg.SMTPPass, From: cfg.SMTPFrom, To: cfg.NotifyEmail, OnReady: cfg.NotifyOnReady,
	}, log)
	if notifier.Enabled() {
		log.Info("notifications on", "transports", notifier.Transports(), "onReady", notifier.OnReady())
	}
	accounts := users.New(cfg.DataDir)

	// The TV app is re-signed with a key that stays in the data directory,
	// so each new image's app installs over the one before.
	apkSigner, newKey, err := apksign.LoadOrCreate(filepath.Join(cfg.DataDir, "apk-signing.pem"))
	if err != nil {
		log.Warn("TV app signing key unavailable; the app is served as built", "err", err)
		apkSigner = nil
	} else {
		log.Info("TV app signing key ready", "created", newKey, "sha256", apkSigner.Fingerprint())
	}

	recorder, err := dvr.New(dvr.Config{
		DataDir: cfg.DataDir, RecordingsDir: cfg.RecordingsDir, FFmpegPath: caps.FFmpegPath, ComskipPath: comskip,
		Encoder: caps.Encoder, RenderDevice: cfg.RenderDevice, VideoBitrateK: cfg.VideoBitrateK, AudioBitrateK: cfg.AudioBitrateK,
		Deinterlace: cfg.Deinterlace, PadBefore: cfg.DVRPadBefore, PadAfter: cfg.DVRPadAfter, Lookahead: cfg.DVRLookahead,
		MaxBytes: int64(cfg.DVRMaxGB) << 30, Captions: cfg.Captions, KeepOriginalDays: cfg.DVRKeepOriginalDays,
		Notify: func(kind, title, body string) {
			if kind == "ready" && !notifier.OnReady() {
				return
			}
			notifier.Send(kind+":"+title, title, body)
		},
	}, reg, epg, streams, log)
	if err != nil {
		log.Error("dvr", "err", err)
		os.Exit(1)
	}
	log.Info("dvr ready", "recordings", recorder.RecordingsDir(), "comskip", recorder.ComskipAvailable(), "captions", cfg.Captions, "maxGB", cfg.DVRMaxGB)
	// Live viewers must see the tuners recordings hold as taken.
	streams.ExternalActive = recorder.ActiveChannels
	go recorder.Run(ctx)
	if cfg.HDHREmulate {
		log.Info("HDHomeRun emulation on: add this server to Plex or Jellyfin by its address")
	}

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: api.New(api.Deps{
			Registry: reg, Streams: streams, Client: client, Caps: caps,
			Guide:       epg,
			DVR:         recorder,
			Auth:        authn,
			Audit:       auditLog,
			Geo:         geo,
			Users:       accounts,
			APKSigner:   apkSigner,
			HDHREmulate: cfg.HDHREmulate,
			Notify:      notifier.Transports(),
			DataDir:     cfg.DataDir,
			APKDir:      cfg.APKDir,
			Hosts:       cfg.HDHomeRunHosts, WebDir: cfg.WebDir, Version: version, Logs: logs, Log: log,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	streams.StopAll()
}

// encodeTest reproduces one session's ffmpeg run in the foreground so a
// stall or crash can be read in full. It uses the same configuration,
// encoder selection and self-test as the server.
func encodeTest(cfg config.Config, argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: opendvr -encode-test <channel|stream-url> [seconds]")
		return 2
	}
	target := argv[0]
	seconds := "8"
	if len(argv) > 1 {
		seconds = argv[1]
	}
	log := newLogger("info")
	ctx := context.Background()

	ffmpegPath, err := stream.FindFFmpeg(cfg.FFmpegPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	stream.ConfigureDriverPath(ffmpegPath)
	caps, err := stream.Probe(ctx, ffmpegPath, cfg.RenderDevice, stream.Encoder(cfg.Encoder))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("ffmpeg: %s\nencoder: %s (requested %s)\n", caps.Version, caps.Encoder, caps.Requested)
	if caps.Encoder.IsHardware() {
		if err := stream.SelfTest(ctx, ffmpegPath, caps.Encoder, cfg.RenderDevice); err != nil {
			fmt.Printf("self-test: FAILED: %v\n", err)
		} else {
			fmt.Println("self-test: ok")
		}
	}

	src := tuner.Source{URL: target}
	if !strings.Contains(target, "://") {
		client := hdhomerun.NewClient(8 * time.Second)
		reg := tuner.NewRegistry(client, cfg.HDHomeRunHosts, cfg.AutoDiscover, log)
		if err := reg.Refresh(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "lineup:", err)
			return 1
		}
		src, err = reg.ResolveSource(ctx, target, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	fmt.Printf("source: %s\n\n", src.URL)

	outDir, err := os.MkdirTemp("", "opendvr-encode-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(outDir)

	m, err := stream.NewManager(stream.Config{
		FFmpegPath: ffmpegPath, BufferDir: outDir, Encoder: caps.Encoder, RenderDevice: cfg.RenderDevice,
		SegmentSeconds: cfg.SegmentSeconds, WindowSegments: cfg.WindowSegments(),
		VideoBitrateK: cfg.VideoBitrateK, AudioBitrateK: cfg.AudioBitrateK, Deinterlace: cfg.Deinterlace,
	}, nil, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	args := m.Args(src, outDir, "verbose")
	// Bound the run: insert -t before the output path.
	args = append(args[:len(args)-1], "-t", seconds, args[len(args)-1])
	fmt.Println("command:", ffmpegPath, strings.Join(args, " "))
	fmt.Println()

	cmd := exec.Command(ffmpegPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	start := time.Now()
	runErr := cmd.Run()
	segs := 0
	entries, _ := os.ReadDir(outDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ts") {
			segs++
		}
	}
	fmt.Printf("\nfinished in %s: exit=%v, segments written=%d\n", time.Since(start).Round(time.Millisecond), runErr, segs)
	if runErr != nil || segs == 0 {
		return 1
	}
	return 0
}

// selfAddrs lists this host's IP addresses, which the tuner registry uses
// to recognise its own streams in a device's tuner status.
func selfAddrs() []string {
	var out []string
	for _, i := range netdiag.Run(context.Background(), "").Interfaces {
		out = append(out, i.Addrs...)
	}
	return out
}

// logNetwork prints the container's addresses and default route so a
// misconfigured Docker network is visible in the first lines of the log.
func logNetwork(log *slog.Logger) {
	r := netdiag.Run(context.Background(), "")
	var addrs []string
	for _, i := range r.Interfaces {
		addrs = append(addrs, i.Name+"="+strings.Join(i.Addrs, ","))
	}
	gw := r.DefaultGateway
	if gw == "" {
		gw = "none (container cannot reach other networks)"
	}
	log.Info("network", "interfaces", strings.Join(addrs, " "), "gateway", gw)
}

// healthcheck probes the running server; used by the Docker HEALTHCHECK.
func healthcheck(listen string) int {
	addr := listen
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/health")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func newLogger(level string) *slog.Logger { return newLoggerTo(level, os.Stdout) }

func newLoggerTo(level string, w io.Writer) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl}))
}
