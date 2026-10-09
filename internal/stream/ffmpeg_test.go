package stream

import (
	"path/filepath"
	"strings"
	"testing"
)

func baseOpts(enc Encoder) TranscodeOptions {
	return TranscodeOptions{
		Input:          "http://10.0.0.5:5004/auto/v5.1",
		OutDir:         "/buffer/5.1",
		Encoder:        enc,
		RenderDevice:   "/dev/dri/renderD128",
		SegmentSeconds: 2,
		WindowSegments: 900,
		VideoBitrateK:  6000,
		AudioBitrateK:  192,
		Deinterlace:    true,
	}
}

func joined(args []string) string { return " " + strings.Join(args, " ") + " " }

// Decode must always be software: forcing a hardware decode of MPEG-2 on
// consumer Intel GPUs produced a fragile pipeline that died on the first
// glitchy broadcast frame.
func TestBuildArgsNeverUsesHardwareDecode(t *testing.T) {
	for _, enc := range []Encoder{EncoderQSV, EncoderVAAPI, EncoderSoftware} {
		s := joined(BuildArgs(baseOpts(enc)))
		if strings.Contains(s, "-hwaccel") {
			t.Errorf("%s: must not request hardware decode: %s", enc, s)
		}
	}
}

func TestBuildArgsResilience(t *testing.T) {
	s := joined(BuildArgs(baseOpts(EncoderQSV)))
	for _, want := range []string{
		" -reconnect 1 ",
		" -reconnect_streamed 1 ",
		" -loglevel info ",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestBuildArgsQSV(t *testing.T) {
	s := joined(BuildArgs(baseOpts(EncoderQSV)))
	for _, want := range []string{
		" -init_hw_device vaapi=va:/dev/dri/renderD128,driver=iHD -init_hw_device qsv=hw@va -filter_hw_device hw ",
		" -vf yadif=mode=send_frame:deint=interlaced,format=nv12,hwupload=extra_hw_frames=64 ",
		" -c:v h264_qsv ",
		" -b:v 6000k ",
		" -hls_list_size 900 ",
		" -force_key_frames expr:gte(t,n_forced*2) ",
		" -forced_idr 1 ",
		" -g 60 ",
		" " + filepath.Join("/buffer/5.1", PlaylistName) + " ",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
	if strings.Contains(s, "libx264") || strings.Contains(s, "deinterlace_qsv") {
		t.Errorf("qsv args must use software deinterlace and the qsv encoder only")
	}
	// Without deinterlace the upload filter must still be present so the
	// encoder gets GPU frames.
	o := baseOpts(EncoderQSV)
	o.Deinterlace = false
	s = joined(BuildArgs(o))
	if !strings.Contains(s, " -vf format=nv12,hwupload=extra_hw_frames=64 ") {
		t.Errorf("qsv without deinterlace must still upload: %s", s)
	}
}

func TestBuildArgsVAAPI(t *testing.T) {
	s := joined(BuildArgs(baseOpts(EncoderVAAPI)))
	if !strings.Contains(s, " -vf yadif=mode=send_frame:deint=interlaced,format=nv12,hwupload ") {
		t.Errorf("vaapi filter chain wrong: %s", s)
	}
	if !strings.Contains(s, " -c:v h264_vaapi ") {
		t.Errorf("vaapi encoder missing: %s", s)
	}
}

func TestBuildArgsSoftware(t *testing.T) {
	o := baseOpts(EncoderSoftware)
	s := joined(BuildArgs(o))
	if !strings.Contains(s, " -c:v libx264 ") || !strings.Contains(s, "yadif") {
		t.Errorf("software args wrong: %s", s)
	}
	if strings.Contains(s, "hwupload") || strings.Contains(s, "init_hw_device") {
		t.Errorf("software args must not touch the GPU: %s", s)
	}
	o.Deinterlace = false
	if strings.Contains(joined(BuildArgs(o)), "-vf") {
		t.Errorf("software without deinterlace needs no filter")
	}
}

func TestBuildArgsInputBeforeOutput(t *testing.T) {
	args := BuildArgs(baseOpts(EncoderVAAPI))
	in, out := -1, -1
	for i, a := range args {
		if a == "-i" {
			in = i
		}
		if strings.HasSuffix(a, PlaylistName) {
			out = i
		}
	}
	if in < 0 || out < 0 || in > out {
		t.Fatalf("input (%d) must precede output (%d)", in, out)
	}
	if out != len(args)-1 {
		t.Fatalf("playlist must be the last argument")
	}
}
