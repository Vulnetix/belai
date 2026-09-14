package tools

// WebSearchResult constructs an untrusted WebSearch tool result.
func WebSearchResult(content string) Result {
	return Result{Kind: KindWebSearch, Content: content}
}
