// Package svgguard admits an SVG that a model drew, or refuses it. It is the
// pixels analogue of imageguard for a vector image: the model's reply is
// untrusted bytes, so nothing is repaired and nothing is passed through. The
// reply is parsed against a closed list of drawing elements and attributes, and
// the harness writes the bytes that are kept from the parsed tree. One thing
// outside the lists, anywhere, refuses the whole image.
//
// What is allowed is what Pix is drawn from: shapes, paths, gradients, clip
// paths and a blur filter. Nothing that can run code or fetch anything is on the
// list: no script, style, text, link, image, use, foreignObject or animation
// element, no event attribute, no reference except url(#id) to an id defined in
// the same image, no entity, DOCTYPE, processing instruction or comment inside
// the image. The root must keep Pix's own viewBox, so a generated avatar is a
// variant of Pix and never another picture.
//
// The image is the first <svg> to the last </svg> of the reply; whatever is
// outside it is dropped, never kept.
//
// The same code and the same test corpus are in vdb-site (api/internal/svgguard),
// which checks the image again before it serves it, and the website cleans it a
// third time before it draws it. Keep the three in step.
package svgguard

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Limits on an admitted image.
const (
	// MaxBytes bounds the image the guard reads.
	MaxBytes = 48 << 10
	// MaxNodes bounds the elements it admits.
	MaxNodes = 1500
	// MaxDepth bounds how deep they nest.
	MaxDepth = 12
	// MaxBlur bounds a blur's standard deviation, so a filter cannot be made slow.
	MaxBlur = 20
)

// ViewBox is the one viewBox an admitted image may have: Pix's own.
const ViewBox = "96 26 320 320"

// Namespace is the SVG namespace.
const Namespace = "http://www.w3.org/2000/svg"

// ErrInvalid is returned for every refusal. The text names the kind of problem
// and never repeats anything the model wrote.
var ErrInvalid = errors.New("svgguard: the image is not admitted")

func refuse(why string) error { return fmt.Errorf("%w: %s", ErrInvalid, why) }

var elements = map[string]bool{
	"svg": true, "g": true, "defs": true, "path": true, "circle": true, "ellipse": true, "rect": true, "line": true,
	"polyline": true, "polygon": true, "linearGradient": true, "radialGradient": true, "stop": true, "clipPath": true,
	"filter": true, "feGaussianBlur": true, "feMerge": true, "feMergeNode": true,
}

var attributes = map[string]bool{
	"id": true, "fill": true, "fill-opacity": true, "fill-rule": true, "clip-rule": true, "stroke": true, "stroke-width": true,
	"stroke-linecap": true, "stroke-linejoin": true, "stroke-dasharray": true, "stroke-dashoffset": true, "stroke-opacity": true,
	"stroke-miterlimit": true, "opacity": true, "transform": true, "d": true, "cx": true, "cy": true, "r": true, "rx": true,
	"ry": true, "x": true, "y": true, "x1": true, "y1": true, "x2": true, "y2": true, "width": true, "height": true,
	"points": true, "offset": true, "stop-color": true, "stop-opacity": true, "gradientUnits": true, "gradientTransform": true,
	"spreadMethod": true, "fx": true, "fy": true, "clip-path": true, "filter": true, "viewBox": true, "role": true,
	"aria-label": true, "stdDeviation": true, "in": true, "result": true, "filterUnits": true, "clipPathUnits": true,
	"preserveAspectRatio": true,
}

// urlAttributes may hold url(#id); no other attribute may hold a reference.
var urlAttributes = map[string]bool{"fill": true, "stroke": true, "clip-path": true, "filter": true}

var (
	idPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)
	urlPattern = regexp.MustCompile(`^url\(#([A-Za-z][A-Za-z0-9_-]{0,31})\)$`)
	// plainValue is every character a drawing attribute needs: digits, letters,
	// the signs numbers and colours use, parentheses and white space. No colon,
	// slash, quote or angle bracket, so no URL, scheme or markup fits.
	plainValue = regexp.MustCompile(`^[A-Za-z0-9#%.,()+\- _\s]*$`)
	pathValue  = regexp.MustCompile(`^[MmLlHhVvCcSsQqTtAaZz0-9eE.,+\-\s]*$`)
)

// node is a parsed element.
type node struct {
	name     string
	attrs    [][2]string
	children []*node
}

// Sanitize returns the SVG the harness would keep for the model's reply, or an
// error that begins with ErrInvalid. The reply may have prose around the image;
// the image is the first <svg> to the last </svg>.
func Sanitize(in []byte) ([]byte, error) {
	start := bytes.Index(in, []byte("<svg"))
	end := bytes.LastIndex(in, []byte("</svg>"))
	if start < 0 || end < start {
		return nil, refuse("no svg element")
	}
	doc := in[start : end+len("</svg>")]
	if len(doc) > MaxBytes {
		return nil, refuse("the image is too large")
	}
	root, err := parse(doc)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	var refs []string
	if err := check(root, 0, ids, &refs, new(int)); err != nil {
		return nil, err
	}
	for _, r := range refs {
		if !ids[r] {
			return nil, refuse("a reference points at an id the image does not define")
		}
	}
	var out bytes.Buffer
	write(&out, root)
	if out.Len() > MaxBytes {
		return nil, refuse("the image is too large")
	}
	return out.Bytes(), nil
}

// parse reads the document into a tree, refusing anything that is not an
// element or white space between elements.
func parse(doc []byte) (*node, error) {
	d := xml.NewDecoder(bytes.NewReader(doc))
	d.Strict = true
	d.Entity = nil
	var stack []*node
	var root *node
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, refuse("the image is not well-formed")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != "" {
				return nil, refuse("a namespaced element")
			}
			n := &node{name: t.Name.Local}
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					if a.Value != Namespace {
						return nil, refuse("a namespace other than svg")
					}
				case a.Name.Space != "":
					return nil, refuse("a namespaced attribute")
				default:
					n.attrs = append(n.attrs, [2]string{a.Name.Local, a.Value})
				}
			}
			if root != nil && len(stack) == 0 {
				return nil, refuse("more than one root element")
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			// RawToken does not match end tags to start tags; the guard does.
			if len(stack) == 0 || stack[len(stack)-1].name != t.Name.Local || t.Name.Space != "" {
				return nil, refuse("the image is not well-formed")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return nil, refuse("text inside the image")
			}
		case xml.Comment, xml.ProcInst, xml.Directive:
			return nil, refuse("a comment, instruction or declaration")
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, refuse("the image is not well-formed")
	}
	return root, nil
}

// check walks the tree against the lists and the limits.
func check(n *node, depth int, ids map[string]bool, refs *[]string, count *int) error {
	if *count++; *count > MaxNodes {
		return refuse("too many elements")
	}
	if depth >= MaxDepth {
		return refuse("nested too deeply")
	}
	if !elements[n.name] {
		return refuse("an element that is not drawing")
	}
	if n.name == "svg" && depth != 0 {
		return refuse("a nested svg")
	}
	seen := map[string]bool{}
	for _, a := range n.attrs {
		k, v := a[0], a[1]
		if !attributes[k] {
			return refuse("an attribute that is not drawing")
		}
		if seen[k] {
			return refuse("a repeated attribute")
		}
		seen[k] = true
		// A reference is url(#id) in an attribute that takes one, written exactly
		// that way. url() in any other form, case or place is refused.
		if strings.Contains(strings.ToLower(v), "url(") && !(urlAttributes[k] && urlPattern.MatchString(v)) {
			return refuse("a reference that is not url(#id)")
		}
		switch {
		case k == "id":
			if !idPattern.MatchString(v) || ids[v] {
				return refuse("an id that is malformed or repeated")
			}
			ids[v] = true
		case urlAttributes[k] && strings.HasPrefix(v, "url("):
			*refs = append(*refs, urlPattern.FindStringSubmatch(v)[1])
		case k == "d" || k == "points":
			if !pathValue.MatchString(v) {
				return refuse("path data with characters a path does not use")
			}
		case k == "aria-label":
			if len(v) > 120 || !plainValue.MatchString(v) {
				return refuse("a label that is not plain text")
			}
		case k == "stdDeviation":
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil || f < 0 || f > MaxBlur {
				return refuse("a blur out of range")
			}
		default:
			if !plainValue.MatchString(v) || len(v) > 512 {
				return refuse("an attribute value with characters a drawing does not use")
			}
		}
		if strings.Contains(strings.ToLower(v), "script") {
			return refuse("an attribute value that names a script")
		}
	}
	if n.name == "svg" {
		if vb := strings.Join(strings.Fields(attr(n, "viewBox")), " "); vb != ViewBox {
			return refuse("a viewBox that is not Pix's")
		}
	}
	for _, c := range n.children {
		if err := check(c, depth+1, ids, refs, count); err != nil {
			return err
		}
	}
	return nil
}

func attr(n *node, key string) string {
	for _, a := range n.attrs {
		if a[0] == key {
			return a[1]
		}
	}
	return ""
}

// write serialises the admitted tree. The namespace is always written once, on
// the root, and every value is escaped even though the lists already exclude
// what would need it.
func write(b *bytes.Buffer, n *node) {
	b.WriteByte('<')
	b.WriteString(n.name)
	if n.name == "svg" {
		b.WriteString(` xmlns="` + Namespace + `"`)
	}
	for _, a := range n.attrs {
		b.WriteByte(' ')
		b.WriteString(a[0])
		b.WriteString(`="`)
		_ = xml.EscapeText(b, []byte(a[1]))
		b.WriteByte('"')
	}
	if len(n.children) == 0 {
		b.WriteString("/>")
		return
	}
	b.WriteByte('>')
	for _, c := range n.children {
		write(b, c)
	}
	b.WriteString("</" + n.name + ">")
}
