package config

import (
	"fmt"
	"math"
	"regexp"
)

// TTS defaults (docs/tts.md).
const (
	DefaultTTSVoice = "en-US-AndrewMultilingualNeural"
	// MaxTTSCacheMB bounds tts.cache_mb.
	MaxTTSCacheMB = 4096
	// TTSSpeedMin and TTSSpeedMax bound tts.speed.
	TTSSpeedMin = 0.5
	TTSSpeedMax = 3.0
	// DefaultTTSCacheMB is the audio cache cap when none is set.
	DefaultTTSCacheMB = 256
)

// ttsVoiceRE mirrors internal/tts: a plain voice name, so a setting can never
// add markup to a request. Repeated here because config sits below that
// package.
var ttsVoiceRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// TTSSettings configures reading replies aloud (docs/tts.md). A per-user
// preference: what is read aloud is sent to Microsoft's read-aloud service, so
// a repository cannot turn it on, pick a voice or widen the cache, and the
// project layer is dropped.
type TTSSettings struct {
	// Enabled turns reading aloud on. Default false. It is turned on by the
	// /tts on command, which is also the consent to send text off the machine.
	Enabled *bool `json:"enabled,omitempty"`
	// Consented records that the user agreed to send text to the service. Only
	// /tts on writes it.
	Consented *bool `json:"consented,omitempty"`
	// ReadReports reads each turn's final reply aloud when the turn ends.
	// Default false.
	ReadReports *bool `json:"read_reports,omitempty"`
	// Voice is the voice name, such as en-GB-RyanNeural.
	Voice string `json:"voice,omitempty"`
	// Speed is the playback speed, 0.5 to 3. Default 1.
	Speed *float64 `json:"speed,omitempty"`
	// CacheMB caps the audio cache in megabytes; 0 turns the cache off.
	// Default 256.
	CacheMB *int `json:"cache_mb,omitempty"`
}

// TTSEnabled reports whether reading aloud is on and was agreed to. Default
// false: nothing is sent without both.
func (s *TTSSettings) TTSEnabled() bool {
	return s != nil && s.Enabled != nil && *s.Enabled && s.Consented != nil && *s.Consented
}

// TTSConsented reports whether the user agreed to send text to the service.
func (s *TTSSettings) TTSConsented() bool {
	return s != nil && s.Consented != nil && *s.Consented
}

// TTSReadReports reports whether final replies are read aloud by themselves.
func (s *TTSSettings) TTSReadReports() bool {
	return s != nil && s.ReadReports != nil && *s.ReadReports
}

// TTSVoiceOr returns the voice, the default when unset.
func (s *TTSSettings) TTSVoiceOr() string {
	if s == nil || s.Voice == "" {
		return DefaultTTSVoice
	}
	return s.Voice
}

// TTSSpeedOr returns the speed, 1 when unset.
func (s *TTSSettings) TTSSpeedOr() float64 {
	if s == nil || s.Speed == nil {
		return 1
	}
	return *s.Speed
}

// TTSCacheMBOr returns the cache cap in megabytes; 0 means off.
func (s *TTSSettings) TTSCacheMBOr() int {
	if s == nil || s.CacheMB == nil {
		return DefaultTTSCacheMB
	}
	return *s.CacheMB
}

func (s *TTSSettings) merge(from *TTSSettings) {
	if from.Enabled != nil {
		s.Enabled = from.Enabled
	}
	if from.Consented != nil {
		s.Consented = from.Consented
	}
	if from.ReadReports != nil {
		s.ReadReports = from.ReadReports
	}
	if from.Voice != "" {
		s.Voice = from.Voice
	}
	if from.Speed != nil {
		s.Speed = from.Speed
	}
	if from.CacheMB != nil {
		s.CacheMB = from.CacheMB
	}
}

// ValidateTTS rejects a tts block with a value it does not understand, naming
// the key.
func ValidateTTS(s Settings) error {
	t := s.TTS
	if t == nil {
		return nil
	}
	if t.Voice != "" && !ttsVoiceRE.MatchString(t.Voice) {
		return fmt.Errorf("tts.voice %q must be a voice name of letters, digits and -, such as en-GB-RyanNeural", t.Voice)
	}
	if t.Speed != nil && (math.IsNaN(*t.Speed) || *t.Speed < TTSSpeedMin || *t.Speed > TTSSpeedMax) {
		return fmt.Errorf("tts.speed %v must be between %v and %v", *t.Speed, TTSSpeedMin, TTSSpeedMax)
	}
	if t.CacheMB != nil && (*t.CacheMB < 0 || *t.CacheMB > MaxTTSCacheMB) {
		return fmt.Errorf("tts.cache_mb %d must be between 0 and %d", *t.CacheMB, MaxTTSCacheMB)
	}
	return nil
}
