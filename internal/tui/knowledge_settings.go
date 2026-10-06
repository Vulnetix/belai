package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// The /settings rows of the knowledge store (docs/knowledge.md). All five are
// per-user host-performance sizes, saved to the user's own settings whatever
// scope the screen shows: a repository cannot raise them.

const knowledgeRowPrefix = "knowledge."

// knowledgeRows builds the five rows.
func knowledgeRows(s config.Settings, origin map[string]config.Source) []settingsRow {
	index, project, result := s.KnowledgeLimits()
	topicChunks, topicDocs := s.KnowledgeTopics()
	src := sourceLabel(origin["knowledge"])
	return []settingsRow{
		{key: "knowledge.max_index_tokens", label: "knowledge per profile", kind: "text", value: strconv.Itoa(index), src: src,
			help: fmt.Sprintf("most estimated tokens of documents one agent profile keeps indexed, %d to %d; global only", config.MinKnowledgeCorpusTokens, config.MaxKnowledgeCorpusTokens)},
		{key: "knowledge.max_project_tokens", label: "knowledge for project", kind: "text", value: strconv.Itoa(project), src: src,
			help: fmt.Sprintf("most estimated tokens of .vulnetix output and @ files kept indexed for a project, %d to %d; global only", config.MinKnowledgeCorpusTokens, config.MaxKnowledgeCorpusTokens)},
		{key: "knowledge.max_result_tokens", label: "knowledge per search", kind: "text", value: strconv.Itoa(result), src: src,
			help: fmt.Sprintf("most estimated tokens one search returns across every index, %d to %d; global only", config.MinKnowledgeResultTokens, config.MaxKnowledgeResultTokens)},
		{key: "knowledge.topic_chunks", label: "knowledge topic chunks", kind: "text", value: strconv.Itoa(topicChunks), src: src,
			help: fmt.Sprintf("most chunks of one document sent to the decision backend to find its topics; a longer one is sampled, %d to %d; global only", config.MinKnowledgeTopicChunks, config.MaxKnowledgeTopicChunks)},
		{key: "knowledge.topic_budget_docs", label: "knowledge topic documents", kind: "text", value: strconv.Itoa(topicDocs), src: src,
			help: fmt.Sprintf("most documents one refresh sends to the decision backend for topics, 0 to %d (0 sends none); global only", config.MaxKnowledgeTopicDocs)},
	}
}

// knowledgeRaw is the editor's starting text: the stored value, or empty when
// the key is unset.
func (a *App) knowledgeRaw(key string) string {
	k := a.settings.Knowledge
	if k == nil {
		return ""
	}
	p := knowledgeField(k, key)
	if p == nil || *p == nil {
		return ""
	}
	return strconv.Itoa(**p)
}

// knowledgeCommit validates and saves one knowledge row. An empty value clears
// it back to the default.
func (a *App) knowledgeCommit(key, val string) error {
	if val == "" {
		return a.knowledgeUnset(key)
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return fmt.Errorf("%s must be a whole number of tokens", key)
	}
	probe := config.Settings{Knowledge: &config.KnowledgeSettings{}}
	set := func(k *config.KnowledgeSettings) error {
		p := knowledgeField(k, key)
		if p == nil {
			return fmt.Errorf("cannot edit %q", key)
		}
		*p = &n
		return nil
	}
	if err := set(probe.Knowledge); err != nil {
		return err
	}
	if err := config.ValidateKnowledge(probe); err != nil {
		return err
	}
	return a.mutateGlobalSetting(func(s *config.Settings) {
		if s.Knowledge == nil {
			s.Knowledge = &config.KnowledgeSettings{}
		}
		_ = set(s.Knowledge)
	})
}

// knowledgeUnset clears one knowledge key in the global layer.
func (a *App) knowledgeUnset(key string) error {
	if !strings.HasPrefix(key, knowledgeRowPrefix) {
		return fmt.Errorf("cannot edit %q", key)
	}
	return a.mutateGlobalSetting(func(s *config.Settings) {
		if s.Knowledge == nil {
			return
		}
		if p := knowledgeField(s.Knowledge, key); p != nil {
			*p = nil
		}
		if *s.Knowledge == (config.KnowledgeSettings{}) {
			s.Knowledge = nil
		}
	})
}

// knowledgeField returns the settings field a row edits, or nil for a key that
// is not one.
func knowledgeField(k *config.KnowledgeSettings, key string) **int {
	switch key {
	case "knowledge.max_index_tokens":
		return &k.MaxIndexTokens
	case "knowledge.max_project_tokens":
		return &k.MaxProjectTokens
	case "knowledge.max_result_tokens":
		return &k.MaxResultTokens
	case "knowledge.topic_chunks":
		return &k.TopicChunks
	case "knowledge.topic_budget_docs":
		return &k.TopicBudgetDocs
	}
	return nil
}
