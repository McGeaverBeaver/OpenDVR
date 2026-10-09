package tuner

import (
	"testing"

	"github.com/McGeaverBeaver/opendvr/internal/hdhomerun"
)

func TestMergeLineupsOrdersAndMerges(t *testing.T) {
	lineups := map[string][]hdhomerun.Channel{
		"B": {
			{GuideNumber: "10.1", GuideName: "TEN", URL: "http://b:5004/auto/v10.1"},
			{GuideNumber: "5.1", GuideName: "FIVE", HD: 1, URL: "http://b:5004/auto/v5.1"},
		},
		"A": {
			{GuideNumber: "5.1", GuideName: "FIVE", HD: 1, URL: "http://a:5004/auto/v5.1"},
			{GuideNumber: "5.2", GuideName: "FIVE-2", URL: "http://a:5004/auto/v5.2"},
		},
	}
	got := mergeLineups(lineups)
	want := []string{"5.1", "5.2", "10.1"}
	if len(got) != len(want) {
		t.Fatalf("got %d channels, want %d", len(got), len(want))
	}
	for i, n := range want {
		if got[i].GuideNumber != n {
			t.Errorf("index %d = %s, want %s", i, got[i].GuideNumber, n)
		}
	}
	if len(got[0].Sources) != 2 || got[0].Sources[0].DeviceID != "A" {
		t.Errorf("5.1 sources = %+v", got[0].Sources)
	}
	if !got[0].HD {
		t.Errorf("5.1 should be HD")
	}
}
