package hdhomerun

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	payload := buildDiscoverPayload()
	frame := encodeFrame(typeDiscoverReq, payload)

	typ, got, err := decodeFrame(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if typ != typeDiscoverReq {
		t.Fatalf("type = 0x%04x, want 0x%04x", typ, typeDiscoverReq)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch")
	}

	// Corrupt one byte: CRC must reject it.
	frame[5] ^= 0xFF
	if _, _, err := decodeFrame(frame); err == nil {
		t.Fatalf("expected crc failure")
	}
}

func TestParseDiscoverReply(t *testing.T) {
	var p []byte
	p = appendTag(p, tagDeviceType, binary.BigEndian.AppendUint32(nil, deviceTypeTuner))
	p = appendTag(p, tagDeviceID, binary.BigEndian.AppendUint32(nil, 0x1234ABCD))
	p = appendTag(p, tagTunerCount, []byte{2})
	p = appendTag(p, tagBaseURL, []byte("http://192.168.1.50:80"))
	p = appendTag(p, tagLineupURL, []byte("http://192.168.1.50:80/lineup.json"))
	frame := encodeFrame(typeDiscoverRpy, p)

	from := &net.UDPAddr{IP: net.ParseIP("192.168.1.50"), Port: discoverPort}
	dev, err := parseDiscoverReply(frame, from)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if dev.DeviceID != "1234ABCD" {
		t.Errorf("DeviceID = %q", dev.DeviceID)
	}
	if dev.TunerCount != 2 {
		t.Errorf("TunerCount = %d", dev.TunerCount)
	}
	if dev.BaseURL != "http://192.168.1.50:80" {
		t.Errorf("BaseURL = %q", dev.BaseURL)
	}
	if dev.Addr != "192.168.1.50" {
		t.Errorf("Addr = %q", dev.Addr)
	}
}

func TestLongTagLength(t *testing.T) {
	long := make([]byte, 300)
	for i := range long {
		long[i] = byte(i)
	}
	p := appendTag(nil, tagBaseURL, long)
	tags, err := parseTags(p)
	if err != nil {
		t.Fatalf("parseTags: %v", err)
	}
	if len(tags[tagBaseURL]) != 300 {
		t.Fatalf("len = %d, want 300", len(tags[tagBaseURL]))
	}
}

func TestRewriteHost(t *testing.T) {
	cases := map[string]string{
		"http://192.168.1.50:5004/auto/v5.1": "http://10.9.0.2:5004/auto/v5.1",
		"http://192.168.1.50/lineup.json":    "http://10.9.0.2/lineup.json",
	}
	for in, want := range cases {
		if got := rewriteHost(in, "10.9.0.2"); got != want {
			t.Errorf("rewriteHost(%q) = %q, want %q", in, got, want)
		}
	}
	if got := rewriteHost("http://x/y", ""); got != "http://x/y" {
		t.Errorf("empty host should be a no-op, got %q", got)
	}
}
