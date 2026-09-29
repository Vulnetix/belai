package tui

import (
	"context"

	"github.com/vulnetix/belai/internal/modeltest"
)

// The package's existing tests drive /model edits and assert the settings
// file afterwards. Selections are now written only after their test passes,
// so these tests get a tester that passes every plan inline. Tests of the
// test flow itself set App.modelTester and App.modelTestAsync.
func init() {
	testModelTestSync = true
	testModelTester = func(_ context.Context, steps []modeltest.Step, _ *modeltest.Env, _ func(modeltest.Event)) modeltest.Report {
		rep := modeltest.Report{Passed: true}
		for _, s := range steps {
			rep.Steps = append(rep.Steps, modeltest.StepResult{Name: s.Name, Outcome: modeltest.Outcome{Status: modeltest.StatusOK}})
		}
		return rep
	}
}
