package renderhost

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os/exec"
	"time"
)

// Packet contains only control metadata. Credentials and diagnostic output stay in the renderer.
type Packet struct {
	Token               string `json:"token"`
	Generation          uint64 `json:"generation"`
	PID                 int    `json:"pid"`
	Window              uint64 `json:"window,omitempty"`
	Kind                string `json:"kind"`
	X, Y, Width, Height int32
	Visible             bool
}

type Worker struct {
	Generation                               uint64
	Token                                    string
	PID                                      int
	Window                                   uint64
	Enhanced, Connected, LayoutReady, Failed bool
	Authenticating                           bool
	ConnectDeadline                          time.Time
	ctx                                      context.Context
	cancel                                   context.CancelFunc
	commands                                 chan Packet
	layout                                   chan Packet
	done                                     chan struct{}
}

func (w *Worker) Context() context.Context { return w.ctx }
func (w *Worker) Done() <-chan struct{}    { return w.done }

func (w *Worker) BasicConnected() bool {
	return w != nil && !w.Enhanced && w.Connected && !w.Failed
}

func (w *Worker) Send(kind string) bool {
	select {
	case w.commands <- Packet{Kind: kind}:
		return true
	default:
		return false
	}
}

type event struct {
	worker *Worker
	packet Packet
}

// Host's state is owned by the UI thread. All process and pipe operations run off that thread.
type Host struct {
	Active, Candidate *Worker
	Sequential        bool
	generation        uint64
	closed            bool
	workers           map[*Worker]bool
	events            chan event
	factory           func(*Worker) *exec.Cmd
}

func New(factory func(*Worker) *exec.Cmd) *Host {
	return &Host{factory: factory, workers: make(map[*Worker]bool), events: make(chan event, 64)}
}

func ShouldUpgrade(automatic bool, state string) bool { return automatic && state == "ready" }

func InitialEnhanced(automatic, enhanced bool) bool { return !automatic && enhanced }

func ShouldRestoreBasic(automatic bool, previous, active *Worker) bool {
	return automatic && previous != nil && previous.Enhanced && (previous.Failed || previous != active)
}

func (h *Host) Begin(enhanced bool) bool {
	if h.closed || h.Candidate != nil {
		return false
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return false
	}
	h.generation++
	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{Generation: h.generation, Token: hex.EncodeToString(secret[:]), Enhanced: enhanced, ctx: ctx, cancel: cancel, commands: make(chan Packet, 16), layout: make(chan Packet, 1), done: make(chan struct{})}
	var retired []*Worker
	if h.Sequential {
		for previous := range h.workers {
			previous.cancel()
			retired = append(retired, previous)
		}
		h.Active = nil
	}
	h.Candidate, h.workers[w] = w, true
	go h.run(w, retired)
	return true
}

// Poll validates an HWND's process and independent top-level window before admitting a packet.
// Optional deadlines only bound transport attempts; authentication may take arbitrarily long.
func (h *Host) Poll(validate func(Packet) bool) (changed, failed bool) {
	for {
		select {
		case e := <-h.events:
			w, p := e.worker, e.packet
			if !h.workers[w] || h.closed {
				continue
			}
			if p.Kind == "exit" {
				delete(h.workers, w)
				w.cancel()
				if h.Candidate == w {
					h.Candidate = nil
					failed = true
				}
				if h.Active == w {
					h.Active = nil
					changed = true
					failed = true
				}
				continue
			}
			if w != h.Active && w != h.Candidate {
				continue
			}
			if p.Token != w.Token || p.Generation != w.Generation || p.PID <= 0 {
				continue
			}
			if p.Kind == "started" {
				w.PID = p.PID
				continue
			}
			if p.PID != w.PID {
				continue
			}
			if p.Kind == "failed" {
				w.Failed = true
				w.Connected, w.LayoutReady = false, false
				if h.Candidate == w {
					h.Candidate = nil
					w.cancel()
				}
				if h.Active == w {
					changed = true
				}
				failed = true
				continue
			}
			if p.Window == 0 || !validate(p) {
				continue
			}
			if p.Kind == "window" {
				w.Window = p.Window
			}
			if w.Window != p.Window || w.Failed {
				continue
			}
			if p.Kind == "authenticating" {
				w.Authenticating = true
			}
			if p.Kind == "connected" {
				w.Connected = true
			}
			if p.Kind == "layout-ready" && w.Connected && p.Visible && p.Width >= 200 && p.Height >= 200 {
				w.LayoutReady = true
			}
		default:
			if w := h.Candidate; w != nil && !w.Connected && !w.Authenticating && !w.ConnectDeadline.IsZero() && !time.Now().Before(w.ConnectDeadline) {
				w.Failed = true
				w.cancel()
				h.Candidate, failed = nil, true
			}
			if w := h.Candidate; w != nil && w.Connected && w.LayoutReady && !w.Failed {
				old := h.Active
				h.Active, h.Candidate = w, nil
				if old != nil {
					old.cancel()
				}
				changed = true
			}
			return
		}
	}
}

func (h *Host) Position(window uint64, x, y, width, height int32, visible bool) {
	for w := range h.workers {
		if w.Window != window {
			continue
		}
		p := Packet{Kind: "layout", X: x, Y: y, Width: width, Height: height, Visible: visible}
		select {
		case <-w.layout:
		default:
		}
		w.layout <- p
		return
	}
}

// Cancel and Close terminate owned processes; a definitive renderer failure also releases its slot.
func (h *Host) Cancel() {
	if h.Candidate != nil {
		h.Candidate.cancel()
		h.Candidate = nil
	}
}

func (h *Host) Close() {
	if h.closed {
		return
	}
	h.closed = true
	for w := range h.workers {
		w.cancel()
	}
	h.Active, h.Candidate = nil, nil
}

// Wait runs after the native window closes, so renderer cleanup cannot block the UI.
func (h *Host) Wait(ctx context.Context) {
	for w := range h.workers {
		select {
		case <-w.done:
		case <-ctx.Done():
			return
		}
	}
}

func (h *Host) run(w *Worker, retired []*Worker) {
	emit := func(p Packet) {
		select {
		case h.events <- event{w, p}:
		case <-w.ctx.Done():
		}
	}
	defer func() {
		// Exit must remain observable even after an explicit cancellation.
		close(w.done)
		h.events <- event{w, Packet{Kind: "exit"}}
	}()
	for _, previous := range retired {
		select {
		case <-previous.done:
		case <-w.ctx.Done():
			return
		}
	}
	cmd := h.factory(w)
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err = cmd.Start(); err != nil {
		return
	}
	cleanup, err := contain(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = stdin.Close()
		_ = cmd.Wait()
		return
	}
	defer cleanup()
	emit(Packet{Kind: "started", Token: w.Token, Generation: w.Generation, PID: cmd.Process.Pid})
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		encoder := json.NewEncoder(stdin)
		write := func(p Packet) error {
			p.Token, p.Generation, p.PID = w.Token, w.Generation, cmd.Process.Pid
			return encoder.Encode(p)
		}
		if write(Packet{Kind: "start"}) != nil {
			return
		}
		for {
			select {
			case p := <-w.commands:
				if write(p) != nil {
					return
				}
			case p := <-w.layout:
				if write(p) != nil {
					return
				}
			case <-finished:
				return
			case <-w.ctx.Done():
				_ = write(Packet{Kind: "close"})
				return
			}
		}
	}()
	go func() {
		select {
		case <-w.ctx.Done():
			grace := time.NewTimer(2 * time.Second)
			defer grace.Stop()
			select {
			case <-finished:
			case <-grace.C:
				_ = cmd.Process.Kill()
			}
		case <-finished:
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024), 4096)
	for scanner.Scan() {
		var p Packet
		if json.Unmarshal(scanner.Bytes(), &p) == nil && p.PID == cmd.Process.Pid && (p.Kind == "window" || p.Kind == "connected" || p.Kind == "authenticating" || p.Kind == "layout-ready" || p.Kind == "failed") {
			emit(p)
		}
	}
	_ = cmd.Wait()
}
