package machineprobe

import (
	"context"
	"strings"
	"testing"
)

func TestProbeDoesNotFail(t *testing.T) {
	// Probe is best-effort: missing values read as 0 and must never panic or
	// error, regardless of whether llama-server is installed or /proc exists.
	r := Probe(context.Background())
	if r.CPUs <= 0 {
		t.Fatalf("Probe.CPUs = %d, want > 0", r.CPUs)
	}
	if r.RAMTotalMiB < 0 || r.RAMFreeMiB < 0 || r.DiskFreeMiB < 0 {
		t.Fatalf("Probe returned negative memory/disk: %+v", r)
	}
	// GPUs and server binary are optional; everything else must be populated.
	_ = r.GPUs
	_ = r.ServerBinary
}

func TestParseListDevices(t *testing.T) {
	out := "device 0: Vulkan0, total 34046 MiB, free 29082 MiB\n" +
		"device 1: CUDA0: 12288 MiB\n" +
		"no backend here\n"
	gpus := parseListDevices(out)
	if len(gpus) != 2 {
		t.Fatalf("gpus = %+v, want 2", gpus)
	}
	if gpus[0].Backend != "vulkan" || gpus[0].VRAMMiB != 29082 {
		t.Fatalf("vulkan gpu = %+v, want backend vulkan free 29082", gpus[0])
	}
	if gpus[1].Backend != "cuda" || gpus[1].VRAMMiB != 12288 {
		t.Fatalf("cuda gpu = %+v, want 12288", gpus[1])
	}
}

func TestParseListDevicesAllBackends(t *testing.T) {
	// One line per backend keyword: each must be recognised and pick the
	// smaller (free) MiB figure when both total and free are reported.
	lines := []struct {
		line    string
		backend string
		vram    int64
	}{
		{"device: Vulkan0 total 8192 MiB free 4096 MiB", "vulkan", 4096},
		{"device: CUDA0 16384 MiB", "cuda", 16384},
		{"device: ROCm 12288 MiB", "rocm", 12288},
		{"device: Metal 65536 MiB", "metal", 65536},
		{"device: OpenCL 2048 MiB", "opencl", 2048},
		{"device: HIP 3072 MiB", "hip", 3072},
		{"device: SYCL 1024 MiB", "sycl", 1024},
	}
	var out string
	for _, l := range lines {
		out += l.line + "\n"
	}
	gpus := parseListDevices(out)
	if len(gpus) != len(lines) {
		t.Fatalf("gpus = %d, want %d", len(gpus), len(lines))
	}
	for i, want := range lines {
		if gpus[i].Backend != want.backend || gpus[i].VRAMMiB != want.vram {
			t.Fatalf("gpus[%d] = %+v, want backend %q vram %d", i, gpus[i], want.backend, want.vram)
		}
	}
}

func TestParseListDevicesEmpty(t *testing.T) {
	if got := parseListDevices("no devices found"); len(got) != 0 {
		t.Fatalf("gpus = %+v, want none", got)
	}
}

func TestIsSharedBackend(t *testing.T) {
	for _, b := range []string{"vulkan", "Vulkan", "METAL"} {
		if !isSharedBackend(b) {
			t.Errorf("isSharedBackend(%q) = false, want true", b)
		}
	}
	for _, b := range []string{"cuda", "rocm", "opencl", ""} {
		if isSharedBackend(b) {
			t.Errorf("isSharedBackend(%q) = true, want false", b)
		}
	}
}

func TestAssessUnknownSize(t *testing.T) {
	r := Report{GPUs: []GPU{{Backend: "cuda", VRAMMiB: 16000}}}
	if v := r.Assess("model", 0); v.Suitable {
		t.Fatalf("verdict should be unsuitable for unknown size, got %+v", v)
	}
}

func TestAssessSharedGPUHonestCaveat(t *testing.T) {
	r := Report{
		GPUs: []GPU{{Backend: "vulkan", VRAMMiB: 29082}},
	}
	v := r.Assess("qwen-12b", 7000)
	if !v.Suitable {
		t.Fatalf("verdict should be suitable, got %+v", v)
	}
	if !strings.Contains(v.Reason, "prefill") || !strings.Contains(v.Reason, "integrated") {
		t.Fatalf("verdict must state the prefill caveat honestly: %q", v.Reason)
	}
}

func TestAssessTooLarge(t *testing.T) {
	r := Report{GPUs: []GPU{{Backend: "vulkan", VRAMMiB: 8000}}}
	if v := r.Assess("huge", 30000); v.Suitable {
		t.Fatalf("verdict should be unsuitable, got %+v", v)
	}
}

func TestAssessCPUOnly(t *testing.T) {
	r := Report{RAMFreeMiB: 30000}
	if v := r.Assess("small", 6000); !v.Suitable || !strings.Contains(v.Reason, "CPU-only") {
		t.Fatalf("cpu-only verdict = %+v", v)
	}
	// Too big for RAM with no GPU.
	if v := r.Assess("huge", 60000); v.Suitable {
		t.Fatalf("verdict should be unsuitable, got %+v", v)
	}
}

func TestAssessDiscreteGPUFits(t *testing.T) {
	// A discrete (non-shared) GPU must not emit the integrated caveat.
	r := Report{GPUs: []GPU{{Backend: "cuda", VRAMMiB: 16000}}}
	v := r.Assess("model", 8000)
	if !v.Suitable {
		t.Fatalf("verdict should be suitable, got %+v", v)
	}
	if strings.Contains(v.Reason, "integrated") {
		t.Fatalf("discrete GPU verdict must not mention integrated graphics: %q", v.Reason)
	}
}
