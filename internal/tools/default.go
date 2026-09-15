package tools

// Default builds the default tool registry for a working directory: Read
// (bounded to 64 KiB), WebFetch, and WebSearch when a search backend is
// configured.
func Default(workdir string) *Registry {
	var list []Tool
	list = append(list, &Read{Root: workdir, MaxBytes: 64 * 1024})
	list = append(list, &WebFetch{})
	ws := &WebSearch{}
	if ws.Available() {
		list = append(list, ws)
	}
	return NewRegistry(list...)
}
