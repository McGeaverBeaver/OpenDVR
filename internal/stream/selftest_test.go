package stream

import (
	"context"
	"strings"
	"testing"
)

func TestSelfTestSoftwareIsNoop(t *testing.T) {
	if err := SelfTest(context.Background(), "/nonexistent/ffmpeg", EncoderSoftware, "/dev/dri/renderD128"); err != nil {
		t.Fatalf("software self-test should not run ffmpeg: %v", err)
	}
}

func TestSelfTestReportsHardwareFailure(t *testing.T) {
	ffmpeg, err := FindFFmpeg("")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	// A bogus render node must produce a readable error, not a hang.
	err = SelfTest(context.Background(), ffmpeg, EncoderVAAPI, "/dev/dri/does-not-exist")
	if err == nil {
		t.Fatalf("expected failure with a bogus render device")
	}
	if !strings.Contains(err.Error(), "self-test failed") {
		t.Fatalf("unexpected error text: %v", err)
	}
	t.Logf("error surfaced as: %v", err)
}

func TestConfigureDriverPathIgnoresOtherBuilds(t *testing.T) {
	t.Setenv("LIBVA_DRIVERS_PATH", "")
	if _, set := ConfigureDriverPath("/usr/bin/ffmpeg"); set {
		t.Fatalf("must not set a driver path for a non-jellyfin ffmpeg")
	}
}
