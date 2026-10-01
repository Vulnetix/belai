package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/knowledge/kbgate"
	"github.com/vulnetix/belai/internal/knowledge/tags"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// knowledgeReport is the JSON shape of `belai agent knowledge -json`: counts and
// addresses only, never chunk text.
type knowledgeReport struct {
	Indexes  []knowledgeIndexReport `json:"indexes"`
	Warnings []string               `json:"warnings,omitempty"`
}

type knowledgeIndexReport struct {
	Name      string         `json:"name"`
	Tokens    int            `json:"tokens"`
	Cap       int            `json:"cap"`
	Documents []knowledgeDoc `json:"documents"`
}

type knowledgeDoc struct {
	Address   string `json:"address"`
	Chunks    int    `json:"chunks"`
	Tokens    int    `json:"tokens"`
	Dropped   int    `json:"dropped,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Type, Language and Topics are the document's tags: ids from the tag
	// tables and the vocabulary, never text from the document.
	Type     string   `json:"type,omitempty"`
	Language string   `json:"language,omitempty"`
	Topics   []string `json:"topics,omitempty"`
	Tagged   string   `json:"tagged_by,omitempty"`
}

// agentKnowledge implements `belai agent knowledge [-index] [-json] [NAME]`:
// the status of the retrieval indexes (docs/knowledge.md), and with -index a
// refresh through the security classifier first.
func agentKnowledge(ctx context.Context, fs *flag.FlagSet, rest []string, stdout, stderr io.Writer, asJSON *bool) (int, error) {
	index := fs.Bool("index", false, "bring the indexes up to date first (classifies new text)")
	trust := fs.Bool("trust-dir", false, "trust the repository first (with -index)")
	providerName := fs.String("provider", "", "provider override (with -index)")
	model := fs.String("model", "", "model override (with -index)")
	if err := parseInterleaved(fs, rest); err != nil || fs.NArg() > 1 {
		return 2, errors.New("usage: belai agent knowledge [-index] [-json] [NAME]")
	}
	wd, _ := os.Getwd()
	repo := repoRoot(wd)

	var prof *knowledge.Profile
	if fs.NArg() == 1 {
		p, err := agentprofile.Load(fs.Arg(0))
		if err != nil {
			return 1, err
		}
		paths := p.KnowledgePaths()
		if len(paths) == 0 {
			return 1, fmt.Errorf("profile %q lists no documents: add a knowledge block with paths to it", p.Name)
		}
		if p.ID == "" {
			return 1, fmt.Errorf("profile %q has no id yet: save it again (belai agent import -force) to give it one", p.Name)
		}
		prof = &knowledge.Profile{ID: p.ID, Name: p.Name, Paths: paths}
	}

	settings, pol, err := repoPolicy(repo)
	if err != nil {
		return 1, err
	}
	indexCap, projectCap, _ := settings.KnowledgeLimits()
	opts := knowledge.Options{Root: repo, Profile: prof, IndexTokens: indexCap, ProjectTokens: projectCap}

	if *index {
		if err := trustRepo(repo, *trust); err != nil {
			return 1, err
		}
		if err := run.PreloadClassifier(run.ResolveSecurityClassifier(settings.Classifier)); err != nil {
			return 1, fmt.Errorf("load embedded classifier: %w", err)
		}
		resolver, err := newResolver(repo)
		if err != nil {
			return 1, err
		}
		wantProvider, wantModel := workerModel(*providerName, *model, settings, config.LoadState)
		cfg, err := run.ResolveWithSource(wantModel, wantProvider, os.Getenv, resolver)
		if err != nil {
			return 1, err
		}
		if cfg, err = withClassifier(cfg, settings, resolver); err != nil {
			return 1, err
		}
		client := httpclient.Default()
		opts.ProfileGate = kbgate.New(cfg, client, nil, pol, tools.KindRead)
		opts.ProjectGate = kbgate.New(cfg, client, nil, pol, tools.KindRemote)
		opts.Tagger = kbgate.NewTagger(cfg, settings)
	}

	store := knowledge.Open(opts)
	if *index {
		rep, err := store.Refresh(ctx)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stderr, "project: %d documents, %d chunks, %d reused, %d dropped by the classifier, %d skipped%s\n",
			rep.Project.Docs, rep.Project.Chunks, rep.Project.Reused, rep.Project.Dropped, rep.Project.Skipped, capNote(rep.Project))
		if prof != nil {
			fmt.Fprintf(stderr, "%s: %d documents, %d chunks, %d reused, %d dropped by the classifier, %d skipped%s\n",
				prof.Name, rep.Profile.Docs, rep.Profile.Chunks, rep.Profile.Reused, rep.Profile.Dropped, rep.Profile.Skipped, capNote(rep.Profile))
		}
		if f := rep.Project.Failed + rep.Profile.Failed; f > 0 {
			fmt.Fprintf(stderr, "%d document(s) could not be classified and were not indexed\n", f)
		}
	}

	out := knowledgeReport{Warnings: store.Warnings()}
	docs := store.Docs()
	names := make([]string, 0, len(docs))
	for n := range docs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		// The session index holds @ files of a running session; a command has
		// none, and an index with no document has nothing to report.
		if n == knowledge.SessionScope || len(docs[n]) == 0 {
			continue
		}
		ir := knowledgeIndexReport{Name: sanitize.Line(n, 64), Cap: projectCap}
		if prof != nil && n == prof.Name {
			ir.Cap = indexCap
		}
		for _, d := range docs[n] {
			ir.Tokens += d.Tokens
			ir.Documents = append(ir.Documents, knowledgeDoc{
				Address: sanitize.Line(d.Address, 300), Chunks: d.Chunks, Tokens: d.Tokens, Dropped: d.Dropped, Truncated: d.Truncated,
				Type: d.Tags.Kind, Language: d.Tags.Lang, Topics: topicIDs(d.Tags), Tagged: taggedBy(d.Tags),
			})
		}
		out.Indexes = append(out.Indexes, ir)
	}
	if *asJSON {
		return 0, jsonOut(stdout, out)
	}
	if len(out.Indexes) == 0 {
		fmt.Fprintln(stdout, "nothing is indexed yet; run `belai agent knowledge -index`")
	}
	for _, ir := range out.Indexes {
		fmt.Fprintf(stdout, "%s: %d of %d tokens\n", ir.Name, ir.Tokens, ir.Cap)
		tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		for _, d := range ir.Documents {
			note := ""
			if d.Dropped > 0 {
				note += fmt.Sprintf(" %d dropped", d.Dropped)
			}
			if d.Truncated {
				note += " truncated"
			}
			if d.Type != "" {
				note += " type=" + d.Type
			}
			if len(d.Topics) > 0 {
				note += " topics=" + strings.Join(d.Topics, ",")
			}
			fmt.Fprintf(tw, "  %s\t%d chunks\t%d tokens\t%s\n", d.Address, d.Chunks, d.Tokens, strings.TrimSpace(note))
		}
		if err := tw.Flush(); err != nil {
			return 1, err
		}
	}
	for _, w := range out.Warnings {
		fmt.Fprintln(stderr, "warning:", sanitize.Line(w, 300))
	}
	return 0, nil
}

// topicIDs lists a document's topic ids, best first.
func topicIDs(r tags.Result) []string {
	out := make([]string, 0, len(r.Topics))
	for _, t := range r.Topics {
		out = append(out, t.ID)
	}
	return out
}

// taggedBy says who decided the topics: the pattern detector or a decision
// backend.
func taggedBy(r tags.Result) string {
	if r.Src == tags.SrcNone {
		return ""
	}
	return r.Src.String()
}

func capNote(s knowledge.Stats) string {
	if s.Truncated {
		return " (the token cap stopped it: raise knowledge.max_index_tokens or max_project_tokens)"
	}
	return ""
}
