package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	gops "github.com/mitchellh/go-ps"
)

const maxSamples = 300

type metricInfo struct {
	name   string
	label  string
	source string
	notes  string
}

var allMetrics = []metricInfo{
	{"cpu", "CPU usage (ticks/sec)", "/proc/<pid>/stat", "delta"},
	{"rss", "Resident set size (memory pages)", "/proc/<pid>/statm", ""},
	{"vsize", "Virtual memory size (pages)", "/proc/<pid>/statm", ""},
	{"threads", "Number of threads", "/proc/<pid>/stat", ""},
	{"fds", "Open file descriptor count", "/proc/<pid>/fd/", "requires ownership"},
	{"read_bytes", "Storage bytes read per second", "/proc/<pid>/io", "delta; requires ownership"},
	{"write_bytes", "Storage bytes written per second", "/proc/<pid>/io", "delta; requires ownership"},
	{"minflt", "Minor page faults per second", "/proc/<pid>/stat", "delta"},
	{"majflt", "Major page faults per second", "/proc/<pid>/stat", "delta"},
	{"ctx_switch", "Context switches per second", "/proc/<pid>/status", "delta"},
}

func printMetrics() {
	fmt.Println("Available metrics:")
	fmt.Printf("  %-12s  %-38s  %-24s  %s\n", "NAME", "DESCRIPTION", "SOURCE", "NOTES")
	fmt.Printf("  %-12s  %-38s  %-24s  %s\n",
		"------------", "--------------------------------------", "------------------------", "-----")
	for _, m := range allMetrics {
		fmt.Printf("  %-12s  %-38s  %-24s  %s\n", m.name, m.label, m.source, m.notes)
	}
}

func metricsHelpText() string {
	var sb strings.Builder
	sb.WriteString("Comma-separated list of metrics to display.\nAvailable metrics:\n")
	for _, m := range allMetrics {
		line := fmt.Sprintf("  %-12s  %s  (%s)", m.name, m.label, m.source)
		if m.notes != "" {
			line += "  [" + m.notes + "]"
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

type Sample struct {
	Timestamp int64            `json:"t"`
	Values    map[string]int64 `json:"m"`
}

type ProcessStats struct {
	mu          sync.Mutex
	samples     []Sample
	prevRaw     map[string]int64
	initialized bool
}

var (
	statsMap        = make(map[string]*ProcessStats)
	statsMapMu      sync.RWMutex
	selectedMetrics []string
	metricsSet      map[string]bool
)

func hasMetric(name string) bool {
	return metricsSet[name]
}

func getProcessPID(processName string) int {
	procs, _ := gops.Processes()
	for _, p := range procs {
		if p.Executable() == processName {
			return p.Pid()
		}
	}
	return 0
}

func collectOnce(name string, pst *ProcessStats) {
	pid := getProcessPID(name)
	ts := time.Now().UnixNano() / int64(time.Millisecond)

	if pid == 0 {
		pst.mu.Lock()
		pst.initialized = false
		pst.samples = append(pst.samples, Sample{Timestamp: ts, Values: map[string]int64{}})
		if len(pst.samples) > maxSamples {
			pst.samples = pst.samples[len(pst.samples)-maxSamples:]
		}
		pst.mu.Unlock()
		return
	}

	// Copy previous raw accumulators without holding lock during I/O.
	pst.mu.Lock()
	prevRaw := make(map[string]int64, len(pst.prevRaw))
	for k, v := range pst.prevRaw {
		prevRaw[k] = v
	}
	initialized := pst.initialized
	pst.mu.Unlock()

	values := make(map[string]int64)
	newRaw := make(map[string]int64)

	// /proc/pid/stat — cpu, minflt, majflt, threads
	if hasMetric("cpu") || hasMetric("minflt") || hasMetric("majflt") || hasMetric("threads") {
		dat, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err == nil {
			fields := strings.Fields(string(dat))
			if len(fields) >= 20 {
				if hasMetric("cpu") {
					utime, _ := strconv.ParseInt(fields[13], 10, 64)
					ktime, _ := strconv.ParseInt(fields[14], 10, 64)
					raw := utime + ktime
					if initialized {
						values["cpu"] = raw - prevRaw["cpu"]
					}
					newRaw["cpu"] = raw
				}
				if hasMetric("minflt") {
					raw, _ := strconv.ParseInt(fields[9], 10, 64)
					if initialized {
						values["minflt"] = raw - prevRaw["minflt"]
					}
					newRaw["minflt"] = raw
				}
				if hasMetric("majflt") {
					raw, _ := strconv.ParseInt(fields[11], 10, 64)
					if initialized {
						values["majflt"] = raw - prevRaw["majflt"]
					}
					newRaw["majflt"] = raw
				}
				if hasMetric("threads") {
					v, _ := strconv.ParseInt(fields[19], 10, 64)
					values["threads"] = v
				}
			}
		}
	}

	// /proc/pid/statm — vsize, rss
	if hasMetric("vsize") || hasMetric("rss") {
		datm, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
		if err == nil {
			sm := strings.Fields(string(datm))
			if len(sm) >= 2 {
				if hasMetric("vsize") {
					v, _ := strconv.ParseInt(sm[0], 10, 64)
					values["vsize"] = v
				}
				if hasMetric("rss") {
					v, _ := strconv.ParseInt(sm[1], 10, 64)
					values["rss"] = v
				}
			}
		}
	}

	// /proc/pid/io — read_bytes, write_bytes (requires process ownership or root)
	if hasMetric("read_bytes") || hasMetric("write_bytes") {
		dat, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/io", pid))
		if err == nil {
			for _, line := range strings.Split(string(dat), "\n") {
				parts := strings.SplitN(strings.TrimSpace(line), ": ", 2)
				if len(parts) != 2 {
					continue
				}
				val, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
				if err != nil {
					continue
				}
				switch parts[0] {
				case "read_bytes":
					if hasMetric("read_bytes") {
						if initialized {
							values["read_bytes"] = val - prevRaw["read_bytes"]
						}
						newRaw["read_bytes"] = val
					}
				case "write_bytes":
					if hasMetric("write_bytes") {
						if initialized {
							values["write_bytes"] = val - prevRaw["write_bytes"]
						}
						newRaw["write_bytes"] = val
					}
				}
			}
		}
	}

	// /proc/pid/fd — fds (count open file descriptors; requires process ownership or root)
	if hasMetric("fds") {
		entries, err := ioutil.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err == nil {
			values["fds"] = int64(len(entries))
		}
	}

	// /proc/pid/status — ctx_switch (voluntary + involuntary context switches)
	if hasMetric("ctx_switch") {
		dat, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err == nil {
			var vol, nvol int64
			for _, line := range strings.Split(string(dat), "\n") {
				f := strings.Fields(line)
				if len(f) < 2 {
					continue
				}
				switch f[0] {
				case "voluntary_ctxt_switches:":
					vol, _ = strconv.ParseInt(f[1], 10, 64)
				case "nonvoluntary_ctxt_switches:":
					nvol, _ = strconv.ParseInt(f[1], 10, 64)
				}
			}
			raw := vol + nvol
			if initialized {
				values["ctx_switch"] = raw - prevRaw["ctx_switch"]
			}
			newRaw["ctx_switch"] = raw
		}
	}

	pst.mu.Lock()
	for k, v := range newRaw {
		pst.prevRaw[k] = v
	}
	pst.initialized = true
	pst.samples = append(pst.samples, Sample{Timestamp: ts, Values: values})
	if len(pst.samples) > maxSamples {
		pst.samples = pst.samples[len(pst.samples)-maxSamples:]
	}
	pst.mu.Unlock()
}

func startCollector(names []string) {
	statsMapMu.Lock()
	for _, name := range names {
		statsMap[name] = &ProcessStats{prevRaw: make(map[string]int64)}
	}
	statsMapMu.Unlock()

	go func() {
		ticker := time.NewTicker(time.Second)
		for range ticker.C {
			statsMapMu.RLock()
			for name, pst := range statsMap {
				collectOnce(name, pst)
			}
			statsMapMu.RUnlock()
		}
	}()
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	result := make(map[string][]Sample)
	statsMapMu.RLock()
	for name, pst := range statsMap {
		pst.mu.Lock()
		samples := make([]Sample, len(pst.samples))
		copy(samples, pst.samples)
		pst.mu.Unlock()
		result[name] = samples
	}
	statsMapMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// __METRICS__ is replaced at request time with the JSON array of selected metric names.
const htmlTemplate = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>Process Monitor</title>
  <script src="https://cdn.jsdelivr.net/npm/chart.js@4/dist/chart.umd.min.js"></script>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body { font-family: sans-serif; background: #1a1a2e; color: #eee; padding: 24px; }
    h1 { color: #4dc9f6; margin-bottom: 24px; }
    h2 { color: #aaa; font-size: 1rem; margin-bottom: 12px; }
    .charts { display: grid; grid-template-columns: 1fr 1fr; gap: 24px; }
    .card { background: #16213e; border-radius: 8px; padding: 20px; }
    canvas { width: 100% !important; height: 260px !important; }
  </style>
</head>
<body>
  <h1>Process Monitor</h1>
  <div class="charts" id="charts"></div>
  <script>
    const METRICS = __METRICS__;

    const METRIC_LABELS = {
      cpu:         'CPU (ticks/sec)',
      rss:         'RSS Memory (pages)',
      vsize:       'Virtual Memory (pages)',
      threads:     'Thread Count',
      fds:         'Open File Descriptors',
      read_bytes:  'Disk Read (bytes/sec)',
      write_bytes: 'Disk Write (bytes/sec)',
      minflt:      'Minor Page Faults/sec',
      majflt:      'Major Page Faults/sec',
      ctx_switch:  'Context Switches/sec',
    };

    const PALETTE = ['#4dc9f6','#f67019','#f53794','#acc236','#166a8f','#00a950','#58595b'];

    function makeChart(id) {
      return new Chart(document.getElementById(id), {
        type: 'line',
        data: { datasets: [] },
        options: {
          animation: false,
          parsing: false,
          scales: {
            x: {
              type: 'linear',
              title: { display: true, text: 'seconds ago', color: '#aaa' },
              ticks: { color: '#aaa' },
              grid: { color: '#2a2a4a' }
            },
            y: {
              beginAtZero: true,
              ticks: { color: '#aaa' },
              grid: { color: '#2a2a4a' }
            }
          },
          plugins: { legend: { labels: { color: '#eee' } } }
        }
      });
    }

    // Dynamically create a card + canvas for each metric.
    const chartsDiv = document.getElementById('charts');
    const charts = {};
    METRICS.forEach(metric => {
      const card = document.createElement('div');
      card.className = 'card';
      card.innerHTML = '<h2>' + (METRIC_LABELS[metric] || metric) + '</h2><canvas id="chart_' + metric + '"></canvas>';
      chartsDiv.appendChild(card);
      charts[metric] = makeChart('chart_' + metric);
    });

    let knownProcesses = [];

    function ensureDatasets(processes) {
      processes.forEach((name, i) => {
        if (knownProcesses.indexOf(name) === -1) {
          const color = PALETTE[i % PALETTE.length];
          METRICS.forEach(metric => {
            charts[metric].data.datasets.push({
              label: name, data: [],
              borderColor: color, backgroundColor: color + '33',
              fill: false, tension: 0.2, pointRadius: 2
            });
          });
          knownProcesses.push(name);
        }
      });
    }

    async function poll() {
      try {
        const resp = await fetch('/metrics');
        const data = await resp.json();
        const now = Date.now();
        const processes = Object.keys(data).sort();
        ensureDatasets(processes);

        knownProcesses.forEach((name, i) => {
          const samples = data[name] || [];
          METRICS.forEach(metric => {
            charts[metric].data.datasets[i].data = samples.map(s => ({
              x: (s.t - now) / 1000,
              y: (s.m && s.m[metric] !== undefined) ? s.m[metric] : null
            }));
            charts[metric].update();
          });
        });
      } catch (e) {
        console.error('poll error:', e);
      }
    }

    poll();
    setInterval(poll, 2000);
  </script>
</body>
</html>`

func mainPageHandler(w http.ResponseWriter, r *http.Request) {
	metricsJSON, _ := json.Marshal(selectedMetrics)
	page := strings.Replace(htmlTemplate, "__METRICS__", string(metricsJSON), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page)
}

func main() {
	processesFlag := flag.String("processes", "python2", "Comma-separated list of process names to monitor.")
	metricsFlag := flag.String("metrics", "cpu,rss", metricsHelpText())
	listMetrics := flag.Bool("list-metrics", false, "Print all available metrics and exit.")
	flag.Parse()

	if *listMetrics {
		printMetrics()
		return
	}

	names := strings.Split(*processesFlag, ",")
	for i, n := range names {
		names[i] = strings.TrimSpace(n)
	}

	selectedMetrics = strings.Split(*metricsFlag, ",")
	for i, m := range selectedMetrics {
		selectedMetrics[i] = strings.TrimSpace(m)
	}
	metricsSet = make(map[string]bool)
	for _, m := range selectedMetrics {
		metricsSet[m] = true
	}

	startCollector(names)

	http.HandleFunc("/", mainPageHandler)
	http.HandleFunc("/metrics", metricsHandler)

	fmt.Printf("Monitoring: %v\nMetrics:    %v\nListening on http://localhost:8090\n", names, selectedMetrics)
	http.ListenAndServe(":8090", nil)
}
