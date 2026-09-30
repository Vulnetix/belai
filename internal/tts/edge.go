package tts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vulnetix/belai/internal/netguard"
)

// Edge speaks to Microsoft's read-aloud service, the endpoint the Edge browser
// uses. It is unofficial: it can change or start refusing without notice, and
// the text sent leaves the machine. The host is fixed, https only, no
// redirect is followed, and the only credential is a public constant.
const (
	// edgeFormat is what the service is asked for. It offers MP3 and WebM/Opus and
	// refuses raw PCM, so the audio is decoded in process (mp3.go).
	edgeFormat = "audio-24khz-48kbitrate-mono-mp3"
	edgeHost   = "speech.platform.bing.com"
	edgePath   = "/consumer/speech/synthesize/readaloud/edge/v1"
	edgeToken  = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	// DefaultVoice is used when none is set.
	DefaultVoice = "en-US-AndrewMultilingualNeural"

	// maxAudioBytes bounds what one request may return.
	maxAudioBytes = 64 << 20
)

var voiceRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// edgeChrom is the browser version the service is told it is talking to. The
// service checks it, and it moves with Edge releases, so it is a variable the
// live test can probe.
var edgeChrom = "143.0.3650.75"

// edgeAgent is the User-Agent for edgeChrom.
func edgeAgent() string {
	major, _, _ := strings.Cut(edgeChrom, ".")
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + major + ".0.0.0 Safari/537.36 Edg/" + major + ".0.0.0"
}

// ValidVoice reports whether v is a plain voice name, such as
// en-GB-RyanNeural, and so safe to put in a request.
func ValidVoice(v string) bool { return voiceRE.MatchString(v) }

// Edge is the read-aloud Engine.
type Edge struct {
	// URL replaces the endpoint. For tests: it must pass netguard's endpoint
	// rules, so it can only be https or a loopback address.
	URL string
	// Retries is how many times a failed request is tried again. Zero means 3.
	Retries int
	// Sleep replaces time.Sleep between retries (tests).
	Sleep func(time.Duration)
	// rawAudio returns the service's MP3 undecoded (building test fixtures).
	rawAudio bool

	skew atomic.Int64 // seconds the server's clock is ahead of ours
}

// NewEdge returns an Edge engine for the real service.
func NewEdge() *Edge { return &Edge{} }

// Name implements Engine.
func (*Edge) Name() string { return "edge" }

// SecMSGEC is the token the service checks: the time rounded down to five
// minutes, in 100 ns ticks since 1601, hashed with the public client token.
func SecMSGEC(now time.Time) string {
	secs := now.Unix() + 11644473600
	secs -= secs % 300
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d%s", secs*10_000_000, edgeToken)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

var fullVoiceRE = regexp.MustCompile(`^([a-z]{2,})-([A-Z]{2,})-(.+Neural)$`)

// voiceName expands en-US-AriaNeural to the long name the service expects.
func voiceName(v string) string {
	m := fullVoiceRE.FindStringSubmatch(v)
	if m == nil {
		return v
	}
	lang, region, name := m[1], m[2], m[3]
	if a, b, ok := strings.Cut(name, "-"); ok {
		region, name = region+"-"+a, b
	}
	return fmt.Sprintf("Microsoft Server Speech Text to Speech Voice (%s-%s, %s)", lang, region, name)
}

func (e *Edge) endpoint() (*url.URL, error) {
	raw := "wss://" + edgeHost + edgePath
	if e.URL != "" {
		raw = e.URL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	check := *u
	switch u.Scheme {
	case "wss":
		check.Scheme = "https"
	case "ws":
		check.Scheme = "http"
	default:
		return nil, fmt.Errorf("tts: endpoint scheme %q is not wss", u.Scheme)
	}
	if _, err := netguard.CheckURL(check.String(), netguard.Endpoint); err != nil {
		return nil, err
	}
	if e.URL == "" && u.Hostname() != edgeHost {
		return nil, errors.New("tts: the endpoint host is fixed")
	}
	return u, nil
}

// Synthesize implements Engine. The service returns MP3, which is decoded to PCM.
// A request that fails is retried with the
// backoff the service's users settled on (1, 2, 4, 8 seconds at most); a clock
// skew refusal is corrected from the server's Date header and retried at once.
func (e *Edge) Synthesize(ctx context.Context, text, voice string) ([]byte, error) {
	if !ValidVoice(voice) {
		return nil, fmt.Errorf("tts: %q is not a voice name", voice)
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("tts: nothing to say")
	}
	text = cleanSSMLText(text)
	retries := e.Retries
	if retries <= 0 {
		retries = 3
	}
	sleep := e.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pcm, err := e.once(ctx, text, voice)
		if err == nil {
			return pcm, nil
		}
		last = err
		var hs *errHandshake
		if errors.As(err, &hs) {
			if hs.Resp.StatusCode == http.StatusForbidden {
				if t, perr := http.ParseTime(hs.Resp.Header.Get("Date")); perr == nil {
					e.skew.Store(int64(time.Until(t).Round(time.Second) / time.Second))
					continue // corrected: try again without waiting
				}
			}
			if hs.Resp.StatusCode == http.StatusUnauthorized || hs.Resp.StatusCode == http.StatusForbidden {
				return nil, err // refused for another reason: retrying will not help
			}
		}
		if attempt < retries {
			sleep(min(time.Duration(1<<attempt)*time.Second, 8*time.Second))
		}
	}
	return nil, last
}

func (e *Edge) once(ctx context.Context, text, voice string) ([]byte, error) {
	u, err := e.endpoint()
	if err != nil {
		return nil, err
	}
	id := randHex(16)
	q := url.Values{}
	q.Set("TrustedClientToken", edgeToken)
	q.Set("ConnectionId", id)
	q.Set("Sec-MS-GEC", SecMSGEC(time.Now().Add(time.Duration(e.skew.Load())*time.Second)))
	q.Set("Sec-MS-GEC-Version", "1-"+edgeChrom)
	full := *u
	full.RawQuery = q.Encode()
	hdr := http.Header{
		"Pragma":          {"no-cache"},
		"Cache-Control":   {"no-cache"},
		"Origin":          {"chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold"},
		"User-Agent":      {edgeAgent()},
		"Accept-Language": {"en-US,en;q=0.9"},
		"Cookie":          {"muid=" + strings.ToUpper(randHex(16)) + ";"},
	}
	conn, err := dialWS(ctx, &full, hdr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	stamp := time.Now().UTC().Format("Mon Jan 02 2006 15:04:05") + " GMT+0000 (Coordinated Universal Time)"
	cfg := "X-Timestamp:" + stamp + "\r\nContent-Type:application/json; charset=utf-8\r\nPath:speech.config\r\n\r\n" +
		`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"false"},"outputFormat":"` + edgeFormat + `"}}}}` + "\r\n"
	if err := conn.WriteText(cfg); err != nil {
		return nil, err
	}
	ssml := "<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'><voice name='" +
		voiceName(voice) + "'><prosody pitch='+0Hz' rate='+0%' volume='+0%'>" + text + "</prosody></voice></speak>"
	req := "X-RequestId:" + id + "\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:" + stamp + "Z\r\nPath:ssml\r\n\r\n" + ssml
	if err := conn.WriteText(req); err != nil {
		return nil, err
	}

	var audio []byte
	for {
		op, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("tts: read: %w", err)
		}
		switch op {
		case opText:
			if headerPath(msg) == "turn.end" {
				if len(audio) == 0 {
					return nil, errors.New("tts: no audio bytes were received")
				}
				if e.rawAudio {
					return audio, nil
				}
				return decodeMP3(audio)
			}
		case opBinary:
			if len(msg) < 2 {
				return nil, errors.New("tts: short audio frame")
			}
			hl := int(binary.BigEndian.Uint16(msg))
			if 2+hl > len(msg) {
				return nil, errors.New("tts: audio frame header overruns the frame")
			}
			if headerPath(msg[2:2+hl]) != "audio" {
				continue
			}
			if len(audio)+len(msg)-2-hl > maxAudioBytes {
				return nil, errors.New("tts: audio too large")
			}
			audio = append(audio, msg[2+hl:]...)
		}
	}
}

// headerPath reads the Path header of a message's header block.
func headerPath(b []byte) string {
	if i := bytes.Index(b, []byte("\r\n\r\n")); i >= 0 {
		b = b[:i]
	}
	for _, line := range strings.Split(string(b), "\r\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Path") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// cleanSSMLText escapes text for SSML and drops characters XML cannot carry.
func cleanSSMLText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && r != 0x7f && r != 0xFFFE && r != 0xFFFF {
			return r
		}
		return -1
	}, s)
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
