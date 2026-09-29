package localinfer

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LogClass names what a server's startup output says went wrong, so a caller
// can offer a specific fix instead of the raw log.
type LogClass string

const (
	// LogUnknown is output with no recognised failure.
	LogUnknown LogClass = ""
	// LogUpgrade is a llama.cpp build too old for the model's architecture.
	LogUpgrade LogClass = "upgrade"
	// LogCorrupt is a model file that is truncated or not a GGUF.
	LogCorrupt LogClass = "corrupt"
	// LogOOM is a model that does not fit in memory.
	LogOOM LogClass = "oom"
	// LogGPU is a GPU backend failure; a CPU-only relaunch may work.
	LogGPU LogClass = "gpu"
	// LogPortBusy is a bind failure on the chosen port.
	LogPortBusy LogClass = "port"
)

// ClassifyLog maps llama-server startup output to a failure class. The
// patterns are the messages llama.cpp prints for each case; anything else is
// LogUnknown and the caller shows the log tail.
func ClassifyLog(output string) LogClass {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "unknown model architecture"),
		strings.Contains(lower, "unsupported model architecture"):
		return LogUpgrade
	case strings.Contains(lower, "invalid magic"),
		strings.Contains(lower, "failed to read magic"),
		strings.Contains(lower, "tensor data is not within"),
		strings.Contains(lower, "gguf_init_from_file"):
		return LogCorrupt
	case strings.Contains(lower, "failed to allocate"),
		strings.Contains(lower, "out of memory"),
		strings.Contains(lower, "cannot allocate memory"):
		return LogOOM
	case strings.Contains(lower, "cuda error"),
		strings.Contains(lower, "ggml_vulkan"),
		strings.Contains(lower, "vk::"),
		strings.Contains(lower, "ggml_metal"),
		strings.Contains(lower, "hip error"):
		return LogGPU
	case isAddrInUse(output):
		return LogPortBusy
	case strings.Contains(lower, "failed to load model"),
		strings.Contains(lower, "error loading model"):
		return LogCorrupt
	}
	return LogUnknown
}

// DecisionServerOptions shapes the argv of a llama-server that serves a
// decision model rather than chat. The argv is fixed by the harness: loopback
// only, the model by path, the alias the catalogue names.
type DecisionServerOptions struct {
	ModelPath string
	Alias     string
	Port      int
	// NGL is the number of layers offloaded to a GPU. Negative means "all"
	// (99); zero keeps the model on the CPU, which is the fallback when a GPU
	// backend fails to start.
	NGL     int
	CtxSize int
}

// DecisionArgs returns the llama-server argv for a decision model. Unlike
// Args it sets no chat template (--jinja) and no sampling defaults: a
// decision call reads the logits of one position and sends its own sampling
// fields. One slot keeps the prompt cache for the state a series of questions
// shares, and the context is sized for tool results, not conversations.
func DecisionArgs(o DecisionServerOptions) []string {
	ctx := o.CtxSize
	if ctx <= 0 {
		ctx = 8192
	}
	ngl := o.NGL
	if ngl < 0 {
		ngl = 99
	}
	port := o.Port
	if port <= 0 {
		port = 8080
	}
	args := []string{"-m", o.ModelPath}
	if o.Alias != "" {
		args = append(args, "--alias", o.Alias)
	}
	return append(args,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"-np", "1",
		"--ctx-size", strconv.Itoa(ctx),
		"--cache-reuse", "256",
		"--n-gpu-layers", strconv.Itoa(ngl),
	)
}

var buildRe = regexp.MustCompile(`build[: ]+(\d+)`)

// Version returns llama-server's build number (0 when it cannot be read) and
// the first line of its version output.
func Version(ctx context.Context, bin Binary) (int, string) {
	if bin.Path == "" {
		return 0, ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin.Path, "--version").CombinedOutput()
	text := strings.TrimSpace(string(out))
	first := text
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "version") || strings.Contains(line, "build") {
			first = strings.TrimSpace(line)
			break
		}
	}
	if m := buildRe.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, first
	}
	return 0, first
}

// LlamaServer finds the llama-server binary on PATH. Decision models need
// llama-server specifically: they are read through its raw /completion
// endpoint, which ollama and vllm do not offer in the same shape.
func LlamaServer() (Binary, bool) {
	p, err := exec.LookPath("llama-server")
	if err != nil {
		return Binary{}, false
	}
	return Binary{Name: "llama-server", Path: p}, true
}
