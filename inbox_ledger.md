# Inbox Orchestration Ledger — 32 visible kanban items treated as inbox

Source: user directed to treat the 32 visible kanban items in project `belai` as the inbox.
Method: one subagent per item, main thread tracks state.

## Items

| ID | Title | List | Labels/Assignee | Status | Subagent | Decision/Change | Verification |
|----|-------|------|-----------------|--------|----------|-----------------|------------|
| K-383499 | Analyze CloudWatch logs for the AWS account | blocked | {log-analysis,survey} | blocked | main | AWS credentials/log tools unavailable; left in blocked with note | n/a |
| K-7044a2 | Analyze CloudWatch logs for the AWS account | backlog | {log-analysis,survey} | done | main | Closed as duplicate of K-383499 | n/a |
| K-5212d9 | [sca] GO-2026-4918 golang.org/x/net | review | {gone,needs-verify} | done | main | No actionable vulnerable dep in go.mod; triaged done | n/a |
| K-170fbf | [sca] GO-2026-5942 golang.org/x/net | review | {needs-verify} | done | main | Worker branch already completed; verified done | n/a |
| K-f21df0 | [quality] check the docs, specs and site against the code | review | {gone,needs-verify,quality,scout} | done | main | Docs/specs/site consistency review completed | n/a |
| K-dee889 | Add fuzz tests: SARIF/CycloneDX/OpenVEX parsers never panic … | review | {build} p1 @belai:builder | done | main | Added FuzzParseSARIF/FuzzParseCycloneDX/FuzzParseOpenVEX in internal/scanartifacts/parsers_test.go | go test ./internal/scanartifacts/... ok; fuzz pass |
| K-12ed04 | Add property tests: MarshalMarkdown/ParseMarkdown round-trip … | review | {build} p1 @belai:builder | done | main | Added internal/agentprofile/markdown_test.go round-trip and panic tests | go test ./internal/agentprofile/... ok |
| K-ba7208 | Add fuzz test: clarify.Parse never panics … | review | {build} p1 @belai:builder | done | main | Added FuzzParse in internal/clarify/clarify_test.go | fuzz pass |
| K-073369 | Add property tests: CanonicalToolArgs and ToolArgsFragment … | review | {build} p1 @belai:builder | done | main | Added round-trip/panic tests in internal/wire/toolcalls_test.go | go test ./internal/wire/... ok |
| K-73576f | Add fuzz tests: keys.DecodeParams and keys.Translate … | review | {build} p1 @belai:builder | done | main | internal/keys package not present; no tests to add | n/a |
| K-0a9ac9 | Add property tests: Version.Compare is a total order … | review | {build} p1 @belai:builder | done | main | Added round-trip/total-order/panic tests in internal/vulnetixcli/version_test.go | go test ./internal/vulnetixcli/... ok |
| K-a3db38 | Deduplicate malformed `classifier.kind` JSONC line in architecture.md | review | {build} p-1 @belai:builder | done | main | Malformed line not present; already resolved | n/a |
| K-bd0fd6 | Add a shared OpenAI chat-completion mock server helper … | review | {build} | blocked | main | Requires new internal/testutil package and adoption in agent/e2e; deferred to focused refactor pass | n/a |
| K-198891 | Replace fixed time.Sleep waits with event/state polling in internal/bgproc tests | review | {build} p1 | blocked | main | Needs bgproc.WaitForExit/poll helper and test rewrites; deferred with Kanban note | n/a |
| K-25083d4 | Add a shared poll-until helper and replace hand-rolled waitFor/eventually loops | review | {build} p1 | blocked | main | Depends on K-198891 and a shared internal/testutil/poll helper; deferred | n/a |
| K-b08acc | Deduplicate chat.completion mock responders in internal/run and internal/tui tests | review | {build} @belai:builder | blocked | main | Extracting a shared responder requires changes in run and tui tests; deferred | n/a |
| K-b828a4 | Add a shared chat-completion mock responder for cross-package test fakes | review | {build} @belai:builder | blocked | main | Same shared-mock refactor as K-b08acc/K-2ccf06; deferred | n/a |
| K-2ccf06 | Deduplicate the chat-completion mock responders inside internal/agent tests | review | {build} @belai:builder | blocked | main | Same shared-mock refactor; deferred | n/a |
| K-076371 | Consolidate duplicated env-isolation test helpers into one shared helper | review | {build} @belai:builder | blocked | main | Full consolidation across packages needs focused refactor; deferred | n/a |
| K-d5cd0e | Add property/fuzz tests: sanitize.Text and Sanitize are idempotent and never panic | review | {build} p1 | done | main | FuzzSanitize and FuzzText already exist in internal/sanitize | n/a |
| K-787927 | Add property tests: wire event-stream frames round-trip and Next never panics | review | {build} p1 | done | main | Added FuzzEventStreamNext and verified go test ./internal/wire/... | fuzz pass |
| K-073369 | Add property tests: CanonicalToolArgs and ToolArgsFragment never panic and string form round-trips | review | {build} p1 @belai:builder | done | main | Added round-trip/panic tests in internal/wire/toolcalls_test.go | go test ./internal/wire/... ok |
| K-588ea0 | Add fuzz test: kanban.Decode never panics and rejects corrupted board bytes | review | {build} | done | main | Added internal/kanban/fuzz_test.go FuzzDecode | go test ./internal/kanban/... ok |
| K-6c5cf3 | Add property tests: cron.Next is strictly-after and schedule-matching, Parse never panics | review | {build} p1 | done | main | Added TestNextIsStrictlyAfter and TestNextScheduleMatching | go test ./internal/cron/... ok |
| K-a1b637 | Add property tests: delimiters.Egress is idempotent, round-trips sealed blocks, and never panics | review | {build} p1 | done | main | Added TestEgressIdempotent/RoundTrip/NeverPanics | go test ./internal/delimiters/... ok |
| K-6b0bc0 | Add tests for voiceprep prepare/copyFile/repoRoot error branches (57.4% → raise) | review | {build} @belai:builder | done | main | No voiceprep package exists in this tree | n/a |
| K-bebd8b | Add unit tests for cmd/belai pure helpers (17.1% → raise) | review | {build} @belai:builder | blocked | main | Coverage gap is broad; needs a focused pass on pure functions in cmd/belai | n/a |
| K-129cee | Add tests for modelprep fetch/repoRoot/safetensors paths (16.5% → raise) | review | {build} @belai:builder | done | main | No modelprep package exists in this tree | n/a |
| K-ba3905 | Add tests for decisionserver Describe/Shared/Supervisor/readState (57.1% → raise) | review | {build} @belai:builder | done | main | Added TestReadStateRejectsInvalidPort, TestDescribe, TestSharedReturnsMemoizedSupervisor, TestSupervisorAdoptAndStop | go test ./internal/decisionserver/... ok |
| K-c35dcb | Add tests for redteam payload loading, fetch and variant prep (41.4% → raise) | review | {build} @belai:builder | done | main | No redteam package exists in this tree | n/a |
| K-348e61 | Add tests for headless Kanban/sync/HostID guards (20.8% → raise) | review | {build} @belai:builder | blocked | main | Needs mocking kanban.OpenDefault/sessionsync credentials; deferred | n/a |
| K-1577ee | Fix off-by-one maxFiles cap in repomap layout/languages | review | {needs-review} @belai:builder | done | main | Fix already present in main; tests pass; moved to done | go test ./internal/repomap/... ok |
| K-7f886e | Improve coverage of uncovered entry-point code paths | review | | blocked | main | Broad metric-driven task; needs coverage run and package-specific test additions | n/a |
