package sanitize

import (
	"strings"
	"testing"
)

func BenchmarkTextPlain100K(b *testing.B) {
	s := strings.Repeat("plain output line with no angle brackets at all\n", 2200)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		Text(s)
	}
}

func BenchmarkTextCode100K(b *testing.B) {
	s := strings.Repeat("func main() { if a < b && c > d { return } } // see <T> and </div>\n", 1500)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		Text(s)
	}
}

func BenchmarkTextANSI100K(b *testing.B) {
	s := strings.Repeat("\x1b[32mok\x1b[0m   pkg/foo  0.123s\n", 3500)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		Text(s)
	}
}
