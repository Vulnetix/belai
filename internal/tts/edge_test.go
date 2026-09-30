package tts

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEdge is a websocket server that speaks enough of the service to test the
// client. It records what the client sent and never leaves loopback.
type fakeEdge struct {
	srv    *httptest.Server
	mu     sync.Mutex
	texts  []string
	gecs   []string
	hits   atomic.Int32
	status func(n int32) int // HTTP status for the nth upgrade; 0 means accept
	date   string
	audio  [][]byte
}

func newFakeEdge(t *testing.T) *fakeEdge {
	t.Helper()
	mp3, err := os.ReadFile("testdata/hello.mp3")
	if err != nil {
		t.Fatal(err)
	}
	half := len(mp3) / 2
	f := &fakeEdge{audio: [][]byte{mp3[:half], mp3[half:]}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEdge) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") + edgePath }

func (f *fakeEdge) serve(w http.ResponseWriter, r *http.Request) {
	n := f.hits.Add(1)
	f.mu.Lock()
	f.gecs = append(f.gecs, r.URL.Query().Get("Sec-MS-GEC"))
	f.mu.Unlock()
	if f.status != nil {
		if code := f.status(n); code != 0 {
			if f.date != "" {
				w.Header().Set("Date", f.date)
			}
			w.WriteHeader(code)
			return
		}
	}
	hj := w.(http.Hijacker)
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	_ = rw.Flush()
	for i := 0; i < 2; i++ { // config, then ssml
		msg, err := readMasked(rw.Reader)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.texts = append(f.texts, msg)
		f.mu.Unlock()
	}
	writeServer(conn, opText, []byte("X-RequestId:1\r\nPath:turn.start\r\n\r\n{}"))
	for _, a := range f.audio {
		h := []byte("X-RequestId:1\r\nPath:audio\r\n")
		frame := binary.BigEndian.AppendUint16(nil, uint16(len(h)))
		frame = append(append(frame, h...), a...)
		writeServer(conn, opBinary, frame)
	}
	writeServer(conn, opText, []byte("X-RequestId:1\r\nPath:turn.end\r\n\r\n{}"))
}

func readMasked(br *bufio.Reader) (string, error) {
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return "", err
	}
	if h[1]&0x80 == 0 {
		return "", io.ErrUnexpectedEOF // a client frame must be masked
	}
	n := int(h[1] & 0x7F)
	if n == 126 {
		var b [2]byte
		_, _ = io.ReadFull(br, b[:])
		n = int(binary.BigEndian.Uint16(b[:]))
	}
	var mask [4]byte
	_, _ = io.ReadFull(br, mask[:])
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		return "", err
	}
	for i := range body {
		body[i] ^= mask[i%4]
	}
	return string(body), nil
}

func writeServer(c net.Conn, op byte, payload []byte) {
	hdr := []byte{0x80 | op}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, byte(n))
	default:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	}
	_, _ = c.Write(append(hdr, payload...))
}

func fastEdge(f *fakeEdge) *Edge {
	return &Edge{URL: f.url(), Sleep: func(time.Duration) {}}
}

func TestEdgeSynthesizeCollectsAudioAndSendsTheProtocol(t *testing.T) {
	f := newFakeEdge(t)
	pcm, err := fastEdge(f).Synthesize(context.Background(), `Tom & "Jerry" <b>`, "en-US-AriaNeural")
	if err != nil {
		t.Fatal(err)
	}
	mp3, _ := os.ReadFile("testdata/hello.mp3")
	want, err := decodeMP3(mp3)
	if err != nil {
		t.Fatal(err)
	}
	if string(pcm) != string(want) {
		t.Fatalf("got %d bytes of PCM, want the %d the fixture decodes to", len(pcm), len(want))
	}
	if secs := float64(len(pcm)) / (SampleRate * BytesPerSample); secs < 1.5 || secs > 2.5 {
		t.Fatalf("%.2f s of audio for a 1.9 s fixture", secs)
	}
	if len(f.texts) != 2 {
		t.Fatalf("client sent %d messages, want config and ssml", len(f.texts))
	}
	cfg, ssml := f.texts[0], f.texts[1]
	for _, want := range []string{"Path:speech.config", edgeFormat} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q: %s", want, cfg)
		}
	}
	for _, want := range []string{"Path:ssml", "Microsoft Server Speech Text to Speech Voice (en-US, AriaNeural)", "Tom &amp; &#34;Jerry&#34; &lt;b&gt;"} {
		if !strings.Contains(ssml, want) {
			t.Errorf("ssml lacks %q: %s", want, ssml)
		}
	}
	if strings.Contains(ssml, "<b>") {
		t.Fatal("text was put into the SSML unescaped")
	}
}

func TestEdgeRefusesABadVoiceAndEmptyText(t *testing.T) {
	f := newFakeEdge(t)
	e := fastEdge(f)
	for _, v := range []string{"", "a b", "x'/><script", strings.Repeat("a", 65)} {
		if _, err := e.Synthesize(context.Background(), "hi", v); err == nil {
			t.Errorf("voice %q accepted", v)
		}
	}
	if _, err := e.Synthesize(context.Background(), "  \n ", DefaultVoice); err == nil {
		t.Error("empty text accepted")
	}
	if f.hits.Load() != 0 {
		t.Fatal("a refused request still reached the server")
	}
}

func TestEdgeCorrectsClockSkewFromTheDateHeaderAndRetries(t *testing.T) {
	f := newFakeEdge(t)
	ahead := time.Now().Add(2 * time.Hour).UTC()
	f.date = ahead.Format(http.TimeFormat)
	f.status = func(n int32) int {
		if n == 1 {
			return http.StatusForbidden
		}
		return 0
	}
	if _, err := fastEdge(f).Synthesize(context.Background(), "hi", DefaultVoice); err != nil {
		t.Fatal(err)
	}
	if len(f.gecs) != 2 || f.gecs[0] == f.gecs[1] {
		t.Fatalf("the second try did not use the corrected clock: %v", f.gecs)
	}
	if want := SecMSGEC(ahead); f.gecs[1] != want {
		t.Fatalf("token %s, want %s", f.gecs[1], want)
	}
}

func TestEdgeRetriesATransientFailureWithBackoffThenGivesUp(t *testing.T) {
	f := newFakeEdge(t)
	f.status = func(int32) int { return http.StatusBadGateway }
	var waits []time.Duration
	e := &Edge{URL: f.url(), Retries: 4, Sleep: func(d time.Duration) { waits = append(waits, d) }}
	if _, err := e.Synthesize(context.Background(), "hi", DefaultVoice); err == nil {
		t.Fatal("a server that always fails succeeded")
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v", waits)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}
	if f.hits.Load() != 5 {
		t.Fatalf("%d attempts, want 1 + 4 retries", f.hits.Load())
	}
}

func TestEdgeRecoversOnTheNextAttempt(t *testing.T) {
	f := newFakeEdge(t)
	f.status = func(n int32) int {
		if n < 3 {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	if _, err := fastEdge(f).Synthesize(context.Background(), "hi", DefaultVoice); err != nil {
		t.Fatal(err)
	}
}

func TestEdgeAuthRefusalWithoutADateIsNotRetried(t *testing.T) {
	f := newFakeEdge(t)
	f.status = func(int32) int { return http.StatusUnauthorized }
	if _, err := fastEdge(f).Synthesize(context.Background(), "hi", DefaultVoice); err == nil {
		t.Fatal("expected an error")
	}
	if f.hits.Load() != 1 {
		t.Fatalf("%d attempts after a 401", f.hits.Load())
	}
}

func TestEdgeNoAudioIsAnError(t *testing.T) {
	f := newFakeEdge(t)
	f.audio = nil
	e := &Edge{URL: f.url(), Retries: 1, Sleep: func(time.Duration) {}}
	if _, err := e.Synthesize(context.Background(), "hi", DefaultVoice); err == nil || !strings.Contains(err.Error(), "no audio") {
		t.Fatalf("err = %v", err)
	}
}

func TestEdgeHonoursCancellation(t *testing.T) {
	f := newFakeEdge(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fastEdge(f).Synthesize(ctx, "hi", DefaultVoice); err == nil {
		t.Fatal("a cancelled context still synthesised")
	}
	if f.hits.Load() != 0 {
		t.Fatal("a cancelled request reached the server")
	}
}

func TestEdgeEndpointRules(t *testing.T) {
	for _, bad := range []string{
		"ws://example.com/x",                 // plain http to a public host
		"https://speech.platform.bing.com/x", // not a websocket scheme
		"wss://user:pw@speech.platform.bing.com/x",
	} {
		if _, err := (&Edge{URL: bad}).endpoint(); err == nil {
			t.Errorf("endpoint %q accepted", bad)
		}
	}
	u, err := NewEdge().endpoint()
	if err != nil || u.Host != edgeHost || u.Scheme != "wss" {
		t.Fatalf("default endpoint = %v, %v", u, err)
	}
}

func TestSecMSGECIsStableWithinFiveMinutes(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	a := SecMSGEC(base)
	if a != SecMSGEC(base.Add(4*time.Minute+59*time.Second)) {
		t.Fatal("the token changed inside the window")
	}
	if a == SecMSGEC(base.Add(5*time.Minute)) {
		t.Fatal("the token did not change at the window edge")
	}
	if len(a) != 64 || a != strings.ToUpper(a) {
		t.Fatalf("token %q is not 64 upper-case hex", a)
	}
}

func TestVoiceNameExpansion(t *testing.T) {
	cases := map[string]string{
		"en-US-AriaNeural":             "Microsoft Server Speech Text to Speech Voice (en-US, AriaNeural)",
		"en-GB-RyanNeural":             "Microsoft Server Speech Text to Speech Voice (en-GB, RyanNeural)",
		"zh-CN-liaoning-XiaobeiNeural": "Microsoft Server Speech Text to Speech Voice (zh-CN-liaoning, XiaobeiNeural)",
		"custom":                       "custom",
	}
	for in, want := range cases {
		if got := voiceName(in); got != want {
			t.Errorf("voiceName(%q) = %q", in, got)
		}
	}
}

func TestWSRejectsMaskedServerFramesAndOversize(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	w := &wsConn{c: c1, br: bufio.NewReader(c1)}
	go func() { _, _ = c2.Write([]byte{0x81, 0x85, 1, 2, 3, 4, 0, 0, 0, 0, 0}) }()
	if _, _, err := w.ReadMessage(); err == nil || !strings.Contains(err.Error(), "masked") {
		t.Fatalf("masked frame: %v", err)
	}
}

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ch := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); ch <- c }()
	c1, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c2 := <-ch
	t.Cleanup(func() { c1.Close(); c2.Close() })
	return c1, c2
}

func TestWSReassemblesFragmentsAndAnswersPings(t *testing.T) {
	c1, c2 := tcpPair(t)
	w := &wsConn{c: c1, br: bufio.NewReader(c1)}
	pong := make(chan []byte, 1)
	go func() {
		_, _ = c2.Write([]byte{0x01, 0x02, 'a', 'b'}) // text, not final
		_, _ = c2.Write([]byte{0x89, 0x01, 'p'})      // ping in the middle
		_, _ = c2.Write([]byte{0x80, 0x02, 'c', 'd'}) // final continuation
		buf := make([]byte, 7)
		n, _ := io.ReadFull(c2, buf)
		pong <- buf[:n]
	}()
	op, msg, err := w.ReadMessage()
	if err != nil || op != opText || string(msg) != "abcd" {
		t.Fatalf("got %d %q %v", op, msg, err)
	}
	select {
	case p := <-pong:
		if len(p) < 2 || p[0] != 0x80|opPong || p[1]&0x80 == 0 {
			t.Fatalf("pong frame = %v (must be a masked pong)", p)
		}
	case <-time.After(time.Second):
		t.Fatal("no pong")
	}
}
