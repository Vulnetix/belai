package run

// environmentFacts returns harness-fetched facts about the machine a session
// runs on, rendered for the system prompt, or "" for none. It is nil in every
// build but the Pix Sandbox one, which sets it from environment_sandbox.go, so
// no other build ever reaches for the instance-metadata address or adds a line
// to the prompt.
var environmentFacts func() string
