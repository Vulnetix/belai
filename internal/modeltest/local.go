package modeltest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/decisionserver"
	"github.com/vulnetix/belai/internal/localinfer"
)

// ensureHFWeights makes sure repo/file is on disk, downloading it after the
// user confirms. It prefers belai's own resumable, checksum-verified HTTPS
// download and falls back to the Hugging Face CLI when that fails for a
// network reason and a genuine CLI is installed.
func ensureHFWeights(ctx context.Context, st *State, label, repo, file string, known localinfer.RemoteFile) Outcome {
	if p := localinfer.FindModelFile(repo, file); p != "" {
		st.ModelPath = p
		return ok("%s is on disk", file)
	}
	info := known
	if ri, err := localinfer.RemoteInfo(ctx, st.Env.Client, repo, file, st.Env.HFToken); err == nil {
		info = ri
	} else if o, stop := downloadFailure(err, repo); stop {
		return o
	}
	dest, _ := localinfer.ModelsDir()
	if !st.confirm(ctx, DownloadOffer{Label: label, What: repo + " · " + file, Size: info.Size, Dest: dest}) {
		if ctx.Err() != nil {
			return fail("cancelled")
		}
		return fail(fmt.Sprintf("download of %s (%s) declined, so %s was not saved", file, Sizes(info.Size), label),
			Hint{Key: "r", Text: "ask again", Action: ActRetry})
	}
	var path string
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		path, err = localinfer.DownloadFile(ctx, st.Env.Client, repo, file, st.Env.HFToken, info, st.progress)
		var de *localinfer.DownloadError
		if err == nil || ctx.Err() != nil || !errors.As(err, &de) || de.Class != localinfer.DownloadChecksum {
			break
		}
		st.log("the download failed its checksum and was removed; downloading again")
	}
	if err != nil && ctx.Err() == nil {
		var de *localinfer.DownloadError
		if errors.As(err, &de) && de.Class == localinfer.DownloadNetwork {
			if bin, ok := localinfer.HFCLI(); ok {
				st.log("direct download failed (" + oneLine(err.Error(), 120) + "); trying the Hugging Face CLI")
				path, err = hfCLIDownload(ctx, st, bin, repo, file, info.Size)
			}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return fail("download cancelled; it resumes where it stopped next time")
		}
		o, _ := downloadFailure(err, repo)
		return o
	}
	st.ModelPath = path
	return ok("downloaded %s (%s)", file, Sizes(info.Size))
}

// hfCLIDownload runs the Hugging Face CLI, reporting progress from the size
// of its partial file.
func hfCLIDownload(ctx context.Context, st *State, bin, repo, file string, total int64) (string, error) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				st.progress(localinfer.HubPartialSize(repo), total)
			}
		}
	}()
	defer close(done)
	return localinfer.HFDownloadFile(ctx, bin, repo, file, st.Env.HFToken, st.log)
}

// downloadFailure maps a download error to an outcome. stop is false for an
// error the caller may carry on past (a metadata lookup that failed for a
// network reason; the download itself will say more).
func downloadFailure(err error, repo string) (Outcome, bool) {
	var de *localinfer.DownloadError
	if !errors.As(err, &de) {
		return fail("download failed: "+oneLine(err.Error(), 160), retryHint()), true
	}
	switch de.Class {
	case localinfer.DownloadAuth:
		return fail("Hugging Face refused access to "+repo, hfTokenHint(), retryHint()), true
	case localinfer.DownloadNotFound:
		return fail(oneLine(de.Error(), 160), Hint{Text: "the repo or file may have been renamed; update belai for the current catalogue"}), true
	case localinfer.DownloadDisk:
		return fail("not enough disk space or the models directory is not writable: "+oneLine(de.Error(), 120),
			Hint{Text: "free some space (set BELAI_MODELS_DIR to use another disk)"}, retryHint()), true
	case localinfer.DownloadChecksum:
		return fail("the download failed its SHA-256 check twice", retryHint()), true
	case localinfer.DownloadRate:
		return fail("Hugging Face is rate-limiting downloads", hfTokenHint(), retryHint()), true
	}
	return fail("download failed: "+oneLine(de.Error(), 160), retryHint()), false
}

// DecisionLocalSteps is the ladder for a local decision model.
func DecisionLocalSteps(m decisions.LocalModel, timeout time.Duration, maxState int) []Step {
	var d *decisions.Llama
	return []Step{
		{Name: "llama-server", Run: func(ctx context.Context, st *State) Outcome {
			bin, found := localinfer.LlamaServer()
			if !found {
				return fail("llama-server is not on PATH; the local decision model runs in it", installLlamaHint(), retryHint())
			}
			build, line := localinfer.Version(ctx, bin)
			_ = line
			if m.MinBuild > 0 && build > 0 && build < m.MinBuild {
				return fail(fmt.Sprintf("llama-server build %d is too old for %s, which needs build %d or later", build, m.Label, m.MinBuild), upgradeLlamaHint(), retryHint())
			}
			if build == 0 {
				return ok("found %s", bin.Path)
			}
			return ok("llama-server build %d", build)
		}},
		{Name: "weights", Run: func(ctx context.Context, st *State) Outcome {
			return ensureHFWeights(ctx, st, m.Label, m.Repo, m.File, localinfer.RemoteFile{Size: m.SizeBytes})
		}},
		{Name: "start", Run: func(ctx context.Context, st *State) Outcome {
			h, o := startDecisionServer(ctx, st, m)
			if h != nil {
				st.Handle = h
				d = &decisions.Llama{BaseURL: h.BaseURL, Model: m, Client: st.Env.Client, Timeout: timeout, MaxStateBytes: maxState}
			}
			return o
		}},
		{Name: "answer", Run: func(ctx context.Context, st *State) Outcome {
			res, err := d.Decide(ctx, decisions.Request{
				State:     "nothing to commit, working tree clean",
				Questions: map[string]decisions.Question{"q": decisions.Noul(injectionProposition)},
			})
			if err != nil {
				if decisions.ClassOf(err) == decisions.ClassUnavailable && strings.Contains(err.Error(), "did not land") {
					return fail("the model's answer did not land on the option letters, so this llama-server build and model disagree on the prompt",
						upgradeLlamaHint(), Hint{Key: "D", Text: "download the weights again", Action: ActRedownload}, retryHint())
				}
				return fail("no answer: "+oneLine(err.Error(), 160), retryHint())
			}
			o := ok("answered in %s (letter mass %.2f)", res.Meta.Latency.Round(time.Millisecond), res.Meta.LetterMass)
			if res.Meta.LetterMass < 0.2 {
				o = warn(fmt.Sprintf("answered, but only %.0f%% of the model's probability was on the answer letters; its confidence is less reliable", res.Meta.LetterMass*100))
			}
			o.Metrics = map[string]string{"latency": res.Meta.Latency.String()}
			return o
		}},
		sanityStep(func() decisions.Decider { return d }),
		intentStep(func() decisions.Decider { return d }),
		{Name: "speed", Run: func(ctx context.Context, st *State) Outcome {
			long := strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }\n", 40)
			start := time.Now()
			_, err := d.Decide(ctx, decisions.Request{State: long, Questions: map[string]decisions.Question{"q": decisions.Noul(injectionProposition)}})
			el := time.Since(start)
			limit := timeout
			if limit <= 0 {
				limit = decisions.LocalTimeout
			}
			if err != nil {
				return warn(fmt.Sprintf("a ~800-token tool result took over %s; results that long are checked by the agent model instead", limit.Round(time.Second)), gpuHint())
			}
			if el > limit/2 {
				return warn(fmt.Sprintf("a ~800-token tool result took %s; longer results will be checked by the agent model", el.Round(100*time.Millisecond)), gpuHint())
			}
			return ok("a ~800-token tool result took %s", el.Round(100*time.Millisecond))
		}},
	}
}

// startDecisionServer finds or launches the server, relaunching on the CPU
// when a GPU backend fails.
func startDecisionServer(ctx context.Context, st *State, m decisions.LocalModel) (*decisionserver.Handle, Outcome) {
	opts := decisionserver.Options{
		Registry: st.Env.Registry, OnLine: st.log, Deadline: st.Env.LocalDeadline,
		NGL: st.Env.NGL, ModelPath: st.ModelPath, Client: st.Env.Client,
	}
	h, err := decisionserver.Ensure(ctx, m, opts)
	if err == nil {
		if h.Owned {
			return h, ok("started on port %d", h.Port)
		}
		return h, ok("using the running server on port %d", h.Port)
	}
	var le *localinfer.LaunchError
	if errors.As(err, &le) && (le.Class == localinfer.LogGPU || le.Class == localinfer.LogOOM) && opts.NGL != 0 {
		st.log("the GPU backend failed; starting again with the model on the CPU")
		opts.NGL = 0
		if h, err2 := decisionserver.Ensure(ctx, m, opts); err2 == nil {
			return h, warn(fmt.Sprintf("started on port %d on the CPU after the GPU backend failed", h.Port), gpuHint())
		} else {
			err = err2
		}
	}
	return nil, launchFailure(err)
}

func launchFailure(err error) Outcome {
	if errors.Is(err, decisionserver.ErrNoBinary) {
		return fail("llama-server is not on PATH", installLlamaHint(), retryHint())
	}
	if errors.Is(err, decisionserver.ErrWeightsMissing) {
		return fail("the model file is missing", retryHint())
	}
	if errors.Is(err, decisionserver.ErrUpgrade) {
		return fail(oneLine(err.Error(), 160), upgradeLlamaHint(), retryHint())
	}
	var le *localinfer.LaunchError
	if errors.As(err, &le) {
		tail := logTail(le.Output, 2)
		switch le.Class {
		case localinfer.LogUpgrade:
			return fail("this llama-server build does not know the model's architecture: "+tail, upgradeLlamaHint(), retryHint())
		case localinfer.LogCorrupt:
			return fail("llama-server could not read the model file: "+tail,
				Hint{Key: "D", Text: "delete belai's copy and download it again", Action: ActRedownload}, upgradeLlamaHint())
		case localinfer.LogOOM:
			return fail("the model does not fit in memory: "+tail, Hint{Text: "close other large programs and retry"}, retryHint())
		case localinfer.LogGPU:
			return fail("the GPU backend failed, on the CPU too: "+tail, Hint{Key: "L", Text: "retry with the model on the CPU", Action: ActCPU}, retryHint())
		}
		if le.ExitCode >= 0 {
			return fail(fmt.Sprintf("llama-server exited during startup (code %d): %s", le.ExitCode, tail), retryHint())
		}
		return fail("llama-server did not finish loading in time: "+tail,
			Hint{Text: "a first load from a slow disk can take a while; retry"}, retryHint())
	}
	return fail("could not start the decision server: "+oneLine(err.Error(), 160), retryHint())
}

func logTail(out string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 0 {
		return "no output"
	}
	return oneLine(strings.Join(lines, " · "), 220)
}

// EnsureOllamaModel is the weights step for an ollama model: present per
// /api/show, else pulled through /api/pull after the user confirms.
func EnsureOllamaModel(root, model string) Step {
	return Step{Name: "weights", Run: func(ctx context.Context, st *State) Outcome {
		root = strings.TrimSuffix(strings.TrimRight(root, "/"), "/v1")
		code, err := postJSON(ctx, st.Env.Client, root+"/api/show", map[string]any{"model": model}, nil)
		if err != nil {
			return fail("ollama is not answering at "+root, installOllamaHint(), retryHint())
		}
		if code == http.StatusOK {
			return ok("%s is pulled", model)
		}
		if code != http.StatusNotFound {
			return fail(fmt.Sprintf("ollama answered HTTP %d for %s", code, model), retryHint())
		}
		if !st.confirm(ctx, DownloadOffer{Label: model, What: "ollama pull " + model, Dest: "ollama's model store"}) {
			if ctx.Err() != nil {
				return fail("cancelled")
			}
			return fail("pull of "+model+" declined, so it was not saved", Hint{Key: "r", Text: "ask again", Action: ActRetry})
		}
		return ollamaPull(ctx, st, root, model)
	}}
}

func ollamaPull(ctx context.Context, st *State, root, model string) Outcome {
	raw, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, root+"/api/pull", bytes.NewReader(raw))
	if err != nil {
		return fail(err.Error())
	}
	resp, err := st.Env.Client.Do(req)
	if err != nil {
		return fail("ollama stopped answering during the pull", retryHint())
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var last string
	for sc.Scan() {
		var ev struct {
			Status    string `json:"status"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Error != "" {
			return fail("ollama pull failed: "+oneLine(ev.Error, 160), Hint{Text: "check the model name on ollama.com/library"}, retryHint())
		}
		if ev.Total > 0 {
			st.progress(ev.Completed, ev.Total)
		}
		last = ev.Status
	}
	if ctx.Err() != nil {
		return fail("pull cancelled; ollama resumes it next time")
	}
	if last != "success" {
		return fail("ollama pull ended without success ("+oneLine(last, 60)+")", retryHint())
	}
	return ok("pulled %s", model)
}

func postJSON(ctx context.Context, c *http.Client, url string, body any, out any) (int, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

// LlamaChatSteps are the local steps for a chat model on llama-server: when
// the server is not running and the model id names a Hugging Face GGUF repo
// ("org/repo" or "org/repo:QUANT"), download it after confirmation and
// launch it; otherwise the server must already be running.
func LlamaChatSteps(baseURL, model string, port int) []Step {
	return []Step{{Name: "server", Run: func(ctx context.Context, st *State) Outcome {
		if localinfer.ProbeRunning(ctx, []string{baseURL}) != "" {
			return ok("llama-server is answering at %s", strings.TrimSuffix(baseURL, "/v1"))
		}
		repo, quant, isRepo := hfModelID(model)
		if !isRepo {
			return fail("llama-server is not running at "+strings.TrimSuffix(baseURL, "/v1"),
				providersHint("launch a local model from providers → llama-server"), retryHint())
		}
		bin, found := localinfer.LlamaServer()
		if !found {
			return fail("llama-server is not on PATH", installLlamaHint(), retryHint())
		}
		files, err := localinfer.RepoGGUFs(ctx, st.Env.Client, repo, st.Env.HFToken)
		if err != nil {
			o, _ := downloadFailure(err, repo)
			return o
		}
		f, found := localinfer.PickGGUF(files, quant)
		if !found {
			return fail(fmt.Sprintf("%s has no %s GGUF", repo, quant), Hint{Text: "name the quantisation after a colon, for example " + repo + ":Q8_0"})
		}
		if o := ensureHFWeights(ctx, st, repo, repo, f.Name, f.RemoteFile); o.Status == StatusFail {
			return o
		}
		args := localinfer.Args(localinfer.ArgsOptions{ModelPath: st.ModelPath, Port: port})
		stop, err := localinfer.Launch(ctx, bin, args, baseURL, localinfer.LaunchOptions{
			Deadline: st.Env.LocalDeadline, Registry: st.Env.Registry, OnLine: st.log,
			OnPort: func(p int) { port = p },
		})
		if err != nil {
			return launchFailure(err)
		}
		st.ChatServer = &LaunchedServer{Port: port, Stop: stop}
		return ok("started %s on port %d", f.Name, port)
	}}}
}

// hfModelID splits "org/repo[:QUANT]"; the quant defaults to Q4_K_M.
func hfModelID(id string) (repo, quant string, ok bool) {
	repo, quant, _ = strings.Cut(id, ":")
	if quant == "" {
		quant = "Q4_K_M"
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(repo, " \\") {
		return "", "", false
	}
	return repo, quant, true
}
