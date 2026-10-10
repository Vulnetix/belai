package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vulnetix/belai/internal/agent"
)

// CoordSpec is the forge coordinator's answer to a request a worker filed, as
// a steer dispatch carries it and belai rc hands it to the worker: ids, an
// event, integers and one https link. Nothing in it is text for the model; the
// worker states it through agent.CoordFact's fixed templates.
type CoordSpec struct {
	Worker  string `json:"worker"`
	Item    string `json:"item"`
	Request string `json:"request"`
	Event   string `json:"event"`
	PR      int    `json:"pr"`
	PRURL   string `json:"prUrl"`
	Until   int64  `json:"until"`
	Reason  string `json:"reason"`
}

// Validate checks every field: a worker id, an item id, a request id, and an
// event whose numbers, link and reason agent.NewCoordFact accepts.
func (c CoordSpec) Validate() error {
	switch {
	case !ValidID(c.Worker):
		return errors.New("that is not a worker id")
	case !forgeIdent.MatchString(c.Item):
		return errors.New("that is not an item id")
	case !ValidRequestID(c.Request):
		return errors.New("that is not a forge request id")
	}
	if _, err := c.Fact(); err != nil {
		return err
	}
	return nil
}

// Fact is the spec as the harness fact the worker's goal loop takes.
func (c CoordSpec) Fact() (agent.CoordFact, error) {
	return agent.NewCoordFact(c.Event, c.PR, c.PRURL, c.Until, c.Reason)
}

// maxCoordBytes bounds a coordination file.
const maxCoordBytes = 4 << 10

// coordPath is the coordination file belai rc leaves for a worker, beside its
// record. List reads only ".json" names that are records and ignores it.
func (r *Registry) coordPath(id string) string { return filepath.Join(r.dir, id+".coord.json") }

// WriteCoord leaves a checked coordinator answer for worker spec.Worker, read
// and deleted by the worker between passes. A later answer replaces one the
// worker has not read yet.
func (r *Registry) WriteCoord(spec CoordSpec) error {
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("fleet: %w", err)
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	return writeAtomic(r.dir, spec.Worker+".coord.json", data)
}

// TakeCoord reads and deletes worker id's coordination file. ok is false when
// there is none. A file that does not decode or validate is deleted and
// returned as an error, so it is never read twice.
func (r *Registry) TakeCoord(id string) (CoordSpec, bool, error) {
	if !ValidID(id) {
		return CoordSpec{}, false, nil
	}
	p := r.coordPath(id)
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return CoordSpec{}, false, nil
	}
	if err != nil {
		return CoordSpec{}, false, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCoordBytes+1))
	f.Close()
	_ = os.Remove(p)
	if err != nil {
		return CoordSpec{}, false, err
	}
	if len(data) > maxCoordBytes {
		return CoordSpec{}, false, errors.New("the coordination file is too large")
	}
	var spec CoordSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return CoordSpec{}, false, errors.New("the coordination file does not decode")
	}
	if err := spec.Validate(); err != nil {
		return CoordSpec{}, false, err
	}
	if spec.Worker != id {
		return CoordSpec{}, false, errors.New("the coordination file names another worker")
	}
	return spec, true, nil
}
