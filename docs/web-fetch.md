# WebFetch cache and fetched-page search

A session that fetches the same documentation page twice, or needs a second
detail from a page it read ten turns ago, should not pay for another round trip
and another classification of a page it already holds. Two session-scoped,
in-memory features cover that. Both keep the rule that **nothing reaches the
model without being sanitised and classified**: a cache hit is not an exemption,
and a search only reads text that was classified when it was indexed.

- Code: `internal/tools/webpages.go` (the page store, the `SearchFetched` tool),
  `internal/tools/webfetch.go` (the cache lookup and staging),
  `internal/agent/webindex.go` (the index and the settle step),
  `Session.promoteResult` in `internal/agent/agent.go`,
  `internal/config/webfetch.go` (settings).
- Reuses: `internal/knowledge` (chunking, ranking, the in-memory `Index`) and
  `internal/knowledge/kbgate` (the ingestion gate).
- Settings: `web_fetch` (below).

## The cache

`WebFetch` checks and canonicalises the URL first (`internal/netguard`, the same
policy as ever), then looks the canonical URL up. The key is that canonical
form without its fragment (a server never sees it) and without a default port,
so `http://host:80/a#x` and `http://host/a` are one page.

- **A hit makes no request.** It returns the cached text as an ordinary
  `WebFetch` result, so it goes through the same sanitise step and the same
  security classifier as a fresh fetch (guardrails off skips only the
  classifier, exactly as for a fresh result). With a `prompt`, the `web_fetch`
  role answers over the cached page and the answer is classified.
- **A miss is a normal fetch.** The URL policy, the five-redirect limit, the
  per-redirect URL check and the pinned, validated dial all apply as before. A
  hit never skips the URL policy: it runs before the lookup.
- **The model is told.** A page served from the cache begins with
  `[served from this session's WebFetch cache, fetched 3m ago; the page may have
  changed since]`, or, for an answer, the note follows the answer's header. The
  line is composed by the harness (`tools.CacheNote`).
- **Only admitted pages are held.** A fresh page is *staged* with the result;
  after the session promotes the result it either admits the page into the cache
  or evicts it:
  - admitted (the classifier said proceed, or guardrails are off): the
    sanitised page text is cached;
  - withheld (flagged, a classifier error, or a warn-posture refusal): the
    staged page is dropped and anything held for that URL, in the cache and in
    the index, is evicted. A flagged result is never cached and never served
    again.
  - Only a 2xx response is staged, so an error page is never cached.
- **Bounded, in memory, per session.** At most 32 pages and 16 MiB, oldest
  first out, each served for at most `web_fetch.cache_ttl_seconds` (default 15
  minutes). Nothing is written to disk.
- **Subagents get none.** An explore, recovery or fan-out subagent builds its own
  registry, whose page store is off, so it fetches from the network and keeps
  nothing. Only a top-level session (`agent.Options.WebPages`: the TUI, a
  headless run, a background agent, a fleet worker) switches it on.

## Searching what was fetched

`SearchFetched(query)` returns the best passages from pages this session already
fetched, ranked by similarity, each as `URL:first-last (score%)` with its lines.
It is a deferred tool (load it with `ToolSearch`; the sealed tools briefing names
it), registered while `web_fetch.index` is on and `WebFetch` is on the surface.

It takes only a query. It has no URL or path argument, so it cannot be used to
read anything new: it only knows pages `WebFetch` returned and the harness
admitted.

How a page gets in:

1. After the page's `WebFetch` result is admitted, the text is split into
   chunks (`knowledge.SplitText`, about 200 tokens each), at most about 12,000
   tokens per page, and handed to an in-memory `knowledge.Index`.
2. Every chunk is sanitised and then admitted by `kbgate.New(…, KindWebFetch)`:
   the ordinary classifier pipeline, with the posture level checked before the
   classifier is called. A flagged chunk is dropped and never stored, and a
   gate error stores nothing for the page. A flagged *result* means the page is
   never handed to the index at all.
3. Classifying a long page takes classifier round trips, so ingestion runs in
   the background, one page at a time, with a small queue. A page becomes
   searchable when its chunks have been admitted; a search made earlier answers
   that nothing matches yet. A page that finds the queue full is not indexed (it
   was still returned to the model).

A search is a lookup: it calls no model and makes no request. Because every chunk
was classified when it was indexed, `SearchFetched` results are
`tools.KindFetched`, which is sanitise-only and not in `tools.classifierKinds`
(the same rule as the knowledge store's `kb+` rows; see
[Knowledge](knowledge.md)). Never put text in that kind that did not come from
this index.

Extra rules around the index:

- **Guardrails off.** With the classifier level at ignore, ingestion sanitises
  only, and the page is marked as unclassified. Once guardrails are on again its
  text is never returned from a search, and a re-fetch of the same page
  re-classifies it instead of keeping the earlier, unclassified copy.
- **Permissions.** A `WebFetch` deny rule that matches a page's URL hides it from
  search, whenever the rule was added.
- **Bounded.** The index holds at most `web_fetch.index_tokens` estimated tokens
  (default 60,000); the oldest page makes room for a new one. A search returns at
  most `knowledge.max_result_tokens`. Nothing is written to disk, and the index
  ends with the session.
- **Subagents get none**, as above.

## Settings

| Key | Default | Meaning |
| --- | --- | --- |
| `web_fetch.cache` | `true` | Cache admitted pages for the session and serve repeat fetches from memory |
| `web_fetch.cache_ttl_seconds` | `900` | How long a cached page is served (10 to 3600) |
| `web_fetch.index` | `true` | Index admitted pages and register `SearchFetched` |
| `web_fetch.index_tokens` | `60000` | Estimated size the index may hold (2,000 to 200,000) |

These change what a session re-fetches and re-reads, never what is admitted, so
any settings layer may set them, and a project file may turn either off. The
sizes are clamped to fixed ceilings, so no layer can ask for unbounded memory.

## Security summary

- Untrusted content stays untrusted: staged and cached text is sanitised; a hit
  is classified like a fresh result; a withheld result is never cached or
  indexed and evicts what was held.
- No second copy of a control: the cache calls `sanitize.Sanitize`, the index
  calls `knowledge.Index.Ingest` (which sanitises) and the existing classifier
  pipeline; URL checks stay in `internal/netguard`.
- Search is path-free and model-free; its results are classified at ingestion.
- Nothing is written to disk, logged or sent to telemetry.
