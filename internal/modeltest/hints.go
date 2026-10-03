package modeltest

import (
	"runtime"

	"github.com/vulnetix/belai/internal/deciderserver"
)

func retryHint() Hint {
	return Hint{Key: "r", Text: "run the test again", Action: ActRetry}
}

func providersHint(text string) Hint {
	return Hint{Key: "p", Text: text, Action: ActProviders}
}

// installLlamaHint is how to get llama-server on this OS.
func installLlamaHint() Hint {
	switch runtime.GOOS {
	case "darwin":
		return Hint{Text: "install llama.cpp: brew install llama.cpp"}
	case "windows":
		return Hint{Text: "install llama.cpp: winget install llama.cpp (or scoop install llama.cpp)"}
	}
	return Hint{Text: "install llama.cpp from your distribution (for example pacman -S llama.cpp or apt install llama.cpp) or a ggml-org/llama.cpp release build, so llama-server is on PATH"}
}

func upgradeLlamaHint() Hint {
	switch runtime.GOOS {
	case "darwin":
		return Hint{Text: "update llama.cpp: brew upgrade llama.cpp"}
	case "windows":
		return Hint{Text: "update llama.cpp: winget upgrade llama.cpp"}
	}
	return Hint{Text: "update llama.cpp to a recent build; this model needs Qwen3.5 support"}
}

func installDeciderHint() Hint {
	return Hint{Text: "install the server: " + deciderserver.InstallHint + ", so strands-decider is on PATH"}
}

func installOllamaHint() Hint {
	return Hint{Text: "start ollama (ollama serve) or install it from ollama.com"}
}

func hfTokenHint() Hint {
	return providersHint("add a Hugging Face token under providers → huggingface (only needed for gated repos)")
}

func gpuHint() Hint {
	return Hint{Text: "a GPU build of llama.cpp answers in tens of milliseconds; on a CPU, long tool results are checked by the agent model instead"}
}
