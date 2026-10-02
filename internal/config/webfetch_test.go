package config

import "testing"

func TestWebFetchSettingsDefaultsAndClamps(t *testing.T) {
	var s Settings
	if !s.WebFetchCacheEnabled() || !s.WebFetchIndexEnabled() {
		t.Fatal("the cache and the index default on")
	}
	if s.WebFetchCacheTTLSeconds() != DefaultWebFetchCacheTTLSeconds || s.WebFetchIndexTokens() != DefaultWebFetchIndexTokens {
		t.Fatal("unset sizes take the defaults")
	}
	f := false
	huge, tiny, neg := 1<<30, 1, -5
	s.WebFetch = &WebFetchSettings{Cache: &f, Index: &f, CacheTTLSeconds: &huge, IndexTokens: &huge}
	if s.WebFetchCacheEnabled() || s.WebFetchIndexEnabled() {
		t.Fatal("false turns both off")
	}
	if s.WebFetchCacheTTLSeconds() != MaxWebFetchCacheTTLSeconds || s.WebFetchIndexTokens() != MaxWebFetchIndexTokens {
		t.Fatal("sizes are clamped to a fixed ceiling, so no layer can ask for unbounded memory")
	}
	s.WebFetch = &WebFetchSettings{CacheTTLSeconds: &tiny, IndexTokens: &tiny}
	if s.WebFetchCacheTTLSeconds() != minWebFetchCacheTTLSeconds || s.WebFetchIndexTokens() != minWebFetchIndexTokens {
		t.Fatal("sizes are clamped to a floor")
	}
	s.WebFetch = &WebFetchSettings{CacheTTLSeconds: &neg, IndexTokens: &neg}
	if s.WebFetchCacheTTLSeconds() != DefaultWebFetchCacheTTLSeconds || s.WebFetchIndexTokens() != DefaultWebFetchIndexTokens {
		t.Fatal("a non-positive size is unset")
	}
}

// Any layer may turn the cache off, and a later layer overrides field by field.
func TestWebFetchSettingsMergeByField(t *testing.T) {
	f, tr, ttl := false, true, 60
	user := &WebFetchSettings{Index: &tr, CacheTTLSeconds: &ttl}
	project := &WebFetchSettings{Cache: &f}
	got := mergeWebFetch(user, project)
	s := Settings{WebFetch: got}
	if s.WebFetchCacheEnabled() {
		t.Fatal("the project layer could not turn the cache off")
	}
	if !s.WebFetchIndexEnabled() || s.WebFetchCacheTTLSeconds() != 60 {
		t.Fatal("a field the later layer did not set was lost")
	}
	if mergeWebFetch(user, nil) != user {
		t.Fatal("a nil layer must change nothing")
	}
}
