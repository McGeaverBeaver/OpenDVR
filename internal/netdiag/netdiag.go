// Package netdiag answers "can this container reach my HDHomeRun, and if
// not, where does it stop?" It is used by the /api/diagnostics endpoint,
// the Status page and the -check command line flag.
package netdiag

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/McGeaverBeaver/opendvr/internal/hdhomerun"
)

// Check is one probe result.
type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Latency string `json:"latency,omitempty"`
	Detail  string `json:"detail"`
}

// Interface is one network interface of the container.
type Interface struct {
	Name  string   `json:"name"`
	Addrs []string `json:"addrs"`
}

// Report is the complete diagnostic.
type Report struct {
	Host           string      `json:"host"`
	ResolvedIPs    []string    `json:"resolvedIps"`
	Interfaces     []Interface `json:"interfaces"`
	DefaultGateway string      `json:"defaultGateway"`
	Checks         []Check     `json:"checks"`
	Hint           string      `json:"hint"`
}

// Run probes host from this process's network namespace.
func Run(ctx context.Context, host string) Report {
	r := Report{Host: host}
	r.Interfaces = interfaces()
	r.DefaultGateway = defaultGateway()
	if host == "" {
		return r
	}

	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		r.Checks = append(r.Checks, Check{Name: "dns", Detail: err.Error()})
		r.Hint = "The host name does not resolve. Use the tuner's IP address."
		return r
	}
	r.ResolvedIPs = ips
	target := ips[0]

	r.Checks = append(r.Checks, ping(ctx, target))
	r.Checks = append(r.Checks, tcp(ctx, target, 80))
	r.Checks = append(r.Checks, tcp(ctx, target, 5004))
	r.Checks = append(r.Checks, httpDiscover(ctx, target))
	r.Checks = append(r.Checks, udpDiscover(ctx, target))
	r.Hint = hint(r)
	return r
}

func ping(ctx context.Context, ip string) Check {
	bin, err := exec.LookPath("ping")
	if err != nil {
		return Check{Name: "icmp ping", Detail: "ping binary not available in this image"}
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	start := time.Now()
	out, err := exec.CommandContext(ctx, bin, "-c", "2", "-W", "2", "-n", ip).CombinedOutput()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := ""
	if len(lines) > 0 {
		last = lines[len(lines)-1]
	}
	if err != nil {
		return Check{Name: "icmp ping", Detail: "no reply: " + last}
	}
	return Check{Name: "icmp ping", OK: true, Latency: time.Since(start).Round(time.Millisecond).String(), Detail: last}
}

func tcp(ctx context.Context, ip string, port int) Check {
	name := fmt.Sprintf("tcp %d", port)
	d := net.Dialer{Timeout: 4 * time.Second}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
	if err != nil {
		return Check{Name: name, Detail: classify(err)}
	}
	c.Close()
	return Check{Name: name, OK: true, Latency: time.Since(start).Round(time.Millisecond).String(), Detail: "connected"}
}

func httpDiscover(ctx context.Context, ip string) Check {
	name := "http discover.json"
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ip+"/discover.json", nil)
	start := time.Now()
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return Check{Name: name, Detail: classify(err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 160 {
		snippet = snippet[:160] + "…"
	}
	ok := resp.StatusCode == http.StatusOK && strings.Contains(snippet, "DeviceID")
	return Check{Name: name, OK: ok, Latency: time.Since(start).Round(time.Millisecond).String(), Detail: resp.Status + " " + snippet}
}

func udpDiscover(ctx context.Context, ip string) Check {
	name := "udp 65001 discovery"
	dev, err := hdhomerun.DiscoverUnicast(ctx, ip, 3*time.Second)
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	return Check{Name: name, OK: true, Detail: fmt.Sprintf("device %s, %d tuners, %s", dev.DeviceID, dev.TunerCount, dev.BaseURL)}
}

func classify(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "connection refused"):
		return "refused: host is reachable but nothing listens on that port"
	case strings.Contains(s, "no route to host"):
		return "no route to host: the container has no route to that network"
	case strings.Contains(s, "network is unreachable"):
		return "network unreachable: no route to that network"
	case strings.Contains(s, "i/o timeout") || strings.Contains(s, "deadline exceeded"):
		return "timeout: packets leave but nothing comes back (routing or firewall)"
	}
	return s
}

func hint(r Report) string {
	ok := map[string]bool{}
	for _, c := range r.Checks {
		ok[c.Name] = c.OK
	}
	switch {
	case ok["http discover.json"]:
		return "The tuner is reachable. If the lineup still fails, rescan from the Channels page."
	case ok["tcp 80"] && !ok["http discover.json"]:
		return "Port 80 answers but discover.json does not. Is this IP really an HDHomeRun?"
	case ok["icmp ping"] && !ok["tcp 80"]:
		return "ICMP works but TCP 80 does not. A firewall between the sites is blocking TCP to the tuner."
	case !ok["icmp ping"] && !ok["tcp 80"] && !ok["udp 65001 discovery"]:
		if r.DefaultGateway == "" {
			return "The container has no default gateway. Check its Docker network type."
		}
		return "Nothing reaches the tuner from this container. On Unraid this usually means the container is on a custom (br0/macvlan) network that bypasses the host's VPN route, or the VPN peer does not route the Docker subnet. Switch the container to Bridge (default) or add the Docker subnet to the VPN's allowed IPs."
	}
	return ""
}

func interfaces() []Interface {
	var out []Interface
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		var as []string
		for _, a := range addrs {
			as = append(as, a.String())
		}
		out = append(out, Interface{Name: i.Name, Addrs: as})
	}
	return out
}

// defaultGateway reads the IPv4 default route from /proc/net/route.
func defaultGateway() string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		b, err := hex.DecodeString(fields[2])
		if err != nil || len(b) != 4 {
			continue
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, binary.LittleEndian.Uint32(b))
		return fmt.Sprintf("%s via %s", ip, fields[0])
	}
	return ""
}

// Print writes a human readable report.
func Print(w io.Writer, r Report) {
	fmt.Fprintf(w, "Target: %s", r.Host)
	if len(r.ResolvedIPs) > 0 {
		fmt.Fprintf(w, " (%s)", strings.Join(r.ResolvedIPs, ", "))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Default gateway: %s\n", orNone(r.DefaultGateway))
	for _, i := range r.Interfaces {
		fmt.Fprintf(w, "Interface %-8s %s\n", i.Name, strings.Join(i.Addrs, " "))
	}
	fmt.Fprintln(w)
	for _, c := range r.Checks {
		status := "FAIL"
		if c.OK {
			status = "ok  "
		}
		lat := ""
		if c.Latency != "" {
			lat = " (" + c.Latency + ")"
		}
		fmt.Fprintf(w, "%s %-22s %s%s\n", status, c.Name, c.Detail, lat)
	}
	if r.Hint != "" {
		fmt.Fprintf(w, "\n%s\n", r.Hint)
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
