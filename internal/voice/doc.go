// Package voice is speech-to-text for the composer. It captures the
// microphone through a helper program, cuts the stream into utterances,
// recognises them with a Whisper model run by pure Go code (internal/voice/asr),
// and reports the text to the TUI, which decides when it may appear.
//
// The Engine is the only entry point. It never touches the composer: it reports
// state and transcripts, and the caller tells it whether text can be accepted
// right now (SetReady). When the answer is no, the microphone is closed and any
// audio in progress is dropped, so nothing is heard while nothing can be shown.
//
// Audio lives in memory only. It is never written to disk, logged or sent
// anywhere; only the recognised text leaves the package.
package voice
