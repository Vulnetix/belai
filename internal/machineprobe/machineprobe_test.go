package machineprobe

import (
	"strings"
	"testing"
)

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

func TestParseListDevicesEmpty(t *testing.T) {
	if got := parseListDevices("no devices found"); len(got) != 0 {
		t.Fatalf("gpus = %+v, want none", got)
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
}
