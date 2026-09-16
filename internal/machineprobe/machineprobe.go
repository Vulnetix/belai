// Package machineprobe measures the host machine and produces a plain-language
// suitability verdict for running a local inference server. It is honest about
// the prefill bottleneck of integrated GPUs: an iGPU shares LPDDR bandwidth
// with the CPU, so a large tool result (a long prefill) may classify slower
// than a small frontier model despite plenty of free VRAM.
package machineprobe

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// GPU describes one usable accelerator backend.
type GPU struct {
	Backend string // "vulkan", "cuda", "rocm", "metal", …
	Name    string
	VRAMMiB int64 // free when reported, else total
}

// Report is the raw measurement result.
type Report struct {
	CPUs         int
	RAMTotalMiB  int64
	RAMFreeMiB   int64
	DiskFreeMiB  int64
	GPUs         []GPU
	ServerBinary string // llama-server path when present, else ""
}

// Verdict is the plain-language suitability assessment for one candidate model.
type Verdict struct {
	Suitable bool
	Reason   string
}

// Probe measures the host without shelling out beyond a best-effort
// `llama-server --list-devices` GPU probe. Missing values read as 0 and never
// fail the probe.
func Probe(ctx context.Context) Report {
	r := Report{CPUs: runtime.NumCPU()}
	r.RAMTotalMiB, r.RAMFreeMiB = memoryMiB()
	r.DiskFreeMiB = diskFreeMiB()
	r.GPUs = probeGPUs(ctx)
	r.ServerBinary = serverBinary()
	return r
}

// Assess returns the suitability verdict for a model of modelSizeMiB against
// the report. It states the iGPU prefill caveat honestly when the only GPU is
// integrated/shared-memory.
func (r Report) Assess(modelName string, modelSizeMiB int64) Verdict {
	if modelSizeMiB <= 0 {
		return Verdict{Suitable: false, Reason: "model size unknown; measure the download first"}
	}

	var gpuFree int64
	var shared bool
	for _, g := range r.GPUs {
		if g.VRAMMiB > gpuFree {
			gpuFree = g.VRAMMiB
		}
		shared = shared || isSharedBackend(g.Backend)
	}
	if gpuFree > 0 {
		if modelSizeMiB <= gpuFree {
			reason := fmt.Sprintf("%s (~%d MiB) fits the %d MiB GPU budget and will run", modelName, modelSizeMiB, gpuFree)
			if shared {
				reason += ". The GPU is integrated and shares LPDDR bandwidth with the CPU, so prefill — the security classifier's bottleneck for large tool results — will be the limiting factor; large payloads may classify slower than a small frontier model. The chunked classifier keeps chunks small so they classify concurrently against the local server."
			}
			return Verdict{Suitable: true, Reason: reason}
		}
		return Verdict{Suitable: false, Reason: fmt.Sprintf("%s (~%d MiB) does not fit the %d MiB GPU budget; it would spill to CPU and be unusably slow", modelName, modelSizeMiB, gpuFree)}
	}
	if modelSizeMiB <= r.RAMFreeMiB {
		return Verdict{Suitable: true, Reason: fmt.Sprintf("no GPU detected; %s (~%d MiB) fits the %d MiB free RAM and would run CPU-only (slow, but correct)", modelName, modelSizeMiB, r.RAMFreeMiB)}
	}
	return Verdict{Suitable: false, Reason: fmt.Sprintf("%s (~%d MiB) exceeds the %d MiB free RAM and no GPU is available", modelName, modelSizeMiB, r.RAMFreeMiB)}
}

// isSharedBackend reports whether a GPU backend shares system memory with the
// CPU (integrated graphics), where prefill bandwidth is the bottleneck.
func isSharedBackend(backend string) bool {
	switch strings.ToLower(backend) {
	case "vulkan", "metal":
		// Vulkan here is the Radeon 780M iGPU path; Metal covers Apple unified
		// memory. Both are shared-memory in practice for integrated parts.
		return true
	default:
		return false
	}
}

func serverBinary() string {
	if p, err := exec.LookPath("llama-server"); err == nil {
		return p
	}
	return ""
}

// probeGPUs runs `llama-server --list-devices` when the binary is present.
// The output is best-effort: an error or unparseable output yields no GPUs.
func probeGPUs(ctx context.Context) []GPU {
	bin := serverBinary()
	if bin == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "--list-devices").CombinedOutput()
	if err != nil {
		return nil
	}
	return parseListDevices(string(out))
}

var miBRe = regexp.MustCompile(`(\d+)\s*MiB`)

// parseListDevices extracts one GPU per line that names a backend and carries a
// MiB figure. When a line reports both total and free, the smaller (free)
// figure wins: that is the honest budget for an additional model.
func parseListDevices(out string) []GPU {
	var gpus []GPU
	for _, line := range strings.Split(out, "\n") {
		lower := strings.ToLower(line)
		backend := ""
		for _, b := range []string{"vulkan", "cuda", "rocm", "metal", "opencl", "hip", "sycl"} {
			if strings.Contains(lower, b) {
				backend = b
				break
			}
		}
		if backend == "" {
			continue
		}
		var minMiB int64
		for _, m := range miBRe.FindAllStringSubmatch(line, -1) {
			v, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				continue
			}
			if minMiB == 0 || v < minMiB {
				minMiB = v
			}
		}
		if minMiB <= 0 {
			continue
		}
		gpus = append(gpus, GPU{Backend: backend, Name: strings.TrimSpace(line), VRAMMiB: minMiB})
	}
	return gpus
}

func memoryMiB() (total, free int64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	vals := map[string]int64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		vals[strings.TrimSuffix(fields[0], ":")] = kb
	}
	if a, ok := vals["MemAvailable"]; ok {
		return vals["MemTotal"] / 1024, a / 1024
	}
	return vals["MemTotal"] / 1024, (vals["MemFree"] + vals["Buffers"] + vals["Cached"]) / 1024
}
