package sharing

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIdleTimeoutRenewsDuringUploadAndDownload(t *testing.T) {
	const idle = 200 * time.Millisecond
	const count = 8
	server := httptest.NewServer(idleHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if string(data) != strings.Repeat("x", count) {
			http.Error(w, "unexpected upload", 400)
			return
		}
		for range count {
			if _, err = io.WriteString(w, "y"); err != nil {
				return
			}
			if err = http.NewResponseController(w).Flush(); err != nil {
				return
			}
			<-time.After(40 * time.Millisecond)
		}
	}), idle))
	defer server.Close()
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		for range count {
			if _, err := io.WriteString(writer, "x"); err != nil {
				return
			}
			<-time.After(40 * time.Millisecond)
		}
	}()
	defer reader.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Post(server.URL, "application/octet-stream", reader)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(data) != strings.Repeat("y", count) {
		t.Fatal(response.Status, string(data), err)
	}
}

func TestIdleTimeoutProtectsAutomaticBodyDrain(t *testing.T) {
	for _, closeBody := range []bool{false, true} {
		t.Run(fmt.Sprintf("close=%t", closeBody), func(t *testing.T) {
			server := httptest.NewServer(idleHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if closeBody {
					_ = r.Body.Close()
				}
				_, _ = io.WriteString(w, "ok")
			}), 50*time.Millisecond))
			defer server.Close()
			connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if err = connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err = io.WriteString(connection, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 1\r\nConnection: close\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			// The missing body byte must not hold the handler or final response open.
			if _, err = io.ReadAll(connection); err != nil {
				t.Fatal("unread request body bypassed the idle timeout", err)
			}
		})
	}
}

func TestIdleTimeoutStopsStalledUpload(t *testing.T) {
	finished := make(chan error, 1)
	server := httptest.NewServer(idleHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		finished <- err
	}), 50*time.Millisecond))
	defer server.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	response := make(chan error, 1)
	go func() {
		res, err := http.Post(server.URL, "application/octet-stream", reader)
		if res != nil {
			res.Body.Close()
		}
		response <- err
	}()
	if _, err := fmt.Fprint(writer, "partial"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("stalled upload was accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("idle upload was not interrupted")
	}
	writer.Close()
	select {
	case <-response:
	case <-time.After(time.Second):
		t.Fatal("client did not close")
	}
}
