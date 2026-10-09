package prompt

// decisionContract is the harness-authored guidance for a session that has the
// decision tools (mcp__clef__*, docs/mcp.md "The decision server"). It is trusted
// harness text, rendered only when Options.DecisionTools is set, and kept short:
// each tool's own description carries its arguments and result, so this only says
// what the decision model is, when it beats reasoning alone, and how to read it.
func decisionContract() string {
	return "Decision tools (mcp__clef__*).\n" +
		"- What: a small decision model. You send a question and the options or facts it depends on; it returns numbers (a probability, weights, an ordering, a confidence) and never prose. It sees only the text you send, so it cannot read files or run anything.\n" +
		"- When: you are unsure and the answer is a choice. A yes/no gate: decide_boolean, or gate_decision when a weak answer must not proceed. One pick from a fixed list (a label, a severity, a next step): decide_enum. How close the options are: weigh_options. An order or a shortlist of candidates: rank_options, top_k_options, compare_pair, rank_pairwise for a few close ones. Several questions about the same facts: decide_batch, one call instead of many.\n" +
		"- Why: it is faster and steadier than reasoning your way to a pick, and it says how sure it is, which your own reasoning does not. Reach for it when you would otherwise guess or deliberate over the options.\n" +
		"- How: write a self-contained question and put the facts it depends on in context. Treat the result as evidence, not an order: act on a high confidence, and on a low confidence or an \"uncertain\" gate gather more facts or ask a person.\n" +
		"- Not for: anything reading or running a command settles (check the file, run the test), or deciding that a command is safe to run when a permission rule or the user already answers it.\n"
}
