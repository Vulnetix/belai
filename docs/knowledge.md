# Knowledge

Belai can search reference material by meaning, with the file tools an agent
already uses. An agent that calls `Grep`, `Glob` or `Read` gets its usual
filesystem answer, and beside it the passages from two kinds of knowledge that
resemble what it asked for.

- **Profile knowledge** is the documents an [agent profile](agent-profiles.md)
  lists: a handbook, a style guide, runbooks, a standards folder.
- **Project knowledge** is the scanner output the Vulnetix tools leave in the
  project's `.vulnetix` directory (SARIF, CycloneDX, OpenVEX and the rest), and
  the files you attach to a session with `@`.

Everything runs in the Belai process in pure Go. There is no cgo, no embedding
model to download and no service to call, so it works offline and in every
release build.

## Who can search what

| Knowledge | Searched by |
| --- | --- |
| A profile's documents | A fleet worker running that profile, a background agent running it, and the foreground session while that agent is engaged, in agent, plan and goal mode |
| The project's `.vulnetix` output | Every session in the project: foreground in any mode, fleet workers, background agents, headless and ACP sessions |
| Files attached with `@` | The foreground session that attached them, until it ends |

A handoff subagent restricted to the paths a plan names does not get knowledge,
because its reads are limited to those paths. Explore subagents read the
checkout only.

Retrieval rides on the three file tools, so it follows the same limits as they
do. A profile whose `tools` list leaves out `Grep` has no knowledge through
`Grep`, and a worker with a deny rule on a path cannot read that path through a
search either (see [Safety](#safety)).

## What the tools return

A model's own calls add a labelled block after the filesystem result. A harness
call, such as the file Belai reads for an `@` attachment, gets the filesystem
alone.

**`Grep`** also searches knowledge when the search is not narrowed: no `path`,
no `glob`, no `type`, and not `count` mode. The pattern's words are the query.
Matching passages come back as rows in the same `path:line:text` shape, under a
header, with the address of the document in place of a path:

```
src/auth.go:42:func rotateKey() {
[knowledge: passages from reference documents, ranked by similarity rather than exact match; rows are kb+address:line:text]
kb+handbook/security/keys.md:12:Rotate the signing key every ninety days.
kb+handbook/security/keys.md:13:Revoke refresh tokens when a key is rotated.
kb+project/.vulnetix/sast.20260901101500.sarif:7:SARIF result rule VNX-GO-SQLI severity high in db/query.go line 40
```

In `files_with_matches` mode it lists the addresses only. The filesystem search
and the knowledge search run at the same time.

**`Glob`** lists knowledge documents as well, unless a `path` narrows it. A
document is listed when its address matches the pattern, or, marked
`(similar)`, when its text resembles the pattern's words:

```
kb+project/.vulnetix/sast.20260901101500.sarif
kb+handbook/security/keys.md  (similar)
```

**`Read`** of a file appends up to three passages that resemble the part of the
file it returned, when they are close enough to be worth showing, under the
header `[Related knowledge: …]`. A passage from the file's own indexed copy is
left out.

Addresses look like `kb+<scope>/<path>`: the scope is the profile's name,
`project`, or `session`. They are not files on disk and are not read with
`Read`.

## Profile knowledge

List the documents in the profile file, which only you write:

```json
{
  "name": "handbook-reviewer",
  "knowledge": {
    "paths": ["~/handbook", "/srv/standards/api-guidelines.md"]
  }
}
```

Each path is a file or a directory, absolute or starting with `~/`, up to 32 of
them. A directory is walked with the same rules the `/locate` inventory uses:
`.gitignore` and `.ignore` apply, and symlinks, hidden files, dependency
directories, binary files, files over 16 MiB and files that hold credentials
(`.env`, key stores, `credentials*`, `id_rsa*`) are never indexed. A listed path
that is itself a symlink is skipped.

The listed documents stay on this host. A profile installed from the Vulnetix
library cannot carry the block (the install is refused, because a library copy
must not choose which local files get indexed), a backup leaves it out, and
replacing a profile from the library keeps the paths you listed here.

## Project knowledge

Belai reads `<project>/.vulnetix` itself, at a fixed place, and never takes a
path from a model. The index follows what the Vulnetix tools write:

| File | Indexed as |
| --- | --- |
| SARIF (`*.sarif`) | one passage per result: rule id, severity, file and line |
| CycloneDX (`*.cdx.json`) | one passage per component (name, version, purl) and per vulnerability (id, severity, affected references) |
| OpenVEX (`vex*.json`) | one passage per statement: vulnerability, product, status, justification |
| `memory.yaml`, `capabilities.yaml`, package scans, the analyze report, and other `.json`, `.yaml`, `.md` and `.txt` files | text, in passages of about 200 tokens |

A structured passage is composed from identifier fields only. It never holds a
result's message, a code snippet or a matched secret value. Native third-party
reports (a secret scanner's findings hold the secrets), tool logs, symlinks,
and Belai's own state under `.vulnetix` are never indexed. Only the newest copy
of a repeated scan is.

The index is refreshed in the background when a session starts and at the start
of each turn, at most every twenty seconds, so a scan you ran a moment ago is
searchable within a turn or two. A file that has gone leaves the index at the next refresh. A
fleet worker uses the project's own `.vulnetix`, not its worktree's.

Files attached with `@` are indexed for the session only. They are added after
Belai admits the attachment, are not written to disk, and go when the session
ends. They share the project size limit with the `.vulnetix` output, which is
indexed first.

## Settings

```json
{
  "knowledge": {
    "max_index_tokens": 200000,
    "max_project_tokens": 200000,
    "max_result_tokens": 3000
  }
}
```

These size the store on this host, in estimated tokens (four characters each).
They are about memory and disk and the time an index takes to build, not about
what you are charged.

| Key | Default | Range | Project layer |
| --- | --- | --- | --- |
| `knowledge.max_index_tokens` | `200000`; the most one agent profile keeps indexed | 1000 to 5000000 | dropped |
| `knowledge.max_project_tokens` | `200000`; the most the project keeps indexed (`.vulnetix` output and `@` files together) | 1000 to 5000000 | dropped |
| `knowledge.max_result_tokens` | `3000`; the most one search returns, across every index | 200 to 50000 | dropped |

A value outside its range fails settings resolution with a message naming the
key. A repository cannot raise a limit, so the whole `knowledge` key is ignored
in a project's `.vulnetix/settings.json`. Set it in your global `settings.json`
or in `/settings`, which has three rows in the *Knowledge* group, always saved
to your global file: `knowledge per profile`, `knowledge for project` and
`knowledge per search`. `x` returns a row to its default.

When a corpus is larger than its limit, documents are taken in order and the
rest are left out. `belai agent knowledge` says so, and lowering a limit trims
an existing index on the next load.

## Command

```
belai agent knowledge [-index] [-json] [NAME]
```

Prints each index with its size against its limit and one row per document:
address, passages, tokens, and how many were dropped at ingestion. It prints
counts and addresses, never passage text. With a profile `NAME` it shows that
profile's documents as well as the project's. With `-index` it first brings the
indexes up to date, which sends new text through the security classifier (see
below); `-trust-dir`, `-provider` and `-model` apply to that step. A profile
needs an id, which `belai agent import -force` gives it.

## How it works

- **Passages.** Text is cut into passages of about 200 tokens on line
  boundaries, with a small overlap, each keeping its line range.
- **Vectors.** A passage becomes a hashed vector: its words, word pairs and
  character trigrams are hashed into a 65536-dimension space, weighted by how
  rare each is across the corpus, and scaled to unit length. A query is turned
  into a vector the same way. Because trigrams are part of it, `authenticate`
  finds `authentication`. It matches on shared vocabulary, so it will not find a
  passage that says the same thing in different words.
- **Index.** Vectors are searched through an inverted index, and a result is a
  cosine similarity. The corpus is bounded by the limits above, so a search is
  an in-memory lookup that takes milliseconds.
- **Storage.** Each index is one file in Belai's state directory:
  `knowledge/profiles/<profile id>/index.bin` and
  `knowledge/projects/<hash of the project root>/index.bin`, mode 0600. The file
  carries a magic, a version and a SHA-256 trailer. A file that fails the check
  is not used and not overwritten, and the session starts with an empty index
  and a warning. The state directory is hidden from every sandboxed command, so
  a model reaches the text only through the three file tools.
- **Updates.** A document is read again only when its size or modification time
  changes, and its passages are classified again only when its SHA-256 changes.

## Safety

- **Classified once, at ingestion.** Each passage is sanitised (delimiter
  markup, control and bidirectional characters removed) and sent through the
  security classifier before it is stored. Document passages are classified as
  file text and scanner passages as third-party text, in batches that are split
  until a flagged passage stands alone. A flagged passage is never stored, and
  a classifier that cannot answer stores nothing for that document. A search
  afterwards looks up text that was already admitted, so it calls no model.
- **Guardrails off** sends nothing to the classifier at ingestion either, as
  everywhere else. The text is still sanitised.
- **`Read` stays classified.** Its result is a `Read` result, so the related
  passages go through the classifier again with the file's own text.
- **Permission rules apply to the source.** A passage whose source file a
  `Read` deny rule blocks is not returned, and a document it blocks is not
  listed. The rule is matched against the absolute path and, under a workspace
  root, the root-relative one.
- **Local only.** Profile documents come from paths in your own profile file.
  The project's index is keyed by the trusted repository root, never a worktree.
  Telemetry, the audit log and session sync carry no passage text.

## Business rules

Every rule names the test that holds it. `internal/knowledge/docparity_test.go`
fails when a test named here does not exist, and when a test in the knowledge
test files is not named here, so this table and the code move together.

| ID | Rule | Tests |
| --- | --- | --- |
| K1 | Retrieval rides only on a model's own `Grep`, `Glob` or `Read` call. A harness read (an `@` attachment, a prefetched file) gets the filesystem alone | `TestHarnessReadGetsNoKnowledge`, `TestGrepAddsKnowledgeRowsOnlyForAModelCall` |
| K2 | `Grep` adds knowledge rows only when it is not narrowed: no `path`, `glob` or `type`, and not `count` mode | `TestGrepKnowledgeIsSkippedWhenScopedOrCounted` |
| K3 | In `files_with_matches` mode `Grep` lists document addresses only | `TestGrepFilesModeListsDocumentAddresses` |
| K4 | The query is the pattern's words: regular expression syntax is stripped | `TestKnowledgeQueryStripsRegexSyntax` |
| K5 | A knowledge row is clipped to 200 characters and numbered by its line range; one search returns at most `knowledge.max_result_tokens` across every index | `TestKnowledgeRowsClipAndNumber`, `TestSearchTokenCapAndAllow` |
| K6 | `Glob` lists a document when its address matches the pattern, or, marked `(similar)`, when its text resembles the pattern's words; a `path` turns knowledge off | `TestGlobListsMatchingAndSimilarDocuments`, `TestGlobFindsKnowledgeDocuments` |
| K7 | `Read` appends up to three passages that resemble what it returned and score at least 0.25, never ones from the file's own indexed copy or that repeat its text | `TestReadAppendsRelatedPassages`, `TestReadSkipsItsOwnSourceAndVerbatim`, `TestReadAppendsRelatedKnowledgeForAModelCall` |
| K8 | A `Read` deny rule on a passage's source file hides that passage and its document | `TestReadDenyRuleHidesTheSourceFromRetrieval` |
| K9 | Rows reach the model with no classifier call: text was classified once, at ingestion | `TestGrepRowsReachTheModelWithoutAClassifierCall`, `TestKnowledgeRowsReachGrepClassifiedOnceAtIngestion` |
| K10 | One hub serves every narrowing of the file tools, so a restricted registry shares the same store | `TestHubIsSharedByEveryNarrowing` |
| K11 | A profile lists up to 32 paths, each absolute or starting `~/`; unknown keys in the block and unreadable paths are refused; the block survives the markdown form and does not change what the agent may do | `TestKnowledgeLimitMatchesTheStore`, `TestKnowledgeAcceptsAbsoluteAndHomePaths`, `TestKnowledgeRefusesWhatItCannotRead`, `TestKnowledgeRejectsUnknownKeysInsideTheBlock`, `TestKnowledgeSurvivesMarkdownAndIsBehavioural` |
| K12 | A directory is walked with the `/locate` rules: only eligible files are indexed, a symlinked root is refused and a missing path is skipped | `TestSyncProfileIndexesEligibleFilesOnly`, `TestSyncProfileRefusesSymlinkRootAndSkipsMissing` |
| K13 | A profile installed from the library that lists documents is refused, a backup leaves the block out and a replace keeps the paths listed on this host | `TestInstallRefusesAProfileThatListsLocalDocuments`, `TestBackupLeavesDocumentsOutAndAReplaceKeepsThem` |
| K14 | The engaged agent's documents are searchable in agent, plan and goal mode, follow a swapped agent, and the session build carries the store | `TestEngagedProfileDocumentsFollowTheEngagedAgentInEveryMode`, `TestSetFollowsASwappedProfile`, `TestSessionBuildCarriesTheKnowledgeStore` |
| K15 | The project index comes from `<project>/.vulnetix` at a fixed place: structured artifacts become one passage per record, composed from identifier fields only, and secrets, logs and Belai's own state are never indexed | `TestSyncProjectStructuredAndSkipsSecrets`, `TestRecordsSARIFOnePerResultAndNoMessage`, `TestRecordsCycloneDXComponentsAndVulns`, `TestRecordsOpenVEXAndOtherKinds` |
| K16 | Only the newest copy of a repeated scan is indexed, and a newer scan replaces it at the next refresh | `TestSyncProjectIndexesOnlyTheNewestCopyOfARepeatedScan` |
| K17 | The project index refreshes in the background at session start and each turn, at most every 20 seconds; a forced refresh during a run is queued | `TestKnowledgeRefreshIntervalIsTwentySeconds`, `TestRefreshAsyncIsThrottled`, `TestForcedRefreshDuringARunIsQueued` |
| K18 | A document is read again only when its size or modification time changes, and its passages are classified again only when its SHA-256 changes | `TestUnchangedFilesAreNotReadAgain`, `TestIngestIsIncrementalOnSHA` |
| K19 | A file that has gone leaves the index at the next refresh | `TestRetainRemovesGoneDocuments`, `TestSyncProfileIsIncrementalAndDropsGoneFiles`, `TestSyncProjectDroppedDirectoryDropsChunks` |
| K20 | Files attached with `@` are indexed for the session only, after the attachment is admitted, and share the project limit with the `.vulnetix` output | `TestAttachedFilesAreIndexedForTheSessionOnly`, `TestAddTextSessionFile`, `TestStoreSessionFilesShareTheProjectCap` |
| K21 | The three size settings have defaults and ranges, name the key when out of range, and are the user's alone (a project layer's `knowledge` key is dropped) | `TestKnowledgeLimitsDefaultAndOverride`, `TestValidateKnowledgeNamesTheKey`, `TestKnowledgeIsPerUser`, `TestKnowledgeGlobalOutOfRangeFailsResolve` |
| K22 | `/settings` has three rows in the Knowledge group, always saved to the global file, which reject an out-of-range value and return to the default on `x` | `TestSettingsKnowledgeRowsDefaultAndGrouped`, `TestSettingsKnowledgeIsSavedGlobalWhateverTheScope`, `TestSettingsKnowledgeRejectsOutOfRangeAndClears` |
| K23 | When a corpus is larger than its limit documents are taken in order and the rest are left out; lowering a limit trims an existing index | `TestCapStopsIngestion`, `TestSyncProfileCap`, `TestSetLimitsTrimsAndGatesCanBeReplaced` |
| K24 | `belai agent knowledge` prints counts and addresses, never passage text, and needs a profile that lists documents | `TestAgentKnowledgeStatusListsAddressesAndNoText`, `TestAgentKnowledgeNeedsAProfileThatListsDocuments` |
| K25 | Passages are about 200 tokens cut on line boundaries with their line range kept; words are split on names and filler is dropped | `TestSplitTextKeepsLineRanges`, `TestSplitTextLongLineAndBlank`, `TestTermsSplitsNamesAndDropsFiller` |
| K26 | The vector for a text is deterministic; the best passage ranks first; related word forms meet | `TestEmbeddingIsDeterministic`, `TestSearchRanksTheRelevantDocumentFirst`, `TestSearchMeetsRelatedWordForms` |
| K27 | A set searches every index it holds and a nil set is inert | `TestSetMatchAndNil` |
| K28 | Passages are sanitised (delimiter markup removed), classified before they are stored, and a flagged passage is never stored | `TestGateDropsFlaggedChunksAndFailsClosed`, `TestIngestSanitisesDelimiterMarkup`, `TestGateIsBatchedForManySmallChunks` |
| K29 | A classifier that cannot answer stores nothing for the document | `TestStoreFailsClosedWhenTheGateErrors`, `TestClassifierFailureFailsClosed` |
| K30 | With guardrails off nothing is sent to the classifier; the text is still sanitised | `TestGuardrailsOffNeverCallsTheClassifier`, `TestKnowledgeIngestionSendsNothingWhenGuardrailsAreOff`, `TestNilLevelsIsTheOrdinaryPath` |
| K31 | An index file round trips with its checksum; a missing one is empty; a symlinked one is refused | `TestStoreRoundTripAndIntegrity`, `TestStoreMissingIsEmptyAndSymlinkRefused`, `TestStoreRefreshSearchAndReopen` |
| K32 | A profile's directory is keyed by its id, never its name; anything that is not a lowercase UUID is refused | `TestProfileKnowledgeDirRefusesNonIDs` |
| K33 | With no store, or no profile, every tool is filesystem only | `TestStoreNilAndNoProfileAreInert`, `TestNoStoreMeansFilesystemOnly`, `TestGrepWithoutAStoreIsUnchanged` |
| K34 | A knowledge address is plain text of the form `kb+scope/path`, never a path that exists | `TestAddressIsPlain` |

## Edge cases

| ID | Case | Behaviour | Test |
| --- | --- | --- | --- |
| E1 | There is no `.vulnetix` directory, or it is a symlink | An absent directory leaves the project index empty; a symlinked or non-directory one is refused with an error and nothing is indexed | `TestSyncProjectNoDirAndSymlinkDir` |
| E2 | The `.vulnetix` directory is deleted after it was indexed | Its passages leave the index at the next refresh | `TestSyncProjectDroppedDirectoryDropsChunks` |
| E3 | The index file is corrupt | It is not used and not overwritten: the session starts with an empty index and a warning | `TestStoreCorruptIndexIsEmptyWarnedAndNotOverwritten` |
| E4 | A passage appended to a `Read` quotes a read trailer (`[Read: lines 1–2 of 9; …]`) | The repeated-read bookkeeping looks only at the file's own text, so a whole-file read is still recorded as whole | `TestRecordReadIgnoresATrailerQuotedByAPassage` |
| E5 | A `Grep` result is withheld by the guardrails | The knowledge rows beside it are left alone: they were admitted at ingestion | `TestWithholdGrepLeavesKnowledgeRowsAlone` |
| E6 | A scanner artifact is not valid JSON | `Records` returns an error; an artifact of any other kind returns nothing | `TestRecordsMalformedIsAnError`, `TestRecordsOpenVEXAndOtherKinds` |
| E7 | Many tiny passages | They are classified in batches, split until a flagged one stands alone | `TestGateIsBatchedForManySmallChunks` |
| E8 | A listed path is missing | It is skipped; the rest are indexed | `TestSyncProfileRefusesSymlinkRootAndSkipsMissing` |
| E9 | A profile or file name has spaces, `..` or control characters | The address is normalised to plain text (`../a/./b\x01c.md` under `my profile:x` becomes `kb+my-profile-x/a/b_c.md`), and an ordinary path is never mistaken for an address | `TestAddressIsPlain` |
| E10 | A profile is swapped mid-session | The store follows the engaged agent at the next turn | `TestSetFollowsASwappedProfile` |
