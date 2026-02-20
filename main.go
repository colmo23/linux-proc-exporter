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

type Sample struct {
	Timestamp int64 `json:"t"`
	CPU       int   `json:"cpu"`
	VSize     int   `json:"vsize"`
	RSS       int   `json:"rss"`
}

type ProcessStats struct {
	mu          sync.Mutex
	samples     []Sample
	prevCPU     int
	initialized bool
}

var (
	statsMap   = make(map[string]*ProcessStats)
	statsMapMu sync.RWMutex
)

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
		pst.samples = append(pst.samples, Sample{Timestamp: ts})
		if len(pst.samples) > maxSamples {
			pst.samples = pst.samples[len(pst.samples)-maxSamples:]
		}
		pst.mu.Unlock()
		return
	}

	dat, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return
	}
	fields := strings.Fields(string(dat))
	if len(fields) < 15 {
		return
	}
	utime, _ := strconv.Atoi(fields[13])
	ktime, _ := strconv.Atoi(fields[14])
	totalCPU := utime + ktime

	datm, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return
	}
	sm := strings.Fields(string(datm))
	if len(sm) < 2 {
		return
	}
	vsize, _ := strconv.Atoi(sm[0])
	rss, _ := strconv.Atoi(sm[1])

	pst.mu.Lock()
	var cpuDelta int
	if pst.initialized {
		cpuDelta = totalCPU - pst.prevCPU
	}
	pst.prevCPU = totalCPU
	pst.initialized = true
	pst.samples = append(pst.samples, Sample{
		Timestamp: ts,
		CPU:       cpuDelta,
		VSize:     vsize,
		RSS:       rss,
	})
	if len(pst.samples) > maxSamples {
		pst.samples = pst.samples[len(pst.samples)-maxSamples:]
	}
	pst.mu.Unlock()
}

func startCollector(names []string) {
	statsMapMu.Lock()
	for _, name := range names {
		statsMap[name] = &ProcessStats{}
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

const htmlPage = `<!DOCTYPE html>
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
  <div class="charts">
    <div class="card">
      <h2>CPU (ticks/sec)</h2>
      <canvas id="cpuChart"></canvas>
    </div>
    <div class="card">
      <h2>Memory RSS (pages)</h2>
      <canvas id="rssChart"></canvas>
    </div>
  </div>
  <script>
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

    const cpuChart = makeChart('cpuChart');
    const rssChart = makeChart('rssChart');

    let knownProcesses = [];

    function ensureDatasets(processes) {
      processes.forEach((name, i) => {
        if (knownProcesses.indexOf(name) === -1) {
          const color = PALETTE[i % PALETTE.length];
          cpuChart.data.datasets.push({ label: name, data: [], borderColor: color, backgroundColor: color + '33', fill: false, tension: 0.2, pointRadius: 2 });
          rssChart.data.datasets.push({ label: name, data: [], borderColor: color, backgroundColor: color + '33', fill: false, tension: 0.2, pointRadius: 2 });
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
          const cpuPts = samples.map(s => ({ x: (s.t - now) / 1000, y: s.cpu }));
          const rssPts = samples.map(s => ({ x: (s.t - now) / 1000, y: s.rss }));
          cpuChart.data.datasets[i].data = cpuPts;
          rssChart.data.datasets[i].data = rssPts;
        });

        cpuChart.update();
        rssChart.update();
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, htmlPage)
}

func main() {
	processesFlag := flag.String("processes", "python2", "Comma-separated list of process names to monitor.")
	flag.Parse()

	names := strings.Split(*processesFlag, ",")
	for i, n := range names {
		names[i] = strings.TrimSpace(n)
	}

	startCollector(names)

	http.HandleFunc("/", mainPageHandler)
	http.HandleFunc("/metrics", metricsHandler)

	fmt.Printf("Monitoring: %v\nListening on http://localhost:8090\n", names)
	http.ListenAndServe(":8090", nil)
}
