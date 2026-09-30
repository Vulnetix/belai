package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tts"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/voicecmd"
)

// Reading aloud (docs/tts.md). internal/tts owns the service, the cache and
// the player; this file owns when a message is read, the card in the thread
// and what the keys, the mouse and a spoken "stop" do to it.
//
// The text read aloud is sent to Microsoft's read-aloud service, so nothing
// here runs until the user has turned it on with /tts on, which is also the
// consent. The card is render-only state: the Message holds a PlayerCard and
// no audio, and is Ephemeral, so it never reaches the session record, sync or
// a model.

const (
	ttsAnimInterval = 100 * time.Millisecond
	ttsTitleWords   = 8
	// ttsEchoWindow is how long after playback ends a transcript that matches
	// what was read is still taken for the speaker, not the user.
	ttsEchoWindow = 3 * time.Second
)

// ttsVoices are the voices the /settings row steps through. Any valid name can
// be set with /tts voice or in settings.json.
var ttsVoices = []string{
	"en-US-AndrewMultilingualNeural", "en-US-BrianMultilingualNeural", "en-US-AriaNeural",
	"en-US-AvaMultilingualNeural", "en-GB-RyanNeural", "en-GB-ThomasNeural",
}

// ttsState is the TUI's side of reading aloud.
type ttsState struct {
	// Seams for tests; nil means the real implementation.
	engine   tts.Engine
	open     tts.Opener
	cacheDir string
	noPace   bool // play as fast as the writer takes it (tests)

	cache    *tts.Cache
	cacheKey string

	player   *tts.Player
	cancel   context.CancelFunc
	gen      int
	cardIdx  int // the card's message, -1 when none
	srcIdx   int // the message being read
	title    string
	spoken   map[string]bool // the words being read, for the echo filter
	total    int
	done     int
	status   string
	finished bool // the synthesis stream is over
	ch       <-chan tts.Result
	cached   bool

	frame     int
	animating bool
	scrub     bool
	scrubHit  components.Hit
	echoUntil time.Time
}

type (
	ttsResultMsg struct {
		gen    int
		ch     <-chan tts.Result
		r      tts.Result
		closed bool
	}
	ttsAnimMsg struct{ gen int }
)

func (a *App) ttsEngine() tts.Engine {
	if a.tts.engine == nil {
		a.tts.engine = tts.NewEdge()
	}
	return a.tts.engine
}

// ttsCache returns the audio cache for the current settings, rebuilt when the
// size changes.
func (a *App) ttsCache() *tts.Cache {
	mb := a.settings.TTS.TTSCacheMBOr()
	dir := a.tts.cacheDir
	if dir == "" {
		d, err := tts.DefaultCacheDir()
		if err != nil {
			mb = 0 // nowhere to keep it: the cache is off, playback still works
		}
		dir = d
	}
	key := fmt.Sprintf("%s|%d", dir, mb)
	if a.tts.cache == nil || a.tts.cacheKey != key {
		a.tts.cache, a.tts.cacheKey = tts.NewCache(dir, mb), key
	}
	return a.tts.cache
}

// ttsActive reports whether sound is playing or about to, which is when a
// spoken "stop" means the audio.
func (a *App) ttsActive() bool {
	return a.tts.player != nil && a.tts.player.Snapshot().State.Active()
}

// ttsLive reports whether there is a clip to control: playing, paused or done.
func (a *App) ttsLive() bool {
	if a.tts.player == nil {
		return false
	}
	switch a.tts.player.Snapshot().State {
	case tts.Failed, tts.Idle:
		return false
	}
	return true
}

// ttsKey is ctrl+b, used the way ctrl+c copies: the hovered message, else the
// last final reply. Pressed on the message that is being read it pauses or
// resumes; on another it switches to that one.
func (a *App) ttsKey() tea.Cmd {
	idx := -1
	if a.view == viewChat && a.hover.text && a.hover.msg >= 0 && a.hover.msg < len(a.messages) &&
		strings.TrimSpace(a.messages[a.hover.msg].Text()) != "" {
		idx = a.hover.msg
	}
	if idx < 0 {
		if last := a.trailingAssistant(); last >= 0 && strings.TrimSpace(a.messages[last].Text()) != "" {
			idx = last
		}
	}
	if idx < 0 {
		a.addSystem("read aloud: there is no reply to read yet")
		return nil
	}
	if a.ttsLive() && a.tts.srcIdx == idx && a.messageIs(a.tts.cardIdx, components.PlayerRole) {
		a.tts.player.Toggle()
		return a.ttsAnimStart()
	}
	return a.ttsSpeak(a.messages[idx].Text(), idx)
}

func (a *App) messageIs(idx int, role string) bool {
	return idx >= 0 && idx < len(a.messages) && a.messages[idx].Role == role
}

// ttsAutoRead reads a turn's final reply when tts.read_reports is on.
func (a *App) ttsAutoRead(reply string) tea.Cmd {
	t := a.settings.TTS
	if !t.TTSEnabled() || !t.TTSReadReports() || strings.TrimSpace(reply) == "" {
		return nil
	}
	last := a.trailingAssistant()
	if last < 0 {
		return nil
	}
	return a.ttsSpeak(reply, last)
}

// ttsSpeak reads text aloud: it prepares it, starts the player and the
// synthesis, and puts the card in the thread. A clip already playing is
// replaced.
func (a *App) ttsSpeak(text string, src int) tea.Cmd {
	if !a.settings.TTS.TTSEnabled() {
		a.addSystem("read aloud is off: /tts on turns it on, and agrees to send the text to Microsoft's read-aloud service")
		return nil
	}
	text = sanitize.Text(text)
	groups := tts.Prepare(text)
	if len(groups) == 0 {
		a.addSystem("read aloud: nothing to read in that message")
		return nil
	}
	open := a.tts.open
	if open == nil {
		o, _, err := tts.NewExecOpener()
		if err != nil {
			a.addSystem("read aloud: " + err.Error())
			return nil
		}
		open = o
	}
	a.ttsStop(true)

	p := tts.NewPlayer(open)
	if a.tts.noPace {
		p.Pace = 0
	}
	p.SetSpeed(a.settings.TTS.TTSSpeedOr())
	ctx, cancel := context.WithCancel(context.Background())
	t := &a.tts
	t.gen++
	t.player, t.cancel, t.srcIdx = p, cancel, src
	t.title, t.total, t.done, t.finished, t.cached, t.status = ttsTitle(text), len(groups), 0, false, false, "synthesising"
	t.spoken = spokenWords(groups)
	a.messages = append(a.messages, components.Message{Role: components.PlayerRole, Ephemeral: true})
	t.cardIdx = len(a.messages) - 1
	a.ttsRefreshCard()
	p.Play()
	ch := tts.Synthesize(ctx, a.ttsEngine(), a.ttsCache(), a.settings.TTS.TTSVoiceOr(), groups)
	t.ch = ch
	return tea.Batch(ttsWait(t.gen, ch), a.ttsAnimStart())
}

func ttsWait(gen int, ch <-chan tts.Result) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		return ttsResultMsg{gen: gen, ch: ch, r: r, closed: !ok}
	}
}

// ttsTitle is the first few words of what is read, for the card.
func ttsTitle(text string) string {
	words := strings.Fields(text)
	if len(words) > ttsTitleWords {
		words = append(words[:ttsTitleWords], "…")
	}
	return sanitize.Line(strings.Join(words, " "), 80)
}

// spokenWords is the set of words in what is read aloud.
func spokenWords(groups []string) map[string]bool {
	set := map[string]bool{}
	for _, g := range groups {
		for _, w := range strings.Fields(voicecmd.Normalize(g)) {
			set[w] = true
		}
	}
	return set
}

// ttsEcho reports whether a transcript is the speaker, not the user: playing or
// just played, at least two words, and most of them are words being read. A
// single word is never echo, so "stop" always gets through.
func (a *App) ttsEcho(raw string) bool {
	t := &a.tts
	if t.player == nil || len(t.spoken) == 0 {
		return false
	}
	if !a.ttsActive() && !time.Now().Before(t.echoUntil) {
		return false
	}
	words := strings.Fields(voicecmd.Normalize(raw))
	if len(words) < 2 {
		return false
	}
	hit := 0
	for _, w := range words {
		if t.spoken[w] {
			hit++
		}
	}
	return hit*10 >= len(words)*6
}

// handleTTSMsg handles the synthesis results and the animation tick.
func (a *App) handleTTSMsg(msg tea.Msg) (tea.Cmd, bool) {
	t := &a.tts
	switch m := msg.(type) {
	case ttsResultMsg:
		if m.gen != t.gen || t.player == nil {
			return nil, true // a clip that was replaced or stopped
		}
		if m.closed {
			if !t.finished {
				t.finished = true
				t.player.Finish(nil)
			}
			a.ttsRefreshCard()
			return nil, true
		}
		switch {
		case m.r.Err != nil:
			t.finished = true
			t.status = "failed: " + sanitize.Line(m.r.Err.Error(), 120)
			t.player.Finish(errors.New(t.status))
			a.ttsRefreshCard()
			return nil, true
		default:
			t.player.Append(m.r.PCM)
			t.cached = t.cached || m.r.Cached
			t.done = m.r.Index + 1
			if m.r.Cached {
				t.done, t.total = 1, 1
			}
		}
		a.ttsRefreshCard()
		return ttsWait(m.gen, m.ch), true
	case ttsAnimMsg:
		if m.gen != t.gen {
			return nil, true
		}
		t.frame++
		a.ttsRefreshCard()
		if a.ttsBusy() {
			return ttsAnimTick(t.gen), true
		}
		t.animating = false
		if a.tts.player != nil {
			t.echoUntil = time.Now().Add(ttsEchoWindow)
		}
		return nil, true
	}
	return nil, false
}

// ttsBusy reports whether the card still moves.
func (a *App) ttsBusy() bool {
	if a.tts.player == nil {
		return false
	}
	return a.tts.player.Snapshot().State.Active() || !a.tts.finished
}

func ttsAnimTick(gen int) tea.Cmd {
	return tea.Tick(ttsAnimInterval, func(time.Time) tea.Msg { return ttsAnimMsg{gen: gen} })
}

// ttsAnimStart starts the animation tick if it is not running.
func (a *App) ttsAnimStart() tea.Cmd {
	t := &a.tts
	if t.player == nil || t.animating {
		return nil
	}
	t.animating = true
	return ttsAnimTick(t.gen)
}

// ttsStatus is the card's status line.
func (a *App) ttsStatus(s tts.Snapshot) string {
	t := &a.tts
	switch {
	case s.State == tts.Failed:
		if s.Err != nil {
			return sanitize.Line(s.Err.Error(), 120)
		}
		return "failed"
	case !t.finished && t.cached:
		return "from cache"
	case !t.finished:
		return fmt.Sprintf("synthesising %d of %d", min(t.done+1, t.total), t.total)
	case t.cached:
		return "from cache"
	case s.State == tts.Ended:
		return "done · cached for replay"
	}
	return "ready"
}

// ttsRefreshCard copies the player's state into the card's message.
func (a *App) ttsRefreshCard() {
	t := &a.tts
	if t.player == nil || !a.messageIs(t.cardIdx, components.PlayerRole) {
		return
	}
	s := t.player.Snapshot()
	a.messages[t.cardIdx].Player = &components.PlayerCard{
		Title: t.title, State: s.State.String(), Pos: s.Pos, Dur: s.Dur, Final: s.Final,
		Speed: s.Speed, Levels: s.Levels, Status: a.ttsStatus(s), Frame: t.frame,
	}
}

// ttsStop stops playback and synthesis. With release the player is dropped
// (a new clip is starting or voice is being torn down); without it the card
// stays and Play starts again.
func (a *App) ttsStop(release bool) {
	t := &a.tts
	if t.player == nil {
		return
	}
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if release {
		// The old card stays in the thread as it was left: frozen, with its
		// buttons ignored (only the current card answers clicks).
		if a.messageIs(t.cardIdx, components.PlayerRole) {
			if c := a.messages[t.cardIdx].Player; c != nil {
				frozen := *c
				frozen.State, frozen.Status, frozen.Levels = "stopped", "replaced", nil
				a.messages[t.cardIdx].Player = &frozen
			}
		}
		t.player.Close()
		t.player, t.cardIdx, t.spoken = nil, -1, nil
		t.echoUntil = time.Time{}
		return
	}
	t.player.Stop()
	t.echoUntil = time.Now().Add(ttsEchoWindow)
	a.ttsRefreshCard()
}

// ttsHit applies a click on the card. owner is the message the clicked line
// belongs to: only the current card answers.
func (a *App) ttsHit(owner int, h components.Hit, col int) tea.Cmd {
	t := &a.tts
	if t.player == nil || owner != t.cardIdx {
		return nil
	}
	snap := t.player.Snapshot()
	switch {
	case h.Action == components.PlayerToggle:
		if snap.State == tts.Failed {
			return nil
		}
		t.player.Toggle()
	case h.Action == components.PlayerStop:
		a.ttsStop(false)
	case h.Action == components.PlayerBack:
		t.player.Seek(clampDur(snap.Pos-components.PlayerSkip, 0, snap.Dur))
	case h.Action == components.PlayerFwd:
		t.player.Seek(clampDur(snap.Pos+components.PlayerSkip, 0, snap.Dur))
	case h.Action == components.PlayerSeek:
		t.scrub, t.scrubHit = true, h
		a.ttsSeekTo(col)
	case strings.HasPrefix(h.Action, components.PlayerSpeed):
		f, err := strconv.ParseFloat(strings.TrimPrefix(h.Action, components.PlayerSpeed), 64)
		if err != nil {
			return nil
		}
		t.player.SetSpeed(f)
	default:
		return nil
	}
	a.ttsRefreshCard()
	return a.ttsAnimStart()
}

// ttsSeekTo moves the playhead to the frame column col on the scrub bar.
func (a *App) ttsSeekTo(col int) {
	t := &a.tts
	if t.player == nil || t.scrubHit.Width < 2 {
		return
	}
	frac := float64(col-t.scrubHit.Col) / float64(t.scrubHit.Width-1)
	frac = math.Max(0, math.Min(1, frac))
	dur := t.player.Snapshot().Dur
	t.player.Seek(time.Duration(frac * float64(dur)))
	a.ttsRefreshCard()
}

// ttsMouse handles a press over a card's clickable regions. It reports whether
// the press was one, so text selection does not start.
func (a *App) ttsMouse(p components.Pos) (bool, tea.Cmd) {
	if p.Line < 0 || p.Line >= len(a.lastFrame.lines) {
		return false, nil
	}
	line := a.lastFrame.lines[p.Line]
	for _, h := range line.Hits {
		if p.Col >= h.Col && p.Col < h.Col+h.Width {
			return true, a.ttsHit(line.Owner, h, p.Col)
		}
	}
	return false, nil
}

// ttsVoiceStop is the spoken "stop" while something is being read.
func (a *App) ttsVoiceStop() bool {
	if !a.ttsActive() {
		return false
	}
	a.ttsStop(false)
	return true
}

// ttsSpeedChoices are the speeds the /settings row steps through.
func ttsSpeedChoices() []string {
	out := make([]string, len(components.PlayerSpeeds))
	for i, s := range components.PlayerSpeeds {
		out[i] = strconv.FormatFloat(s, 'g', -1, 64)
	}
	return out
}

const ttsDisclosure = "the text you have read aloud is sent to Microsoft's read-aloud service (speech.platform.bing.com), an unofficial endpoint that can change or stop without notice"

// ttsCommand is /tts.
func (a *App) ttsCommand(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	switch sub {
	case "", "status":
		a.addSystem(a.ttsStatusText())
	case "on":
		if !a.ttsSave(func(t *config.TTSSettings) { t.Enabled, t.Consented = voiceBool(true), voiceBool(true) }) {
			return nil
		}
		msg := "read aloud: on. " + strings.ToUpper(ttsDisclosure[:1]) + ttsDisclosure[1:] + ". ctrl+b reads the reply under the pointer or the last one; /tts off turns it off"
		if _, name, err := tts.NewExecOpener(); err != nil {
			msg += ". " + err.Error()
		} else if a.tts.open == nil {
			msg += " (playing through " + name + ")"
		}
		a.addSystem(msg)
	case "off":
		if a.ttsSave(func(t *config.TTSSettings) { t.Enabled = voiceBool(false) }) {
			a.ttsStop(true)
			a.addSystem("read aloud: off")
		}
	case "stop":
		if a.tts.player == nil {
			a.addSystem("read aloud: nothing is playing")
			return nil
		}
		a.ttsStop(false)
	case "reports":
		if len(fields) < 2 || (fields[1] != "on" && fields[1] != "off") {
			a.addSystem("usage: /tts reports on|off")
			return nil
		}
		on := fields[1] == "on"
		if a.ttsSave(func(t *config.TTSSettings) { t.ReadReports = voiceBool(on) }) {
			a.addSystem("read aloud reports: " + fields[1])
		}
	case "voice":
		if len(fields) != 2 {
			a.addSystem("usage: /tts voice NAME, such as en-GB-RyanNeural")
			return nil
		}
		if !tts.ValidVoice(fields[1]) {
			a.addSystem("read aloud: " + sanitize.Line(fields[1], 60) + " is not a voice name (letters, digits and - only)")
			return nil
		}
		if a.ttsSave(func(t *config.TTSSettings) { t.Voice = fields[1] }) {
			a.addSystem("read aloud voice: " + fields[1])
		}
	case "speed":
		f, err := 0.0, errors.New("usage")
		if len(fields) == 2 {
			f, err = strconv.ParseFloat(fields[1], 64)
		}
		if err != nil || f < config.TTSSpeedMin || f > config.TTSSpeedMax {
			a.addSystem(fmt.Sprintf("usage: /tts speed %g to %g", config.TTSSpeedMin, config.TTSSpeedMax))
			return nil
		}
		if a.ttsSave(func(t *config.TTSSettings) { t.Speed = &f }) {
			if a.tts.player != nil {
				a.tts.player.SetSpeed(f)
				a.ttsRefreshCard()
			}
			a.addSystem("read aloud speed: " + components.SpeedLabel(f))
		}
	case "cache":
		c := a.ttsCache()
		if len(fields) >= 2 && fields[1] == "clear" {
			if err := c.Clear(); err != nil {
				a.addSystem("read aloud: could not clear the cache: " + sanitize.Line(err.Error(), 160))
				return nil
			}
			a.addSystem("read aloud: cache cleared")
			return nil
		}
		a.addSystem(fmt.Sprintf("read aloud cache: %.1f MB of %d MB · /tts cache clear empties it", float64(c.Size())/(1<<20), a.settings.TTS.TTSCacheMBOr()))
	default:
		a.addSystem("usage: /tts [status|on|off|stop|reports on|off|voice NAME|speed N|cache clear]")
	}
	return nil
}

func (a *App) ttsStatusText() string {
	t := a.settings.TTS
	state := "off"
	switch {
	case t.TTSEnabled():
		state = "on"
	case t.TTSConsented():
		state = "off (agreed earlier)"
	}
	return fmt.Sprintf("read aloud: %s · reports %s · voice %s · speed %s · cache %d MB\nkey: ctrl+b · engine: Microsoft read-aloud (%s)",
		state, onOffLabel(t.TTSReadReports()), t.TTSVoiceOr(), components.SpeedLabel(t.TTSSpeedOr()), t.TTSCacheMBOr(),
		"text is sent off this machine")
}

// ttsSave writes one tts setting to the global settings file and reloads.
func (a *App) ttsSave(write func(*config.TTSSettings)) bool {
	if err := a.mutateGlobalSetting(func(s *config.Settings) { write(ttsSettings(s)) }); err != nil {
		a.addSystem("read aloud: could not save the setting: " + sanitize.Line(err.Error(), 200))
		return false
	}
	a.refreshFooter()
	return true
}

func ttsSettings(s *config.Settings) *config.TTSSettings {
	if s.TTS == nil {
		s.TTS = &config.TTSSettings{}
	}
	return s.TTS
}

// ttsToggle flips a tts.* toggle row. Turning it on needs the consent /tts on
// gives, so the row explains instead of sending text by surprise.
func (a *App) ttsToggle(key string) error {
	if key == "tts.enabled" && !a.settings.TTS.TTSEnabled() && !a.settings.TTS.TTSConsented() {
		a.addSystem("read aloud is off: " + ttsDisclosure + ". Run /tts on to agree and turn it on.")
		return nil
	}
	return a.mutateGlobalSetting(func(s *config.Settings) {
		t := ttsSettings(s)
		switch key {
		case "tts.enabled":
			t.Enabled = voiceBool(!(t.Enabled != nil && *t.Enabled))
		case "tts.read_reports":
			t.ReadReports = voiceBool(!t.TTSReadReports())
		}
	})
}

// ttsChoose steps a tts.* choose row to its next option.
func (a *App) ttsChoose(key string, opts []string) error {
	return a.mutateGlobalSetting(func(s *config.Settings) {
		t := ttsSettings(s)
		switch key {
		case "tts.voice":
			t.Voice = opts[(indexOfString(opts, t.TTSVoiceOr())+1)%len(opts)]
		case "tts.speed":
			cur := strconv.FormatFloat(t.TTSSpeedOr(), 'g', -1, 64)
			f, _ := strconv.ParseFloat(opts[(indexOfString(opts, cur)+1)%len(opts)], 64)
			t.Speed = &f
		case "tts.cache_mb":
			cur := strconv.Itoa(t.TTSCacheMBOr())
			n, _ := strconv.Atoi(opts[(indexOfString(opts, cur)+1)%len(opts)])
			t.CacheMB = &n
		}
	})
}

// ttsUnset returns a tts.* row to its default.
func (a *App) ttsUnset(key string) error {
	return a.mutateGlobalSetting(func(s *config.Settings) {
		if s.TTS == nil {
			return
		}
		switch key {
		case "tts.enabled":
			s.TTS.Enabled = nil
		case "tts.read_reports":
			s.TTS.ReadReports = nil
		case "tts.voice":
			s.TTS.Voice = ""
		case "tts.speed":
			s.TTS.Speed = nil
		case "tts.cache_mb":
			s.TTS.CacheMB = nil
		}
	})
}

// ttsAfterSetting applies a tts.* row edited in /settings.
func (a *App) ttsAfterSetting(key string) tea.Cmd {
	switch key {
	case "tts.enabled":
		if !a.settings.TTS.TTSEnabled() {
			a.ttsStop(true)
		}
	case "tts.speed":
		if a.tts.player != nil {
			a.tts.player.SetSpeed(a.settings.TTS.TTSSpeedOr())
			a.ttsRefreshCard()
		}
	}
	return nil
}

// ttsVoiceOpts is the voice row's options: the curated list, with the current
// voice added when it is a custom one, so stepping never skips it.
func ttsVoiceOpts(current string) []string {
	if indexOfString(ttsVoices, current) >= 0 {
		return ttsVoices
	}
	return append([]string{current}, ttsVoices...)
}

func clampDur(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}
