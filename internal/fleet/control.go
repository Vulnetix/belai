package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Live controls for a running worker, from this machine: the control room,
// `belai agent control`, a crew supervisor or the fallback policy write a typed
// control file beside the worker's record, and the worker reads it between
// cards and before every round. Like the pause marker it is a file of fixed
// fields, never free text the worker would act on: a model the host cannot run
// is refused, and the refusal is written back as an acknowledgement the
// control room shows.

// Control is what a worker is asked to change. Seq orders the writes: the
// worker applies a control once, the first time it sees a higher Seq.
type Control struct {
	Seq int64 `json:"seq"`
	// By says who asked: "human", "supervisor" or "fallback".
	By string `json:"by,omitempty"`
	At int64  `json:"at,omitempty"`
	// Drain asks the worker to finish its card and leave.
	Drain bool `json:"drain,omitempty"`
	// Target and Effort change what the next turn runs on; a zero Target
	// keeps the model. Auto hands the model back to the fallback policy after
	// a human pinned one.
	Target Target `json:"target,omitzero"`
	Effort string `json:"effort,omitempty"`
	Auto   bool   `json:"auto,omitempty"`
}

// Ack statuses.
const (
	AckAccepted = "accepted"
	AckRefused  = "refused"
)

// ControlAck is the worker's answer to a control.
type ControlAck struct {
	Seq    int64  `json:"seq"`
	Status string `json:"status"`
	// Reason is the refusal, one sanitized line.
	Reason string `json:"reason,omitempty"`
	At     int64  `json:"at"`
}

func (r *Registry) controlPath(id string) string { return filepath.Join(r.dir, id+".control.json") }

func (r *Registry) ackPath(id string) string { return filepath.Join(r.dir, id+".control.ack.json") }

// SetControl edits a worker's control under the registry's control lock and
// returns what was written. fn starts from the last control, so a drain asked
// earlier is not lost when a model change follows it.
func (r *Registry) SetControl(id, by string, fn func(*Control)) (Control, error) {
	if !ValidID(id) {
		return Control{}, fmt.Errorf("fleet: %q is not a worker id", id)
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return Control{}, err
	}
	release, err := config.AcquireFileLock(filepath.Join(r.dir, ".control.lock"))
	if err != nil {
		return Control{}, err
	}
	defer release()
	c, _ := r.Control(id)
	fn(&c)
	c.Seq++
	c.By, c.At = by, r.now().UnixMilli()
	return c, writeJSONAtomic(r.dir, r.controlPath(id), c)
}

// Control reads a worker's control; false when none was written.
func (r *Registry) Control(id string) (Control, bool) {
	if !ValidID(id) {
		return Control{}, false
	}
	var c Control
	data, err := os.ReadFile(r.controlPath(id))
	if err != nil || json.Unmarshal(data, &c) != nil {
		return Control{}, false
	}
	return c, true
}

// Ack reads a worker's last acknowledgement.
func (r *Registry) Ack(id string) (ControlAck, bool) {
	if !ValidID(id) {
		return ControlAck{}, false
	}
	var a ControlAck
	data, err := os.ReadFile(r.ackPath(id))
	if err != nil || json.Unmarshal(data, &a) != nil {
		return ControlAck{}, false
	}
	return a, true
}

func (r *Registry) writeAck(id string, a ControlAck) error {
	if !ValidID(id) {
		return fmt.Errorf("fleet: %q is not a worker id", id)
	}
	a.Reason = sanitize.Line(a.Reason, 240)
	return writeJSONAtomic(r.dir, r.ackPath(id), a)
}

// ControlRun applies fn to the control of every live worker of a run, or of
// those in one repository of it when repo is set.
func (r *Registry) ControlRun(runID, repo, by string, fn func(*Control)) ([]string, error) {
	var ids []string
	var errs []error
	for _, rec := range r.RunWorkers(runID) {
		if repo != "" && rec.Repo != repo {
			continue
		}
		if _, err := r.SetControl(rec.ID, by, fn); err != nil {
			errs = append(errs, err)
			continue
		}
		ids = append(ids, rec.ID)
	}
	return ids, errors.Join(errs...)
}

// applyControl takes a control the worker has not seen yet: a drain is kept
// for the loop, and a model or effort goes through the worker's controls, so
// a model this host cannot run is refused here and acknowledged as such.
func (w *Worker) applyControl() {
	if w.Registry == nil {
		return
	}
	c, ok := w.Registry.Control(w.Record.ID)
	if !ok || c.Seq <= w.ctlSeq {
		return
	}
	w.ctlSeq = c.Seq
	ack := ControlAck{Seq: c.Seq, Status: AckAccepted, At: w.clock().UnixMilli()}
	if c.Drain && !w.Record.Draining {
		w.Record.Draining = true
		w.logf("control (%s): drain after the card in hand", c.By)
	}
	if !c.Target.Zero() || c.Effort != "" {
		if err := w.setTarget(c.Target, c.Effort); err != nil {
			ack.Status, ack.Reason = AckRefused, err.Error()
			w.logf("control (%s): refused %s: %s", c.By, c.Target, sanitize.Line(err.Error(), 240))
		} else {
			w.logf("control (%s): next turn on %s", c.By, w.currentTarget())
			// A pool target the control picked is where the pool cycles on
			// from.
			if run := w.crewRun; run != nil {
				if i := slices.Index(run.Pool, w.currentTarget()); i >= 0 {
					w.Record.PoolIndex = i
				}
			}
			if c.By == "human" && !c.Auto {
				w.pinned = true
			}
		}
	}
	if c.Auto {
		w.pinned = false
	}
	w.save()
	if err := w.Registry.writeAck(w.Record.ID, ack); err != nil {
		w.logf("control ack: %v", err)
	}
}

// setTarget moves the next turn to t (when set) and effort (when set).
func (w *Worker) setTarget(t Target, effort string) error {
	if w.Controls == nil {
		return errors.New("this worker cannot change its model")
	}
	if err := w.Controls.SetTarget(t.Provider, t.Model, effort); err != nil {
		return err
	}
	w.syncTarget()
	return nil
}

// currentTarget is the target the next turn runs on.
func (w *Worker) currentTarget() Target {
	if w.Controls == nil {
		return Target{Provider: w.Cfg.Provider, Model: w.Cfg.Model}
	}
	st := w.Controls.State()
	return Target{Provider: st.Provider, Model: st.Model}
}

// syncTarget copies the current model into the record.
func (w *Worker) syncTarget() {
	t := w.currentTarget()
	w.Record.Provider, w.Record.Model = t.Provider, t.Model
	if w.Controls != nil {
		w.Record.Effort = w.Controls.State().Effort
	} else {
		w.Record.Effort = w.Cfg.Effort
	}
}
