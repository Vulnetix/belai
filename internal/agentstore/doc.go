// Package agentstore implements the read-only search builtins that recover
// prior context from other coding agents' transcript and memory stores.
//
// Every filesystem path this package touches comes from the static registry in
// registry.go, never from caller input. The SearchSessions, ReadSession and
// SearchMemory tools expose the registry but take no path argument, so they
// cannot be used as a general read primitive for arbitrary files.
//
// Session text is arbitrary content written by other models — the textbook
// prompt-injection carrier. Callers must classify results before promotion
// (the tools package marks them KindAgentStore, which is in classifierKinds).
package agentstore
