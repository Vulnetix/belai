package tools

// WebFetchResult constructs an untrusted WebFetch tool result.
func WebFetchResult(content string) Result {
	return Result{Kind: KindWebFetch, Content: content}
}
