package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/sanitize"
)

// answersPath is the -answers flag: a headless -prompt run answers the
// questions it asks (the goal kickoff's clarification round, AskUserQuestion,
// a stuck goal's question) from this file of the user's own answers. Empty
// means the run asks nothing, as before.
var answersPath string

// maxAnswersBytes bounds the answers file.
const maxAnswersBytes = 64 * 1024

// scriptedAnswer answers one questionnaire. Choose is the option index picked
// in every group (0, the first and recommended option, by default); Note is
// the user's free text on the first group; Skip declines every question.
// Match, a case-insensitive regular expression, reserves the entry for the
// first questionnaire whose question text matches; entries without it are
// used in order by the questionnaires no Match entry claims.
type scriptedAnswer struct {
	Choose *int   `json:"choose,omitempty"`
	Note   string `json:"note,omitempty"`
	Skip   bool   `json:"skip,omitempty"`
	Match  string `json:"match,omitempty"`

	re   *regexp.Regexp
	used bool
}

// answerScript hands out the scripted answers in order. Once they run out,
// every further questionnaire is declined, as a user who stopped answering.
type answerScript struct {
	mu      sync.Mutex
	answers []scriptedAnswer
	next    int
	log     io.Writer
}

// loadAnswerScript reads and validates the answers file. The file is the
// user's own words, so each note is admitted later exactly like typed answers.
func loadAnswerScript(path string, log io.Writer) (*answerScript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("answers: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxAnswersBytes+1))
	if err != nil {
		return nil, fmt.Errorf("answers: %w", err)
	}
	if len(b) > maxAnswersBytes {
		return nil, fmt.Errorf("answers: %s is larger than %d bytes", path, maxAnswersBytes)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var answers []scriptedAnswer
	if err := dec.Decode(&answers); err != nil {
		return nil, fmt.Errorf("answers: %s must be a JSON array of {\"choose\", \"note\", \"skip\"}: %w", path, err)
	}
	for i, a := range answers {
		if a.Choose != nil && *a.Choose < 0 {
			return nil, fmt.Errorf("answers: entry %d: choose must not be negative", i)
		}
		if a.Match != "" {
			re, err := regexp.Compile("(?i)" + a.Match)
			if err != nil {
				return nil, fmt.Errorf("answers: entry %d: match: %w", i, err)
			}
			answers[i].re = re
		}
	}
	return &answerScript{answers: answers, log: log}, nil
}

// answer returns the next scripted answer for q and reports the exchange.
func (s *answerScript) answer(q clarify.Questionnaire) clarify.Answers {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, g := range q.Groups {
		labels := make([]string, 0, len(g.Options))
		for _, o := range g.Options {
			labels = append(labels, o.Label)
		}
		fmt.Fprintf(s.log, "belai: question %d: %s [%s]\n", i+1, sanitize.Line(g.Context, 400), sanitize.Line(strings.Join(labels, " | "), 400))
	}
	a, ok := s.pick(q)
	if !ok {
		fmt.Fprintln(s.log, "belai: no scripted answer left; declining")
		return skippedAll(q)
	}
	if a.Skip {
		fmt.Fprintln(s.log, "belai: scripted answer: declined")
		return skippedAll(q)
	}
	var out clarify.Answers
	for i, g := range q.Groups {
		pick := 0
		if a.Choose != nil {
			pick = *a.Choose
		}
		if pick >= len(g.Options) {
			pick = len(g.Options) - 1
		}
		item := clarify.Answer{GroupIndex: i, Chosen: []int{pick}}
		if i == 0 {
			item.Note = a.Note
		}
		out.Items = append(out.Items, item)
	}
	fmt.Fprintf(s.log, "belai: scripted answer: option %d%s\n", out.Items[0].Chosen[0]+1, noteSuffix(a.Note))
	return out
}

// pick claims the answer for q: the first unused Match entry whose pattern
// matches a question, else the next unused entry without one.
func (s *answerScript) pick(q clarify.Questionnaire) (scriptedAnswer, bool) {
	var text strings.Builder
	for _, g := range q.Groups {
		text.WriteString(g.Context + "\n")
	}
	for i := range s.answers {
		a := &s.answers[i]
		if !a.used && a.re != nil && a.re.MatchString(text.String()) {
			a.used = true
			return *a, true
		}
	}
	for ; s.next < len(s.answers); s.next++ {
		a := &s.answers[s.next]
		if !a.used && a.re == nil {
			a.used = true
			s.next++
			return *a, true
		}
	}
	return scriptedAnswer{}, false
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return ", note: " + sanitize.Line(note, 200)
}

func skippedAll(q clarify.Questionnaire) clarify.Answers {
	var out clarify.Answers
	for i := range q.Groups {
		out.Items = append(out.Items, clarify.Answer{GroupIndex: i, Skipped: true})
	}
	return out
}

// observe wraps a headless observer so questionnaires are answered from the
// script. Every other event passes through unchanged.
func (s *answerScript) observe(next func(agent.Event)) func(agent.Event) {
	return func(e agent.Event) {
		next(e)
		if e.Kind != agent.EventClarifyAskKind || e.Reply == nil || e.Clarify == nil {
			return
		}
		e.Reply <- s.answer(*e.Clarify)
	}
}
