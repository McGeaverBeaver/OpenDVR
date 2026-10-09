// Package mcpserver exposes OpenDVR to AI assistants over the Model
// Context Protocol. Mounted at /mcp by the API server, it lets Claude
// Desktop (or any MCP client) read the lineup, tuner status, active
// sessions, failures and server log, start or stop channels, and run the
// DVR: search the guide, record programmes, manage passes and recordings.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/McGeaverBeaver/opendvr/internal/dvr"
	"github.com/McGeaverBeaver/opendvr/internal/guide"
	"github.com/McGeaverBeaver/opendvr/internal/hdhomerun"
	"github.com/McGeaverBeaver/opendvr/internal/logbuf"
	"github.com/McGeaverBeaver/opendvr/internal/netdiag"
	"github.com/McGeaverBeaver/opendvr/internal/stream"
	"github.com/McGeaverBeaver/opendvr/internal/tuner"
)

// Deps are the server components the tools read from and act on.
type Deps struct {
	Registry *tuner.Registry
	Streams  *stream.Manager
	Client   *hdhomerun.Client
	Caps     stream.Capabilities
	Guide    *guide.Service
	DVR      *dvr.Service
	Hosts    []string
	Logs     *logbuf.Buffer
	Version  string
}

// New builds the MCP server with all tools registered.
func New(d Deps) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "OpenDVR", Version: d.Version}, nil)
	toolCount = 0
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}

	type empty struct{}

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_status",
		Description: "Server overview: version, uptime, ffmpeg and encoder in use (Quick Sync, VAAPI or software), hardware self-test result, degraded flag, devices, active streams and recent failure count. Start here when something is wrong.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
		devices := d.Registry.Devices()
		return text(map[string]any{
			"version":        d.Version,
			"ffmpeg":         d.Caps,
			"degraded":       d.Streams.Degraded(),
			"hosts":          d.Hosts,
			"lastRefresh":    d.Registry.LastRefresh(),
			"deviceCount":    len(devices),
			"devices":        devices,
			"channelCount":   len(d.Registry.Channels()),
			"activeStreams":  d.Streams.Statuses(),
			"recentFailures": len(d.Streams.Failures()),
		})
	})

	type listChannelsIn struct {
		Query string `json:"query,omitempty" jsonschema:"Optional filter matched against the channel number or name, e.g. 57 or CITY"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_channels",
		Description: "List the merged channel lineup across all HDHomeRun tuners: guide number, name, HD, DRM and which devices carry it.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listChannelsIn) (*mcp.CallToolResult, any, error) {
		q := strings.ToLower(strings.TrimSpace(in.Query))
		var out []tuner.Channel
		for _, c := range d.Registry.Channels() {
			if q == "" || strings.Contains(c.GuideNumber, q) || strings.Contains(strings.ToLower(c.GuideName), q) {
				out = append(out, c)
			}
		}
		if out == nil {
			out = []tuner.Channel{}
		}
		return text(out)
	})

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_tuners",
		Description: "Live tuner status from every HDHomeRun: which tuner is locked to which channel, signal strength and quality, the client IP it is streaming to, and the bitrate.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
		type devStatus struct {
			Device hdhomerun.Device        `json:"device"`
			Host   string                  `json:"host"`
			Tuners []hdhomerun.TunerStatus `json:"tuners,omitempty"`
			Error  string                  `json:"error,omitempty"`
		}
		var out []devStatus
		for _, ds := range d.Registry.Devices() {
			st := devStatus{Device: ds.Device, Host: ds.Host}
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			tuners, err := d.Client.Status(cctx, &ds.Device)
			cancel()
			if err != nil {
				st.Error = err.Error()
			} else {
				st.Tuners = tuners
			}
			out = append(out, st)
		}
		if out == nil {
			out = []devStatus{}
		}
		return text(out)
	})

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_streams",
		Description: "Active transcoding sessions: one per tuned channel, shared by every viewer of that channel. Shows encoder, buffered segments, start time, last viewer activity and the viewers on it (name, address, paused).",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
		return text(d.Streams.Statuses())
	})

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_viewers",
		Description: "Everyone watching right now: viewer handle, display name, channel, client address, when they started, last heartbeat and whether they are paused. Viewers expire when their player stops sending heartbeats.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
		return text(d.Streams.Viewers())
	})

	type guideIn struct {
		Channel string `json:"channel,omitempty" jsonschema:"Guide number such as 57.1; omit for what is on every channel right now"`
		Hours   int    `json:"hours,omitempty" jsonschema:"With a channel: how many hours of listings to return (default 6, max 48)"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_guide",
		Description: "Programme listings. Without a channel: what is on now and next on every channel. With a channel: its listings for the next few hours (title, episode, synopsis, times as unix seconds).",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in guideIn) (*mcp.CallToolResult, any, error) {
		if d.Guide == nil {
			return nil, nil, fmt.Errorf("guide not available")
		}
		if in.Channel == "" {
			return text(map[string]any{"status": d.Guide.Status(), "channels": d.Guide.NowNext(time.Now().Unix())})
		}
		hours := in.Hours
		if hours <= 0 {
			hours = 6
		}
		if hours > 48 {
			hours = 48
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		to := time.Now().Add(time.Duration(hours) * time.Hour).Unix()
		d.Guide.EnsureWindow(cctx, to)
		c, ok := d.Guide.Channel(in.Channel)
		if !ok {
			return nil, nil, fmt.Errorf("no listings for channel %s", in.Channel)
		}
		var progs []guide.Program
		now := time.Now().Unix()
		for _, p := range c.Programs {
			if p.End > now && p.Start < to {
				progs = append(progs, p)
			}
		}
		c.Programs = progs
		return text(c)
	})

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_failures",
		Description: "The last ten stream failures with the error, the tail of ffmpeg's output and the exact ffmpeg command line. This is where a crash or stall explains itself.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
		return text(d.Streams.Failures())
	})

	type channelIn struct {
		Channel string `json:"channel" jsonschema:"Guide number such as 57.1"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "start_stream",
		Description: "Tune a channel and start (or join) its transcoding session, waiting until the first segment is ready. Returns the HLS playlist path and session status. Use it to test whether a channel plays without opening a browser.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in channelIn) (*mcp.CallToolResult, any, error) {
		if in.Channel == "" {
			return nil, nil, fmt.Errorf("channel is required")
		}
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		start := time.Now()
		s, err := d.Streams.Acquire(cctx, in.Channel)
		if err != nil {
			var busy *tuner.ErrTunersBusy
			if errors.As(err, &busy) {
				return text(map[string]any{
					"error":   "all tuners are busy",
					"channel": in.Channel,
					"tuners":  busy.Tuners,
					"note":    "tuners marked shared are channels this server is already streaming; viewers can join those without a tuner",
				})
			}
			return nil, nil, err
		}
		return text(map[string]any{
			"channel":  s.Channel,
			"encoder":  s.Encoder,
			"device":   s.Source.DeviceID,
			"readyIn":  time.Since(start).Round(time.Millisecond).String(),
			"playlist": "/api/channels/" + s.Channel + "/stream/index.m3u8",
			"started":  s.Started,
			"note":     "the session stops on its own when nobody fetches the playlist for a while; call stop_stream to free the tuner now",
		})
	})

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "stop_stream",
		Description: "Stop a channel's transcoding session and free its tuner. Any viewer still watching will lose the stream.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in channelIn) (*mcp.CallToolResult, any, error) {
		if in.Channel == "" {
			return nil, nil, fmt.Errorf("channel is required")
		}
		_, running := d.Streams.Session(in.Channel)
		d.Streams.Stop(in.Channel)
		return text(map[string]any{"channel": in.Channel, "wasRunning": running})
	})

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "rescan_tuners",
		Description: "Re-read every HDHomeRun's device info and channel lineup now instead of waiting for the periodic refresh.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := d.Registry.Refresh(cctx); err != nil {
			return nil, nil, err
		}
		return text(map[string]any{"devices": d.Registry.Devices(), "channels": len(d.Registry.Channels())})
	})

	type connIn struct {
		Host string `json:"host,omitempty" jsonschema:"Tuner IP or hostname to probe; defaults to the first configured HDHomeRun"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "test_connectivity",
		Description: "Probe a host from inside the container: ping, TCP 80 and 5004, HTTP discover.json and UDP discovery, plus the container's own addresses and default gateway and a diagnosis hint. Use when the tuner is not found.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in connIn) (*mcp.CallToolResult, any, error) {
		host := in.Host
		if host == "" && len(d.Hosts) > 0 {
			host = d.Hosts[0]
		}
		if host == "" {
			return nil, nil, fmt.Errorf("no host given and no HDHomeRun hosts configured")
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return text(netdiag.Run(cctx, host))
	})

	type logsIn struct {
		Lines int `json:"lines,omitempty" jsonschema:"How many of the most recent server log lines to return (default 200, max 1000)"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_logs",
		Description: "The server's recent log lines (HTTP requests, session starts and stops, ffmpeg warnings, device polls).",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in logsIn) (*mcp.CallToolResult, any, error) {
		n := in.Lines
		if n <= 0 {
			n = 200
		}
		if n > 1000 {
			n = 1000
		}
		if d.Logs == nil {
			return nil, nil, fmt.Errorf("log buffer not enabled")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(d.Logs.Lines(n), "\n")}}}, nil, nil
	})

	addDVRTools(srv, d)
	return srv
}

// ToolCount is how many tools the server registers, for the Status page.
func ToolCount() int { return toolCount }

var toolCount int

// Handler returns an http.Handler speaking MCP over Streamable HTTP.
//
// Stateless with JSON responses: every POST is self-contained, which is what
// bridges like mcp-remote and hosted connectors expect, and nothing needs
// to be kept per client.
func Handler(srv *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
}

func text(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}
