package guestops

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCommandChild(t *testing.T) {
	if os.Getenv("MY_VM_EXEC_HELPER") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "exit":
		os.Stdout.Write([]byte("hello\x00世界"))
		os.Stderr.Write([]byte("stderr"))
		os.Exit(7)
	case "large":
		os.Stdout.Write(bytes.Repeat([]byte("x"), outputLimit+128))
		os.Exit(0)
	case "timeout":
		for {
			time.Sleep(100 * time.Millisecond)
		}
	}
	os.Exit(8)
}
func TestCommandTreeChild(t *testing.T) {
	if os.Getenv("MY_VM_TREE_HELPER") != "1" {
		return
	}
	mode, path := os.Args[len(os.Args)-1], os.Args[len(os.Args)-2]
	if mode == "parent" {
		exe, _ := os.Executable()
		child := exec.Command(exe, "-test.run=^TestCommandTreeChild$", "--", path, "child")
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
		for {
			time.Sleep(25 * time.Millisecond)
		}
	}
	for {
		os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"+strconv.FormatInt(time.Now().UnixNano(), 10)), 0600)
		time.Sleep(20 * time.Millisecond)
	}
}

func TestExecuteTimeoutStopsDescendants(t *testing.T) {
	t.Setenv("MY_VM_TREE_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "heartbeat")
	data, _ := json.Marshal(Command{Args: []string{exe, "-test.run=^TestCommandTreeChild$", "--", path, "parent"}, Timeout: 1})
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest("POST", "/exec", bytes.NewReader(data)))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("descendant did not start", err, response.Body.String())
	}
	pid, _ := strconv.Atoi(strings.Split(string(before), "\n")[0])
	defer func() {
		if process, e := os.FindProcess(pid); e == nil {
			process.Kill()
			process.Release()
		}
	}()
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("descendant kept writing after timeout", err)
	}
	if !strings.Contains(response.Body.String(), "context deadline exceeded") {
		t.Fatal(response.Body.String())
	}
}

func TestExecuteExitOutputLimitAndTimeout(t *testing.T) {
	t.Setenv("MY_VM_EXEC_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exit", "large", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			timeout := 5
			if mode == "timeout" {
				timeout = 1
			}
			data, _ := json.Marshal(Command{Args: []string{exe, "-test.run=^TestCommandChild$", "--", mode}, Timeout: timeout})
			request := httptest.NewRequest("POST", "/exec", bytes.NewReader(data))
			response := httptest.NewRecorder()
			Handler().ServeHTTP(response, request)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			var result Result
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "exit":
				if result.ExitCode != 7 || string(result.Stdout) != "hello\x00世界" || string(result.Stderr) != "stderr" {
					t.Fatalf("%+v", result)
				}
			case "large":
				if result.ExitCode != 0 || len(result.Stdout) != outputLimit || !result.Truncated {
					t.Fatalf("code=%d size=%d truncated=%v", result.ExitCode, len(result.Stdout), result.Truncated)
				}
			case "timeout":
				if result.ExitCode == 0 || result.Error != "context deadline exceeded" {
					t.Fatalf("%+v", result)
				}
			}
		})
	}
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest("POST", "/exec", strings.NewReader(`{"args":["program"],"directory":"relative","timeout":1}`)))
	if response.Code != 400 {
		t.Fatal("relative working directory accepted", response.Code)
	}
}

type brokenReader struct{}

func (brokenReader) Read(data []byte) (int, error) {
	copy(data, "partial")
	return 7, errors.New("transfer interrupted")
}

func TestReceiveFilePreservesDestinationOnFailureAndConflict(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.bin")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ReceiveFile(target, strings.NewReader("replacement"), false); err == nil {
		t.Fatal("overwrote without permission")
	}
	if err := ReceiveFile(target, brokenReader{}, true); err == nil {
		t.Fatal("accepted interrupted transfer")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "original" {
		t.Fatal(string(data), err)
	}
	if err = ReceiveFile(target, strings.NewReader("replacement"), true); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest("GET", "/file?path="+filepath.ToSlash(target), nil))
	if response.Code != 200 || response.Body.String() != "replacement" {
		t.Fatal(response.Code, response.Body.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary file leaked", entries, err)
	}
	response = httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest("PUT", "/file?path=relative", io.NopCloser(strings.NewReader("unsafe"))))
	if response.Code != 400 {
		t.Fatal("accepted relative target")
	}
}
