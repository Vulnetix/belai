package config

// WebFetchSettings configures what a session keeps of the pages it fetches
// (docs/web-fetch.md): a short-lived in-memory cache of admitted page text,
// and an in-memory search index over the same pages.
//
// Neither changes what is admitted. A cache hit takes the same sanitise and
// classify path as a fresh fetch, and an indexed chunk was classified when it
// was indexed, so any settings layer may set these keys; sizes are clamped to
// fixed ceilings so a layer cannot ask for unbounded memory.
type WebFetchSettings struct {
	// Cache turns the per-session WebFetch cache on. Default on.
	Cache *bool `json:"cache,omitempty"`
	// CacheTTLSeconds is how long a cached page is served.
	CacheTTLSeconds *int `json:"cache_ttl_seconds,omitempty"`
	// Index turns the per-session search index (the SearchFetched tool) on.
	// Default on.
	Index *bool `json:"index,omitempty"`
	// IndexTokens is the estimated size the index may hold in all.
	IndexTokens *int `json:"index_tokens,omitempty"`
}

// WebFetch cache and index defaults and ceilings.
const (
	DefaultWebFetchCacheTTLSeconds = 900
	MaxWebFetchCacheTTLSeconds     = 3600
	minWebFetchCacheTTLSeconds     = 10
	DefaultWebFetchIndexTokens     = 60000
	MaxWebFetchIndexTokens         = 200000
	minWebFetchIndexTokens         = 2000
)

// WebFetchCacheEnabled reports whether fetched pages are cached for the
// session. Default on.
func (s Settings) WebFetchCacheEnabled() bool {
	return s.WebFetch == nil || s.WebFetch.Cache == nil || *s.WebFetch.Cache
}

// WebFetchCacheTTLSeconds is the effective cache lifetime, clamped to a sane
// range.
func (s Settings) WebFetchCacheTTLSeconds() int {
	n := DefaultWebFetchCacheTTLSeconds
	if s.WebFetch != nil && s.WebFetch.CacheTTLSeconds != nil && *s.WebFetch.CacheTTLSeconds > 0 {
		n = *s.WebFetch.CacheTTLSeconds
	}
	return min(max(n, minWebFetchCacheTTLSeconds), MaxWebFetchCacheTTLSeconds)
}

// WebFetchIndexEnabled reports whether fetched pages are indexed for search.
// Default on.
func (s Settings) WebFetchIndexEnabled() bool {
	return s.WebFetch == nil || s.WebFetch.Index == nil || *s.WebFetch.Index
}

// WebFetchIndexTokens is the effective size cap of the search index, clamped.
func (s Settings) WebFetchIndexTokens() int {
	n := DefaultWebFetchIndexTokens
	if s.WebFetch != nil && s.WebFetch.IndexTokens != nil && *s.WebFetch.IndexTokens > 0 {
		n = *s.WebFetch.IndexTokens
	}
	return min(max(n, minWebFetchIndexTokens), MaxWebFetchIndexTokens)
}

// mergeWebFetch layers src over dst field by field.
func mergeWebFetch(dst, src *WebFetchSettings) *WebFetchSettings {
	if src == nil {
		return dst
	}
	out := WebFetchSettings{}
	if dst != nil {
		out = *dst
	}
	if src.Cache != nil {
		out.Cache = src.Cache
	}
	if src.CacheTTLSeconds != nil {
		out.CacheTTLSeconds = src.CacheTTLSeconds
	}
	if src.Index != nil {
		out.Index = src.Index
	}
	if src.IndexTokens != nil {
		out.IndexTokens = src.IndexTokens
	}
	return &out
}
