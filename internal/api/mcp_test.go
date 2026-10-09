package api

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/McGeaverBeaver/opendvr/internal/hdhomerun"
	"github.com/McGeaverBeaver/opendvr/internal/logbuf"
	"github.com/McGeaverBeaver/opendvr/internal/stream"
	"github.com/McGeaverBeaver/opendvr/internal/tuner"
)

// Drives the /mcp endpoint with the SDK's own client over HTTP, the same
// path Claude Desktop takes through mcp-remote.
func TestMCPEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ffmpeg, err := stream.FindFFmpeg("")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	logs := logbuf.New(100)
	log := slog.New(slog.NewTextHandler(logs.Tee(io.Discard), nil))
	hdhr := fakeHDHomeRun(t, ffmpeg)

	client := hdhomerun.NewClient(5 * time.Second)
	reg := tuner.NewRegistry(client, []string{hdhr.URL}, false, log)
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	streams, err := stream.NewManager(stream.Config{
		FFmpegPath: ffmpeg, BufferDir: t.TempDir(), Encoder: stream.EncoderSoftware,
		SegmentSeconds: 1, WindowSegments: 30, IdleTimeout: time.Minute, ReadyTimeout: 40 * time.Second,
		VideoBitrateK: 1200, AudioBitrateK: 96, Deinterlace: true,
	}, reg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(streams.StopAll)

	srv := httptest.NewServer(New(Deps{
		Registry: reg, Streams: streams, Client: client,
		Caps:  stream.Capabilities{FFmpegPath: ffmpeg, Encoder: stream.EncoderSoftware},
		Hosts: []string{hdhr.URL}, WebDir: t.TempDir(), Version: "test", Logs: logs, Log: log,
	}).Handler())
	t.Cleanup(srv.Close)

	ctx := context.Background()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"get_status", "list_channels", "get_tuners", "list_streams", "get_failures", "start_stream", "stop_stream", "rescan_tuners", "test_connectivity", "get_logs"} {
		if !names[want] {
			t.Errorf("tool %q missing; have %v", want, names)
		}
	}

	call := func(name string, args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s returned tool error: %+v", name, res.Content)
		}
		if len(res.Content) == 0 {
			t.Fatalf("%s: no content", name)
		}
		tc, ok := res.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("%s: content is %T", name, res.Content[0])
		}
		return tc.Text
	}

	if out := call("list_channels", map[string]any{"query": "5"}); !strings.Contains(out, `"guideNumber": "5.1"`) {
		t.Errorf("list_channels: %s", out)
	}
	if out := call("get_tuners", nil); !strings.Contains(out, "tuner0") {
		t.Errorf("get_tuners: %s", out)
	}
	if out := call("get_status", nil); !strings.Contains(out, `"channelCount": 2`) {
		t.Errorf("get_status: %s", out)
	}
	if out := call("start_stream", map[string]any{"channel": "5.1"}); !strings.Contains(out, "index.m3u8") {
		t.Errorf("start_stream: %s", out)
	}
	if out := call("list_streams", nil); !strings.Contains(out, `"channel": "5.1"`) {
		t.Errorf("list_streams: %s", out)
	}
	if out := call("stop_stream", map[string]any{"channel": "5.1"}); !strings.Contains(out, `"wasRunning": true`) {
		t.Errorf("stop_stream: %s", out)
	}
	if out := call("get_logs", map[string]any{"lines": 50}); !strings.Contains(out, "session") {
		t.Errorf("get_logs should include session lines: %s", out)
	}

	// A tool error (unknown channel) comes back as a tool result, not a protocol failure.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "start_stream", Arguments: map[string]any{"channel": "99.9"}})
	if err != nil || !res.IsError {
		t.Errorf("unknown channel should be a tool error: err=%v res=%+v", err, res)
	}
}
