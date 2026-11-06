package rolemanager

import "strings"

// RecordCachePrefixChanged emits cache_prefix_changed to the activity feed
// (docs/prompt-cache.md): the stable system block or the tool definitions of a
// session changed between two calls with the same tool set, so the provider's
// cached prefix will be written again. part is "system" or "tools"; anything
// else is recorded as "prefix". No prompt text is recorded, only the part.
func RecordCachePrefixChanged(part string) {
	switch part {
	case "system", "tools":
	default:
		part = "prefix"
	}
	recordTimed(EventCachePrefix, "changed", part, "part="+part, 0, "", 0)
}

func cachePrefixDescription(a Activity) Description {
	part := strings.TrimSpace(field(a.Detail, "part"))
	what := "The cached request prefix"
	switch part {
	case "system":
		what = "The stable system block"
	case "tools":
		what = "The tool definitions"
	}
	return Description{
		Summary: what + " changed between two calls of this session",
		Outcome: "the provider writes the cache again on the next call",
		Tone:    ToneCaution,
		Levels:  LevelDecisions,
	}
}
