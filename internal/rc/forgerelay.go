package rc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// DefaultForgeRelayEvery is how often the daemon files what its workers spooled
// for the forge coordinator.
const DefaultForgeRelayEvery = 5 * time.Second

// maxSpoolBytes is the contract's cap on a forge request body.
const maxSpoolBytes = 8 << 10

// staleBundle is how long a handed-over bundle is kept: the coordinator's
// request expires after a day, so a bundle twice that old serves nobody.
const staleBundle = 48 * time.Hour

// forgeRelay files spooled forge requests until ctx ends.
func (d *Daemon) forgeRelay(ctx context.Context) {
	defer d.wg.Done()
	t := time.NewTicker(d.o.ForgeRelayEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.relayForge(ctx)
	}
}

// spoolDir is the registry directory the relay reads, or "".
func (d *Daemon) spoolDir() string {
	if d.o.ForgeSpool != "" {
		return d.o.ForgeSpool
	}
	reg, err := fleet.OpenRegistry(nil)
	if err != nil {
		return ""
	}
	return reg.Dir()
}

// relayForge files every spooled forge request once: each <worker>.forge
// directory's <requestId>.json, checked field by field (fleet.ValidForgeRequest)
// and against the names it was found under. A request the server took (2xx) or
// refused (4xx) is deleted; any other answer leaves it for the next sweep. A
// malformed file is deleted unread. It returns how many it filed.
func (d *Daemon) relayForge(ctx context.Context) int {
	dir := d.spoolDir()
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	filed := 0
	for _, e := range entries {
		worker, ok := strings.CutSuffix(e.Name(), ".forge")
		if !ok || !e.IsDir() || !fleet.ValidID(worker) {
			continue
		}
		spool := fleet.SpoolDir(dir, worker)
		files, _ := os.ReadDir(spool)
		for _, f := range files {
			id, ok := strings.CutSuffix(f.Name(), ".json")
			if !ok || !f.Type().IsRegular() || strings.HasPrefix(f.Name(), ".") {
				continue
			}
			path := filepath.Join(spool, f.Name())
			req, err := readSpool(path)
			if err == nil && (req.RequestID != id || req.WorkerID != worker) {
				err = errors.New("the request does not match the file it was spooled in")
			}
			if err != nil {
				d.logf("forge request %s of %s dropped: %v", short(id), worker, err)
				_ = os.Remove(path)
				continue
			}
			status, got, err := d.o.Client.FileForgeRequest(ctx, d.o.HostID, req)
			switch {
			case err != nil:
				if ctx.Err() == nil {
					d.logf("forge request %s: %v (retrying)", req.RequestID, err)
				}
			case status >= 200 && status <= 299:
				d.logf("forge request %s filed for %s (%s)", req.RequestID, worker, statusWord(got.Status))
				_ = os.Remove(path)
				filed++
			case status >= 400 && status <= 499:
				// Never taken: the card, branch or repository did not check out.
				d.logf("forge request %s refused by the website (HTTP %d); dropped", req.RequestID, status)
				_ = os.Remove(path)
				removeBundle(req.RequestID)
			default:
				d.logf("forge request %s: HTTP %d (retrying)", req.RequestID, status)
			}
		}
	}
	pruneBundles(time.Now())
	return filed
}

// readSpool reads one spooled request: at most the contract's 8 KiB, no field
// the contract does not name, and every field in shape.
func readSpool(path string) (sessionsync.ForgeRequest, error) {
	var req sessionsync.ForgeRequest
	f, err := os.Open(path)
	if err != nil {
		return req, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSpoolBytes+1))
	if err != nil {
		return req, err
	}
	if len(data) > maxSpoolBytes {
		return req, errors.New("over 8 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, errors.New("it does not decode")
	}
	return req, fleet.ValidForgeRequest(req)
}

// statusWord keeps a server status to a short identifier for the log.
func statusWord(s string) string {
	if s == "" || len(s) > 16 || strings.Trim(s, "abcdefghijklmnopqrstuvwxyz_") != "" {
		return "accepted"
	}
	return s
}

// removeBundle deletes a refused request's bundle.
func removeBundle(id string) {
	if !fleet.ValidRequestID(id) {
		return
	}
	if dir, err := config.ForgeDir(); err == nil {
		_ = os.Remove(filepath.Join(dir, id+".bundle"))
	}
}

// pruneBundles deletes bundles older than staleBundle.
func pruneBundles(now time.Time) {
	dir, err := config.ForgeDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".bundle") || !e.Type().IsRegular() {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > staleBundle {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// steerWorker leaves a coordinator answer for one of this host's live workers.
// It reads the worker from the registry, so an id the host does not run, or a
// worker that already ended, is refused.
func steerWorker(spec fleet.CoordSpec) error {
	reg, err := fleet.OpenRegistry(nil)
	if err != nil {
		return err
	}
	rec, err := reg.Get(spec.Worker)
	if err != nil || !rec.State.Live() {
		return errors.New("that worker is not running on this host")
	}
	return reg.WriteCoord(spec)
}
