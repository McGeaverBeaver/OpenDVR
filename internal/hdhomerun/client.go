// Package hdhomerun talks to SiliconDust HDHomeRun tuners over their plain
// HTTP API (discover.json, lineup.json, status.json) and the UDP discovery
// protocol.
package hdhomerun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Device is the device description returned by /discover.json.
type Device struct {
	FriendlyName    string `json:"FriendlyName"`
	ModelNumber     string `json:"ModelNumber"`
	FirmwareName    string `json:"FirmwareName"`
	FirmwareVersion string `json:"FirmwareVersion"`
	DeviceID        string `json:"DeviceID"`
	DeviceAuth      string `json:"DeviceAuth"`
	BaseURL         string `json:"BaseURL"`
	LineupURL       string `json:"LineupURL"`
	TunerCount      int    `json:"TunerCount"`
}

// Channel is one entry of /lineup.json.
type Channel struct {
	GuideNumber string `json:"GuideNumber"`
	GuideName   string `json:"GuideName"`
	VideoCodec  string `json:"VideoCodec,omitempty"`
	AudioCodec  string `json:"AudioCodec,omitempty"`
	HD          int    `json:"HD,omitempty"`
	DRM         int    `json:"DRM,omitempty"`
	Favorite    int    `json:"Favorite,omitempty"`
	URL         string `json:"URL"`
}

// TunerStatus is one entry of /status.json.
type TunerStatus struct {
	Resource              string `json:"Resource"`
	VctNumber             string `json:"VctNumber,omitempty"`
	VctName               string `json:"VctName,omitempty"`
	Frequency             int    `json:"Frequency,omitempty"`
	SignalStrengthPercent int    `json:"SignalStrengthPercent,omitempty"`
	SignalQualityPercent  int    `json:"SignalQualityPercent,omitempty"`
	SymbolQualityPercent  int    `json:"SymbolQualityPercent,omitempty"`
	TargetIP              string `json:"TargetIP,omitempty"`
	NetworkRate           int    `json:"NetworkRate,omitempty"`
}

// InUse reports whether the tuner is currently streaming to a client.
func (t TunerStatus) InUse() bool {
	return t.TargetIP != "" || t.VctNumber != ""
}

// Client is an HTTP client for HDHomeRun devices.
type Client struct {
	http *http.Client
}

// NewClient returns a client with the given per-request timeout.
func NewClient(timeout time.Duration) *Client {
	return &Client{http: &http.Client{Timeout: timeout}}
}

// NormalizeBaseURL turns "10.0.0.5", "10.0.0.5:80" or "http://10.0.0.5/"
// into "http://10.0.0.5".
func NormalizeBaseURL(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	return strings.TrimRight(host, "/")
}

// Discover fetches /discover.json from the given host.
//
// The device reports its own LAN address in BaseURL and LineupURL. When the
// configured host differs (NAT, VPN hairpin), the configured host wins so
// that every URL we hand out is one we know is reachable.
func (c *Client) Discover(ctx context.Context, host string) (*Device, error) {
	base := NormalizeBaseURL(host)
	if base == "" {
		return nil, fmt.Errorf("empty host")
	}
	var d Device
	if err := c.getJSON(ctx, base+"/discover.json", &d); err != nil {
		return nil, fmt.Errorf("discover %s: %w", host, err)
	}
	cfgHost := hostOf(base)
	if d.BaseURL == "" {
		d.BaseURL = base
	} else {
		d.BaseURL = rewriteHost(d.BaseURL, cfgHost)
	}
	if d.LineupURL == "" {
		d.LineupURL = d.BaseURL + "/lineup.json"
	} else {
		d.LineupURL = rewriteHost(d.LineupURL, cfgHost)
	}
	if d.DeviceID == "" {
		return nil, fmt.Errorf("discover %s: response has no DeviceID", host)
	}
	return &d, nil
}

// Lineup fetches the channel lineup of a device. Stream URLs are rewritten
// to the device's (possibly overridden) host.
func (c *Client) Lineup(ctx context.Context, d *Device) ([]Channel, error) {
	var chans []Channel
	if err := c.getJSON(ctx, d.LineupURL, &chans); err != nil {
		return nil, fmt.Errorf("lineup %s: %w", d.DeviceID, err)
	}
	host := hostOf(d.BaseURL)
	for i := range chans {
		if chans[i].URL == "" {
			chans[i].URL = fmt.Sprintf("http://%s:5004/auto/v%s", host, chans[i].GuideNumber)
			continue
		}
		chans[i].URL = rewriteHost(chans[i].URL, host)
	}
	return chans, nil
}

// Status fetches the live tuner status of a device.
func (c *Client) Status(ctx context.Context, d *Device) ([]TunerStatus, error) {
	var st []TunerStatus
	if err := c.getJSON(ctx, d.BaseURL+"/status.json", &st); err != nil {
		return nil, fmt.Errorf("status %s: %w", d.DeviceID, err)
	}
	return st, nil
}

func (c *Client) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: %s: %s", u, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// hostOf returns the host part (without port) of a URL.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if h, _, err := net.SplitHostPort(u.Host); err == nil {
		return h
	}
	return u.Host
}

// rewriteHost replaces the host of raw with newHost, keeping any port.
func rewriteHost(raw, newHost string) string {
	if newHost == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if _, port, err := net.SplitHostPort(u.Host); err == nil && port != "" {
		u.Host = net.JoinHostPort(newHost, port)
	} else {
		u.Host = newHost
	}
	return u.String()
}
