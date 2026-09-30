package acp

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
)

// maxClientName bounds the editor name used in a session name.
const maxClientName = 32

// maxSessionTitle bounds the prompt excerpt in a session name.
const maxSessionTitle = 60

// clientName reads the editor's declared name from an initialize request: the
// human title when it gave one, else its programmatic name. The value is the
// editor's own text, so it is cleaned to one short line with no brackets (the
// name is later wrapped in some) and is empty when nothing usable remains.
func clientName(params json.RawMessage) string {
	var p struct {
		ClientInfo struct {
			Name  string `json:"name"`
			Title string `json:"title"`
		} `json:"clientInfo"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	n := p.ClientInfo.Title
	if strings.TrimSpace(n) == "" {
		n = p.ClientInfo.Name
	}
	n = strings.NewReplacer("[", "", "]", "").Replace(sanitize.Line(n, maxClientName))
	return strings.TrimSpace(n)
}

// sessionName is the display name of an editor session: the editor's name in
// square brackets, then what the session is about. detail is a prompt's first
// line or, before any prompt, the directory's base name.
func sessionName(client, detail string) string {
	detail = sanitize.Line(detail, maxSessionTitle)
	if client == "" {
		return detail
	}
	if detail == "" {
		return "[" + client + "]"
	}
	return "[" + client + "] " + detail
}

// dirName is the last element of a session directory.
func dirName(cwd string) string {
	b := filepath.Base(filepath.Clean(cwd))
	if b == "." || b == string(filepath.Separator) {
		return ""
	}
	return b
}
