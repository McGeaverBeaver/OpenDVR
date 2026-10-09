// Package tuner keeps track of known HDHomeRun devices and the merged
// channel lineup across them.
package tuner

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/hdhomerun"
)

// Source is one way to obtain a channel: a specific device and stream URL.
type Source struct {
	DeviceID string `json:"deviceId"`
	URL      string `json:"url"`
}

// Channel is a channel merged across all devices that carry it.
type Channel struct {
	GuideNumber string   `json:"guideNumber"`
	GuideName   string   `json:"guideName"`
	HD          bool     `json:"hd"`
	DRM         bool     `json:"drm"`
	VideoCodec  string   `json:"videoCodec,omitempty"`
	AudioCodec  string   `json:"audioCodec,omitempty"`
	Favorite    bool     `json:"favorite"`
	Sources     []Source `json:"sources"`
}

// DeviceState is a device plus the last outcome of polling it.
type DeviceState struct {
	Device       hdhomerun.Device `json:"device"`
	Host         string           `json:"host"`
	ChannelCount int              `json:"channelCount"`
	LastSeen     time.Time        `json:"lastSeen"`
	LastError    string           `json:"lastError,omitempty"`
	Discovered   bool             `json:"discovered"`
}

// Registry polls devices and exposes the merged lineup.
type Registry struct {
	client       *hdhomerun.Client
	hosts        []string
	autoDiscover bool
	log          *slog.Logger

	mu        sync.RWMutex
	devices   map[string]*DeviceState
	channels  []Channel
	lastRun   time.Time
	selfAddrs map[string]bool
}

// NewRegistry builds a registry for the given pinned hosts.
func NewRegistry(client *hdhomerun.Client, hosts []string, autoDiscover bool, log *slog.Logger) *Registry {
	return &Registry{
		client:       client,
		hosts:        hosts,
		autoDiscover: autoDiscover,
		log:          log,
		devices:      map[string]*DeviceState{},
	}
}

// Refresh re-reads every device and rebuilds the channel list. Devices
// that fail to answer keep their last lineup but record the error.
func (r *Registry) Refresh(ctx context.Context) error {
	hosts := map[string]bool{}
	for _, h := range r.hosts {
		hosts[h] = false
	}
	if r.autoDiscover {
		dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		found, err := hdhomerun.DiscoverBroadcast(dctx, 1500*time.Millisecond)
		cancel()
		if err != nil {
			r.log.Warn("udp discovery failed", "err", err)
		}
		for _, d := range found {
			if _, pinned := hosts[d.Addr]; !pinned {
				hosts[d.Addr] = true
			}
		}
	}
	if len(hosts) == 0 {
		return fmt.Errorf("no HDHomeRun hosts configured and none discovered")
	}

	type result struct {
		host       string
		discovered bool
		dev        *hdhomerun.Device
		chans      []hdhomerun.Channel
		err        error
	}
	results := make(chan result, len(hosts))
	for host, discovered := range hosts {
		go func(host string, discovered bool) {
			res := result{host: host, discovered: discovered}
			dev, err := r.client.Discover(ctx, host)
			if err != nil {
				res.err = err
				results <- res
				return
			}
			res.dev = dev
			res.chans, res.err = r.client.Lineup(ctx, dev)
			results <- res
		}(host, discovered)
	}

	lineups := map[string][]hdhomerun.Channel{}
	var firstErr error
	r.mu.Lock()
	defer r.mu.Unlock()
	for range hosts {
		res := <-results
		if res.err != nil {
			r.log.Warn("device poll failed", "host", res.host, "err", res.err)
			if firstErr == nil {
				firstErr = res.err
			}
			// Keep a stale record for the host so the UI can show the error.
			for _, st := range r.devices {
				if st.Host == res.host {
					st.LastError = res.err.Error()
				}
			}
			continue
		}
		st := r.devices[res.dev.DeviceID]
		if st == nil {
			st = &DeviceState{}
			r.devices[res.dev.DeviceID] = st
			r.log.Info("device online", "id", res.dev.DeviceID, "model", res.dev.ModelNumber, "tuners", res.dev.TunerCount, "host", res.host)
		}
		st.Device = *res.dev
		st.Host = res.host
		st.ChannelCount = len(res.chans)
		st.LastSeen = time.Now()
		st.LastError = ""
		st.Discovered = res.discovered
		lineups[res.dev.DeviceID] = res.chans
	}
	if len(lineups) > 0 {
		r.channels = mergeLineups(lineups)
	}
	r.lastRun = time.Now()
	if len(lineups) == 0 && firstErr != nil {
		return firstErr
	}
	return nil
}

// Run refreshes on an interval until ctx is cancelled.
func (r *Registry) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Refresh(ctx); err != nil {
				r.log.Warn("lineup refresh failed", "err", err)
			}
		}
	}
}

// Devices returns a snapshot of known devices, sorted by ID.
func (r *Registry) Devices() []DeviceState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]DeviceState, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device.DeviceID < out[j].Device.DeviceID })
	return out
}

// Device returns one device by ID.
func (r *Registry) Device(id string) (hdhomerun.Device, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.devices[id]
	if !ok {
		return hdhomerun.Device{}, false
	}
	return st.Device, true
}

// Channels returns the merged lineup.
func (r *Registry) Channels() []Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Channel, len(r.channels))
	copy(out, r.channels)
	return out
}

// Channel looks up a channel by guide number.
func (r *Registry) Channel(guideNumber string) (Channel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.channels {
		if c.GuideNumber == guideNumber {
			return c, true
		}
	}
	return Channel{}, false
}

// LastRefresh reports when the registry last polled.
func (r *Registry) LastRefresh() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastRun
}

func mergeLineups(lineups map[string][]hdhomerun.Channel) []Channel {
	byNumber := map[string]*Channel{}
	ids := make([]string, 0, len(lineups))
	for id := range lineups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, c := range lineups[id] {
			m := byNumber[c.GuideNumber]
			if m == nil {
				m = &Channel{
					GuideNumber: c.GuideNumber,
					GuideName:   c.GuideName,
					HD:          c.HD == 1,
					DRM:         c.DRM == 1,
					VideoCodec:  c.VideoCodec,
					AudioCodec:  c.AudioCodec,
					Favorite:    c.Favorite == 1,
				}
				byNumber[c.GuideNumber] = m
			}
			m.Sources = append(m.Sources, Source{DeviceID: id, URL: c.URL})
		}
	}
	out := make([]Channel, 0, len(byNumber))
	for _, c := range byNumber {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return lessGuideNumber(out[i].GuideNumber, out[j].GuideNumber) })
	return out
}

// lessGuideNumber orders "5.1" < "5.2" < "10.1" numerically.
func lessGuideNumber(a, b string) bool {
	am, as := splitGuideNumber(a)
	bm, bs := splitGuideNumber(b)
	if am != bm {
		return am < bm
	}
	if as != bs {
		return as < bs
	}
	return a < b
}

func splitGuideNumber(s string) (float64, float64) {
	parts := strings.SplitN(s, ".", 2)
	major, _ := strconv.ParseFloat(parts[0], 64)
	var minor float64
	if len(parts) == 2 {
		minor, _ = strconv.ParseFloat(parts[1], 64)
	}
	return major, minor
}
