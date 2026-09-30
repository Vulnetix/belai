package run

import (
	"bytes"
	"image"
	_ "image/jpeg"
	_ "image/png"

	"github.com/vulnetix/belai/internal/models"
	"github.com/vulnetix/belai/internal/wire"
)

// An image reaches a turn in one of two ways: a capture tool returned it on a
// tool turn (Screenshot), or the user attached it to a user turn (@file,
// clipboard, an ACP image block). Either way the bytes were admitted by
// internal/imageguard (decoded, bounded, re-encoded), never by the text
// classifier. This file is where those bytes are shaped for a request: only
// the newest image is sent, and a model without image input is told so in the
// harness's own words.

const (
	// imageNoVisionToolNote replaces a captured image the model cannot take.
	imageNoVisionToolNote = "\n[harness: an image was captured but this model does not accept image input, so it was not sent]"
	// imageNoVisionUserNote replaces an image the user attached.
	imageNoVisionUserNote = "\n[harness: an image was attached but this model does not accept image input, so it was not sent]"
	// imageEarlierNote replaces an older image, so a long session does not
	// pay for every image it ever held.
	imageEarlierNote = "\n[harness: an earlier image is not sent again]"
)

// acceptsImages reports whether the configured model takes image input. A
// provider profile's declaration wins; otherwise the model id decides. Kiro
// asks its live catalogue instead and does not use this.
func (c Config) acceptsImages() bool {
	if c.Vision != nil {
		return *c.Vision
	}
	return models.Vision(c.Provider, c.Model)
}

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

// hasImages reports whether the turn carries an image.
func hasImages(t Turn) bool { return len(imageAttachments(t.Attachments)) > 0 }

// prepareImages returns turns with tool and user images fitted to the model.
// It never changes the conversation it was given: a turn it edits is copied.
// With vision false every image is dropped and the turn's text says so; with
// vision true only the newest turn that has images keeps them, whichever role
// it is.
func prepareImages(turns []Turn, vision bool) []Turn {
	newest := -1
	for i := len(turns) - 1; i >= 0; i-- {
		if hasImages(turns[i]) {
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
		if !hasImages(out[i]) {
			continue
		}
		if vision && i == newest {
			continue
		}
		note := imageEarlierNote
		if !vision {
			note = imageNoVisionToolNote
			if out[i].Role == "user" {
				note = imageNoVisionUserNote
			}
		}
		out[i].Content += note
		out[i].Attachments = imageless(out[i].Attachments)
	}
	return out
}

// imageless returns atts without its image attachments, so a turn that also
// carries a text attachment keeps it.
func imageless(atts []Attachment) []Attachment {
	var out []Attachment
	for _, a := range atts {
		if a.Kind != AttachmentImage {
			out = append(out, a)
		}
	}
	return out
}

// turnImages returns the image inputs of a turn for the chat surface.
func turnImages(t Turn) []wire.ImageInput {
	var out []wire.ImageInput
	for _, a := range imageAttachments(t.Attachments) {
		out = append(out, wire.ImageInput{MediaType: a.MediaType, Data: a.Data})
	}
	return out
}

// requestHasImages reports whether any chat message carries an image, for the
// headers a provider wants on such a request.
func requestHasImages(msgs []wire.OpenAIChatMessage) bool {
	for _, m := range msgs {
		if len(m.Images) > 0 {
			return true
		}
	}
	return false
}

// Image size, for a token estimate. Vision models bill an image by its area
// (about one token per 750 pixels for Claude, a few hundred tiles for others);
// the estimate follows that rule, floored so a tiny image is not free and
// capped where the providers cap it, and it is deliberately on the high side,
// as text estimates are.
const (
	imageTokenPixels = 750
	imageTokenFloor  = 85
	imageTokenCap    = 1600
)

// ImageTokens estimates the tokens one PNG or JPEG costs, from its header. An
// unreadable image costs the cap.
func ImageTokens(data []byte) int {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return imageTokenCap
	}
	n := cfg.Width * cfg.Height / imageTokenPixels
	return min(max(n, imageTokenFloor), imageTokenCap)
}

// turnImageTokens is the estimated cost of the images on one turn.
func turnImageTokens(t Turn) int {
	n := 0
	for _, a := range imageAttachments(t.Attachments) {
		n += ImageTokens(a.Data)
	}
	return n
}
