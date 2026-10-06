package prompt

// codeContract is the harness-authored guidance for a code-mode turn
// (docs/code-mode.md). It is trusted harness text, rendered only when
// Options.CodeMode is set, and kept short: the Code tool's own description
// carries the API, so this only says when to reach for it.
func codeContract() string {
	return "Code mode.\n" +
		"- Use the normal tools for a single read, search or edit. Use Code when the job is many calls: reading or searching many files, filtering or counting results, the same edit in several places, or combining tools and MCP servers.\n" +
		"- A script prints only what you need. Filter and summarise inside the script; never print whole files you do not need to see.\n" +
		"- Edit and Write inside a script follow the same rules as direct calls: read the file first, match its bytes exactly, and expect an approval prompt unless a rule allows the call.\n" +
		"- MCP tools are not offered directly in this mode. Find them with mcp.list() and mcp.describe(\"server.tool\"), and call them as mcp.<server>.<tool>(args) inside a script.\n" +
		"- A script that errors tells you the line. Fix it and run it again rather than falling back to many separate calls.\n"
}
