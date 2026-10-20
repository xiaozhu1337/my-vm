package renderhost

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// Session authenticates the inherited pipes before the renderer creates any native resources.
type Session struct {
	Token          string
	Generation     uint64
	PID            int
	InitialDisplay Packet
	input          *bufio.Scanner
	output         *json.Encoder
	commands       chan Packet
	layout         chan Packet
}

func Open(in io.Reader, out io.Writer, token string, generation uint64) (*Session, error) {
	if len(token) != 64 || generation == 0 {
		return nil, errors.New("invalid renderer identity")
	}
	s := &Session{Token: token, Generation: generation, PID: os.Getpid(), input: bufio.NewScanner(in), output: json.NewEncoder(out), commands: make(chan Packet, 16), layout: make(chan Packet, 1)}
	s.input.Buffer(make([]byte, 1024), 4096)
	if !s.input.Scan() {
		return nil, errors.New("renderer parent disconnected")
	}
	var p Packet
	if json.Unmarshal(s.input.Bytes(), &p) != nil || !s.accept(p) || p.Kind != "start" {
		return nil, errors.New("invalid renderer handshake")
	}
	s.InitialDisplay = p
	return s, nil
}

func (s *Session) accept(p Packet) bool {
	return p.Token == s.Token && p.Generation == s.Generation && p.PID == s.PID
}

func (s *Session) Report(kind string, window uintptr) {
	_ = s.output.Encode(Packet{Token: s.Token, Generation: s.Generation, PID: s.PID, Window: uint64(window), Kind: kind})
}

func (s *Session) ReportLayout(window uintptr, width, height int32) {
	_ = s.output.Encode(Packet{Token: s.Token, Generation: s.Generation, PID: s.PID, Window: uint64(window), Kind: "layout-ready", Width: width, Height: height, Visible: true})
}

func (s *Session) Listen(wake func()) {
	go func() {
		for s.input.Scan() {
			var p Packet
			if json.Unmarshal(s.input.Bytes(), &p) != nil || !s.accept(p) {
				continue
			}
			switch p.Kind {
			case "close", "keys", "focus", "release-focus", "forget":
				s.commands <- p
				wake()
			case "layout":
				select {
				case <-s.layout:
				default:
				}
				s.layout <- p
				wake()
			}
		}
		s.commands <- Packet{Kind: "close"}
		wake()
	}()
}

func (s *Session) Next() Packet {
	select {
	case command := <-s.commands:
		return command
	default:
	}
	select {
	case command := <-s.layout:
		return command
	default:
		return Packet{}
	}
}
