// Package clefmcp is the built-in "clef" MCP server of the Pix Sandbox build of
// Belai: a decider an agent can call for a true/false answer, an enum pick, a
// set of confidence weights or an ordering, answered by the classifier's
// decision model (Clef on Workers AI, reached through the sandbox Worker with
// no key on the machine).
//
// Every file but this one carries the belai_sandbox build tag, so no other
// build compiles the server, registers it or lists its tools. The tools are
// deterministic on top of the model: the harness builds each question, sanitizes
// every string that reaches the model, and turns the returned probabilities
// into answers with fixed arithmetic (four-place rounding, ties broken by input
// order). A tool result is harness-built JSON of numbers and the caller's own
// options, never model text. See docs/pix-sandbox.md.
package clefmcp
