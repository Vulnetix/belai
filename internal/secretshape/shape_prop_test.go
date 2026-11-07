package secretshape

import (
	"math/rand"
	"strings"
	"testing"
)

// knownPrefixes are the anchors every pattern in secretshape.go builds on.
// Each accepted span must contain at least one of them, so an input Holds
// accepts that contains none of them means the matcher fired on a shape we
// did not intend to recognise.
var knownPrefixes = []string{
	"AKIA", "ASIA",
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_",
	"github_pat_",
	"xox", "sk-", "glpat-",
	"PRIVATE KEY",
}

const (
	payloadChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	awsChars     = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	keyInterior  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 "
)

func randomString(r *rand.Rand, n int, alphabet string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[r.Intn(len(alphabet))])
	}
	return b.String()
}

// randomPayload draws n characters from alphabet, forcing the last character
// into tail when tail is non-empty. The patterns close on \b, which only
// matches between a word character and a non-word character, so a payload of
// exactly the minimum length that ends in '-' (allowed by the Slack and
// GitLab classes) never matches: the trailing \b has no word character to
// sit next to. Real tokens end in an alphanumeric character, so generated
// payloads stay within the well-formed shapes.
func randomPayload(r *rand.Rand, n int, alphabet, tail string) string {
	s := randomString(r, n, alphabet)
	if tail != "" && n > 0 {
		s = s[:n-1] + string(tail[r.Intn(len(tail))])
	}
	return s
}

// TestHoldsShapesAreAllDetected generates every known token shape with a
// random valid payload of the required length and class, asserting Holds
// accepts it, and the same payload one character short, asserting Holds
// rejects it.
func TestHoldsShapesAreAllDetected(t *testing.T) {
	r := rand.New(rand.NewSource(1)) // fixed seed: the run is deterministic
	shapes := []struct {
		name     string
		prefix   string
		alphabet string
		tail     string
		minLen   int
	}{
		{"AWS access key id", "AKIA", awsChars, "", 16},
		{"AWS session token", "ASIA", awsChars, "", 16},
		{"GitHub classic token", "ghp_", payloadChars, "", 36},
		{"GitHub classic token", "gho_", payloadChars, "", 36},
		{"GitHub classic token", "ghu_", payloadChars, "", 36},
		{"GitHub classic token", "ghs_", payloadChars, "", 36},
		{"GitHub classic token", "ghr_", payloadChars, "", 36},
		{"GitHub fine-grained token", "github_pat_", payloadChars + "_", "", 50},
		{"Slack token", "xoxa-", payloadChars + "-", payloadChars, 10},
		{"Slack token", "xoxb-", payloadChars + "-", payloadChars, 10},
		{"Slack token", "xoxp-", payloadChars + "-", payloadChars, 10},
		{"Slack token", "xoxr-", payloadChars + "-", payloadChars, 10},
		{"Slack token", "xoxs-", payloadChars + "-", payloadChars, 10},
		{"OpenAI key", "sk-", payloadChars, "", 32},
		{"GitLab token", "glpat-", payloadChars + "_-", payloadChars, 20},
	}
	for _, s := range shapes {
		t.Run(s.name+" "+s.prefix, func(t *testing.T) {
			for i := 0; i < 50; i++ {
				token := s.prefix + randomPayload(r, s.minLen, s.alphabet, s.tail)
				if !Holds([]byte(token)) {
					t.Errorf("Holds(%q) = false, want true", token)
				}
				short := s.prefix + randomPayload(r, s.minLen-1, s.alphabet, "")
				if Holds([]byte(short)) {
					t.Errorf("Holds(%q) = true for a payload one character short of the minimum", short)
				}
			}
		})
	}
	t.Run("private key marker", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			marker := "-----BEGIN " + randomString(r, r.Intn(12), keyInterior) + " PRIVATE KEY"
			if r.Intn(2) == 0 {
				marker += " BLOCK"
			}
			marker += "-----"
			if !Holds([]byte(marker)) {
				t.Errorf("Holds(%q) = false, want true", marker)
			}
			if Holds([]byte(marker[:len(marker)-1])) {
				t.Errorf("Holds(%q) = true with the final dash removed", marker[:len(marker)-1])
			}
		}
	})
}

// TestHoldsRejectsNoise checks random alphanumeric noise and short strings:
// Holds must reject them all. The generators pick charsets and lengths for
// which no pattern can match, so the test is deterministic rather than
// probabilistic.
func TestHoldsRejectsNoise(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	// Lowercase hex cannot contain any token shape: no pattern anchors on a
	// lowercase-hex-only string, so noise of any length is safe to generate.
	for i := 0; i < 200; i++ {
		if s := randomString(r, r.Intn(81), "0123456789abcdef"); Holds([]byte(s)) {
			t.Fatalf("Holds(%q) = true for hex noise", s)
		}
	}
	// Short alphanumeric strings: the shortest accepted shape is "xoxa-" plus
	// ten payload characters (15 total), so length <= 10 can never match.
	for i := 0; i < 200; i++ {
		if s := randomString(r, r.Intn(11), payloadChars); Holds([]byte(s)) {
			t.Fatalf("Holds(%q) = true for a short string", s)
		}
	}
}

// FuzzHolds checks that Holds never panics and that anything it accepts
// contains at least one known token prefix, so a future pattern that fires on
// an unrecognised shape fails the fuzz run.
func FuzzHolds(f *testing.F) {
	for _, s := range []string{
		"-----BEGIN RSA PRIVATE KEY-----\nabc",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY----", // one dash short of a key block
		"key = AKIAABCDEFGHIJKLMNOP",
		"AKIAABCDEFGHIJKLMNOP",
		"AKIAABCDEFGHIJKLMNOP0",  // 17th character kills the trailing \b
		"zzAKIAABCDEFGHIJKLMNOP", // no word boundary before AKIA
		"\x00AKIAABCDEFGHIJKLMNOP",
		"AKIAABCDEFGHIJKLMNOP\x00",
		"ghp_" + strings.Repeat("a", 36),
		"gho_" + strings.Repeat("b", 36),
		"ghu_" + strings.Repeat("c", 36),
		"ghs_" + strings.Repeat("d", 36),
		"ghr_" + strings.Repeat("e", 36),
		"github_pat_" + strings.Repeat("A", 50),
		"xoxa-" + strings.Repeat("a", 10),
		"xoxb-" + strings.Repeat("a", 10),
		"xoxp-" + strings.Repeat("a", 10),
		"xoxr-" + strings.Repeat("a", 10),
		"xoxs-" + strings.Repeat("a", 10),
		"xoxb-" + strings.Repeat("a-", 20), // long payload whose tail is '-'
		"xoxb-GFeQYRvfU-",                  // minimum-length payload ending in '-'
		"sk-" + strings.Repeat("a", 32),
		"sk-" + strings.Repeat("a", 31), // one character short
		"glpat-" + strings.Repeat("_", 10) + strings.Repeat("a", 10),
		"glpat-" + strings.Repeat("a", 19), // one character short
		"plain source code with no token",
		"AKIA short",
		"",
		strings.Repeat("a", 10000),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !Holds([]byte(s)) {
			return
		}
		for _, re := range patterns {
			if loc := re.FindStringIndex(s); loc != nil {
				span := s[loc[0]:loc[1]]
				for _, prefix := range knownPrefixes {
					if strings.Contains(span, prefix) {
						return
					}
				}
			}
		}
		t.Fatalf("Holds(%q) = true but no accepted span carries one of %v", s, knownPrefixes)
	})
}
