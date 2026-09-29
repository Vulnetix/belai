//go:build !belai_voice

package voice

// embeddedModel is empty in a build without the belai_voice tag: the model is
// then a file on disk, fetched by /voice download.
var embeddedModel []byte
