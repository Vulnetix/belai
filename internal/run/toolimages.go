package run

import (
	"github.com/vulnetix/belai/internal/wire"
)

// A tool that captures an image (Screenshot) returns it as an image
// attachment on its tool turn. The bytes were admitted by internal/imageguard
// (decoded, bounded, re-encoded), never by the text classifier. This file is
// where those bytes are shaped for a request: only the newest image is sent,
// and a model without image input is told so in the harness's own words.

const (
	// imageNoVisionNote replaces an image the model cannot take.
	imageNoVisionNote = "\n[harness: an image was captured but this model does not accept image input, so it was not sent]"
	// imageEarlierNote replaces an older image, so a long session does not
	// pay for every screenshot it ever took.
	imageEarlierNote = "\n[harness: an earlier image is not sent again]"
)

// imageAttachments returns the image attachments of atts.
func imageAttachments(atts []Attachment) []Attachment {
	var out []Attachment
	for _, a := range atts {
		if a.Kind == AttachmentImage && len(a.Data) > 0 {
			out = append(out, a)
		}
	}
	return out
}

// prepareToolImages returns turns with tool-turn images fitted to the model.
// It never changes the conversation it was given: a turn it edits is copied.
// With vision false every tool image is dropped and the turn's text says so;
// with vision true only the newest tool turn that has images keeps them.
func prepareToolImages(turns []Turn, vision bool) []Turn {
	newest := -1
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == "tool" && len(imageAttachments(turns[i].Attachments)) > 0 {
			newest = i
			break
		}
	}
	if newest < 0 {
		return turns
	}
	out := make([]Turn, len(turns))
	copy(out, turns)
	for i := range out {
		if out[i].Role != "tool" || len(imageAttachments(out[i].Attachments)) == 0 {
			continue
		}
		if vision && i == newest {
			continue
		}
		note := imageEarlierNote
		if !vision {
			note = imageNoVisionNote
		}
		out[i].Content += note
		out[i].Attachments = nil
	}
	return out
}

// toolImages returns the image inputs of a tool turn for the chat surface.
func toolImages(t Turn) []wire.ImageInput {
	var out []wire.ImageInput
	for _, a := range imageAttachments(t.Attachments) {
		out = append(out, wire.ImageInput{MediaType: a.MediaType, Data: a.Data})
	}
	return out
}
