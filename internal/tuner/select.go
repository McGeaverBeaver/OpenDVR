package tuner

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/hdhomerun"
)

// TunerUse describes one tuner that is currently locked, for the
// "all tuners busy" message. Shared tuners are ones this server is
// streaming from, so a viewer can join that channel without a tuner.
type TunerUse struct {
	DeviceID string `json:"deviceId"`
	Resource string `json:"tuner"`
	Channel  string `json:"channel"`
	Name     string `json:"name,omitempty"`
	Client   string `json:"client,omitempty"`
	Shared   bool   `json:"shared"`
}

// ErrTunersBusy is returned when every tuner that could carry a channel is
// locked. Tuners lists what they are locked to, so the caller can offer
// the viewer those channels instead.
type ErrTunersBusy struct {
	Channel string
	Tuners  []TunerUse
}

func (e *ErrTunersBusy) Error() string {
	var tuned []string
	for _, t := range e.Tuners {
		s := t.Channel
		if t.Name != "" {
			s += " " + t.Name
		}
		if t.Shared {
			s += " (shared)"
		}
		tuned = append(tuned, s)
	}
	if len(tuned) == 0 {
		return fmt.Sprintf("all tuners are busy; cannot tune %s", e.Channel)
	}
	return fmt.Sprintf("all tuners are busy (%s); cannot tune %s", strings.Join(tuned, ", "), e.Channel)
}

// statusTimeout bounds the status.json read made before starting a
// session. A tuner on the LAN answers in tens of milliseconds; one that
// takes longer than this is treated as unknown rather than holding up
// the channel change.
const statusTimeout = 3 * time.Second

// SetSelfAddrs records this server's own IP addresses. A tuner streaming
// to one of them with no matching session is a session we just stopped
// that the device has not released yet; it is counted as free so rapid
// channel surfing is not refused.
func (r *Registry) SetSelfAddrs(addrs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selfAddrs = map[string]bool{}
	for _, a := range addrs {
		if i := strings.Index(a, "/"); i >= 0 {
			a = a[:i]
		}
		if a != "" {
			r.selfAddrs[a] = true
		}
	}
}

// ResolveSource picks a tuner for a channel. active lists the channels
// this server is already streaming, which is what makes a locked tuner
// "shared" rather than busy.
//
// Every device carrying the channel is asked for its live tuner status.
// The device with the most free tuners wins; a device whose status could
// not be read is assumed free but ranked below any device known to be.
// When no tuner is free the result is an *ErrTunersBusy listing what each
// tuner is locked to.
func (r *Registry) ResolveSource(ctx context.Context, guideNumber string, active map[string]bool) (Source, error) {
	ch, ok := r.Channel(guideNumber)
	if !ok {
		return Source{}, fmt.Errorf("unknown channel %q", guideNumber)
	}
	if ch.DRM {
		return Source{}, fmt.Errorf("channel %s is DRM protected and cannot be streamed", guideNumber)
	}
	if len(ch.Sources) == 0 {
		return Source{}, fmt.Errorf("channel %s has no sources", guideNumber)
	}

	r.mu.RLock()
	self := r.selfAddrs
	devices := map[string]hdhomerun.Device{}
	for id, st := range r.devices {
		devices[id] = st.Device
	}
	names := map[string]string{}
	for _, c := range r.channels {
		names[c.GuideNumber] = c.GuideName
	}
	r.mu.RUnlock()

	statuses := map[string][]hdhomerun.TunerStatus{}
	for _, src := range ch.Sources {
		dev, ok := devices[src.DeviceID]
		if !ok {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, statusTimeout)
		st, err := r.client.Status(sctx, &dev)
		cancel()
		if err != nil {
			r.log.Warn("tuner status unavailable; assuming free", "device", src.DeviceID, "err", err)
			continue
		}
		statuses[src.DeviceID] = st
	}
	src, busy, ok := pickSource(ch, devices, statuses, active, self)
	if !ok {
		for i := range busy {
			busy[i].Name = names[busy[i].Channel]
		}
		return Source{}, &ErrTunersBusy{Channel: guideNumber, Tuners: busy}
	}
	return src, nil
}

// pickSource is the pure selection behind ResolveSource. It returns the
// chosen source, or the list of locked tuners when nothing is free.
func pickSource(ch Channel, devices map[string]hdhomerun.Device, statuses map[string][]hdhomerun.TunerStatus, active, self map[string]bool) (Source, []TunerUse, bool) {
	type candidate struct {
		src   Source
		known bool
		free  int
	}
	var cands []candidate
	var busy []TunerUse
	for _, src := range ch.Sources {
		dev, ok := devices[src.DeviceID]
		if !ok {
			continue
		}
		st, known := statuses[src.DeviceID]
		if !known {
			cands = append(cands, candidate{src: src, known: false, free: dev.TunerCount})
			continue
		}
		used := 0
		for _, t := range st {
			if !t.InUse() {
				continue
			}
			shared := active[t.VctNumber]
			// Streaming to us but not one of our sessions: a tuner we just
			// released that the device has not freed yet. Count it free.
			if !shared && self[t.TargetIP] {
				continue
			}
			used++
			busy = append(busy, TunerUse{
				DeviceID: src.DeviceID, Resource: t.Resource, Channel: t.VctNumber,
				Client: t.TargetIP, Shared: shared,
			})
		}
		free := dev.TunerCount - used
		if dev.TunerCount == 0 {
			// Unknown tuner count: trust the status list.
			free = len(st) - used
		}
		cands = append(cands, candidate{src: src, known: true, free: free})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].known != cands[j].known {
			return cands[i].known
		}
		return cands[i].free > cands[j].free
	})
	for _, c := range cands {
		if c.free > 0 {
			return c.src, nil, true
		}
	}
	sort.Slice(busy, func(i, j int) bool {
		if busy[i].DeviceID != busy[j].DeviceID {
			return busy[i].DeviceID < busy[j].DeviceID
		}
		return busy[i].Resource < busy[j].Resource
	})
	return Source{}, busy, false
}
