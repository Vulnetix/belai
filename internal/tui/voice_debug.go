package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/voice"
)

// /voice debug: a diagnostic screen for "I talked and nothing came out". It
// runs its own listening engine (the normal one is paused while it is open),
// so what it shows is the whole path: the hardware belai can see, the level of
// the audio stream, what the speech model heard, what the fast model made of
// it, and the keys the terminal delivered.

const (
	dbgTick     = 100 * time.Millisecond
	dbgHistory  = 48
	dbgLogMax   = 200
	dbgRawMax   = 400 // characters of raw transcript kept and shown
	dbgCheckFor = 3 * time.Second
	dbgMeterW   = 44
	dbgMeterMin = -80.0 // dBFS at the left end of the meter
	dbgNoSound  = -70.0 // a peak at or below this is no sound at all
	dbgCleanTTL = 20 * time.Second
	// dbgKeyGap is how long without a key event starts a new press.
	dbgKeyGap = 1200 * time.Millisecond
)

type (
	voiceDebugTickMsg  struct{}
	voiceDebugEventMsg struct {
		eng *voice.Engine
		ev  voice.Event
	}
	voiceDebugCleanMsg struct {
		raw, text string
		err       error
		took      time.Duration
	}
	voiceDebugHardwareMsg struct{ lines []string }
)

// voiceDebugState is everything the debug screen shows.
type voiceDebugState struct {
	eng    *voice.Engine
	stop   context.CancelFunc
	resume bool // the normal voice engine was running and comes back on exit

	started  time.Time
	hardware []string
	command  string
	problem  string // why the engine could not start, if it could not

	hist          []float64
	peakHold      float64
	maxPeak       float64
	level         voice.Level
	lastState     voice.State
	haveLastState bool

	raw        string
	cleaned    string
	cleanNote  string
	cleanBusy  bool
	cleanDirty bool

	log []string

	// The voice key as the terminal delivers it.
	keyFirst, keyLast time.Time
	keyCount          int
	keyHeld           bool
	keyResolved       bool
}

// voiceDebugStart opens the screen. It pauses the normal engine, starts a
// listening engine of its own and probes the hardware in the background.
func (a *App) voiceDebugStart() tea.Cmd {
	d := &a.vdebug
	if d.eng != nil {
		return nil
	}
	*d = voiceDebugState{started: time.Now(), maxPeak: -100, peakHold: -100, level: voice.Silent}
	d.resume = a.voice.eng != nil
	a.voiceStop()
	d.logf("debug opened")

	v := &a.voice
	device := a.settings.Voice.VoiceDevice()
	src, err := v.source(device)
	switch {
	case err != nil:
		d.problem = sanitize.Line(err.Error(), 300)
	case v.model() == "":
		d.problem = "the speech model is not downloaded: run /voice download"
	default:
		if c, ok := src.(interface{ Command() string }); ok {
			d.command = c.Command()
		}
		ctx, cancel := context.WithCancel(context.Background())
		d.stop = cancel
		d.eng = voice.New(ctx, voice.Config{Source: src, Load: v.recognizer, Mode: voice.ModeListen})
		d.eng.SetReady(true)
		d.eng.SetEnabled(true)
		d.logf("engine started, loading the model")
	}
	if d.problem != "" {
		d.logf("cannot listen: %s", d.problem)
	}
	cmds := []tea.Cmd{a.push(viewVoiceDebug), voiceDebugTickCmd(), voiceDebugHardwareCmd()}
	if d.eng != nil {
		cmds = append(cmds, a.watchVoiceDebug(d.eng))
	}
	return tea.Batch(cmds...)
}

// voiceDebugStop closes the screen's engine and, if the normal one was
// running, starts it again. Safe to call when the screen is not open.
func (a *App) voiceDebugStop() tea.Cmd {
	d := &a.vdebug
	if d.eng == nil && !d.resume {
		return nil
	}
	if d.eng != nil {
		d.eng.Close()
	}
	if d.stop != nil {
		d.stop()
	}
	d.eng, d.stop = nil, nil
	resume := d.resume
	d.resume = false
	if resume {
		return a.voiceStart(false)
	}
	return nil
}

func voiceDebugTickCmd() tea.Cmd {
	return tea.Tick(dbgTick, func(time.Time) tea.Msg { return voiceDebugTickMsg{} })
}

func voiceDebugHardwareCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return voiceDebugHardwareMsg{lines: voice.Devices(ctx)}
	}
}

func (a *App) watchVoiceDebug(e *voice.Engine) tea.Cmd {
	return func() tea.Msg {
		select {
		case ev := <-e.Events():
			return voiceDebugEventMsg{eng: e, ev: ev}
		case <-e.Done():
			return nil
		}
	}
}

// handleVoiceDebugMsg handles the debug screen's messages.
func (a *App) handleVoiceDebugMsg(msg tea.Msg) (tea.Cmd, bool) {
	d := &a.vdebug
	switch m := msg.(type) {
	case voiceDebugHardwareMsg:
		d.hardware = m.lines
		if len(m.lines) == 0 {
			d.logf("no probe program (pactl, arecord) is installed, so inputs are not listed")
		} else {
			d.logf("found %d input line(s)", len(m.lines))
		}
		return nil, true
	case voiceDebugTickMsg:
		if a.view != viewVoiceDebug {
			// Something else took the screen (a global key): do not leave a
			// microphone open behind it.
			return a.voiceDebugStop(), true
		}
		if d.eng == nil {
			return voiceDebugTickCmd(), true
		}
		d.tick(time.Now())
		return voiceDebugTickCmd(), true
	case voiceDebugEventMsg:
		if m.eng != d.eng {
			return nil, true
		}
		cmd := a.watchVoiceDebug(m.eng)
		switch m.ev.Kind {
		case voice.EventState:
			if !d.haveLastState || d.lastState != m.ev.State {
				d.logf("engine %s", m.ev.State)
			}
			d.lastState, d.haveLastState = m.ev.State, true
		case voice.EventError:
			d.problem = sanitize.Line(m.ev.Err.Error(), 300)
			d.logf("error: %s", d.problem)
		case voice.EventTranscript:
			cmd = tea.Batch(cmd, a.voiceDebugHeard(m.ev.Text))
		}
		return cmd, true
	case voiceDebugCleanMsg:
		return a.voiceDebugCleaned(m), true
	}
	return nil, false
}

// voiceDebugHeard takes one raw transcript: it joins the rolling raw text and
// starts the fast-model tidy-up of it.
func (a *App) voiceDebugHeard(text string) tea.Cmd {
	d := &a.vdebug
	text = strings.TrimSpace(sanitize.Text(text))
	if text == "" {
		return nil
	}
	d.raw = lastRunes(strings.TrimSpace(d.raw+" "+text), dbgRawMax)
	d.logf("heard %d characters", len([]rune(text)))
	if a.classifier == nil {
		d.cleaned, d.cleanNote = "", "no fast model is configured, so dictation would use the raw text as it is"
		return nil
	}
	if d.cleanBusy {
		d.cleanDirty = true
		return nil
	}
	return a.voiceDebugClean()
}

func (a *App) voiceDebugClean() tea.Cmd {
	d := &a.vdebug
	d.cleanBusy, d.cleanDirty = true, false
	raw, c := d.raw, a.classifier
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), dbgCleanTTL)
		defer cancel()
		start := time.Now()
		text, err := rolemanager.CleanVoice(ctx, c, raw)
		return voiceDebugCleanMsg{raw: raw, text: text, err: err, took: time.Since(start)}
	}
}

func (a *App) voiceDebugCleaned(m voiceDebugCleanMsg) tea.Cmd {
	d := &a.vdebug
	d.cleanBusy = false
	if m.err != nil {
		d.cleaned = ""
		d.cleanNote = "the fast model did not help (" + sanitize.Line(m.err.Error(), 120) + "): dictation would use the raw text"
		d.logf("cleanup failed: %s", sanitize.Line(m.err.Error(), 80))
	} else {
		d.cleaned = m.text
		d.cleanNote = fmt.Sprintf("tidied by the fast model in %d ms", m.took.Milliseconds())
		d.logf("cleanup ok in %d ms", m.took.Milliseconds())
	}
	if d.cleanDirty {
		return a.voiceDebugClean()
	}
	return nil
}

// tick reads the level and resolves the key analysis. It is called every
// dbgTick while the screen is open.
func (d *voiceDebugState) tick(now time.Time) {
	lv := d.eng.Level()
	d.level = lv
	d.hist = append(d.hist, lv.RMS)
	if len(d.hist) > dbgHistory {
		d.hist = d.hist[len(d.hist)-dbgHistory:]
	}
	if lv.Peak > d.maxPeak {
		d.maxPeak = lv.Peak
	}
	// The peak marker falls away slowly so a brief peak can be seen.
	if lv.Peak >= d.peakHold {
		d.peakHold = lv.Peak
	} else {
		d.peakHold -= 1.5
	}
	d.resolveKey(now)
}

// noteKey records a key the terminal delivered. The voice key is analysed the
// way voiceKey reads it: a first press, then repeats (a hold) or none (a tap).
func (d *voiceDebugState) noteKey(name, voiceKey string, now time.Time) {
	if !voiceKeyMatches(name, voiceKey) {
		d.logf("key %s (not the voice key %s)", sanitize.Line(name, 24), voiceKey)
		return
	}
	if d.keyCount == 0 || now.Sub(d.keyLast) > dbgKeyGap {
		d.keyFirst, d.keyLast, d.keyCount, d.keyHeld, d.keyResolved = now, now, 1, false, false
		d.logf("key %s press", name)
		return
	}
	gap := now.Sub(d.keyLast)
	d.keyLast = now
	d.keyCount++
	if !d.keyHeld {
		d.keyHeld = true
		d.logf("key %s repeat after %d ms: this terminal repeats a held key, so hold works", name, gap.Milliseconds())
	}
}

// resolveKey logs what a finished press amounts to.
func (d *voiceDebugState) resolveKey(now time.Time) {
	if d.keyCount == 0 || d.keyResolved {
		return
	}
	switch {
	case !d.keyHeld && now.Sub(d.keyFirst) >= voiceTapGrace:
		d.keyResolved = true
		d.logf("no repeat within %d ms: that was a tap, and a tap starts a recording that ends when you stop speaking", voiceTapGrace.Milliseconds())
	case d.keyHeld && now.Sub(d.keyLast) >= voiceRepeatGap:
		d.keyResolved = true
		d.logf("release inferred: %d repeats over %.1f s, then none for %d ms", d.keyCount-1, d.keyLast.Sub(d.keyFirst).Seconds(), voiceRepeatGap.Milliseconds())
	}
}

func (d *voiceDebugState) logf(format string, args ...any) {
	ms := int(time.Since(d.started).Milliseconds())
	if d.started.IsZero() {
		ms = 0
	}
	line := fmt.Sprintf("%02d:%02d.%03d  %s", ms/60000, ms/1000%60, ms%1000, fmt.Sprintf(format, args...))
	d.log = append(d.log, sanitize.Line(line, 200))
	if len(d.log) > dbgLogMax {
		d.log = d.log[len(d.log)-dbgLogMax:]
	}
}

// handleVoiceDebugKey is the screen's key handler.
func (a *App) handleVoiceDebugKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &a.vdebug
	switch m.String() {
	case "esc", "q":
		cmd := a.voiceDebugStop()
		a.pop()
		return a, cmd
	case "c":
		d.log, d.raw, d.cleaned, d.cleanNote, d.maxPeak = nil, "", "", "", -100
		d.logf("cleared")
		return a, nil
	}
	d.noteKey(m.String(), a.settings.Voice.VoiceKeyOr(), time.Now())
	return a, nil
}

// --- pure helpers (tested directly) -----------------------------------------

// micVerdict says what the level tells about the microphone. Before the check
// window has passed it only says it is checking.
func micVerdict(elapsed time.Duration, maxPeak float64, samples int64, helper string) string {
	switch {
	case elapsed < dbgCheckFor:
		return "checking the microphone... make some noise or speak"
	case samples == 0:
		return fmt.Sprintf("No audio data arrived from the capture helper (%s) in %.0f s. It is running but delivering nothing, so the fault is before the level check: it may be blocked, or unable to open the input. Try another helper (arecord, ffmpeg or sox) or another voice.device.", helper, elapsed.Seconds())
	case maxPeak <= dbgNoSound:
		return fmt.Sprintf("No sound reached belai in %.0f s (peak %.0f dBFS). Perhaps the microphone is on mute, the wrong input is selected, or voice.device names a silent source. Compare the input list above.", elapsed.Seconds(), maxPeak)
	case maxPeak < voice.SpeechDB:
		return fmt.Sprintf("Sound is arriving (peak %.0f dBFS) but it is below the speech level (%.0f dBFS): speak closer, or raise the input gain.", maxPeak, voice.SpeechDB)
	}
	return fmt.Sprintf("The microphone is working: speech-level sound reached belai (peak %.0f dBFS).", maxPeak)
}

// meterBar draws a level between dbgMeterMin and 0 dBFS as cells, with a
// marker at the speech level and one at the held peak. It is plain text.
func meterBar(rms, peak float64, cells int) string {
	pos := func(db float64) int {
		p := int((db - dbgMeterMin) / -dbgMeterMin * float64(cells))
		return max(0, min(cells, p))
	}
	fill, gate, hold := pos(rms), pos(voice.SpeechDB), pos(peak)
	out := make([]rune, cells)
	for i := range out {
		switch {
		case i < fill:
			out[i] = '█'
		case i == gate:
			out[i] = '┊'
		case i == hold-1 && hold > fill:
			out[i] = '▏'
		default:
			out[i] = '·'
		}
	}
	return string(out)
}

// sparkline draws recent levels as eight-step blocks.
func sparkline(levels []float64) string {
	const blocks = "▁▂▃▄▅▆▇█"
	rs := []rune(blocks)
	out := make([]rune, len(levels))
	for i, db := range levels {
		p := int((db - dbgMeterMin) / -dbgMeterMin * float64(len(rs)))
		out[i] = rs[max(0, min(len(rs)-1, p))]
	}
	return string(out)
}

// lastRunes keeps the last n characters of s.
func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// --- rendering ---------------------------------------------------------------

// voiceDebugView renders the screen.
func (a *App) voiceDebugView() string {
	d := &a.vdebug
	w := max(a.contentWidth()-2, 40)
	head := func(s string) string { return components.AccentStyle.Render(s) }
	dim := components.MutedStyle.Render
	wrap := func(s string) string { return lipgloss.NewStyle().Width(w - 2).Render(s) }

	var b strings.Builder
	b.WriteString(components.AccentStyle.Bold(true).Render("◈ Voice debug") + "  " + dim("esc closes · c clears the log") + "\n")
	b.WriteString(components.LineStyle.Render(strings.Repeat("─", w)) + "\n")

	b.WriteString(head("Hardware") + "\n")
	helper := "none found"
	if d.command != "" {
		helper = d.command
	}
	b.WriteString(fmt.Sprintf("  capture   %s\n", truncateRunes(helper, w-12)))
	switch {
	case len(d.hardware) == 0:
		b.WriteString(dim("  inputs    (probing, or no pactl or arecord installed)") + "\n")
	default:
		for _, l := range d.hardware {
			b.WriteString("  " + truncateRunes(l, w-2) + "\n")
		}
	}
	model := a.voice.model()
	switch model {
	case "":
		model = "not downloaded"
	case "built in":
	default:
		model = voice.ModelFile + " on disk"
	}
	b.WriteString(fmt.Sprintf("  model     %s\n", model))
	engine := "not running"
	if d.eng != nil {
		engine = d.eng.State().String()
	}
	if d.problem != "" {
		engine += " · " + d.problem
	}
	b.WriteString(fmt.Sprintf("  engine    %s\n\n", truncateRunes(engine, w-12)))

	b.WriteString(head("Microphone level") + "\n")
	elapsed := time.Since(d.started)
	var samples int64
	if d.eng != nil {
		samples = d.eng.Samples()
	}
	bar := meterBar(d.level.RMS, d.peakHold, dbgMeterW)
	b.WriteString(fmt.Sprintf("  %s  %s\n", colourMeter(bar, d.level.RMS), dim(fmt.Sprintf("rms %5.1f  peak %5.1f dBFS  speech gate %.0f", d.level.RMS, d.level.Peak, voice.SpeechDB))))
	b.WriteString("  " + dim(sparkline(d.hist)) + "\n")
	b.WriteString(wrap("  "+micVerdict(elapsed, d.maxPeak, samples, helperName(d.command))) + "\n\n")

	b.WriteString(head(fmt.Sprintf("Raw transcript (last %d characters)", dbgRawMax)) + "\n")
	if d.raw == "" {
		b.WriteString(dim("  nothing heard yet: speak, then pause for a second") + "\n\n")
	} else {
		b.WriteString(wrap("  "+d.raw) + "\n\n")
	}

	b.WriteString(head("Fast-model cleanup (live)") + "\n")
	switch {
	case d.cleaned != "":
		// The last result stays up while a newer one is being made.
		b.WriteString(wrap("  "+lipgloss.NewStyle().Foreground(components.ColorVoice).Render(d.cleaned)) + "\n")
		note := d.cleanNote
		if d.cleanBusy {
			note = "updating with what you just said..."
		}
		b.WriteString(dim("  "+note) + "\n")
	case d.cleanBusy:
		b.WriteString(dim("  tidying...") + "\n")
	case d.cleanNote != "":
		b.WriteString(wrap(dim("  "+d.cleanNote)) + "\n")
	default:
		b.WriteString(dim("  waits for a transcript") + "\n")
	}
	b.WriteString("\n" + head("Events") + " " + dim("keys, engine states, transcripts, errors") + "\n")

	used := strings.Count(b.String(), "\n")
	rows := max(a.height-used-3, 4)
	lines := d.log
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	for _, l := range lines {
		b.WriteString("  " + truncateRunes(l, w-2) + "\n")
	}
	if len(lines) == 0 {
		b.WriteString(dim("  press the voice key, or speak") + "\n")
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(b.String())
}

// colourMeter tints a meter bar: teal once the level passes the speech gate,
// muted below it.
func colourMeter(bar string, rms float64) string {
	style := components.MutedStyle
	if rms >= voice.SpeechDB {
		style = lipgloss.NewStyle().Foreground(components.ColorTeal)
	}
	return style.Render(bar)
}

// helperName is the program at the start of a capture command line.
func helperName(command string) string {
	if f := strings.Fields(command); len(f) > 0 {
		return f[0]
	}
	return "none"
}
