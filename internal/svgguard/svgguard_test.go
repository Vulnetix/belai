package svgguard

import (
	"bytes"
	"errors"
	"math/rand"
	"os"
	"strings"
	"testing"
)

const marker = "ZZMARKERZZ"

// svg wraps content in a root with Pix's viewBox.
func svg(content string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="96 26 320 320">` + content + `</svg>`
}

func pixTemplate(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/pix.svg")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAdmitsPixAndDrawingWithinTheLists(t *testing.T) {
	good := map[string]string{
		"a circle": svg(`<circle cx="10" cy="10" r="5" fill="#006860"/>`),
		"gradient and reference": svg(`<defs><radialGradient id="g" cx="50%" cy="50%" r="50%"><stop offset="0%" stop-color="rgb(1,2,3)"/><stop offset="100%" stop-color="#fff" stop-opacity="0.5"/></radialGradient></defs>` +
			`<circle cx="1" cy="2" r="3" fill="url(#g)"/>`),
		"a blur filter":           svg(`<defs><filter id="f" x="-80%" y="-80%" width="260%" height="260%"><feGaussianBlur stdDeviation="6" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter></defs><rect x="1" y="1" width="9" height="9" filter="url(#f)"/>`),
		"a clip path":             svg(`<clipPath id="c"><rect x="0" y="0" width="5" height="5"/></clipPath><g clip-path="url(#c)"><path d="M0 0 L 10 10 C 1 2, 3 4, 5 6 Z" stroke="#000" stroke-width="2" fill="none" stroke-linecap="round"/></g>`),
		"a label":                 `<svg xmlns="http://www.w3.org/2000/svg" viewBox="96 26 320 320" width="512" height="512" role="img" aria-label="Pix, the Vulnetix AI assistant owl"><g/></svg>`,
		"no xmlns":                `<svg viewBox="96 26 320 320"><g/></svg>`,
		"spaced view":             `<svg viewBox=" 96  26 320 320 "><g/></svg>`,
		"polygon":                 svg(`<polygon points="0,0 10,0 5,8" fill="red"/><polyline points="1,1 2,2" stroke="blue"/><ellipse cx="1" cy="1" rx="2" ry="3"/><line x1="0" y1="0" x2="5" y2="5"/>`),
		"whitespace":              "\n  " + svg("\n  <g>\n  </g>\n") + "\n",
		"a whitespace-only CDATA": svg(`<g><![CDATA[   ]]></g>`),
	}
	for name, in := range good {
		if _, err := Sanitize([]byte(in)); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
	out, err := Sanitize(pixTemplate(t))
	if err != nil {
		t.Fatalf("the Pix template was refused: %v", err)
	}
	if bytes.Contains(out, []byte("<!--")) || strings.Count(string(out), "xmlns=") != 1 {
		t.Fatalf("the kept bytes carry a comment or the wrong namespaces: %s", out[:200])
	}
}

func TestTakesTheImageOutOfProseAndFences(t *testing.T) {
	img := svg(`<circle cx="1" cy="1" r="1"/>`)
	for name, in := range map[string]string{
		"prose":  "Here is your avatar:\n" + img + "\nI hope you like it!",
		"fenced": "```svg\n" + img + "\n```",
		"bare":   img,
	} {
		out, err := Sanitize([]byte(in))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.HasPrefix(string(out), "<svg") || !strings.HasSuffix(string(out), "</svg>") || strings.Contains(string(out), "hope") {
			t.Errorf("%s: kept %q", name, out)
		}
	}
}

func TestRefusesEverythingOutsideTheLists(t *testing.T) {
	bad := map[string]string{
		"script element":              svg(`<script>alert(1)</script>`),
		"script with cdata":           svg(`<script><![CDATA[alert(1)]]></script>`),
		"onload on the root":          `<svg viewBox="96 26 320 320" onload="alert(1)"><g/></svg>`,
		"onclick":                     svg(`<circle cx="1" cy="1" r="1" onclick="x()"/>`),
		"style element":               svg(`<style>circle{fill:red}</style>`),
		"style attribute":             svg(`<circle cx="1" cy="1" r="1" style="fill:url(//evil/x)"/>`),
		"class attribute":             svg(`<circle cx="1" cy="1" r="1" class="a"/>`),
		"foreignObject":               svg(`<foreignObject><div/></foreignObject>`),
		"use":                         svg(`<use href="#a"/>`),
		"use with xlink":              `<svg xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="96 26 320 320"><use xlink:href="#a"/></svg>`,
		"image":                       svg(`<image href="data:image/png;base64,AAAA"/>`),
		"a link":                      svg(`<a href="https://x"><g/></a>`),
		"text":                        svg(`<text x="1" y="1">hi</text>`),
		"animate":                     svg(`<animate attributeName="x" to="9"/>`),
		"set":                         svg(`<set attributeName="href" to="javascript:alert(1)"/>`),
		"title":                       svg(`<title>x</title>`),
		"switch":                      svg(`<switch><g/></switch>`),
		"a namespaced element":        `<svg viewBox="96 26 320 320" xmlns:x="urn:x"><x:g/></svg>`,
		"another default namespace":   `<svg xmlns="http://www.w3.org/1999/xhtml" viewBox="96 26 320 320"><g/></svg>`,
		"a namespaced attribute":      `<svg viewBox="96 26 320 320" xmlns:x="urn:x"><g x:a="1"/></svg>`,
		"a billion laughs":            `<!DOCTYPE svg [<!ENTITY a "aaaa"><!ENTITY b "&a;&a;&a;&a;">]><svg viewBox="96 26 320 320"><g id="&b;"/></svg>`,
		"an entity reference":         svg(`<g id="&xxe;"/>`),
		"a comment":                   svg(`<!-- x --><g/>`),
		"a processing instruction":    svg(`<?xml-stylesheet href="x"?><g/>`),
		"text in a group":             svg(`<g>hello</g>`),
		"a cdata with text":           svg(`<g><![CDATA[hello]]></g>`),
		"an external url":             svg(`<circle cx="1" cy="1" r="1" fill="url(https://evil/x.svg#a)"/>`),
		"an undefined reference":      svg(`<circle cx="1" cy="1" r="1" fill="url(#nope)"/>`),
		"a reference in a width":      svg(`<defs><filter id="f"><feGaussianBlur stdDeviation="1"/></filter></defs><rect x="1" y="1" width="url(#f)" height="1"/>`),
		"upper-case url":              svg(`<defs><radialGradient id="a"><stop offset="0"/></radialGradient></defs><circle cx="1" cy="1" r="1" fill="URL(#a)"/>`),
		"a spaced url":                svg(`<defs><radialGradient id="a"><stop offset="0"/></radialGradient></defs><circle cx="1" cy="1" r="1" fill="url( #a )"/>`),
		"a javascript value":          svg(`<circle cx="1" cy="1" r="1" fill="javascript:alert(1)"/>`),
		"a data value":                svg(`<circle cx="1" cy="1" r="1" fill="data:text/html,x"/>`),
		"markup in a value":           svg(`<circle cx="1" cy="1" r="1" fill="&lt;script&gt;"/>`),
		"a quote in a value":          svg(`<circle cx="1" cy="1" r="1" fill="red&quot; onload=&quot;x"/>`),
		"letters in path data":        svg(`<path d="M0 0 L 1 1 foo"/>`),
		"markup in path data":         svg(`<path d="M0 0 &lt;"/>`),
		"a long label":                `<svg viewBox="96 26 320 320" aria-label="` + strings.Repeat("a", 130) + `"><g/></svg>`,
		"markup in a label":           `<svg viewBox="96 26 320 320" aria-label="&lt;b&gt;"><g/></svg>`,
		"a huge blur":                 svg(`<filter id="f"><feGaussianBlur stdDeviation="100"/></filter>`),
		"a negative blur":             svg(`<filter id="f"><feGaussianBlur stdDeviation="-1"/></filter>`),
		"a blur that is not a number": svg(`<filter id="f"><feGaussianBlur stdDeviation="big"/></filter>`),
		"another viewBox":             `<svg viewBox="0 0 100 100"><g/></svg>`,
		"no viewBox":                  `<svg><g/></svg>`,
		"a nested svg":                svg(`<svg viewBox="96 26 320 320"><g/></svg>`),
		"two roots":                   svg(`<g/>`) + svg(`<g/>`),
		"mismatched tags":             svg(`<g></path>`),
		"unclosed":                    `<svg viewBox="96 26 320 320"><g></svg>`,
		"a repeated id":               svg(`<g id="a"/><g id="a"/>`),
		"a malformed id":              svg(`<g id="1a"/>`),
		"a long id":                   svg(`<g id="` + strings.Repeat("a", 40) + `"/>`),
		"a repeated attribute":        `<svg viewBox="96 26 320 320"><circle r="1" r="2"/></svg>`,
		"a space in a name":           `<svg viewBox="96 26 320 320"><g fill = "red" fill="blue"/></svg>`,
		"no svg":                      `<html><body>hello</body></html>`,
		"nothing":                     ``,
		"an end before a start":       `</svg><svg viewBox="96 26 320 320">`,
		"an uppercase svg":            `<SVG viewBox="96 26 320 320"><g/></SVG>`,
	}
	for name, in := range bad {
		out, err := Sanitize([]byte(in))
		if err == nil {
			t.Errorf("%s was admitted: %s", name, out)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: the error does not begin with ErrInvalid: %v", name, err)
		}
	}
}

// The image is the first <svg> to the last </svg>. A declaration, comment or
// instruction before it is not part of it: it is dropped, never kept.
func TestWhatIsOutsideTheImageIsDropped(t *testing.T) {
	in := `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY a "aaaa">]><!-- hi -->` + svg(`<g/>`) + `<script>alert(1)</script>`
	out, err := Sanitize([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `<svg xmlns="http://www.w3.org/2000/svg" viewBox="96 26 320 320"><g/></svg>` {
		t.Fatalf("kept %s", out)
	}
}

func TestRefusalsNeverRepeatWhatTheModelWrote(t *testing.T) {
	for _, in := range []string{
		svg(`<` + marker + `/>`),
		svg(`<g ` + marker + `="1"/>`),
		svg(`<g id="1` + marker + `"/>`),
		svg(`<circle cx="1" cy="1" r="1" fill="url(` + marker + `)"/>`),
		svg(`<g>` + marker + `</g>`),
		`<svg viewBox="` + marker + `"><g/></svg>`,
		svg(`<script>` + marker + `</script>`),
		svg(`<g fill="` + marker + `:"/>`),
	} {
		_, err := Sanitize([]byte(in))
		if err == nil {
			t.Fatalf("admitted %q", in)
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("the error repeats the model's text: %v", err)
		}
	}
}

func TestLimits(t *testing.T) {
	big := svg(`<path d="M` + strings.Repeat("0 0 ", MaxBytes/4) + `"/>`)
	if _, err := Sanitize([]byte(big)); err == nil {
		t.Error("an image over the size limit was admitted")
	}
	many := svg(strings.Repeat(`<g/>`, MaxNodes))
	if _, err := Sanitize([]byte(many)); err == nil {
		t.Error("an image with too many elements was admitted")
	}
	fine := svg(strings.Repeat(`<g/>`, MaxNodes-10))
	if _, err := Sanitize([]byte(fine)); err != nil {
		t.Errorf("an image just under the element limit: %v", err)
	}
	deep := svg(strings.Repeat(`<g>`, MaxDepth) + strings.Repeat(`</g>`, MaxDepth))
	if _, err := Sanitize([]byte(deep)); err == nil {
		t.Error("an image nested too deeply was admitted")
	}
	ok := svg(strings.Repeat(`<g>`, MaxDepth-3) + strings.Repeat(`</g>`, MaxDepth-3))
	if _, err := Sanitize([]byte(ok)); err != nil {
		t.Errorf("an image nested within the limit: %v", err)
	}
}

func TestKeptBytesAreComposedByTheGuardAndStable(t *testing.T) {
	in := svg(`  <g id="a"   opacity="0.5" >` + "\n" + `<circle   r="1"  cx="2" cy="3"/></g>`)
	out, err := Sanitize([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="96 26 320 320"><g id="a" opacity="0.5"><circle r="1" cx="2" cy="3"/></g></svg>`
	if string(out) != want {
		t.Fatalf("kept %s\nwant %s", out, want)
	}
	again, err := Sanitize(out)
	if err != nil || !bytes.Equal(again, out) {
		t.Fatalf("a kept image is not stable: %v\n%s\n%s", err, out, again)
	}
	pix, _ := Sanitize(pixTemplate(t))
	again, err = Sanitize(pix)
	if err != nil || !bytes.Equal(again, pix) {
		t.Fatalf("the kept Pix is not stable: %v", err)
	}
}

// Whatever a mutated image turns into, an admitted one is stable and carries
// nothing that could run or fetch.
func TestMutatedPixIsRefusedOrHarmless(t *testing.T) {
	base := pixTemplate(t)
	rng := rand.New(rand.NewSource(7))
	alphabet := []byte(`<>"'&;:/=!?[]()#% abcxyzSCRIPTonclickhref0123456789`)
	forbidden := []string{"<script", "javascript", "href", "onload", "onclick", "<style", "<!", "<?", "foreignobject", "data:", "http"}
	admitted := 0
	for i := 0; i < 3000; i++ {
		b := append([]byte(nil), base...)
		for n := 1 + rng.Intn(4); n > 0; n-- {
			switch rng.Intn(3) {
			case 0:
				b[rng.Intn(len(b))] = alphabet[rng.Intn(len(alphabet))]
			case 1:
				at := rng.Intn(len(b))
				b = append(b[:at], b[min(len(b), at+1+rng.Intn(20)):]...)
			default:
				at := rng.Intn(len(b))
				ins := []byte(`<script>x</script>`)
				if rng.Intn(2) == 0 {
					ins = []byte(` onload="x" `)
				}
				b = append(b[:at], append(ins, b[at:]...)...)
			}
		}
		out, err := Sanitize(b)
		if err != nil {
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("iteration %d: %v", i, err)
			}
			continue
		}
		admitted++
		lower := strings.ToLower(string(out))
		for _, f := range forbidden {
			// The namespace is the one URL the kept bytes carry.
			if strings.Contains(strings.ReplaceAll(lower, Namespace, ""), f) {
				t.Fatalf("iteration %d: an admitted image carries %q:\n%s", i, f, out)
			}
		}
		again, err := Sanitize(out)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("iteration %d: an admitted image is not stable: %v", i, err)
		}
	}
	if admitted == 0 {
		t.Fatal("the mutation test never admitted an image, so it proves nothing about admitted ones")
	}
}
