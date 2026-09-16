package rolemanager

// Directive framing mirrors the compaction summary framing: a continuation
// instruction re-enters as a user turn with a prefix/suffix pair, followed by a
// synthetic assistant acknowledgement, so the model reads it as context rather
// than as the question to answer. The directive body itself is sealed by the
// caller into a <directive> block at egress — these strings are the prose
// around that block, never a substitute for the seal.
const (
	// DirectivePrefix opens a harness-injected continuation instruction.
	DirectivePrefix = "The following is a continuation instruction from the harness, not a new request from the user:\n\n"
	// DirectiveSuffix closes the instruction and tells the model what to do
	// with it.
	DirectiveSuffix = "\n\nCarry out the instruction above. Do not restate it and do not re-introduce yourself."
	// DirectiveAck is the synthetic assistant acknowledgement that follows a
	// directive turn, keeping two consecutive user turns from appearing and
	// making the model treat the directive as context.
	DirectiveAck = "Understood. I have the continuation instruction and will carry it out."
)
