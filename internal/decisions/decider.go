package decisions

import "strings"

// DeciderProvider is the built-in decision provider for Strands Decider-2B
// run on this machine by its own server (`strands-decider serve`), which
// speaks the /v1/systemone API. Like typesafe it is classifier-only: never a
// chat provider, never in the provider registry, never routed through a
// firewall. internal/deciderserver finds or starts the server.
const DeciderProvider = "strands-decider"

// DeciderFile is one file a decider checkpoint needs on disk, pinned by
// revision, size and SHA-256 so a changed upstream file is refused rather
// than served.
type DeciderFile struct {
	// Path is the file's path in its repository, and under the directory it
	// is placed in.
	Path   string
	Size   int64
	SHA256 string
	// LFS is set for files Hugging Face stores in LFS, whose SHA-256 the Hub
	// API reports; the download checks that report against SHA256 too.
	LFS bool
}

// DeciderModel is one Strands Decider checkpoint: an adapter and a readout
// head (Repo) over a base model (BaseRepo) the server loads by name.
type DeciderModel struct {
	ID    string // settings id: classifier.model
	Label string
	Repo  string
	// Revision pins Repo; BaseRevision pins BaseRepo.
	Revision     string
	BaseRepo     string
	BaseRevision string
	// Checkpoint files are placed in the checkpoint directory the server is
	// given; Base files are placed as a Hugging Face hub snapshot of
	// BaseRepo at BaseRevision, which the server reads offline.
	Checkpoint []DeciderFile
	Base       []DeciderFile
	// MaxOptions is the number of option slots the head reads; a question
	// with more options is never sent.
	MaxOptions int
	// RemoteIDs are the substrings that identify this model in a remote
	// host's catalogue (OpenRouter's model list) or a server's /health.
	RemoteIDs []string
	Blurb     string
}

// Size is the bytes a full download of m takes on disk.
func (m DeciderModel) Size() int64 {
	var n int64
	for _, f := range m.Checkpoint {
		n += f.Size
	}
	for _, f := range m.Base {
		n += f.Size
	}
	return n
}

// Decider2B is Strands Decider-2B (hobson v19): a LoRA adapter and a pointer
// head over Qwen3.5-2B-Base. Hashes are from the release's provenance and the
// Hub's LFS records at the pinned revisions; the small files were hashed when
// the revisions were pinned.
var Decider2B = DeciderModel{
	ID:           "decider-2b",
	Label:        "Strands Decider-2B",
	Repo:         "StrandsAgents/strands-decider-2B-hobson-v19",
	Revision:     "bb282d786bc251fd4e3068de3ada9ddbb38127cd",
	BaseRepo:     "Qwen/Qwen3.5-2B-Base",
	BaseRevision: "b1485b2fa6dfa1287294f269f5fb618e03d52d7c",
	Checkpoint: []DeciderFile{
		{Path: "hobson_config.json", Size: 743, SHA256: "2ae86f2ed56975f68e8f2f368104df8ce0ff4b7d9d146dcaf8eec5626d62449d"},
		{Path: "head.safetensors", Size: 4213224, SHA256: "daad0727152b6185447cee36f78230c144312feb7b19f02749b19645238b5287", LFS: true},
		{Path: "lora/adapter_config.json", Size: 1271, SHA256: "eb48e4ff81569664c4dd2a504b53da598eeaa390c265c2269ce4fe7526eab38a"},
		{Path: "lora/adapter_model.safetensors", Size: 67324872, SHA256: "701bdb895887097f7954ec7eb06f7937d3b790035b5462195eb26abd80a4aebc", LFS: true},
		{Path: "tokenizer.json", Size: 19989592, SHA256: "a2cdd2e108566b09079afa8d266e9e65e7c280f27b771218237a06de5ba9cd86", LFS: true},
		{Path: "tokenizer_config.json", Size: 1128, SHA256: "8671bed7c852ce9e661be94f179a7b4ffd091c2a65aea0363e5501c20318ee45"},
		{Path: "chat_template.jinja", Size: 7755, SHA256: "273d8e0e683b885071fb17e08d71e5f2a5ddfb5309756181681de4f5a1822d80"},
	},
	Base: []DeciderFile{
		{Path: "config.json", Size: 2908, SHA256: "ed1c1723241f23f7f4e23430759cbd7dcfb4103cbdfe052bfe7626b57c2615b4"},
		{Path: "model.safetensors.index.json", Size: 64460, SHA256: "74d2ddfe79f10f35b27b498632f02b97b60dd9ec39b35d7c5a890c399284e319"},
		{Path: "model.safetensors-00001-of-00001.safetensors", Size: 4548221488, SHA256: "928acbf11878c32185bbd863514d191769285065ab9ea14fbfe431303f5fdf2d", LFS: true},
	},
	MaxOptions: 24,
	RemoteIDs:  []string{"strands-decider-2b", "strands-decider", "decider-2b-hobson"},
	Blurb:      "Qwen3.5-2B decision model · pointer head · calibrated · runs on CPU, CUDA or Apple GPU",
}

// DeciderModels is the catalogue of decider checkpoints, in picker order.
var DeciderModels = []DeciderModel{Decider2B}

// DeciderModelByID returns the catalogue entry for id; "" names the first.
func DeciderModelByID(id string) (DeciderModel, bool) {
	if id == "" {
		return DeciderModels[0], true
	}
	for _, m := range DeciderModels {
		if m.ID == id {
			return m, true
		}
	}
	return DeciderModel{}, false
}

// IsDeciderID reports whether a model id a remote host lists, or a
// checkpoint a server reports, names a Strands Decider checkpoint. It
// matches on the catalogue's RemoteIDs, case-insensitively.
func IsDeciderID(id string) bool {
	lower := strings.ToLower(id)
	for _, m := range DeciderModels {
		for _, r := range m.RemoteIDs {
			if strings.Contains(lower, r) {
				return true
			}
		}
	}
	return false
}

// DeciderMaxOptions is the slot count of the decider checkpoint whose remote
// id or catalogue id is model; 0 when model is not a decider.
func DeciderMaxOptions(model string) int {
	if m, ok := DeciderModelByID(model); ok && model != "" {
		return m.MaxOptions
	}
	if IsDeciderID(model) {
		return Decider2B.MaxOptions
	}
	return 0
}
