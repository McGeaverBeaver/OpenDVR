// Package stream runs one transcoding session per tuned channel and serves
// its HLS ring buffer to any number of viewers.
package stream

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Encoder names a video encoding path.
type Encoder string

const (
	EncoderAuto     Encoder = "auto"
	EncoderQSV      Encoder = "qsv"
	EncoderVAAPI    Encoder = "vaapi"
	EncoderSoftware Encoder = "software"
)

// IsHardware reports whether the encoder needs a GPU.
func (e Encoder) IsHardware() bool {
	return e == EncoderQSV || e == EncoderVAAPI
}

// TranscodeOptions describe one ffmpeg invocation.
type TranscodeOptions struct {
	Input          string
	OutDir         string
	Encoder        Encoder // must be concrete, not auto
	RenderDevice   string
	SegmentSeconds int
	WindowSegments int
	VideoBitrateK  int
	AudioBitrateK  int
	Deinterlace    bool
	// LogLevel is ffmpeg's -loglevel; empty means "info".
	LogLevel string
}

// PlaylistName is the HLS playlist file inside a session directory.
const PlaylistName = "index.m3u8"

// BuildArgs returns the ffmpeg argument list for the given options.
//
// All three paths produce the same output: H.264 high profile video, stereo
// AAC audio, MPEG-TS HLS segments of SegmentSeconds with a sliding window of
// WindowSegments entries. Keyframes are forced on segment boundaries so every
// segment is independently decodable, which is what lets viewers seek inside
// the buffer without touching the tuner.
func BuildArgs(o TranscodeOptions) []string {
	seg := strconv.Itoa(o.SegmentSeconds)
	// Decode is always in software. Consumer Intel GPUs dropped MPEG-2
	// hardware decode years ago, so an ATSC 1.0 broadcast falls back to the
	// software decoder anyway; forcing a hardware decode path only creates a
	// fragile mismatch with the hardware deinterlacer that dies on the first
	// glitchy frame ("Invalid frame dimensions 0x0"). We therefore decode
	// and deinterlace in software, which is cheap and robust, and accelerate
	// only the encode, which is the expensive part.
	level := o.LogLevel
	if level == "" {
		level = "info"
	}
	args := []string{
		"-hide_banner", "-nostats", "-loglevel", level,
		"-fflags", "+genpts+discardcorrupt+nobuffer",
		"-analyzeduration", "1000000",
		"-probesize", "2000000",
		// The HDHomeRun serves over HTTP; reconnect if the connection drops
		// mid-stream rather than exiting the whole process. A clean EOF is
		// left alone: that is the tuner telling us it stopped, and the
		// session manager has its own retry policy for that.
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "2",
	}

	args = append(args, hwDeviceArgs(o.Encoder, o.RenderDevice)...)

	args = append(args, "-i", o.Input, "-map", "0:v:0", "-map", "0:a:0?")

	// Build the filter chain: optional software deinterlace, then upload to
	// the GPU for the hardware encoders.
	var filters []string
	if o.Deinterlace {
		// deint=interlaced leaves progressive frames alone, so a mux mixing
		// 720p and 1080i is handled without hurting the progressive feed.
		filters = append(filters, "yadif=mode=send_frame:deint=interlaced")
	}
	switch o.Encoder {
	case EncoderQSV:
		// The encoder keeps surfaces for its async queue and references.
		// A small upload pool is exhausted within the first second and
		// the encoder then blocks forever waiting for a free surface,
		// with no error. 64 is the pool Jellyfin ships with.
		filters = append(filters, "format=nv12", "hwupload=extra_hw_frames=64")
		args = append(args,
			"-vf", strings.Join(filters, ","),
			"-c:v", "h264_qsv",
			"-preset", "veryfast",
			"-profile:v", "high",
			"-look_ahead", "0",
			// Carry the broadcast's CEA-608 captions through as H.264 SEI
			// so hls.js and ExoPlayer can show them.
			"-a53cc", "1",
			// force_key_frames marks a frame as type I, but Quick Sync only
			// emits an IDR (and so a packet the HLS muxer will cut on) when
			// forced_idr is set; without it the muxer waits for the driver's
			// own IDR cadence, which was ~4 seconds and doubled every channel
			// start. The GOP cap is a backstop at 30 fps should the forced
			// keyframe ever be missed; the forced IDR sets the real boundary.
			"-forced_idr", "1",
			"-g", strconv.Itoa(o.SegmentSeconds*30),
		)
	case EncoderVAAPI:
		filters = append(filters, "format=nv12", "hwupload")
		args = append(args,
			"-vf", strings.Join(filters, ","),
			"-c:v", "h264_vaapi",
			"-profile:v", "high",
		)
	default:
		if len(filters) > 0 {
			args = append(args, "-vf", strings.Join(filters, ","))
		}
		args = append(args,
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-profile:v", "high",
			"-pix_fmt", "yuv420p",
			"-a53cc", "1",
		)
	}

	vb := strconv.Itoa(o.VideoBitrateK)
	args = append(args,
		// No B-frames. They need reorder buffering at both the encoder and
		// the player, which added a second or two to every channel start
		// (the encoder log showed GopRefDist 4, MaxDecFrameBuffering 3).
		// Live TV does not benefit from them, and dropping them makes each
		// segment a clean run of forward-only frames.
		"-bf", "0",
		"-b:v", vb+"k",
		"-maxrate", strconv.Itoa(o.VideoBitrateK*4/3)+"k",
		"-bufsize", strconv.Itoa(o.VideoBitrateK*2)+"k",
		"-force_key_frames", "expr:gte(t,n_forced*"+seg+")",
		"-c:a", "aac",
		"-b:a", strconv.Itoa(o.AudioBitrateK)+"k",
		"-ac", "2",
		"-f", "hls",
		"-hls_time", seg,
		"-hls_list_size", strconv.Itoa(o.WindowSegments),
		"-hls_flags", "delete_segments+independent_segments+program_date_time",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(o.OutDir, "seg_%06d.ts"),
		filepath.Join(o.OutDir, PlaylistName),
	)
	return args
}

// hwDeviceArgs initialises the GPU for the encoder only. For Quick Sync the
// device is derived from a VAAPI device bound to the iHD driver, which is
// the form Jellyfin uses on Linux and the one that behaves across driver
// generations. The filter device is what hwupload targets.
func hwDeviceArgs(enc Encoder, renderDevice string) []string {
	switch enc {
	case EncoderQSV:
		return []string{
			"-init_hw_device", "vaapi=va:" + renderDevice + ",driver=iHD",
			"-init_hw_device", "qsv=hw@va",
			"-filter_hw_device", "hw",
		}
	case EncoderVAAPI:
		return []string{
			"-init_hw_device", "vaapi=va:" + renderDevice + ",driver=iHD",
			"-filter_hw_device", "va",
		}
	}
	return nil
}

// FindFFmpeg locates an ffmpeg binary. Jellyfin's build is preferred
// because it ships Intel Quick Sync support out of the box.
func FindFFmpeg(configured string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("FFMPEG_PATH %q: %w", configured, err)
		}
		return configured, nil
	}
	for _, p := range []string{"/usr/lib/jellyfin-ffmpeg/ffmpeg", "/usr/local/bin/ffmpeg"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("ffmpeg not found; set FFMPEG_PATH")
	}
	return p, nil
}

// Capabilities describes what the local ffmpeg can do.
type Capabilities struct {
	FFmpegPath    string  `json:"ffmpegPath"`
	Version       string  `json:"version"`
	HasQSV        bool    `json:"hasQsv"`
	HasVAAPI      bool    `json:"hasVaapi"`
	RenderDevice  string  `json:"renderDevice"`
	RenderPresent bool    `json:"renderPresent"`
	Encoder       Encoder `json:"encoder"`
	// Requested is what configuration asked for; Encoder is what is in use.
	Requested Encoder `json:"requested"`
	// HardwareError holds the self-test output when hardware was abandoned.
	HardwareError string `json:"hardwareError,omitempty"`
	DriverPath    string `json:"driverPath,omitempty"`
}

// Probe inspects ffmpeg and the render device, then resolves the requested
// encoder ("auto" picks the best available).
func Probe(ctx context.Context, ffmpeg, renderDevice string, requested Encoder) (Capabilities, error) {
	caps := Capabilities{FFmpegPath: ffmpeg, RenderDevice: renderDevice, Requested: requested}

	out, err := runCapture(ctx, ffmpeg, "-version")
	if err != nil {
		return caps, fmt.Errorf("ffmpeg -version: %w", err)
	}
	if line, _, ok := strings.Cut(out, "\n"); ok {
		caps.Version = strings.TrimSpace(line)
	}

	enc, err := runCapture(ctx, ffmpeg, "-hide_banner", "-encoders")
	if err != nil {
		return caps, fmt.Errorf("ffmpeg -encoders: %w", err)
	}
	caps.HasQSV = strings.Contains(enc, "h264_qsv")
	caps.HasVAAPI = strings.Contains(enc, "h264_vaapi")
	if _, err := os.Stat(renderDevice); err == nil {
		caps.RenderPresent = true
	}

	switch requested {
	case EncoderQSV, EncoderVAAPI, EncoderSoftware:
		caps.Encoder = requested
	default:
		switch {
		case caps.RenderPresent && caps.HasQSV:
			caps.Encoder = EncoderQSV
		case caps.RenderPresent && caps.HasVAAPI:
			caps.Encoder = EncoderVAAPI
		default:
			caps.Encoder = EncoderSoftware
		}
	}
	return caps, nil
}

func runCapture(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		// ffmpeg -encoders exits 0; -version exits 0. Anything else is real.
		return buf.String(), err
	}
	return buf.String(), nil
}

// SelfTest encodes five seconds of synthetic 720p with the given encoder
// using the same upload and encoder settings as a real session. It takes
// about a second and proves that the GPU, driver and ffmpeg build agree,
// and that the frame pool is large enough not to deadlock the encoder,
// which is far cheaper than discovering either on the first channel someone
// tunes. Returns ffmpeg's error output on failure.
func SelfTest(ctx context.Context, ffmpeg string, enc Encoder, renderDevice string) error {
	if !enc.IsHardware() {
		return nil
	}
	args := []string{"-hide_banner", "-nostats", "-loglevel", "error"}
	args = append(args, hwDeviceArgs(enc, renderDevice)...)
	switch enc {
	case EncoderQSV:
		args = append(args,
			"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30",
			"-frames:v", "150",
			"-vf", "format=nv12,hwupload=extra_hw_frames=64",
			"-c:v", "h264_qsv", "-preset", "veryfast", "-look_ahead", "0",
		)
	case EncoderVAAPI:
		args = append(args,
			"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30",
			"-frames:v", "150",
			"-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi",
		)
	}
	args = append(args, "-f", "null", "-")

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var buf bytes.Buffer
	cmd.Stderr = &buf
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(buf.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s self-test failed: %s", enc, msg)
	}
	return nil
}

// ConfigureDriverPath points libva at the Intel driver bundled with
// jellyfin-ffmpeg when that build is in use and nothing else was configured.
// This mirrors what the official Jellyfin image does.
func ConfigureDriverPath(ffmpeg string) (string, bool) {
	if os.Getenv("LIBVA_DRIVERS_PATH") != "" {
		return os.Getenv("LIBVA_DRIVERS_PATH"), false
	}
	if !strings.Contains(ffmpeg, "jellyfin-ffmpeg") {
		return "", false
	}
	dir := filepath.Join(filepath.Dir(ffmpeg), "lib", "dri")
	if _, err := os.Stat(filepath.Join(dir, "iHD_drv_video.so")); err != nil {
		return "", false
	}
	_ = os.Setenv("LIBVA_DRIVERS_PATH", dir)
	return dir, true
}

// HWDeviceArgs exposes the GPU initialisation for other ffmpeg users (the
// DVR's transcode) so they match the live path exactly.
func HWDeviceArgs(enc Encoder, renderDevice string) []string {
	return hwDeviceArgs(enc, renderDevice)
}
