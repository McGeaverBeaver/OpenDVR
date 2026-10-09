package stream

import (
	"errors"
	"testing"
	"time"
)

func TestLooksLikeNoSignal(t *testing.T) {
	busy := errors.New("ffmpeg exited before producing output: exit status 8 (HTTP error 503 Service Unavailable)")
	cases := []struct {
		name    string
		elapsed time.Duration
		err     error
		want    bool
	}{
		{"instant 503 is a busy tuner", 800 * time.Millisecond, busy, false},
		{"503 after the lock timeout is no signal", 10 * time.Second, busy, true},
		{"slow failure without 503 is something else", 10 * time.Second, errors.New("timed out waiting for stream to start"), false},
		{"nil error", 10 * time.Second, nil, false},
	}
	for _, c := range cases {
		if got := looksLikeNoSignal(c.elapsed, c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
