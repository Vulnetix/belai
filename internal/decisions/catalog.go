package decisions

// Template names the prompt layout a local decision model was trained on.
type Template string

const (
	// TemplateDecider is decider's plain state-first layout:
	//   Context:\n<state>\n\nQuestion: <q>\nOptions:\n(A) ..\nAnswer: (
	// read at the "(" position. Noul options are ["no", "yes"].
	TemplateDecider Template = "decider"
	// TemplateJevK5 is the JevK5/SemIf chat layout Plumb-4B inherits: a
	// Qwen chat turn whose user message is {"evidence","criterion","options"}
	// JSON, read at the first assistant token. Noul options are
	// ["true", "false"].
	TemplateJevK5 Template = "jevk5"
)

// Temps are a local model's calibration temperatures per question type.
type Temps struct {
	Noul, Choice float64
}

// LocalModel is one decision model belai can run behind llama-server.
type LocalModel struct {
	ID    string // settings id: classifier.model
	Label string
	Repo  string // Hugging Face repo
	File  string // exact GGUF file
	// Alias is the name llama-server serves the model under; a running
	// server is reused only when /v1/models reports this alias.
	Alias     string
	SizeBytes int64
	Temps     Temps
	Template  Template
	Blurb     string
}

// LocalModels is the catalogue of local decision models, in picker order.
var LocalModels = []LocalModel{
	{
		ID:        "decider-4b",
		Label:     "Decider-4B",
		Repo:      "Mapika/decider-4b-GGUF",
		File:      "decider-4b-v2.1-Q4_K_M.gguf",
		Alias:     "belai-decider-4b",
		SizeBytes: 2708804640,
		Temps:     Temps{Noul: 1.56, Choice: 1.11},
		Template:  TemplateDecider,
		Blurb:     "Qwen3.5-4B decision model · 4-bit · ~1 s per short check on CPU",
	},
	{
		ID:        "plumb-4b",
		Label:     "Plumb-4B",
		Repo:      "crh225/plumb-4b-GGUF",
		File:      "plumb-4b-v5-Q4_K_M.gguf",
		Alias:     "belai-plumb-4b",
		SizeBytes: 2708804640,
		Temps:     Temps{Noul: 2.07, Choice: 2.07},
		Template:  TemplateJevK5,
		Blurb:     "JevK5-derived decision model · 4-bit · ~1 s per short check on CPU",
	},
}

// LocalModelByID returns the catalogue entry for id.
func LocalModelByID(id string) (LocalModel, bool) {
	for _, m := range LocalModels {
		if m.ID == id {
			return m, true
		}
	}
	return LocalModel{}, false
}
