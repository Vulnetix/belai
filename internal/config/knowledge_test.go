package config

import (
	"strings"
	"testing"
)

func TestKnowledgeLimitsDefaultAndOverride(t *testing.T) {
	i, p, r := Settings{}.KnowledgeLimits()
	if i != DefaultKnowledgeIndexTokens || p != DefaultKnowledgeProjectTokens || r != DefaultKnowledgeResultTokens {
		t.Fatalf("defaults = %d %d %d", i, p, r)
	}
	i, p, r = Settings{Knowledge: &KnowledgeSettings{MaxIndexTokens: ip(50_000), MaxResultTokens: ip(1_000)}}.KnowledgeLimits()
	if i != 50_000 || p != DefaultKnowledgeProjectTokens || r != 1_000 {
		t.Fatalf("overrides = %d %d %d", i, p, r)
	}
}

func TestValidateKnowledgeNamesTheKey(t *testing.T) {
	for key, s := range map[string]Settings{
		"knowledge.max_index_tokens":   {Knowledge: &KnowledgeSettings{MaxIndexTokens: ip(10)}},
		"knowledge.max_project_tokens": {Knowledge: &KnowledgeSettings{MaxProjectTokens: ip(MaxKnowledgeCorpusTokens + 1)}},
		"knowledge.max_result_tokens":  {Knowledge: &KnowledgeSettings{MaxResultTokens: ip(0)}},
	} {
		err := ValidateKnowledge(s)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
	if err := ValidateKnowledge(Settings{Knowledge: &KnowledgeSettings{MaxIndexTokens: ip(MinKnowledgeCorpusTokens), MaxResultTokens: ip(MaxKnowledgeResultTokens)}}); err != nil {
		t.Fatalf("bounds are inclusive: %v", err)
	}
	if err := ValidateKnowledge(Settings{}); err != nil {
		t.Fatal(err)
	}
}

// TestKnowledgeIsPerUser pins the rule docs/knowledge.md states: only the
// user's own layers size the store, and a repository cannot raise it.
func TestKnowledgeIsPerUser(t *testing.T) {
	wd := writeGlobal(t, `{"knowledge":{"max_index_tokens":50000}}`)
	writeProject(t, wd, `{"knowledge":{"max_index_tokens":5000000,"max_project_tokens":5000000,"max_result_tokens":50000}}`)
	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	i, p, r := eff.Settings.KnowledgeLimits()
	if i != 50_000 || p != DefaultKnowledgeProjectTokens || r != DefaultKnowledgeResultTokens {
		t.Fatalf("the project layer changed the limits: %d %d %d", i, p, r)
	}
	if eff.Origin["knowledge"] == SourceProject {
		t.Fatal("knowledge origin must not be the project layer")
	}
	// Settings.Override is the project-over-global path used elsewhere.
	g := Settings{Knowledge: &KnowledgeSettings{MaxResultTokens: ip(4000)}}
	merged := g.Override(Settings{Knowledge: &KnowledgeSettings{MaxResultTokens: ip(50_000)}})
	if _, _, r := merged.KnowledgeLimits(); r != 4000 {
		t.Fatalf("Override let the project raise the result cap to %d", r)
	}
}

func TestKnowledgeGlobalOutOfRangeFailsResolve(t *testing.T) {
	wd := writeGlobal(t, `{"knowledge":{"max_result_tokens":5}}`)
	if _, err := Resolve(wd, noEnv, Settings{}); err == nil || !strings.Contains(err.Error(), "knowledge.max_result_tokens") {
		t.Fatalf("err = %v", err)
	}
}

func TestKnowledgeTopicsDefaultAndOverride(t *testing.T) {
	c, d := Settings{}.KnowledgeTopics()
	if c != DefaultKnowledgeTopicChunks || d != DefaultKnowledgeTopicDocs {
		t.Fatalf("defaults = %d %d", c, d)
	}
	c, d = Settings{Knowledge: &KnowledgeSettings{TopicChunks: ip(30), TopicBudgetDocs: ip(0)}}.KnowledgeTopics()
	if c != 30 || d != 0 {
		t.Fatalf("overrides = %d %d (0 documents is a valid, off, budget)", c, d)
	}
	var k KnowledgeSettings
	k.merge(&KnowledgeSettings{TopicChunks: ip(5), TopicBudgetDocs: ip(7)})
	if *k.TopicChunks != 5 || *k.TopicBudgetDocs != 7 {
		t.Fatalf("merge dropped a topic key: %+v", k)
	}
}

func TestValidateKnowledgeTopicBoundsNameTheKey(t *testing.T) {
	for key, s := range map[string]Settings{
		"knowledge.topic_chunks":      {Knowledge: &KnowledgeSettings{TopicChunks: ip(0)}},
		"knowledge.topic_chunks ":     {Knowledge: &KnowledgeSettings{TopicChunks: ip(MaxKnowledgeTopicChunks + 1)}},
		"knowledge.topic_budget_docs": {Knowledge: &KnowledgeSettings{TopicBudgetDocs: ip(MaxKnowledgeTopicDocs + 1)}},
	} {
		err := ValidateKnowledge(s)
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(key)) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
	if err := ValidateKnowledge(Settings{Knowledge: &KnowledgeSettings{TopicChunks: ip(MinKnowledgeTopicChunks), TopicBudgetDocs: ip(0)}}); err != nil {
		t.Fatalf("bounds are inclusive: %v", err)
	}
}
