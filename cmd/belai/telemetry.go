package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/otel"
	"github.com/vulnetix/belai/internal/run"
)

// startTelemetry starts OTLP export when the user's settings or the OTEL_*
// environment name an endpoint, and returns the flush-and-stop. The project
// is identified only by a hash of its path.
func startTelemetry(settings config.Settings, workdir string) func() {
	cfg := otel.FromSettings(settings.Telemetry, os.Getenv)
	sum := sha256.Sum256([]byte(workdir))
	stop := otel.Start(cfg, hex.EncodeToString(sum[:6]))
	if !otel.Enabled() {
		return stop
	}
	run.SetTelemetryUsage(func(ev run.UsageEvent) {
		est := int64(0)
		if ev.Estimated {
			est = 1
		}
		otel.Add("belai.tokens", int64(ev.Tokens), otel.S(otel.AttrProvider, ev.Provider), otel.S(otel.AttrModel, ev.Model), otel.I(otel.AttrEstimated, est))
		otel.Add("belai.model_calls", 1, otel.S(otel.AttrProvider, ev.Provider), otel.S(otel.AttrModel, ev.Model))
		// Prompt-cache reads and writes, so a dashboard can chart the cache
		// hit share: a fall means something rewrote the prompt prefix.
		if ev.CacheRead > 0 {
			otel.Add("belai.cache_read_tokens", int64(ev.CacheRead), otel.S(otel.AttrProvider, ev.Provider), otel.S(otel.AttrModel, ev.Model))
		}
		if ev.CacheWrite > 0 {
			otel.Add("belai.cache_write_tokens", int64(ev.CacheWrite), otel.S(otel.AttrProvider, ev.Provider), otel.S(otel.AttrModel, ev.Model))
		}
	})
	return func() {
		run.SetTelemetryUsage(nil)
		stop()
	}
}
