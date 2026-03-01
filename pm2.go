package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type PM2Process struct {
	PID   int    `json:"pid"`
	Name  string `json:"name"`
	PM_ID int    `json:"pm_id"`
	Monit struct {
		Memory int64   `json:"memory"`
		CPU    float64 `json:"cpu"`
	} `json:"monit"`
	PM2Env PM2Env
	// Computed
	Healthy     bool
	HealthKnown bool
	HCPort      string // health check port (PORT+200), empty if unknown
	HCDebug     string // last HC attempt, e.g. "GET :9202/health → 200 {\"ok\":true}"
}

// PM2Env holds the typed PM2 environment fields plus a full map of all
// top-level pm2_env keys (which includes user-defined env vars like PORT).
type PM2Env struct {
	Status      string `json:"status"`
	PMUptime    int64  `json:"pm_uptime"`
	RestartTime int    `json:"restart_time"`
	OutLogPath  string `json:"pm_out_log_path"`
	ErrLogPath  string `json:"pm_err_log_path"`
	ExecMode    string `json:"exec_mode"`
	Instances   int    `json:"instances"`
	// All captures every key at the pm2_env level, including user env vars.
	// PM2 stores env vars (PORT, NODE_ENV, etc.) directly in pm2_env, not
	// nested under an "env" sub-object.
	All map[string]any
}

func (e *PM2Env) UnmarshalJSON(data []byte) error {
	// Parse the known typed fields via an alias to avoid infinite recursion.
	type Alias struct {
		Status      string `json:"status"`
		PMUptime    int64  `json:"pm_uptime"`
		RestartTime int    `json:"restart_time"`
		OutLogPath  string `json:"pm_out_log_path"`
		ErrLogPath  string `json:"pm_err_log_path"`
		ExecMode    string `json:"exec_mode"`
		Instances   int    `json:"instances"`
	}
	var a Alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	e.Status = a.Status
	e.PMUptime = a.PMUptime
	e.RestartTime = a.RestartTime
	e.OutLogPath = a.OutLogPath
	e.ErrLogPath = a.ErrLogPath
	e.ExecMode = a.ExecMode
	e.Instances = a.Instances
	// Capture all top-level keys for env var lookups.
	return json.Unmarshal(data, &e.All)
}

func (p *PM2Process) UnmarshalJSON(data []byte) error {
	type Alias struct {
		PID   int    `json:"pid"`
		Name  string `json:"name"`
		PM_ID int    `json:"pm_id"`
		Monit struct {
			Memory int64   `json:"memory"`
			CPU    float64 `json:"cpu"`
		} `json:"monit"`
		PM2Env PM2Env `json:"pm2_env"`
	}
	var a Alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	p.PID = a.PID
	p.Name = a.Name
	p.PM_ID = a.PM_ID
	p.Monit = a.Monit
	p.PM2Env = a.PM2Env
	return nil
}

func (p PM2Process) StatusIcon() string {
	switch p.PM2Env.Status {
	case "online":
		return "●"
	case "stopped", "stopping":
		return "○"
	case "errored":
		return "✕"
	case "launching":
		return "◌"
	default:
		return "·"
	}
}

func (p PM2Process) Uptime() string {
	if p.PM2Env.Status != "online" || p.PM2Env.PMUptime == 0 {
		return "—"
	}
	d := time.Since(time.Unix(p.PM2Env.PMUptime/1000, 0))
	if d < 0 {
		return "—"
	}
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

func (p PM2Process) MemStr() string {
	mb := float64(p.Monit.Memory) / 1024 / 1024
	if mb == 0 {
		return "—"
	}
	if mb >= 1024 {
		return fmt.Sprintf("%.1fG", mb/1024)
	}
	return fmt.Sprintf("%.0fM", mb)
}

// psRow holds per-process data parsed from a single ps snapshot.
type psRow struct {
	ppid int
	cpu  float64
	rss  int64 // KB
}

// psSnapshot runs ps once and returns a map of pid→psRow plus a children map.
func psSnapshot() (rows map[int]psRow, children map[int][]int) {
	rows = map[int]psRow{}
	children = map[int][]int{}
	out, err := exec.Command("ps", "-ax", "-o", "pid=,ppid=,pcpu=,rss=").Output()
	if err != nil {
		return
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		ppid, _ := strconv.Atoi(fields[1])
		cpu, _ := strconv.ParseFloat(fields[2], 64)
		rss, _ := strconv.ParseInt(fields[3], 10, 64)
		if pid > 0 {
			rows[pid] = psRow{ppid, cpu, rss}
			children[ppid] = append(children[ppid], pid)
		}
	}
	return
}

// subtreeDescendants returns all descendant PIDs using a pre-built children map.
func subtreeDescendants(pid int, children map[int][]int) []int {
	var result []int
	queue := children[pid]
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		result = append(result, cur)
		queue = append(queue, children[cur]...)
	}
	return result
}

// enrichWithPS updates CPU and memory on each process using a live ps snapshot,
// summing stats across the entire process subtree (make → bash → node, etc.).
func enrichWithPS(procs []PM2Process) {
	rows, children := psSnapshot()
	for i, p := range procs {
		var cpu float64
		var rss int64
		for _, pid := range append([]int{p.PID}, subtreeDescendants(p.PID, children)...) {
			if r, ok := rows[pid]; ok {
				cpu += r.cpu
				rss += r.rss
			}
		}
		procs[i].Monit.CPU = cpu
		procs[i].Monit.Memory = rss * 1024 // KB → bytes
	}
}

// portFromEnv reads PORT from the environment of a running process using
// `ps eww -p <pid>`, which shows the full environment string. Returns "" if
// not found.
func portFromEnv(pid int) string {
	out, err := exec.Command("ps", "eww", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	for field := range strings.FieldsSeq(string(out)) {
		if after, ok := strings.CutPrefix(field, "PORT="); ok {
			return after
		}
	}
	return ""
}

// findHCPort walks the process subtree rooted at pid looking for a PORT env
// var. Returns "PORT+200" per the convention (gRPC=PORT, HTTP=PORT+100,
// health=PORT+200), or "" if no PORT is found.
func findHCPort(pid int) string {
	_, children := psSnapshot()
	for _, p := range append([]int{pid}, subtreeDescendants(pid, children)...) {
		if port := portFromEnv(p); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n == 0 {
				continue
			}
			return strconv.Itoa(n + 200)
		}
	}
	return ""
}

func pm2List() ([]PM2Process, error) {
	out, err := exec.Command("pm2", "jlist").Output()
	if err != nil {
		return nil, fmt.Errorf("pm2 jlist: %w", err)
	}
	var procs []PM2Process
	if err := json.Unmarshal(out, &procs); err != nil {
		return nil, fmt.Errorf("parse output: %w", err)
	}
	enrichWithPS(procs)
	filtered := procs[:0]
	for _, p := range procs {
		if p.Name != "pm2-logrotate" {
			filtered = append(filtered, p)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Name < filtered[j].Name
	})
	return filtered, nil
}

func pm2Logs(name string) string {
	out, _ := exec.Command("pm2", "logs", name, "--lines", "2000", "--nostream").Output()
	if len(out) == 0 {
		return "(no logs available)\n"
	}
	return string(out)
}

// pm2AllLogs fetches interleaved logs from all processes.
func pm2AllLogs() string {
	out, _ := exec.Command("pm2", "logs", "--lines", "2000", "--nostream").Output()
	if len(out) == 0 {
		return "(no logs available)\n"
	}
	return string(out)
}

func pm2Restart(name string) error { return exec.Command("pm2", "restart", name).Run() }
func pm2Stop(name string) error    { return exec.Command("pm2", "stop", name).Run() }
func pm2Start(name string) error   { return exec.Command("pm2", "start", name).Run() }

// pm2HealthCheck checks the health of a process. It finds PORT from the running
// process environment (via ps eww), computes the HC port as PORT+200, then
// probes HTTP health endpoints. Returns (healthy, hcPort, debugInfo).
func pm2HealthCheck(p PM2Process) (bool, string, string) {
	if p.PM2Env.Status != "online" {
		return false, "", ""
	}
	hcPort := findHCPort(p.PID)
	if hcPort == "" {
		return true, "", "no PORT in env"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for _, path := range []string{"/status/ready", "/health", "/healthz", "/api/health", "/ping", "/"} {
		url := fmt.Sprintf("http://localhost:%s%s", hcPort, path)
		resp, err := client.Get(url)
		if err != nil {
			debug := fmt.Sprintf("GET :%s%s → %s", hcPort, path, err.Error())
			return false, hcPort, debug
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 120))
		resp.Body.Close()
		bodyStr := strings.TrimSpace(string(body))
		debug := fmt.Sprintf("GET :%s%s → %d %s", hcPort, path, resp.StatusCode, bodyStr)
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			return true, hcPort, debug
		}
	}
	return false, hcPort, fmt.Sprintf("no healthy path on :%s", hcPort)
}
