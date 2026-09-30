package tui

import (
	"errors"
	"fmt"
	"github.com/vulnetix/belai/internal/tui/components"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/imageguard"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// An @path that names an image is not read as text. The bytes go straight to
// internal/imageguard (decode, bound, re-encode as a new PNG) and never to the
// text classifier, which cannot read pixels. The path has already been
// resolved against a session root, and it is confined again here after
// following symlinks, so a link cannot lead the read outside the roots.

// imageExtensions are the file extensions treated as images by the @ chooser
// and by admission. Only PNG and JPEG can actually be attached; the others are
// listed so that attaching one gets a clear refusal instead of a text read of
// binary data.
var imageExtensions = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true,
	"bmp": true, "ico": true, "tif": true, "tiff": true, "avif": true,
}

// maxAttachedImages bounds the images one prompt can carry. Providers cap the
// count per request (Anthropic allows 100), and every image costs tokens on
// each round of the turn.
const maxAttachedImages = 8

// attachedImage is an admitted image: the re-encoded PNG and its pixel size.
type attachedImage struct {
	data   []byte
	width  int
	height int
}

// hasImageExtension reports whether path ends in a known image extension.
func hasImageExtension(path string) bool {
	return imageExtensions[strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))]
}

// looksLikeImage reports whether the file at abs is an image, by extension or,
// for a file with none of the known ones, by its first bytes.
func looksLikeImage(abs string) bool {
	if hasImageExtension(abs) {
		return true
	}
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	return n > 0 && strings.HasPrefix(http.DetectContentType(buf[:n]), "image/")
}

// admitImageFile reads the image at root/rel, confined to root, and admits it.
// The error text is safe to show and to hand the model: it is harness text or
// a cleaned, capped decoder reason.
func admitImageFile(root, rel string) (attachedImage, attachMeta, error) {
	confined, err := tools.SanitizePath(root, rel)
	if err != nil {
		return attachedImage{}, attachMeta{}, err
	}
	abs := filepath.Join(root, confined)
	f, err := os.Open(abs)
	if err != nil {
		return attachedImage{}, attachMeta{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return attachedImage{}, attachMeta{}, err
	}
	if !info.Mode().IsRegular() {
		return attachedImage{}, attachMeta{}, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(imageguard.Default.MaxBytes)+1))
	if err != nil {
		return attachedImage{}, attachMeta{}, err
	}
	return admitImageBytes(abs, data)
}

// admitImageBytes admits image bytes read from, or standing for, path. It is
// the one place a user-attached image is checked, whichever way it arrived.
func admitImageBytes(path string, data []byte) (attachedImage, attachMeta, error) {
	if len(data) > imageguard.Default.MaxBytes {
		return attachedImage{}, attachMeta{}, fmt.Errorf("image is over the %d MiB limit", imageguard.Default.MaxBytes>>20)
	}
	// A clear reason for the formats that cannot be attached, before the decoder
	// answers with its own.
	if len(data) > 0 {
		ct := http.DetectContentType(data[:min(len(data), 512)])
		if strings.HasPrefix(ct, "image/") && ct != "image/png" && ct != "image/jpeg" {
			return attachedImage{}, attachMeta{}, fmt.Errorf("only PNG and JPEG images can be attached (this is %s)", ct)
		}
	}
	adm, err := imageguard.Admit(data, imageguard.Default)
	if err != nil {
		return attachedImage{}, attachMeta{}, errors.New(sanitize.Line(err.Error(), 120))
	}
	meta := imageAttachmentMeta(path, adm)
	if meta.FileSize == 0 {
		meta.FileSize = int64(len(data))
	}
	return attachedImage{data: adm.PNG, width: adm.Width, height: adm.Height}, meta, nil
}

// imageNote says, on an image's card, whether the model will get it.
func imageNote(cfg run.Config) string {
	if cfg.AcceptsImages() {
		return "sent"
	}
	return "not sent: this model has no image input"
}

// hiddenFromChooser reports whether the @ chooser leaves path out. PNG and
// JPEG files are offered, because they can be attached; the other image
// formats are not, since attaching one would only be refused.
func hiddenFromChooser(path string) bool {
	if !hasImageExtension(path) {
		return false
	}
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "png", "jpg", "jpeg":
		return false
	}
	return true
}

// hasReferenceAttachments reports whether atts hold anything the mode decision
// should read as "the user pointed at something": text attachments do, an image
// does not, so pasting a picture does not by itself move a prompt into goal
// mode.
func hasReferenceAttachments(atts []run.Attachment) bool {
	for _, a := range atts {
		if a.Kind != run.AttachmentImage {
			return true
		}
	}
	return false
}

// refuseImageSteer reports, and tells the user, that the composer holds an
// image while a turn is running. Steering carries text only.
func (a *App) refuseImageSteer() bool {
	if a.safeImageCount() == 0 {
		return false
	}
	a.addSystem("images cannot be added to a turn that is running — send the prompt again when it finishes")
	return true
}

// imageMarkersFor describes the admitted images in the composer for the
// session record: name, media type, pixel size, file size and token estimate.
// The bytes are never part of it.
func (a *App) imageMarkersFor() []map[string]any {
	var out []map[string]any
	for _, id := range a.attachOrder {
		att := a.attachments[id]
		if att.state != attachSafe || att.img == nil {
			continue
		}
		out = append(out, map[string]any{
			"name":       filepath.Base(att.raw),
			"media_type": att.meta.MIMEType,
			"width":      att.img.width,
			"height":     att.img.height,
			"bytes":      att.meta.FileSize,
			"tokens":     att.meta.Tokens,
		})
	}
	return out
}

// imageMarkerRows rebuilds the transcript rows for the images a user entry
// records. Their cards say the image is not sent again: a resumed session
// never re-reads the file, exactly as it does not re-attach an @file.
func imageMarkerRows(meta map[string]any) []components.Message {
	list, _ := meta["images"].([]any)
	var rows []components.Message
	for _, item := range list {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		name := metaString(m, "name")
		w, _ := metaInt(m, "width")
		h, _ := metaInt(m, "height")
		size, _ := metaInt(m, "bytes")
		tokens, _ := metaInt(m, "tokens")
		if name == "" {
			continue
		}
		note := "not sent again on resume"
		rows = append(rows, components.Message{
			Role:         "tool",
			ToolName:     "Image",
			Status:       "✓",
			IsAttachment: true,
			Content:      fmt.Sprintf("image %s %dx%d, %s, %s", sanitize.Line(name, 80), w, h, formatBytesShort(int64(size)), note),
			AttachMeta: &components.FileMeta{
				Path: sanitize.Line(name, 80), MIMEType: sanitize.Line(metaString(m, "media_type"), 40),
				FileSize: int64(size), Width: w, Height: h, Tokens: tokens, ImageNote: note,
			},
		})
	}
	return rows
}
