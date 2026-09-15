package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Property is a JSON-schema property for a tool definition.
type Property struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// Definition is the static metadata exposed to the model for a tool.
type Definition struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Properties  map[string]Property `json:"properties,omitempty"`
	Required    []string            `json:"required,omitempty"`
}

// Tool is the shared interface for all executable tools.
type Tool interface {
	Definition() Definition
	Kind() Kind
	// Subject returns the permission-rule subject (path, URL, etc.) from args.
	Subject(args map[string]any) string
	Execute(ctx context.Context, args map[string]any) (Result, error)
}

// Registry holds an ordered list of registered tools.
type Registry struct {
	tools []Tool
}

// NewRegistry builds a registry from the provided tools.
func NewRegistry(tools ...Tool) *Registry {
	return &Registry{tools: tools}
}

// Names returns every registered tool name.
func (r *Registry) Names() []string {
	out := make([]string, len(r.tools))
	for i, t := range r.tools {
		out[i] = t.Definition().Name
	}
	return out
}

// Definitions returns the wire definitions for every registered tool.
func (r *Registry) Definitions() []Definition {
	out := make([]Definition, len(r.tools))
	for i, t := range r.tools {
		out[i] = t.Definition()
	}
	return out
}

// Find returns the tool with the given name, if present.
func (r *Registry) Find(name string) (Tool, bool) {
	for _, t := range r.tools {
		if t.Definition().Name == name {
			return t, true
		}
	}
	return nil, false
}

// SanitizePath resolves a user-provided path against root, follows symlinks,
// and returns the clean relative path or an error if it escapes root.
func SanitizePath(root, raw string) (string, error) {
	if strings.Contains(raw, "\x00") {
		return "", fmt.Errorf("path contains NUL")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRoot, raw)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return rel, nil
}
