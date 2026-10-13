package main

import (
	"errors"
	"net/http"
	"regexp"
	"sync"
)

// xfer lets a running transfer be paused, resumed or canceled from either side.
// Pausing just stops reading/writing the stream; TCP flow control then stalls the
// other end, so Resume continues exactly where it stopped.
type xfer struct {
	id       string
	owner    string // device ID allowed to control it besides this PC ("" = this PC only)
	mu       sync.Mutex
	cond     *sync.Cond
	paused   bool
	canceled bool
}

var (
	errCanceled = errors.New("Canceled")
	xidRe       = regexp.MustCompile(`^[a-z0-9-]{6,40}$`)
)

// newXfer registers a transfer. id comes from the sender's UI so it can control it too;
// a bad or taken id gets a random one.
func (a *App) newXfer(id, owner string) *xfer {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !xidRe.MatchString(id) || a.xfers[id] != nil {
		id = randHex(8)
	}
	x := &xfer{id: id, owner: owner}
	x.cond = sync.NewCond(&x.mu)
	a.xfers[id] = x
	return x
}

func (a *App) endXfer(x *xfer) {
	a.mu.Lock()
	delete(a.xfers, x.id)
	a.mu.Unlock()
	x.set("cancel") // wake anything still waiting
}

// wait blocks while paused; returns errCanceled once canceled.
func (x *xfer) wait() error {
	if x == nil {
		return nil
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for x.paused && !x.canceled {
		x.cond.Wait()
	}
	if x.canceled {
		return errCanceled
	}
	return nil
}

func (x *xfer) set(action string) {
	x.mu.Lock()
	switch action {
	case "pause":
		x.paused = true
	case "resume":
		x.paused = false
	case "cancel":
		x.canceled = true
	}
	x.mu.Unlock()
	x.cond.Broadcast()
}

// gate wraps a stream so every read first honors pause/cancel.
type gate struct {
	x *xfer
	r interface{ Read([]byte) (int, error) }
}

func (g gate) Read(p []byte) (int, error) {
	if err := g.x.wait(); err != nil {
		return 0, err
	}
	return g.r.Read(p)
}

// control: POST /api/xfer {id, action: pause|resume|cancel}. This PC controls every transfer;
// a phone only the ones it started.
func (a *App) control(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID, Action string }
	if readJSON(w, r, &req) != nil {
		return
	}
	a.mu.Lock()
	x := a.xfers[req.ID]
	a.mu.Unlock()
	if x == nil {
		return // already finished
	}
	if !isLocal(r) {
		if d := a.device(r); d == nil || d.ID != x.owner {
			http.Error(w, "not your transfer", http.StatusForbidden)
			return
		}
	}
	x.set(req.Action)
	a.publish(true, "xfer", map[string]any{"id": x.id, "action": req.Action})
}

// reqXfer registers the transfer named by ?xid= (the sender's UI picks it so both sides can
// control it); ?paused=1 starts it paused, for the next file of a batch the user paused.
func (a *App) reqXfer(r *http.Request, owner string) *xfer {
	x := a.newXfer(r.URL.Query().Get("xid"), owner)
	if r.URL.Query().Get("paused") == "1" {
		x.set("pause")
	}
	return x
}
