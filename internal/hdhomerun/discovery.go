package hdhomerun

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"sort"
	"time"
)

// HDHomeRun UDP discovery protocol (port 65001).
//
// Frame layout:
//
//	uint16 type (big endian)
//	uint16 payload length (big endian)
//	payload: sequence of TLVs (tag byte, varint length, value)
//	uint32 CRC32 of everything above (little endian)
const (
	discoverPort = 65001

	typeDiscoverReq uint16 = 0x0002
	typeDiscoverRpy uint16 = 0x0003

	tagDeviceType    byte = 0x01
	tagDeviceID      byte = 0x02
	tagTunerCount    byte = 0x10
	tagLineupURL     byte = 0x27
	tagBaseURL       byte = 0x2A
	tagDeviceAuthStr byte = 0x2B

	deviceTypeWildcard uint32 = 0xFFFFFFFF
	deviceTypeTuner    uint32 = 0x00000001
	deviceIDWildcard   uint32 = 0xFFFFFFFF
)

// DiscoveredDevice is the minimal information a UDP reply carries.
type DiscoveredDevice struct {
	Addr       string
	DeviceID   string
	DeviceType uint32
	TunerCount int
	BaseURL    string
	LineupURL  string
}

// DiscoverBroadcast sends a discovery request to the local broadcast address
// and collects replies until timeout elapses or ctx is cancelled.
//
// This only reaches devices on the same L2 segment as the host. A device at
// another site, or a container in Docker bridge mode, will not answer; pin
// those with HDHR_HOSTS instead.
func DiscoverBroadcast(ctx context.Context, timeout time.Duration) ([]DiscoveredDevice, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("udp listen: %w", err)
	}
	defer conn.Close()

	req := encodeFrame(typeDiscoverReq, buildDiscoverPayload())
	dst := &net.UDPAddr{IP: net.IPv4bcast, Port: discoverPort}
	if _, err := conn.WriteTo(req, dst); err != nil {
		return nil, fmt.Errorf("udp broadcast: %w", err)
	}

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)

	seen := map[string]DiscoveredDevice{}
	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break
			}
			if ctx.Err() != nil {
				break
			}
			return nil, fmt.Errorf("udp read: %w", err)
		}
		dev, err := parseDiscoverReply(buf[:n], from)
		if err != nil {
			continue
		}
		seen[dev.DeviceID] = dev
	}

	out := make([]DiscoveredDevice, 0, len(seen))
	for _, d := range seen {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out, nil
}

func buildDiscoverPayload() []byte {
	var p []byte
	p = appendTag(p, tagDeviceType, binary.BigEndian.AppendUint32(nil, deviceTypeWildcard))
	p = appendTag(p, tagDeviceID, binary.BigEndian.AppendUint32(nil, deviceIDWildcard))
	return p
}

func parseDiscoverReply(frame []byte, from net.Addr) (DiscoveredDevice, error) {
	typ, payload, err := decodeFrame(frame)
	if err != nil {
		return DiscoveredDevice{}, err
	}
	if typ != typeDiscoverRpy {
		return DiscoveredDevice{}, fmt.Errorf("unexpected frame type 0x%04x", typ)
	}
	tags, err := parseTags(payload)
	if err != nil {
		return DiscoveredDevice{}, err
	}

	host := ""
	if ua, ok := from.(*net.UDPAddr); ok {
		host = ua.IP.String()
	} else if from != nil {
		host, _, _ = net.SplitHostPort(from.String())
	}

	dev := DiscoveredDevice{Addr: host}
	if v, ok := tags[tagDeviceType]; ok && len(v) == 4 {
		dev.DeviceType = binary.BigEndian.Uint32(v)
	}
	if v, ok := tags[tagDeviceID]; ok && len(v) == 4 {
		dev.DeviceID = fmt.Sprintf("%08X", binary.BigEndian.Uint32(v))
	}
	if v, ok := tags[tagTunerCount]; ok && len(v) >= 1 {
		dev.TunerCount = int(v[0])
	}
	if v, ok := tags[tagBaseURL]; ok {
		dev.BaseURL = string(v)
	}
	if v, ok := tags[tagLineupURL]; ok {
		dev.LineupURL = string(v)
	}
	if dev.BaseURL == "" && host != "" {
		dev.BaseURL = "http://" + host
	}
	if dev.DeviceID == "" {
		return DiscoveredDevice{}, errors.New("reply without device id")
	}
	return dev, nil
}

func encodeFrame(typ uint16, payload []byte) []byte {
	buf := make([]byte, 0, 4+len(payload)+4)
	buf = binary.BigEndian.AppendUint16(buf, typ)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(payload)))
	buf = append(buf, payload...)
	buf = binary.LittleEndian.AppendUint32(buf, crc32.ChecksumIEEE(buf))
	return buf
}

func decodeFrame(b []byte) (uint16, []byte, error) {
	if len(b) < 8 {
		return 0, nil, errors.New("frame too short")
	}
	typ := binary.BigEndian.Uint16(b[0:2])
	n := int(binary.BigEndian.Uint16(b[2:4]))
	if 4+n+4 > len(b) {
		return 0, nil, errors.New("frame length mismatch")
	}
	body := b[:4+n]
	want := binary.LittleEndian.Uint32(b[4+n : 4+n+4])
	if crc32.ChecksumIEEE(body) != want {
		return 0, nil, errors.New("bad crc")
	}
	return typ, b[4 : 4+n], nil
}

// appendTag writes one TLV. Lengths up to 127 use a single byte; longer
// values set the high bit of the first byte and continue in a second byte.
func appendTag(buf []byte, tag byte, value []byte) []byte {
	buf = append(buf, tag)
	n := len(value)
	if n <= 127 {
		buf = append(buf, byte(n))
	} else {
		buf = append(buf, byte(0x80|(n&0x7F)), byte(n>>7))
	}
	return append(buf, value...)
}

func parseTags(p []byte) (map[byte][]byte, error) {
	tags := map[byte][]byte{}
	for i := 0; i < len(p); {
		tag := p[i]
		i++
		if i >= len(p) {
			return nil, errors.New("truncated tag length")
		}
		n := int(p[i] & 0x7F)
		if p[i]&0x80 != 0 {
			i++
			if i >= len(p) {
				return nil, errors.New("truncated tag length")
			}
			n |= int(p[i]) << 7
		}
		i++
		if i+n > len(p) {
			return nil, errors.New("truncated tag value")
		}
		tags[tag] = p[i : i+n]
		i += n
	}
	return tags, nil
}

// DiscoverUnicast sends a discovery request straight to one host instead of
// the broadcast address. HDHomeRun devices answer unicast requests too, so
// this works across routed networks and is a useful connectivity probe that
// does not depend on the device's HTTP server.
func DiscoverUnicast(ctx context.Context, host string, timeout time.Duration) (*DiscoveredDevice, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("udp listen: %w", err)
	}
	defer conn.Close()

	dst, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, fmt.Sprint(discoverPort)))
	if err != nil {
		return nil, err
	}
	if _, err := conn.WriteTo(encodeFrame(typeDiscoverReq, buildDiscoverPayload()), dst); err != nil {
		return nil, fmt.Errorf("udp send: %w", err)
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return nil, fmt.Errorf("no discovery reply from %s within %s", host, timeout)
		}
		dev, perr := parseDiscoverReply(buf[:n], from)
		if perr != nil {
			continue
		}
		return &dev, nil
	}
}
