package tuner

import (
	"strings"
	"testing"

	"github.com/McGeaverBeaver/opendvr/internal/hdhomerun"
)

func twoDevices() (Channel, map[string]hdhomerun.Device) {
	ch := Channel{GuideNumber: "5.1", GuideName: "TEST", Sources: []Source{
		{DeviceID: "A", URL: "http://a/auto/v5.1"},
		{DeviceID: "B", URL: "http://b/auto/v5.1"},
	}}
	devs := map[string]hdhomerun.Device{
		"A": {DeviceID: "A", TunerCount: 2},
		"B": {DeviceID: "B", TunerCount: 2},
	}
	return ch, devs
}

func TestPickSourcePrefersDeviceWithMostFreeTuners(t *testing.T) {
	ch, devs := twoDevices()
	st := map[string][]hdhomerun.TunerStatus{
		"A": {{Resource: "tuner0", VctNumber: "9.1", TargetIP: "10.0.0.9"}, {Resource: "tuner1"}},
		"B": {{Resource: "tuner0"}, {Resource: "tuner1"}},
	}
	src, _, ok := pickSource(ch, devs, st, nil, nil)
	if !ok || src.DeviceID != "B" {
		t.Fatalf("got %+v ok=%v, want device B", src, ok)
	}
}

func TestPickSourceKnownBeatsUnknown(t *testing.T) {
	ch, devs := twoDevices()
	// Only B answered status.json and has a free tuner; A is unknown.
	st := map[string][]hdhomerun.TunerStatus{
		"B": {{Resource: "tuner0"}, {Resource: "tuner1", VctNumber: "7.1", TargetIP: "10.0.0.9"}},
	}
	src, _, ok := pickSource(ch, devs, st, nil, nil)
	if !ok || src.DeviceID != "B" {
		t.Fatalf("got %+v, want known device B", src)
	}
	// With B full, the unknown device is still tried.
	st["B"][0] = hdhomerun.TunerStatus{Resource: "tuner0", VctNumber: "2.1", TargetIP: "10.0.0.9"}
	src, _, ok = pickSource(ch, devs, st, nil, nil)
	if !ok || src.DeviceID != "A" {
		t.Fatalf("got %+v, want unknown device A", src)
	}
}

func TestPickSourceBusyListsTunersAndSharing(t *testing.T) {
	ch, devs := twoDevices()
	st := map[string][]hdhomerun.TunerStatus{
		"A": {
			{Resource: "tuner0", VctNumber: "9.1", TargetIP: "10.0.0.9"},
			{Resource: "tuner1", VctNumber: "57.1", TargetIP: "10.0.0.45"},
		},
		"B": {
			{Resource: "tuner0", VctNumber: "41.1", TargetIP: "10.0.0.7"},
			{Resource: "tuner1", VctNumber: "11.1", TargetIP: "10.0.0.8"},
		},
	}
	active := map[string]bool{"57.1": true}
	_, busy, ok := pickSource(ch, devs, st, active, map[string]bool{"10.0.0.45": true})
	if ok {
		t.Fatal("expected busy")
	}
	if len(busy) != 4 {
		t.Fatalf("busy = %+v, want 4 entries", busy)
	}
	var shared int
	for _, b := range busy {
		if b.Shared {
			shared++
			if b.Channel != "57.1" {
				t.Errorf("shared tuner is %s, want 57.1", b.Channel)
			}
		}
	}
	if shared != 1 {
		t.Errorf("shared = %d, want 1", shared)
	}
	err := &ErrTunersBusy{Channel: "5.1", Tuners: busy}
	if msg := err.Error(); !strings.Contains(msg, "57.1") || !strings.Contains(msg, "shared") {
		t.Errorf("error message %q should name tuned channels and sharing", msg)
	}
}

func TestPickSourceTreatsReleasingTunerAsFree(t *testing.T) {
	ch, devs := twoDevices()
	// Every tuner locked, but A/tuner1 is streaming to us for a channel we
	// no longer serve: the device has not released it yet.
	st := map[string][]hdhomerun.TunerStatus{
		"A": {
			{Resource: "tuner0", VctNumber: "9.1", TargetIP: "10.0.0.9"},
			{Resource: "tuner1", VctNumber: "57.1", TargetIP: "10.0.0.45"},
		},
		"B": {
			{Resource: "tuner0", VctNumber: "41.1", TargetIP: "10.0.0.7"},
			{Resource: "tuner1", VctNumber: "11.1", TargetIP: "10.0.0.8"},
		},
	}
	src, _, ok := pickSource(ch, devs, st, map[string]bool{}, map[string]bool{"10.0.0.45": true})
	if !ok || src.DeviceID != "A" {
		t.Fatalf("got %+v ok=%v, want device A", src, ok)
	}
}
