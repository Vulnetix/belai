// Command recolor-avatar draws a built-in persona's Pix from another persona's
// avatar in a new palette. Every colour of the template is moved in OKLCH: a
// colour whose hue is nearer the template's primary takes the new primary's hue
// and chroma, one nearer the secondary takes the new secondary's, each keeping
// its lightness step from the colour it came from, so the shading and
// highlights read the same. Neutral colours (the cream belly, the eyes, the
// near-black outlines) are left as they are. The result is admitted through
// internal/svgguard and written as the guard's own serialisation, which is what
// TestPersonaAvatarsAreAdmitted requires.
//
//	go run ./tools/recolor-avatar -template <svg> -from '#3f449e,#ee9a6a' \
//	    -to '#4338ca,#34d399' -name 'Harvey Wiley' -out internal/agentprofile/builtin/avatars/<id>.svg
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/svgguard"
)

type lab struct{ L, a, b float64 }

func lin(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func delin(c float64) float64 {
	if c <= 0.0031308 {
		return c * 12.92
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}

func toLab(r, g, b float64) lab {
	r, g, b = lin(r), lin(g), lin(b)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return lab{0.2104542553*l + 0.793617785*m - 0.0040720468*s, 1.9779984951*l - 2.428592205*m + 0.4505937099*s, 0.0259040371*l + 0.7827717662*m - 0.808675766*s}
}

func fromLab(c lab) (float64, float64, float64) {
	l := math.Pow(c.L+0.3963377774*c.a+0.2158037573*c.b, 3)
	m := math.Pow(c.L-0.1055613458*c.a-0.0638541728*c.b, 3)
	s := math.Pow(c.L-0.0894841775*c.a-1.291485548*c.b, 3)
	clamp := func(v float64) float64 { return math.Min(1, math.Max(0, delin(v))) }
	return clamp(4.0767416621*l - 3.3077115913*m + 0.2309699292*s), clamp(-1.2684380046*l + 2.6097574011*m - 0.3413193965*s), clamp(-0.0041960863*l - 0.7034186147*m + 1.707614701*s)
}

func hexLab(h string) lab {
	h = strings.TrimPrefix(h, "#")
	v, _ := strconv.ParseUint(h, 16, 32)
	return toLab(float64(v>>16&0xff)/255, float64(v>>8&0xff)/255, float64(v&0xff)/255)
}

func labHex(c lab) string {
	r, g, b := fromLab(c)
	return fmt.Sprintf("#%02x%02x%02x", int(math.Round(r*255)), int(math.Round(g*255)), int(math.Round(b*255)))
}

func chroma(c lab) float64 { return math.Hypot(c.a, c.b) }
func hue(c lab) float64    { return math.Atan2(c.b, c.a) }

func hueDist(x, y float64) float64 {
	d := math.Abs(x - y)
	if d > math.Pi {
		d = 2*math.Pi - d
	}
	return d
}

// neutral is the chroma under which a colour is left alone.
const neutral = 0.04

// move recolours one template colour from the (fromP, fromS) pair to (toP, toS).
func move(c, fromP, fromS, toP, toS lab) lab {
	if chroma(c) < neutral {
		return c
	}
	base, target := fromP, toP
	if hueDist(hue(c), hue(fromS)) < hueDist(hue(c), hue(fromP)) {
		base, target = fromS, toS
	}
	L := math.Min(0.98, math.Max(0.02, c.L-base.L+target.L))
	scale := 1.0
	if chroma(base) > 0 {
		scale = chroma(target) / chroma(base)
	}
	C := chroma(c) * scale
	h := hue(target)
	return lab{L, C * math.Cos(h), C * math.Sin(h)}
}

var hexRE = regexp.MustCompile(`#[0-9a-fA-F]{6}\b`)

func pair(s string) (lab, lab) {
	p, q, ok := strings.Cut(s, ",")
	if !ok {
		log.Fatalf("a pair is '#primary,#secondary', got %q", s)
	}
	return hexLab(strings.TrimSpace(p)), hexLab(strings.TrimSpace(q))
}

func main() {
	tpl := flag.String("template", "", "the template avatar SVG")
	from := flag.String("from", "", "the template's primary,secondary")
	to := flag.String("to", "", "the new primary,secondary")
	name := flag.String("name", "", "the new display name, for the accessible label")
	tplName := flag.String("template-name", "", "the template's display name, replaced in the label")
	out := flag.String("out", "", "where to write the SVG")
	flag.Parse()
	if *tpl == "" || *from == "" || *to == "" || *name == "" || *out == "" || *tplName == "" {
		flag.Usage()
		os.Exit(2)
	}
	raw, err := os.ReadFile(*tpl)
	if err != nil {
		log.Fatal(err)
	}
	fp, fs := pair(*from)
	tp, ts := pair(*to)
	svg := hexRE.ReplaceAllStringFunc(string(raw), func(h string) string { return labHex(move(hexLab(h), fp, fs, tp, ts)) })
	if !strings.Contains(svg, *tplName) {
		log.Fatalf("the template does not name %q", *tplName)
	}
	svg = strings.ReplaceAll(svg, *tplName, *name)
	clean, err := svgguard.Sanitize([]byte(svg))
	if err != nil {
		log.Fatalf("the guard refuses the result: %v", err)
	}
	if err := os.WriteFile(*out, clean, 0o644); err != nil {
		log.Fatal(err)
	}
}
