//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"my-vm/internal/guestops"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGuestCommandAndBinaryCopies(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "host.bin"), filepath.Join(root, "download.bin")
	binary := []byte{0, 1, 2, 0xff, 0xfe, 0x80}
	if err := os.WriteFile(source, binary, 0600); err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	commands := &commands{ctx: context.Background(), stdout: &output, stderr: &stderr, timeout: 60}
	commands.client = &http.Client{Transport: testTransport(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		if request.URL.Query().Get("vm") != "VM" {
			t.Fatal(request.URL)
		}
		switch request.URL.Path {
		case "/v1/guest/exec":
			var command guestops.Command
			if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
				t.Fatal(err)
			}
			if len(command.Args) != 3 || command.Args[0] != "program" || command.Args[1] != "space argument" || command.Args[2] != "literal$()" {
				t.Fatal(command.Args)
			}
			json.NewEncoder(response).Encode(guestops.Result{ExitCode: 7, Stdout: []byte("out"), Stderr: []byte("err")})
		case "/v1/guest/file":
			if request.URL.Query().Get("path") != `C:\guest file.bin` {
				t.Fatal(request.URL)
			}
			if request.Method == "PUT" {
				data, err := io.ReadAll(request.Body)
				if err != nil || !bytes.Equal(data, binary) || request.ContentLength != int64(len(binary)) {
					t.Fatal(data, err, request.ContentLength)
				}
				response.WriteHeader(204)
			} else {
				response.Write(binary)
			}
		default:
			t.Fatal(request.URL)
		}
		return response.Result(), nil
	})}
	if code, err := commands.execute([]string{"exec", "VM", "program", "space argument", "literal$()"}); err != nil || code != 7 || output.String() != "out" || stderr.String() != "err" {
		t.Fatal(code, err, output.String(), stderr.String())
	}
	if _, err := commands.execute([]string{"copy-to", "VM", source, `C:\guest file.bin`}); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.execute([]string{"copy-from", "VM", `C:\guest file.bin`, target}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(data, binary) {
		t.Fatal(data, err)
	}
	if _, err = commands.execute([]string{"copy-from", "VM", `C:\guest file.bin`, target}); err == nil {
		t.Fatal("overwrote destination without --overwrite")
	}
}
