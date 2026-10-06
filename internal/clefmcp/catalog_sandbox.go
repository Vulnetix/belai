//go:build belai_sandbox

package clefmcp

// Schema helpers: the small subset of JSON Schema the harness keeps from an
// MCP tool (types, properties, items, required, string enums).

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func num(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }

func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

func object(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

const (
	qDesc = "The decision to make, written as a self-contained question or proposition."
	cDesc = "Optional facts the decision depends on. Plain text; it is cleaned before the model sees it."
)

func questionProp() map[string]any { return str(qDesc) }
func ctxProp() map[string]any      { return str(cDesc) }

func optionProps(extra map[string]any, optDesc string) map[string]any {
	p := map[string]any{
		"question": questionProp(),
		"options":  strs(optDesc),
		"context":  ctxProp(),
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

const optsDesc = "The candidates, 2 to 255, each distinct and non-empty. Results echo them exactly as given."

// catalog is every tool, in the order tools/list returns them. Each one asks
// the decision model for probabilities and does the rest with fixed arithmetic:
// weights are normalised to sum to 1 and rounded to four places, and options
// with equal weight keep the order the caller gave them.
var catalog = []toolDef{
	{
		name: "decide_boolean",
		desc: "Decide a true or false question. Returns answer (p_true >= threshold), p_true (the model's probability the proposition is true), confidence (the larger of p_true and 1 - p_true) and margin (how far p_true is from 0.5, doubled). Use it for a yes/no gate where you also want to know how sure the model is. Example: question \"The change only touches documentation\", context \"files: README.md, docs/a.md\".",
		schema: object(map[string]any{
			"question":  str("The proposition to judge, written so that true means yes."),
			"context":   ctxProp(),
			"threshold": num("Optional. The p_true at or above which answer is true. Above 0 and below 1; default 0.5."),
		}, "question"),
		run: (*Server).decideBoolean,
	},
	{
		name: "gate_decision",
		desc: "Decide a true or false question with a confidence bar, for a step that should not proceed on a weak answer. Returns decision \"pass\" when p_true >= min_confidence, \"fail\" when 1 - p_true >= min_confidence, and \"uncertain\" in between. It never passes on low confidence: treat uncertain as a reason to ask a person or gather more facts. Example: question \"This command is safe to run unattended\", min_confidence 0.9.",
		schema: object(map[string]any{
			"question":       str("The proposition to judge, written so that true means proceed."),
			"context":        ctxProp(),
			"min_confidence": num("Optional. The probability needed to pass or fail. Above 0.5 and at most 1; default 0.8."),
		}, "question"),
		run: (*Server).gateDecision,
	},
	{
		name: "decide_enum",
		desc: "Pick one option from a fixed list. Returns choice (the heaviest option), weights (every option's weight in the order given; they sum to 1), confidence (the choice's weight) and margin (its lead over the runner-up). Use it for any enumerated decision such as a label, a category or a next step. Example: question \"Which severity fits this finding\", options [\"low\",\"medium\",\"high\"].",
		schema: object(optionProps(map[string]any{
			"descriptions": map[string]any{"type": "object", "description": "Optional. One short explanation for an option, keyed by the option text."},
		}, optsDesc), "question", "options"),
		run: (*Server).decideEnum,
	},
	{
		name: "weigh_options",
		desc: "Give every option a confidence weight without choosing one. Returns weights in the order given, summing to 1. Use it when you want the whole distribution, for example to combine it with other evidence or to see how close a call is. Example: question \"How likely is each root cause\", options [\"config\",\"network\",\"code\"].",
		schema: object(optionProps(map[string]any{
			"descriptions": map[string]any{"type": "object", "description": "Optional. One short explanation for an option, keyed by the option text."},
		}, optsDesc), "question", "options"),
		run: (*Server).weighOptions,
	},
	{
		name: "rank_options",
		desc: "Order all options from best to worst for a question. Returns ranking, each entry with rank (1 is best), option and weight. One model call, so it is the cheap way to order up to 255 candidates. Equal weights keep the order given, so the same answers always give the same ordering. Example: question \"Which file should I read first to fix the failing test\", options [\"a.go\",\"b.go\",\"c.go\"].",
		schema: object(optionProps(map[string]any{
			"descriptions": map[string]any{"type": "object", "description": "Optional. One short explanation for an option, keyed by the option text."},
		}, optsDesc), "question", "options"),
		run: (*Server).rankOptions,
	},
	{
		name: "top_k_options",
		desc: "Pick the best k options, in order. Like rank_options but returns only the first k as top, with k and omitted (how many were left out). Use it to shortlist from a long list. k must be at least 1 and less than the number of options. Example: question \"Which tests are most likely to catch this regression\", options [...50 test names...], k 5.",
		schema: object(optionProps(map[string]any{
			"descriptions": map[string]any{"type": "object", "description": "Optional. One short explanation for an option, keyed by the option text."},
			"k":            integer("How many options to return. At least 1 and less than the number of options."),
		}, optsDesc), "question", "options", "k"),
		run: (*Server).topK,
	},
	{
		name: "compare_pair",
		desc: "Choose between exactly two candidates. Returns winner (\"a\" or \"b\"), winner_option, tie (true when the weights are equal, in which case a wins), weights for a and b, and margin. Example: question \"Which plan is safer to run first\", a \"migrate then deploy\", b \"deploy then migrate\".",
		schema: object(map[string]any{
			"question": questionProp(),
			"a":        str("The first candidate."),
			"b":        str("The second candidate. Must differ from a."),
			"context":  ctxProp(),
		}, "question", "a", "b"),
		run: (*Server).comparePair,
	},
	{
		name: "rank_pairwise",
		desc: "Order 2 to 11 items by comparing every pair, then counting wins. Slower and costlier than rank_options (n(n-1)/2 comparisons, 55 for 11 items) but it judges each pair on its own, which suits a few items that are close or that differ in several ways. Returns ranking with rank, item, wins and points (the sum of the model's pairwise probabilities), ordered by wins, then points, then the order given, and comparisons (how many pairs were asked). Example: question \"Which library fits a small CLI best\", items [\"cobra\",\"urfave/cli\",\"kong\"].",
		schema: object(map[string]any{
			"question": questionProp(),
			"items":    strs("The items to order, 2 to 11, each distinct and non-empty."),
			"context":  ctxProp(),
		}, "question", "items"),
		run: (*Server).rankPairwise,
	},
	{
		name: "decide_batch",
		desc: "Ask up to 64 independent boolean and enum questions about the same context in one call, which is one model request instead of many. Each question has an id you choose, a type (\"boolean\" or \"enum\"), the question and, for an enum, its options. Returns answers in the order given, each with its id, type and the same result decide_boolean or decide_enum gives. Example: questions [{\"id\":\"risky\",\"type\":\"boolean\",\"question\":\"The diff changes auth code\"},{\"id\":\"area\",\"type\":\"enum\",\"question\":\"Which area\",\"options\":[\"api\",\"ui\",\"infra\"]}].",
		schema: object(map[string]any{
			"context": ctxProp(),
			"questions": map[string]any{
				"type":        "array",
				"description": "1 to 64 questions.",
				"items": object(map[string]any{
					"id":       str("Your name for this question, unique in the call, at most 64 characters."),
					"type":     map[string]any{"type": "string", "enum": []string{"boolean", "enum"}, "description": "boolean for a true/false proposition, enum for a pick from options."},
					"question": questionProp(),
					"options":  strs("For an enum: the candidates, 2 to 255, each distinct and non-empty."),
				}, "id", "type", "question"),
			},
		}, "questions"),
		run: (*Server).decideBatch,
	},
}
