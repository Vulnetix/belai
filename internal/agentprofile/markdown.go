package agentprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Markdown agent definitions are the shape Claude Code, OpenClaw and Hermes
// use: YAML front-matter between --- lines, and the body as the prompt.
//
//	---
//	name: builder
//	description: Implements backlog items labelled build
//	mode: worker
//	kanban:
//	  labels: [build]
//	  on_success: {list: review, labels: [needs-review]}
//	---
//	You implement one kanban item per turn…
//
// The front-matter keys are the JSON profile's keys, checked strictly: an
// unknown key is an error, not ignored, so a typo cannot silently drop a
// safety setting. A Claude Code definition's comma-separated tools string is
// accepted, and its presentation-only color key is dropped. The result is an
// ordinary AgentProfile, validated like any other, and saved as JSON.

// foreignKeys are front-matter keys other harnesses write that carry no
// meaning here.
var foreignKeys = []string{"color"}

// ParseMarkdown parses a markdown agent definition. A missing mode defaults to
// single and a missing autonomy to supervised.
func ParseMarkdown(data []byte) (AgentProfile, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return AgentProfile{}, errors.New("a markdown agent starts with a --- front-matter line")
	}
	front, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return AgentProfile{}, errors.New("the front-matter has no closing --- line")
	}
	// body starts with the remainder of the closing --- line.
	if _, after, found := strings.Cut(body, "\n"); found {
		body = after
	} else {
		body = ""
	}

	var meta map[string]any
	dec := yaml.NewDecoder(strings.NewReader(front))
	if err := dec.Decode(&meta); err != nil {
		return AgentProfile{}, fmt.Errorf("front-matter: %w", err)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	for _, k := range foreignKeys {
		delete(meta, k)
	}
	// Claude Code writes tools as "Read, Grep, Glob".
	if s, ok := meta["tools"].(string); ok {
		var list []any
		for _, t := range strings.Split(s, ",") {
			if t = strings.TrimSpace(t); t != "" {
				list = append(list, t)
			}
		}
		meta["tools"] = list
	}
	prompt := strings.TrimSpace(body)
	if _, set := meta["system_prompt"]; set && prompt != "" {
		return AgentProfile{}, errors.New("set the prompt in the body or in system_prompt, not both")
	}
	if prompt != "" {
		meta["system_prompt"] = prompt
	}
	if _, set := meta["mode"]; !set {
		meta["mode"] = ModeSingle
	}
	if _, set := meta["autonomy"]; !set {
		meta["autonomy"] = AutonomySupervised
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return AgentProfile{}, fmt.Errorf("front-matter: %w", err)
	}
	var p AgentProfile
	jd := json.NewDecoder(bytes.NewReader(raw))
	jd.DisallowUnknownFields()
	if err := jd.Decode(&p); err != nil {
		return AgentProfile{}, fmt.Errorf("front-matter: %w", err)
	}
	return p, p.Validate()
}

// ParseFile parses a .json or .md profile definition strictly.
func ParseFile(name string, data []byte) (AgentProfile, error) {
	if strings.HasSuffix(strings.ToLower(name), ".md") {
		return ParseMarkdown(data)
	}
	var p AgentProfile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return AgentProfile{}, err
	}
	return p, p.Validate()
}

// markdownKeyOrder is the front-matter order MarshalMarkdown writes; any key
// not listed follows in sorted order.
var markdownKeyOrder = []string{
	"name", "id", "display_name", "palette", "avatar_id", "personality",
	"description", "identity", "tools", "mode", "schedule", "monitor_condition", "reflection",
	"max_iterations", "autonomy", "provider", "model", "effort", "guardrails", "ask_permission",
	"facts", "kanban", "workspace", "budget", "memory", "knowledge",
}

// MarshalMarkdown writes p as a markdown agent definition: the JSON keys as
// front-matter and the system prompt as the body. Every value is written in
// JSON form, which YAML reads as flow style, so ParseMarkdown reads the
// result back to the same profile and no string needs YAML quoting rules.
func MarshalMarkdown(p AgentProfile) ([]byte, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "system_prompt")
	var b bytes.Buffer
	b.WriteString("---\n")
	write := func(k string) {
		if v, ok := fields[k]; ok {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
			delete(fields, k)
		}
	}
	for _, k := range markdownKeyOrder {
		write(k)
	}
	rest := make([]string, 0, len(fields))
	for k := range fields {
		rest = append(rest, k)
	}
	sort.Strings(rest)
	for _, k := range rest {
		write(k)
	}
	b.WriteString("---\n")
	b.WriteString(strings.TrimSpace(p.SystemPrompt))
	b.WriteString("\n")
	return b.Bytes(), nil
}
