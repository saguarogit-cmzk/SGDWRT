package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// --- ubus strukture (samo polja koja koristimo) ---

type ubusBoard struct {
	Kernel   string `json:"kernel"`
	Hostname string `json:"hostname"`
	System   string `json:"system"`
	Model    string `json:"model"`
	Rootfs   string `json:"rootfs_type"`
	Release  struct {
		Distribution string `json:"distribution"`
		Version      string `json:"version"`
		Revision     string `json:"revision"`
		Target       string `json:"target"`
		Description  string `json:"description"`
	} `json:"release"`
}

type ubusFS struct {
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
	Used  int64 `json:"used"`
	Avail int64 `json:"avail"`
}

type ubusInfo struct {
	Localtime int64    `json:"localtime"`
	Uptime    int64    `json:"uptime"`
	Load      [3]int64 `json:"load"`
	Memory    struct {
		Total     int64 `json:"total"`
		Free      int64 `json:"free"`
		Buffered  int64 `json:"buffered"`
		Cached    int64 `json:"cached"`
		Available int64 `json:"available"`
	} `json:"memory"`
	Root ubusFS `json:"root"`
	Tmp  ubusFS `json:"tmp"`
	Swap struct {
		Total int64 `json:"total"`
		Free  int64 `json:"free"`
	} `json:"swap"`
}

type ubusIfaceDump struct {
	Interface []struct {
		Name   string `json:"interface"`
		Up     bool   `json:"up"`
		Uptime int64  `json:"uptime"`
		Proto  string `json:"proto"`
		Device string `json:"l3_device"`
		IPv4   []struct {
			Address string `json:"address"`
			Mask    int    `json:"mask"`
		} `json:"ipv4-address"`
		IPv6 []struct {
			Address string `json:"address"`
			Mask    int    `json:"mask"`
		} `json:"ipv6-address"`
		Route []struct {
			Target  string `json:"target"`
			Mask    int    `json:"mask"`
			Nexthop string `json:"nexthop"`
		} `json:"route"`
		DNS []string `json:"dns-server"`
	} `json:"interface"`
}

type ubusDevice struct {
	Type       string `json:"type"`
	Up         bool   `json:"up"`
	Carrier    bool   `json:"carrier"`
	Speed      string `json:"speed"`
	MAC        string `json:"macaddr"`
	Statistics struct {
		RxBytes int64 `json:"rx_bytes"`
		TxBytes int64 `json:"tx_bytes"`
	} `json:"statistics"`
}

// --- handleri ---

func (s *server) handleSystem(w http.ResponseWriter, r *http.Request) {
	var b ubusBoard
	if err := ubusCall(r.Context(), "system", "board", &b); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	model := b.Model
	if model == "" {
		model = b.System
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hostname":        b.Hostname,
		"model":           model,
		"cpu":             b.System,
		"cpu_cores":       runtime.NumCPU(),
		"kernel":          b.Kernel,
		"firmware":        b.Release.Description,
		"openwrt_version": b.Release.Version,
		"revision":        b.Release.Revision,
		"target":          b.Release.Target,
		"rootfs":          b.Rootfs,
		"saguaro_version": version,
	})
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var i ubusInfo
	if err := ubusCall(r.Context(), "system", "info", &i); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	load := make([]float64, 3)
	for n, v := range i.Load {
		load[n] = float64(v) / 65536.0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uptime_seconds": i.Uptime,
		"localtime":      i.Localtime,
		"load":           load,
		"memory": map[string]int64{
			"total":     i.Memory.Total,
			"free":      i.Memory.Free,
			"available": i.Memory.Available,
			"buffered":  i.Memory.Buffered,
			"cached":    i.Memory.Cached,
		},
		"swap": map[string]int64{"total": i.Swap.Total, "free": i.Swap.Free},
	})
}

func (s *server) handleStorage(w http.ResponseWriter, r *http.Request) {
	var i ubusInfo
	if err := ubusCall(r.Context(), "system", "info", &i); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	fs := func(mount string, f ubusFS) map[string]any {
		var pct float64
		if f.Total > 0 {
			pct = float64(f.Used) / float64(f.Total) * 100
		}
		return map[string]any{
			"mount": mount, "total_kb": f.Total, "used_kb": f.Used,
			"available_kb": f.Avail, "used_percent": pct,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"filesystems": []map[string]any{fs("/", i.Root), fs("/tmp", i.Tmp)},
	})
}

func (s *server) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	var dump ubusIfaceDump
	if err := ubusCall(r.Context(), "network.interface", "dump", &dump); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	var devs map[string]ubusDevice
	if err := ubusCall(r.Context(), "network.device", "status", &devs); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	ifaces := make([]map[string]any, 0, len(dump.Interface))
	for _, in := range dump.Interface {
		if in.Name == "loopback" {
			continue
		}
		v4 := make([]string, 0, len(in.IPv4))
		for _, a := range in.IPv4 {
			v4 = append(v4, netCIDR(a.Address, a.Mask))
		}
		v6 := make([]string, 0, len(in.IPv6))
		for _, a := range in.IPv6 {
			v6 = append(v6, netCIDR(a.Address, a.Mask))
		}
		gw := ""
		for _, rt := range in.Route {
			if rt.Target == "0.0.0.0" && rt.Mask == 0 {
				gw = rt.Nexthop
			}
		}
		ifaces = append(ifaces, map[string]any{
			"name": in.Name, "up": in.Up, "proto": in.Proto, "device": in.Device,
			"uptime_seconds": in.Uptime, "ipv4": v4, "ipv6": v6,
			"gateway": gw, "dns": in.DNS,
		})
	}

	// netifd ne prijavljuje portove koji nisu ni u jednoj konfiguraciji
	// (na IN100: eth2/eth3) — dopuna iz sysfs-a da se vide sva 4 porta.
	if entries, err := os.ReadDir("/sys/class/net"); err == nil {
		for _, e := range entries {
			n := e.Name()
			if _, ok := devs[n]; ok || !strings.HasPrefix(n, "eth") {
				continue
			}
			d := ubusDevice{Type: "Network device"}
			d.MAC = readSysfs(n, "address")
			d.Carrier = readSysfs(n, "carrier") == "1"
			d.Up = readSysfs(n, "operstate") == "up"
			if sp := readSysfs(n, "speed"); sp != "" && sp != "-1" {
				d.Speed = sp
			}
			devs[n] = d
		}
	}

	names := make([]string, 0, len(devs))
	for n := range devs {
		if n != "lo" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	devices := make([]map[string]any, 0, len(names))
	for _, n := range names {
		d := devs[n]
		devices = append(devices, map[string]any{
			"name": n, "type": d.Type, "up": d.Up, "carrier": d.Carrier,
			"speed": d.Speed, "mac": d.MAC,
			"rx_bytes": d.Statistics.RxBytes, "tx_bytes": d.Statistics.TxBytes,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"interfaces": ifaces, "devices": devices})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	gw := defaultGateway(ctx)
	gwOK := false
	if gw != "" {
		gwOK = ping(ctx, gw)
	}

	dnsOK := false
	{
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		if _, err := net.DefaultResolver.LookupHost(c, "downloads.openwrt.org"); err == nil {
			dnsOK = true
		}
		cancel()
	}

	netOK := false
	if conn, err := net.DialTimeout("tcp", "1.1.1.1:443", 3*time.Second); err == nil {
		conn.Close()
		netOK = true
	}

	status := "ok"
	if !gwOK || !dnsOK || !netOK {
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          status,
		"gateway":         map[string]any{"address": gw, "reachable": gwOK},
		"dns":             map[string]any{"ok": dnsOK},
		"internet":        map[string]any{"ok": netOK},
		"saguaro_version": version,
		"core_uptime_sec": int64(time.Since(s.started).Seconds()),
	})
}

// defaultGateway čita default rutu iz ubus network.interface dump.
func defaultGateway(ctx context.Context) string {
	var dump ubusIfaceDump
	if err := ubusCall(ctx, "network.interface", "dump", &dump); err != nil {
		return ""
	}
	for _, in := range dump.Interface {
		for _, rt := range in.Route {
			if rt.Target == "0.0.0.0" && rt.Mask == 0 {
				return rt.Nexthop
			}
		}
	}
	return ""
}

// ping koristi busybox ping; čita se samo exit code, ne izlaz.
func ping(ctx context.Context, addr string) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ping", "-c", "1", "-W", "2", addr).Run() == nil
}

func netCIDR(addr string, mask int) string {
	return addr + "/" + strconv.Itoa(mask)
}

func readSysfs(dev, attr string) string {
	b, err := os.ReadFile("/sys/class/net/" + dev + "/" + attr)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
