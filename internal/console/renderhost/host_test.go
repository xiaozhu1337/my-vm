package renderhost

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAutomaticRequiresPositiveReadiness(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, state := range []string{"ready", "unknown", "available", "waiting", "unsupported", "unavailable", ""} {
			if got := ShouldUpgrade(automatic, state); got != (automatic && state == "ready") {
				t.Fatalf("auto=%v state=%s upgrade=%v", automatic, state, got)
			}
		}
		for _, enhanced := range []bool{false, true} {
			if got := InitialEnhanced(automatic, enhanced); got != (!automatic && enhanced) {
				t.Fatal("automatic startup did not select basic")
			}
		}
	}
}

func fakeHost(t *testing.T, scenario string) *Host {
	t.Helper()
	h := New(func(w *Worker) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=^TestRendererProcessHelper$", "--", "--renderhost-test", w.Token, strconv.FormatUint(w.Generation, 10), scenario)
	})
	t.Cleanup(func() {
		var workers []*Worker
		for w := range h.workers {
			workers = append(workers, w)
		}
		h.Close()
		for _, w := range workers {
			select {
			case <-w.Done():
			case <-time.After(3 * time.Second):
				t.Error("owned worker survived close")
			}
		}
	})
	return h
}

func pumpUntil(t *testing.T, h *Host, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		h.Poll(func(p Packet) bool { return p.Window == uint64(p.PID) })
		if condition() {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("fake worker transition did not complete")
		}
	}
}

func TestProcessPromotionAndExplicitClose(t *testing.T) {
	h := fakeHost(t, "connected")
	if !h.Begin(false) {
		t.Fatal("basic did not start")
	}
	basic := h.Candidate
	pumpUntil(t, h, func() bool { return h.Active == basic })
	if basic.Enhanced || !basic.Connected {
		t.Fatal("initial renderer is not basic")
	}
	if !h.Begin(true) {
		t.Fatal("enhanced did not start")
	}
	enhanced := h.Candidate
	if h.Active != basic || h.Begin(true) {
		t.Fatal("candidate replaced basic before connection or duplicate launch allowed")
	}
	pumpUntil(t, h, func() bool { return h.Active == enhanced })
	select {
	case <-basic.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("retired renderer did not honor asynchronous close")
	}
	h.Close()
	select {
	case <-enhanced.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("close did not terminate owned renderer")
	}
	if h.Begin(false) {
		t.Fatal("closed host accepted new work")
	}
}

func TestHungCandidatePreservesBasicUntilExplicitCancel(t *testing.T) {
	h := fakeHost(t, "basic-then-hung")
	h.Begin(false)
	basic := h.Candidate
	pumpUntil(t, h, func() bool { return h.Active == basic })
	h.Begin(true)
	hung := h.Candidate
	pumpUntil(t, h, func() bool { return hung.Window != 0 })
	for i := 0; i < 100; i++ {
		h.Poll(func(Packet) bool { return true })
	}
	if h.Active != basic || h.Candidate != hung || !basic.Connected {
		t.Fatal("hung candidate displaced basic")
	}
	select {
	case <-hung.Done():
		t.Fatal("authentication was timed out")
	default:
	}
	started := time.Now()
	h.Cancel()
	if time.Since(started) > 100*time.Millisecond || h.Active != basic || h.Candidate != nil {
		t.Fatal("cancel blocked UI or discarded basic")
	}
	select {
	case <-hung.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not terminate owned hung process")
	}
	select {
	case <-basic.Done():
		t.Fatal("cancel killed active basic")
	default:
	}
}

func TestFailureAndStaleGenerationPreserveActive(t *testing.T) {
	h := New(nil)
	basic := &Worker{Generation: 1, Token: "basic", PID: 10, Window: 10, Connected: true, commands: make(chan Packet, 4)}
	candidate := &Worker{Generation: 2, Token: "candidate", PID: 20, Window: 20, Enhanced: true, commands: make(chan Packet, 4), cancel: func() {}}
	h.Active, h.Candidate = basic, candidate
	h.workers[basic], h.workers[candidate] = true, true
	packet := func(kind string) Packet {
		return Packet{Kind: kind, Token: candidate.Token, Generation: candidate.Generation, PID: candidate.PID, Window: candidate.Window}
	}
	stale := packet("connected")
	stale.Generation--
	h.events <- event{candidate, stale}
	wrongToken := packet("connected")
	wrongToken.Token = "other-session"
	h.events <- event{candidate, wrongToken}
	wrongPID := packet("connected")
	wrongPID.PID++
	h.events <- event{candidate, wrongPID}
	h.Poll(func(Packet) bool { return true })
	if h.Active != basic || candidate.Connected {
		t.Fatal("stale identity switched active renderer")
	}
	h.events <- event{candidate, packet("connected")}
	h.events <- event{candidate, packet("failed")}
	changed, failed := h.Poll(func(Packet) bool { return true })
	if changed || !failed || h.Active != basic || h.Candidate != nil || !candidate.Failed || len(basic.commands) != 0 {
		t.Fatal("failed candidate closed a usable basic renderer")
	}
	candidate.Failed = false
	h.Candidate = candidate
	h.events <- event{candidate, packet("connected")}
	h.Poll(func(Packet) bool { return false })
	if h.Active != basic {
		t.Fatal("foreign HWND was accepted")
	}
}

func TestCandidateExitDoesNotCloseBasic(t *testing.T) {
	h := fakeHost(t, "basic-then-exit")
	h.Begin(false)
	basic := h.Candidate
	pumpUntil(t, h, func() bool { return h.Active == basic })
	h.Begin(true)
	pumpUntil(t, h, func() bool { return h.Candidate == nil })
	if h.Active != basic {
		t.Fatal("candidate exit lost basic")
	}
}

func TestExplicitCandidateFailureAllowsRetry(t *testing.T) {
	h := fakeHost(t, "basic-then-failed")
	h.Begin(false)
	basic := h.Candidate
	pumpUntil(t, h, func() bool { return h.Active == basic })
	h.Begin(true)
	failed := h.Candidate
	pumpUntil(t, h, func() bool { return h.Candidate == nil })
	if h.Active != basic || !failed.Failed || !h.Begin(true) {
		t.Fatal("definitive failure lost basic or prevented retry")
	}
	select {
	case <-failed.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("failed owned candidate was not cleaned up")
	}
}

func TestActiveEnhancedFailureCanRestoreBasic(t *testing.T) {
	h := fakeHost(t, "fail-on-keys")
	h.Begin(true)
	enhanced := h.Candidate
	pumpUntil(t, h, func() bool { return h.Active == enhanced })
	if ShouldRestoreBasic(true, enhanced, enhanced) {
		t.Fatal("healthy enhanced renderer requested a fallback")
	}
	enhanced.Send("keys")
	pumpUntil(t, h, func() bool { return enhanced.Failed })
	if enhanced.Connected || !ShouldRestoreBasic(true, enhanced, h.Active) || ShouldRestoreBasic(false, enhanced, h.Active) || !ShouldRestoreBasic(true, enhanced, nil) {
		t.Fatal("active failure/exit did not reset connection or respect automatic mode")
	}
	if !h.Begin(false) {
		t.Fatal("failed enhanced renderer prevented basic recovery")
	}
	basic := h.Candidate
	pumpUntil(t, h, func() bool { return h.Active == basic })
	if basic.Enhanced || !basic.Connected {
		t.Fatal("recovery did not connect a basic renderer")
	}
}

func TestFailedBasicCanReconnect(t *testing.T) {
	h := fakeHost(t, "fail-on-keys")
	h.Begin(false)
	basic := h.Candidate
	pumpUntil(t, h, func() bool { return h.Active == basic })
	if !basic.BasicConnected() {
		t.Fatal("healthy basic renderer was not reusable")
	}
	basic.Send("keys")
	pumpUntil(t, h, func() bool { return basic.Failed })
	if basic.BasicConnected() || (*Worker)(nil).BasicConnected() || !h.Begin(false) {
		t.Fatal("failed or absent basic renderer prevented reconnection")
	}
	replacement := h.Candidate
	pumpUntil(t, h, func() bool { return h.Active == replacement })
	if !replacement.BasicConnected() {
		t.Fatal("replacement basic renderer did not connect")
	}
}

func TestReplacingHungActiveTerminatesRetiredProcess(t *testing.T) {
	h := fakeHost(t, "connected-then-hung")
	h.Begin(false)
	basic := h.Candidate
	pumpUntil(t, h, func() bool { return h.Active == basic })
	h.Begin(true)
	enhanced := h.Candidate
	pumpUntil(t, h, func() bool { return h.Active == enhanced })
	if enhanced.BasicConnected() {
		t.Fatal("enhanced renderer was mistaken for a reusable basic renderer")
	}
	select {
	case <-basic.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("retired hung renderer survived replacement")
	}
}

func TestSessionRejectsForgedHandshake(t *testing.T) {
	token := strings.Repeat("a", 64)
	for _, p := range []Packet{{Kind: "start", Token: token, Generation: 1, PID: os.Getpid() + 1}, {Kind: "start", Token: strings.Repeat("b", 64), Generation: 1, PID: os.Getpid()}, {Kind: "start", Token: token, Generation: 2, PID: os.Getpid()}} {
		data, _ := json.Marshal(p)
		if _, err := Open(strings.NewReader(string(data)+"\n"), os.Stdout, token, 1); err == nil {
			t.Fatal("forged parent handshake accepted")
		}
	}
}

// This helper uses only inherited pipes. It never creates windows or initializes COM.
func TestRendererProcessHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--renderhost-test" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	token, generation, scenario := os.Args[index+1], os.Args[index+2], os.Args[index+3]
	gen, _ := strconv.ParseUint(generation, 10, 64)
	s, err := Open(os.Stdin, os.Stdout, token, gen)
	if err != nil {
		os.Exit(2)
	}
	s.Report("window", uintptr(os.Getpid()))
	if scenario == "basic-then-exit" && gen > 1 {
		os.Exit(0)
	}
	if scenario == "basic-then-failed" && gen > 1 {
		s.Report("failed", uintptr(os.Getpid()))
	} else if scenario != "basic-then-hung" || gen == 1 {
		s.Report("connected", uintptr(os.Getpid()))
	}
	if scenario == "connected-then-hung" || scenario == "basic-then-hung" && gen > 1 {
		for {
			time.Sleep(time.Second)
		}
	}
	wake := make(chan struct{}, 1)
	s.Listen(func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	for range wake {
		for p := s.Next(); p.Kind != ""; p = s.Next() {
			if p.Kind == "close" {
				os.Exit(0)
			}
			if p.Kind == "keys" {
				if scenario == "fail-on-keys" {
					s.Report("failed", uintptr(os.Getpid()))
				}
				fmt.Fprintln(os.Stderr, "keys received")
			}
		}
	}
}
