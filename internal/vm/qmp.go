package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

type qmpReply struct {
	ID    string `json:"id"`
	Error *struct {
		Description string `json:"desc"`
	} `json:"error"`
}

type monitor struct {
	conn     net.Conn
	replies  chan qmpReply
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	sequence uint64
	timeout  time.Duration
}

func newMonitor(conn net.Conn) (*monitor, error) {
	decoder := json.NewDecoder(conn)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var greeting map[string]json.RawMessage
	if err := decoder.Decode(&greeting); err != nil {
		return nil, err
	}
	if _, ok := greeting["QMP"]; !ok {
		return nil, errors.New("QMP 握手无效")
	}
	_ = conn.SetDeadline(time.Time{})
	m := &monitor{conn: conn, replies: make(chan qmpReply, 16), done: make(chan struct{}), timeout: 5 * time.Second}
	go func() {
		defer m.close()
		defer close(m.replies)
		for {
			var reply qmpReply
			if err := decoder.Decode(&reply); err != nil {
				return
			}
			if reply.ID == "" {
				continue
			}
			select {
			case m.replies <- reply:
			case <-m.done:
				return
			}
		}
	}()
	if err := m.execute("qmp_capabilities"); err != nil {
		m.close()
		return nil, err
	}
	return m, nil
}

func (m *monitor) close() { m.once.Do(func() { close(m.done); _ = m.conn.Close() }) }

func (m *monitor) execute(command string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sequence++
	id := strconv.FormatUint(m.sequence, 10)
	if err := m.conn.SetWriteDeadline(time.Now().Add(m.timeout)); err != nil {
		return err
	}
	if err := json.NewEncoder(m.conn).Encode(map[string]string{"execute": command, "id": id}); err != nil {
		return err
	}
	timer := time.NewTimer(m.timeout)
	defer timer.Stop()
	for {
		select {
		case reply, ok := <-m.replies:
			if !ok {
				return errors.New("QMP 连接已关闭")
			}
			if reply.ID != id {
				continue
			}
			if reply.Error != nil {
				return fmt.Errorf("QMP %s: %s", command, reply.Error.Description)
			}
			return nil
		case <-timer.C:
			return fmt.Errorf("QMP %s 等待回复超时；可重试操作", command)
		}
	}
}
