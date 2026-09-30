package config

import (
	"fmt"
	"regexp"
)

// Voice modes, deliveries and keys (docs/voice.md).
const (
	VoiceModePushToTalk = "push_to_talk"
	VoiceModeListen     = "listen"

	VoiceDeliveryInsert = "insert"
	VoiceDeliverySubmit = "submit"

	// DefaultVoiceKey is the push-to-talk key when none is set.
	DefaultVoiceKey = "f11"
)

// VoiceKeys are the keys voice.key may name.
var VoiceKeys = []string{"f11", "ctrl+space", "ctrl+]", "ctrl+g", "f13", "f14", "f15", "f16"}

// voiceDeviceRE mirrors internal/voice: a plain identifier that cannot start
// with "-", so a device name can never add an option to the capture helper.
// It is repeated here because config sits below the voice package.
var voiceDeviceRE = regexp.MustCompile(`^[A-Za-z0-9._:,@][A-Za-z0-9._:,@=-]{0,127}$`)

// VoiceSettings configures voice input (docs/voice.md). A per-user
// preference: microphone use, the capture device and whether dictation is sent
// on its own are the user's alone, so the project layer is dropped.
type VoiceSettings struct {
	// Enabled turns voice input on. Default false.
	Enabled *bool `json:"enabled,omitempty"`
	// Mode is push_to_talk (default) or listen.
	Mode string `json:"mode,omitempty"`
	// Delivery is insert (default) or submit.
	Delivery string `json:"delivery,omitempty"`
	// Cleanup passes the transcript through the fast model. Default true.
	Cleanup *bool `json:"cleanup,omitempty"`
	// Key is the push-to-talk (or mute) key. Default f11.
	Key string `json:"key,omitempty"`
	// Log shows voice's automatic notices and its voice_cleanup rows in the
	// transcript. Default true. It changes what the thread shows, never what is
	// recorded in the session.
	Log *bool `json:"log,omitempty"`
	// Device is the capture helper's input device. Empty is its default.
	Device string `json:"device,omitempty"`
	// WakeWord makes listen mode act only on speech that starts with "Hey,
	// Belay". Anything else is dropped unheard: never shown, cleaned up or
	// sent to a model. Needs mode listen. Default false.
	WakeWord *bool `json:"wake_word,omitempty"`
	// Commands turns on the spoken keywords (stop, option N, submit, skip,
	// approve, deny) and Jev's voice_command job. Default true.
	Commands *bool `json:"commands,omitempty"`
}

// VoiceWakeWordEnabled reports whether the wake word gates dictation.
// Default false.
func (s *VoiceSettings) VoiceWakeWordEnabled() bool {
	return s != nil && s.WakeWord != nil && *s.WakeWord
}

// VoiceCommandsEnabled reports whether spoken keywords and voice commands are
// on. Default true.
func (s *VoiceSettings) VoiceCommandsEnabled() bool {
	return s == nil || s.Commands == nil || *s.Commands
}

// VoiceEnabled reports whether voice input is on. Default false.
func (s *VoiceSettings) VoiceEnabled() bool {
	return s != nil && s.Enabled != nil && *s.Enabled
}

// VoiceModeOr returns the capture mode, push_to_talk when unset.
func (s *VoiceSettings) VoiceModeOr() string {
	if s == nil || s.Mode == "" {
		return VoiceModePushToTalk
	}
	return s.Mode
}

// VoiceDeliveryOr returns the delivery, insert when unset.
func (s *VoiceSettings) VoiceDeliveryOr() string {
	if s == nil || s.Delivery == "" {
		return VoiceDeliveryInsert
	}
	return s.Delivery
}

// VoiceCleanupEnabled reports whether transcripts go through the fast model.
// Default true.
func (s *VoiceSettings) VoiceCleanupEnabled() bool {
	return s == nil || s.Cleanup == nil || *s.Cleanup
}

// VoiceKeyOr returns the voice key, DefaultVoiceKey when unset.
func (s *VoiceSettings) VoiceKeyOr() string {
	if s == nil || s.Key == "" {
		return DefaultVoiceKey
	}
	return s.Key
}

// VoiceDevice returns the capture device, empty for the helper's default.
// VoiceLogEnabled reports whether voice's automatic notices and cleanup rows
// show in the transcript. Default true.
func (s *VoiceSettings) VoiceLogEnabled() bool {
	return s == nil || s.Log == nil || *s.Log
}

func (s *VoiceSettings) VoiceDevice() string {
	if s == nil {
		return ""
	}
	return s.Device
}

func (s *VoiceSettings) merge(from *VoiceSettings) {
	if from.Enabled != nil {
		s.Enabled = from.Enabled
	}
	if from.Mode != "" {
		s.Mode = from.Mode
	}
	if from.Delivery != "" {
		s.Delivery = from.Delivery
	}
	if from.Cleanup != nil {
		s.Cleanup = from.Cleanup
	}
	if from.Key != "" {
		s.Key = from.Key
	}
	if from.Log != nil {
		s.Log = from.Log
	}
	if from.Device != "" {
		s.Device = from.Device
	}
	if from.WakeWord != nil {
		s.WakeWord = from.WakeWord
	}
	if from.Commands != nil {
		s.Commands = from.Commands
	}
}

// ValidateVoice rejects a voice block with a value it does not understand,
// naming the key, so a typo never silently opens the microphone in a mode the
// user did not choose.
func ValidateVoice(s Settings) error {
	v := s.Voice
	if v == nil {
		return nil
	}
	switch v.Mode {
	case "", VoiceModePushToTalk, VoiceModeListen:
	default:
		return fmt.Errorf("voice.mode %q is not %s or %s", v.Mode, VoiceModePushToTalk, VoiceModeListen)
	}
	switch v.Delivery {
	case "", VoiceDeliveryInsert, VoiceDeliverySubmit:
	default:
		return fmt.Errorf("voice.delivery %q is not %s or %s", v.Delivery, VoiceDeliveryInsert, VoiceDeliverySubmit)
	}
	if v.Key != "" {
		ok := false
		for _, k := range VoiceKeys {
			ok = ok || k == v.Key
		}
		if !ok {
			return fmt.Errorf("voice.key %q is not one of %v", v.Key, VoiceKeys)
		}
	}
	if v.Device != "" && !voiceDeviceRE.MatchString(v.Device) {
		return fmt.Errorf("voice.device %q must be letters, digits and . _ : , @ = -, and must not start with -", v.Device)
	}
	if v.VoiceWakeWordEnabled() && v.VoiceModeOr() != VoiceModeListen {
		return fmt.Errorf("voice.wake_word needs voice.mode %q: push to talk is not listening, so it cannot hear the wake word", VoiceModeListen)
	}
	return nil
}

// VoiceEnabledOr reports whether voice input is on, using def when the
// setting is unset. Belai passes whether the speech model is built into the
// binary, so a release build is ready to dictate with no setting, and a
// build without the model stays off until asked. An explicit false always
// wins.
func (s *VoiceSettings) VoiceEnabledOr(def bool) bool {
	if s == nil || s.Enabled == nil {
		return def
	}
	return *s.Enabled
}
