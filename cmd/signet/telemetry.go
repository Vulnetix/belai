package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/otel"
	"github.com/vulnetix/signet/internal/run"
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
		otel.Add("signet.tokens", int64(ev.Tokens), otel.S(otel.AttrProvider, ev.Provider), otel.S(otel.AttrModel, ev.Model), otel.I(otel.AttrEstimated, est))
		otel.Add("signet.model_calls", 1, otel.S(otel.AttrProvider, ev.Provider), otel.S(otel.AttrModel, ev.Model))
	})
	return func() {
		run.SetTelemetryUsage(nil)
		stop()
	}
}
