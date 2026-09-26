package agent

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// attachTokenRe matches @file references. It does not match quoted paths with
// spaces (the TUI handles those); non-interactive prompts use simple tokens.
var attachTokenRe = regexp.MustCompile(`(?:^|[\s])@([a-zA-Z0-9._~+\-/]+)`)

// parseAttachTokens extracts the path part of every @file reference in s,
// excluding reserved schemes like @agent:.
func parseAttachTokens(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range attachTokenRe.FindAllStringSubmatch(s, -1) {
		raw := m[1]
		if raw == "" || seen[raw] {
			continue
		}
		if colon := strings.Index(raw, ":"); colon > 0 && raw[:colon] == "agent" {
			continue
		}
		seen[raw] = true
		out = append(out, raw)
	}
	return out
}

// attachFromPrompt turns simple @file references in prompt into classified
// file attachments when the caller supplied no attachments (the
// non-interactive CLI path). The TUI already builds attachments itself, so
// this path is skipped when attachments are present.
func (s *Session) attachFromPrompt(ctx context.Context, prompt string, pipe *rolemanager.Pipeline) []run.Attachment {
	if s.registry == nil || s.registry.Cwd() == nil {
		return nil
	}
	root := s.registry.Cwd().Root()
	readTool, _ := s.execTool("Read")
	if readTool == nil {
		return nil
	}

	var atts []run.Attachment
	seen := map[string]bool{}
	for _, raw := range parseAttachTokens(prompt) {
		if seen[raw] {
			continue
		}
		seen[raw] = true
		rel := filepath.ToSlash(filepath.Clean(raw))
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if !strings.HasPrefix(filepath.Clean(abs), filepath.Clean(root)+string(filepath.Separator)) {
			continue
		}

		res, err := readTool.Execute(ctx, map[string]any{"file_path": rel})
		if err != nil || strings.TrimSpace(res.Content) == "" {
			continue
		}

		plansDir := config.ProjectPlansDir(root)
		if _, ok := plans.DetectHandoff(rel, res.Content, plansDir); !ok {
			continue
		}

		dec, err := pipe.Process(ctx, tools.Result{Kind: tools.KindRead, Content: res.Content})
		if err != nil || dec.Action != rolemanager.ActionProceed {
			continue
		}
		atts = append(atts, run.Attachment{Kind: "file", Label: rel, Body: dec.Content})
	}
	return atts
}
