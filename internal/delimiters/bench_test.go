package delimiters

import (
	"strings"
	"testing"
)

func benchText(n int) string {
	line := "func main() { if a < b && c > d { return } } // see <T> and </div> in generics\n"
	return strings.Repeat(line, n/len(line)+1)[:n]
}

func BenchmarkEgressPlain100K(b *testing.B) {
	s := strings.Repeat("plain output line with no angle brackets at all\n", 2200)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		Egress(s, nil)
	}
}

func BenchmarkEgressAngles100K(b *testing.B) {
	s := benchText(100 << 10)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		Egress(s, nil)
	}
}

func BenchmarkEgressSealed(b *testing.B) {
	body := benchText(50 << 10)
	s := Wrap(KindAttachment, "n0nce", body) + "\n" + benchText(20<<10)
	c := MapChecker{"n0nce": true}
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		Egress(s, c)
	}
}
