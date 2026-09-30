package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/decisions"
)

// JevJob names one of the relevance jobs that use a decision backend. Each is
// a switch in settings (jev.jobs) and a row in /settings while a decision
// backend is configured. A job narrows or reorders what the model sees and
// never approves anything: when its backend is unavailable the deterministic
// behaviour it sits on top of runs unchanged.
type JevJob string

const (
	// JevBashSwap replaces a Bash call with the one builtin tool that does the
	// same job, when one rates above the swap threshold.
	JevBashSwap JevJob = "bash_swap"
)

// JevLSPTriage decides whether another edit pass is likely to clear a file's
// language server errors, and files a board bug when it is not.
const JevLSPTriage JevJob = "lsp_triage"

// JevToolSelection preloads the tools and skills a request is likely to need
// and lists only the relevant skills.
const JevToolSelection JevJob = "tool_selection"

// JevToolSearch merges the ToolSearch keyword match with the backend's ranking.
const JevToolSearch JevJob = "tool_search"

// JevPruneCompaction keeps or clears tool calls and results by relevance when
// the context is compacted, before falling back to a summary.
const JevPruneCompaction JevJob = "prune_compaction"

// JevOptionOrder puts the most likely option first when the model asks the
// user to choose, and marks it (Recommended) when the choice is clear.
const JevOptionOrder JevJob = "option_order"

// JevExploreLocate ranks the files a question is about, before the explore
// subagents start, so they begin where the code is.
const JevExploreLocate JevJob = "explore_locate"

// JevVoiceCommand matches a spoken instruction to one skill, security review,
// crew, supervised process, prompt, agent profile or mode, and runs it only
// when exactly one target rates at or above the voice threshold.
const JevVoiceCommand JevJob = "voice_command"

// JevRequestScale sizes a request as simple or staged, so a simple one skips
// the goal contract, prefetch and verification ceremony and is worked at once.
const JevRequestScale JevJob = "request_scale"

// JevGoalJudge rates a goal pass as complete, partial or not started. A clear
// answer settles the pass; anything else goes to the model judge with the
// scores as a hint.
const JevGoalJudge JevJob = "goal_judge"

// JevJobs lists every shipped job in the order /settings and the docs show
// them. A job is added here in the change that implements it, so /settings
// never offers a switch for work that does not exist.
var JevJobs = []JevJob{
	JevBashSwap,
	JevPruneCompaction,
	JevToolSelection,
	JevToolSearch,
	JevLSPTriage,
	JevOptionOrder,
	JevExploreLocate,
	JevVoiceCommand,
	JevRequestScale,
	JevGoalJudge,
}

// LocatePreview values for jev.locate_previews.
const (
	// LocatePreviewsLocal sends file previews only to a local or self-hosted
	// backend (the default).
	LocatePreviewsLocal = "local"
	// LocatePreviewsHosted also allows the hosted OpenRouter backend.
	LocatePreviewsHosted = "hosted"
	// LocatePreviewsOff sends paths only, never file text.
	LocatePreviewsOff = "off"
)

// JevSettings configures the relevance jobs. All jobs default on; each also
// requires a configured decision backend, so with none they are off and
// hidden. The project layer may turn a job off, never on.
type JevSettings struct {
	// Jobs holds a per-job switch keyed by JevJob. A missing key means on.
	Jobs map[string]bool `json:"jobs,omitempty"`
	// LocatePreviews says where file previews may be sent for explore_locate:
	// "local" (default), "hosted" or "off". User layers only.
	LocatePreviews string `json:"locate_previews,omitempty"`
	// Thresholds overrides the score cut-offs the decision gates and jobs read.
	// Unset keys keep DefaultJevThresholds. User layers only: a project layer's
	// value is dropped.
	Thresholds *JevThresholdSettings `json:"thresholds,omitempty"`
}

// ValidJevJob reports whether name is a job.
func ValidJevJob(name string) bool {
	return slices.Contains(JevJobs, JevJob(name))
}

// ValidateJev checks the jev settings.
func ValidateJev(s Settings) error {
	if s.Jev == nil {
		return nil
	}
	for name := range s.Jev.Jobs {
		if !ValidJevJob(name) {
			return fmt.Errorf("jev.jobs: unknown job %q (want %s)", name, jevJobNames())
		}
	}
	switch s.Jev.LocatePreviews {
	case "", LocatePreviewsLocal, LocatePreviewsHosted, LocatePreviewsOff:
	default:
		return fmt.Errorf("jev.locate_previews %q must be local, hosted or off", s.Jev.LocatePreviews)
	}
	return s.Jev.Thresholds.validate()
}

func jevJobNames() string {
	names := make([]string, len(JevJobs))
	for i, j := range JevJobs {
		names[i] = string(j)
	}
	return strings.Join(names, ", ")
}

// JevConfigured reports whether a decision backend is set as the classifier:
// the local decision model, a self-hosted Jev provider, or OpenRouter's hosted
// Jev model. Without one, no Jev job runs and none is shown in /settings.
func (s Settings) JevConfigured() bool {
	cls := s.Classifier
	if cls == nil || cls.Provider == "" {
		return false
	}
	kind := ""
	if p, ok := s.Providers[cls.Provider]; ok {
		kind = p.Kind
	}
	_, ok := decisions.BackendOf(cls.Provider, kind, cls.Model)
	return ok
}

// JevJobSet reports the job's own switch: true unless the user turned it off.
// It ignores whether a backend is configured; /settings shows this value.
func (s Settings) JevJobSet(job JevJob) bool {
	if s.Jev == nil {
		return true
	}
	on, ok := s.Jev.Jobs[string(job)]
	return !ok || on
}

// JevJobEnabled reports whether the job runs: its switch is on and a decision
// backend is configured.
func (s Settings) JevJobEnabled(job JevJob) bool {
	return s.JevConfigured() && s.JevJobSet(job)
}

// LocatePreviewsMode returns jev.locate_previews, defaulting to "local".
func (s Settings) LocatePreviewsMode() string {
	if s.Jev == nil || s.Jev.LocatePreviews == "" {
		return LocatePreviewsLocal
	}
	return s.Jev.LocatePreviews
}

// mergeJev layers src over dst. When projectLayer is true only the tightening
// moves are taken: a job switched off, and a stricter locate_previews. It
// returns the merged value and whether anything changed.
func mergeJev(dst, src *JevSettings, projectLayer bool) *JevSettings {
	if src == nil {
		return dst
	}
	out := JevSettings{}
	if dst != nil {
		out = *dst
		out.Jobs = maps(dst.Jobs)
	}
	if !projectLayer && src.Thresholds != nil {
		// A repository never sets a threshold: moving a gate is the user's call.
		out.Thresholds = out.Thresholds.overlay(src.Thresholds)
	}
	for name, on := range src.Jobs {
		if projectLayer && on {
			continue // a repository may turn a job off, never on
		}
		if out.Jobs == nil {
			out.Jobs = map[string]bool{}
		}
		out.Jobs[name] = on
	}
	switch {
	case src.LocatePreviews == "":
	case projectLayer:
		// A repository may only narrow where previews go.
		if rankPreviews(src.LocatePreviews) < rankPreviews(out.LocatePreviews) {
			out.LocatePreviews = src.LocatePreviews
		}
	default:
		out.LocatePreviews = src.LocatePreviews
	}
	return &out
}

// rankPreviews orders the modes from most to least permissive; "" is the
// default, local.
func rankPreviews(m string) int {
	switch m {
	case LocatePreviewsHosted:
		return 2
	case LocatePreviewsOff:
		return 0
	}
	return 1
}

func maps(m map[string]bool) map[string]bool {
	if m == nil {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// LocateDestination says where explore_locate would send its questions, and
// whether declaration names from the user's files may go with them. name is
// "" when no decision backend is configured. The rule is the one
// jev.Client.PreviewsAllowed applies: the local decision model and a
// self-hosted server may see declared names, OpenRouter and TypeSafe's hosted
// API see paths only, and jev.locate_previews overrides both ways.
func (s Settings) LocateDestination() (name string, previews bool) {
	cls := s.Classifier
	if cls == nil || cls.Provider == "" {
		return "", false
	}
	kind := ""
	if p, ok := s.Providers[cls.Provider]; ok {
		kind = p.Kind
	}
	backend, ok := decisions.BackendOf(cls.Provider, kind, cls.Model)
	if !ok {
		return "", false
	}
	pref := ""
	if s.Jev != nil {
		pref = s.Jev.LocatePreviews
	}
	local := false
	switch backend {
	case decisions.BackendLocal:
		name, local = "the local decision model", true
	case decisions.BackendSystemOne:
		if cls.Provider == decisions.TypeSafeProvider {
			name = "TypeSafe's hosted API"
		} else {
			name, local = "your self-hosted server "+cls.Provider, true
		}
	default:
		name = "OpenRouter's hosted Jev model"
	}
	switch pref {
	case LocatePreviewsOff:
		return name, false
	case LocatePreviewsHosted:
		return name, true
	}
	return name, local
}
