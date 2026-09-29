package voice

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeProbe answers by the command line it is given.
func fakeProbe(t *testing.T, answers map[string]string) {
	t.Helper()
	old := probe
	probe = func(_ context.Context, name string, args ...string) (string, error) {
		key := name + " " + strings.Join(args, " ")
		if out, ok := answers[key]; ok {
			return out, nil
		}
		return "", errors.New("not installed")
	}
	t.Cleanup(func() { probe = old })
}

func TestDevicesFromPulse(t *testing.T) {
	fakeProbe(t, map[string]string{
		"pactl get-default-source": "alsa_input.usb-Blue_Yeti.analog-stereo\n",
		"pactl list short sources": "0\talsa_output.pci.analog-stereo.monitor\tPipeWire\ts32le 2ch 48000Hz\tSUSPENDED\n" +
			"1\talsa_input.usb-Blue_Yeti.analog-stereo\tPipeWire\ts16le 2ch 48000Hz\tRUNNING\n",
	})
	got := Devices(context.Background())
	want := []string{
		"default input: alsa_input.usb-Blue_Yeti.analog-stereo",
		"input: alsa_output.pci.analog-stereo.monitor (suspended) - plays back an output, not a microphone",
		"input: alsa_input.usb-Blue_Yeti.analog-stereo (running)",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Devices = %q\nwant %q", got, want)
	}
}

func TestDevicesFallsBackToAlsa(t *testing.T) {
	fakeProbe(t, map[string]string{
		"arecord -l": "**** List of CAPTURE Hardware Devices ****\ncard 1: PCH [HDA Intel PCH], device 0: ALC3246 Analog [ALC3246 Analog]\n  Subdevices: 1/1\n",
	})
	got := Devices(context.Background())
	if len(got) != 1 || !strings.HasPrefix(got[0], "alsa card 1: PCH") {
		t.Fatalf("Devices = %q", got)
	}
}

func TestDevicesWithNothingInstalled(t *testing.T) {
	fakeProbe(t, nil)
	if got := Devices(context.Background()); len(got) != 0 {
		t.Fatalf("Devices = %q with no probe installed", got)
	}
}

func TestDevicesAreCappedAndCleaned(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("1\tsrc" + strings.Repeat("x", 300) + "\x1b[31m\tPipeWire\tfmt\tIDLE\n")
	}
	fakeProbe(t, map[string]string{"pactl list short sources": b.String()})
	got := Devices(context.Background())
	if len(got) != maxDeviceLines {
		t.Fatalf("%d lines, want the cap %d", len(got), maxDeviceLines)
	}
	for _, l := range got {
		if len([]rune(l)) > maxDeviceCols || strings.ContainsRune(l, 0x1b) {
			t.Fatalf("line not cleaned or capped: %q", l)
		}
	}
}

func TestSourceLinesSkipsJunk(t *testing.T) {
	if got := sourceLines("\n  \nonlyonefield\n"); len(got) != 0 {
		t.Fatalf("sourceLines = %q", got)
	}
}

func TestFirstLine(t *testing.T) {
	if firstLine("  a\nb\n") != "a" || firstLine("") != "" {
		t.Fatal("firstLine")
	}
}

func TestExecSourceCommandNamesTheHelperAndArgs(t *testing.T) {
	s := &ExecSource{bin: "/usr/bin/parecord", args: []string{"--raw", "--rate=16000", "-"}}
	if got := s.Command(); got != "parecord --raw --rate=16000 -" {
		t.Fatalf("Command = %q", got)
	}
}

func TestRealProbeIgnoresAMissingProgram(t *testing.T) {
	if _, err := probe(context.Background(), "belai-no-such-program-anywhere"); err == nil {
		t.Fatal("probing a missing program succeeded")
	}
}
