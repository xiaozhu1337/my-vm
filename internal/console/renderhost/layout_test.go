package renderhost

import "testing"

func TestPositionSkipsUnchangedLayoutButKeepsTransitions(t *testing.T) {
	worker := &Worker{Window: 7, layout: make(chan Packet, 1)}
	host := &Host{workers: map[*Worker]bool{worker: true}}
	host.Position(7, 10, 20, 1280, 720, true)
	first := <-worker.layout
	host.Position(7, 10, 20, 1280, 720, true)
	select {
	case <-worker.layout:
		t.Fatal("unchanged layout emitted IPC")
	default:
	}
	host.Position(7, 11, 20, 1280, 720, true)
	if next := <-worker.layout; next == first || next.X != 11 {
		t.Fatal(next)
	}
	host.Position(7, 11, 20, 1280, 720, false)
	if next := <-worker.layout; next.Visible {
		t.Fatal("visibility transition dropped")
	}
	replacement := &Worker{Window: 8, layout: make(chan Packet, 1)}
	host.workers[replacement] = true
	host.Position(8, 11, 20, 1280, 720, false)
	select {
	case <-replacement.layout:
	default:
		t.Fatal("new renderer did not receive initial layout")
	}
}
