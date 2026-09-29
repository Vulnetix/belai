package voice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func lookOnly(names ...string) func(string) (string, error) {
	return func(n string) (string, error) {
		for _, have := range names {
			if n == have {
				return "/usr/bin/" + n, nil
			}
		}
		return "", exec.ErrNotFound
	}
}

func TestDeviceValidation(t *testing.T) {
	good := []string{"", "default", "alsa_input.pci-0000_00_1f.3.analog-stereo", "hw:1,0", "plughw:CARD=PCH,DEV=0"}
	for _, d := range good {
		if runtime.GOOS == "windows" && d == "" {
			continue
		}
		if _, err := newExecSource(d, lookOnly("parecord", "ffmpeg")); err != nil {
			t.Errorf("device %q rejected: %v", d, err)
		}
	}
	bad := []string{"-f", "--device=x", "a b", "a;b", "a$(x)", "a\nb", "../x", strings.Repeat("a", 200)}
	for _, d := range bad {
		if _, err := newExecSource(d, lookOnly("parecord")); !errors.Is(err, ErrBadDevice) {
			t.Errorf("device %q: err = %v, want ErrBadDevice", d, err)
		}
	}
}

func TestNoHelperInstalled(t *testing.T) {
	_, err := newExecSource("", lookOnly())
	if runtime.GOOS == "windows" {
		return
	}
	if !errors.Is(err, ErrNoCapture) {
		t.Fatalf("err = %v, want ErrNoCapture", err)
	}
	for _, name := range []string{"parecord", "arecord", "ffmpeg", "sox"} {
		if !strings.Contains(ErrNoCapture.Error(), name) {
			t.Errorf("the message does not name %s", name)
		}
	}
}

func TestHelperPreferenceAndArgv(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux helper order")
	}
	s, err := newExecSource("mic", lookOnly("arecord", "parecord", "ffmpeg"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "parecord" {
		t.Fatalf("picked %s, want parecord first", s.Name())
	}
	joined := strings.Join(s.args, " ")
	for _, want := range []string{"--raw", "--format=s16le", "--rate=16000", "--channels=1", "--device=mic"} {
		if !strings.Contains(joined, want) {
			t.Errorf("parecord argv %q lacks %s", joined, want)
		}
	}
	a, _ := newExecSource("hw:1,0", lookOnly("arecord"))
	if got := strings.Join(a.args, " "); !strings.Contains(got, "-f S16_LE") || !strings.Contains(got, "-r 16000") || !strings.Contains(got, "-D hw:1,0") {
		t.Errorf("arecord argv = %q", got)
	}
	f, _ := newExecSource("", lookOnly("ffmpeg"))
	if got := strings.Join(f.args, " "); !strings.Contains(got, "-ar 16000") || !strings.Contains(got, "-f s16le") || !strings.HasSuffix(got, "-") {
		t.Errorf("ffmpeg argv = %q", got)
	}
	r, _ := newExecSource("", lookOnly("rec"))
	if got := strings.Join(r.args, " "); !strings.Contains(got, "-t raw") || !strings.Contains(got, "-r 16000") {
		t.Errorf("rec argv = %q", got)
	}
}

func TestExecSourceStreamsSamples(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	// 1000 little-endian samples of value 0x0102, delivered as an odd number
	// of bytes at a time by the pipe.
	s := &ExecSource{bin: "/bin/sh", args: []string{"-c", "i=0; while [ $i -lt 1000 ]; do printf '\\002\\001'; i=$((i+1)); done"}}
	st, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for chunk := range st.C {
		for _, v := range chunk {
			if v != 0x0102 {
				t.Fatalf("sample = %#x", v)
			}
		}
		total += len(chunk)
	}
	if total != 1000 {
		t.Fatalf("got %d samples, want 1000", total)
	}
	if err := st.Err(); err == nil || !strings.Contains(err.Error(), "ended unexpectedly") {
		t.Fatalf("Err = %v; a helper that exits on its own is a capture failure", err)
	}
}

func TestExecSourceStopIsNotAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	s := &ExecSource{bin: "/bin/sh", args: []string{"-c", "while true; do printf '\\001\\000'; sleep 0.01; done"}}
	ctx, cancel := context.WithCancel(context.Background())
	st, err := s.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-st.C:
	case <-time.After(5 * time.Second):
		t.Fatal("no audio")
	}
	cancel()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-st.C:
			if !ok {
				if err := st.Err(); err != nil {
					t.Fatalf("a requested stop reported %v", err)
				}
				return
			}
		case <-deadline:
			t.Fatal("the channel never closed after cancel")
		}
	}
}

func TestExecSourceReportsAFailingHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	s := &ExecSource{bin: "/bin/sh", args: []string{"-c", "echo 'no such device' >&2; exit 3"}}
	st, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range st.C {
	}
	err = st.Err()
	if err == nil || !strings.Contains(err.Error(), "no such device") {
		t.Fatalf("Err = %v, want the helper's stderr", err)
	}
}

func TestExecSourceStartFailure(t *testing.T) {
	s := &ExecSource{bin: "/nonexistent/helper"}
	if _, err := s.Start(context.Background()); err == nil {
		t.Fatal("starting a missing helper succeeded")
	}
}

func TestTailWriterKeepsTheEndAndFlattens(t *testing.T) {
	w := &tailWriter{max: 10}
	_, _ = w.Write([]byte("0123456789abc\n\x1b[31mdef"))
	got := w.suffix()
	if strings.ContainsAny(got, "\n\x1b") {
		t.Fatalf("control characters survived: %q", got)
	}
	if !strings.HasSuffix(got, "def") {
		t.Fatalf("suffix = %q", got)
	}
	if (&tailWriter{max: 10}).suffix() != "" {
		t.Fatal("empty writer has a suffix")
	}
}

// parecord treats a lone "-" as the name of a file, not as standard output, so
// belai's argv gives it no file at all. With the "-" the recording went into a
// file called "-" in the working directory and belai received nothing.
func TestParecordIsGivenNoFileToWrite(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux helper order")
	}
	for _, dev := range []string{"", "alsa_input.pci-0000_64_00.6.HiFi__Mic1__source"} {
		s, err := newExecSource(dev, lookOnly("parecord"))
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range s.args {
			if a == "-" || !strings.HasPrefix(a, "--") {
				t.Errorf("parecord argv %q has a positional argument %q that it would take as a file to write", s.args, a)
			}
		}
	}
}

func TestNoHelperWritesRecordingsToTheWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	// A helper that drops a file as soon as it starts: the file must land in the
	// helper's private directory, never in ours.
	s := &ExecSource{bin: "/bin/sh", args: []string{"-c", "echo x > stray-recording; printf '\\001\\000'"}}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range st.C {
	}
	if _, err := os.Stat(filepath.Join(cwd, "stray-recording")); err == nil {
		os.Remove(filepath.Join(cwd, "stray-recording"))
		t.Fatal("a capture helper wrote a file into the working directory")
	}
}

func TestPrivateDirectoryIsRemovedWhenTheHelperEnds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	s := &ExecSource{bin: "/bin/sh", args: []string{"-c", "echo x > f; printf '\\001\\000'"}}
	st, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range st.C {
	}
	_ = st.Err() // Wait has finished by the time the channel closed
	deadline := time.After(5 * time.Second)
	for {
		left, _ := filepath.Glob(filepath.Join(tmp, "belai-voice-*"))
		if len(left) == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the helper's private directory was left behind: %v", left)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
