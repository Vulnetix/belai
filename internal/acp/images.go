package acp

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/imageguard"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
)

// maxPromptImages bounds the images one prompt can carry, as in the TUI.
const maxPromptImages = 8

// imagePlaceholder is the prompt text for a prompt that is only images. It is
// harness text, so no words are put in the user's mouth.
const imagePlaceholder = "(image attached)"

// promptImages admits the image blocks of an editor prompt. Editor content is
// untrusted, so each image passes the same imageguard admission as any other
// attached image, and never a classifier. The declared media type is ignored:
// the decoder decides what the bytes are. A block with a uri and no data is
// refused, because Belai never fetches an address an editor names. It returns
// the admitted attachments, a marker per image for the transcript (no bytes),
// and one harness note for each refused image.
func promptImages(blocks []contentBlock) (atts []run.Attachment, markers []map[string]any, notes []string) {
	n := 0
	for _, c := range blocks {
		if c.Type != "image" {
			continue
		}
		n++
		fail := func(why string) {
			notes = append(notes, fmt.Sprintf("image %d: %s", n, sanitize.Line(why, 120)))
		}
		if len(atts) >= maxPromptImages {
			fail(fmt.Sprintf("at most %d images per prompt", maxPromptImages))
			continue
		}
		if c.Data == "" {
			fail("no image data (an image referenced only by address is not fetched)")
			continue
		}
		// Refuse by encoded length before allocating the decoded bytes.
		if len(c.Data) > base64.StdEncoding.EncodedLen(imageguard.Default.MaxBytes) {
			fail(fmt.Sprintf("image is over the %d MiB limit", imageguard.Default.MaxBytes>>20))
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.Data))
		if err != nil {
			if raw, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(c.Data)); err != nil {
				fail("the image data is not valid base64")
				continue
			}
		}
		adm, err := imageguard.Admit(raw, imageguard.Default)
		if err != nil {
			fail(err.Error())
			continue
		}
		name := fmt.Sprintf("image-%d.png", len(atts)+1)
		atts = append(atts, run.Attachment{Kind: run.AttachmentImage, Label: "@" + name, MediaType: imageguard.MediaType, Data: adm.PNG})
		markers = append(markers, map[string]any{
			"name": name, "media_type": imageguard.MediaType,
			"width": adm.Width, "height": adm.Height,
			"bytes": len(raw), "tokens": run.ImageTokens(adm.PNG),
		})
	}
	return atts, markers, notes
}

// imageNotesDirective tells the model, in harness words, which images the
// editor sent were not admitted. It is a sealed directive, not prompt text.
func imageNotesDirective(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return "Images the editor attached were not admitted and are not in this turn: " +
		strings.Join(notes, "; ") + ". Do not ask for them again; answer from what is present."
}
