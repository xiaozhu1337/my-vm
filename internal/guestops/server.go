package guestops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const Port = 19028
const outputLimit = 4 << 20

type Command struct {
	Args      []string `json:"args"`
	Directory string   `json:"directory,omitempty"`
	Timeout   int      `json:"timeout"`
}

type Result struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    []byte `json:"stdout"`
	Stderr    []byte `json:"stderr"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

type limitedOutput struct {
	data      []byte
	truncated bool
}

func (w *limitedOutput) Write(data []byte) (int, error) {
	count := len(data)
	remaining := outputLimit - len(w.data)
	if len(data) > remaining {
		data = data[:remaining]
		w.truncated = true
	}
	w.data = append(w.data, data...)
	return count, nil
}

func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /exec", execute)
	mux.HandleFunc("GET /file", download)
	mux.HandleFunc("PUT /file", upload)
	return mux
}

func execute(w http.ResponseWriter, r *http.Request) {
	var input Command
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || len(input.Args) == 0 || input.Args[0] == "" || input.Timeout < 1 || input.Timeout > 3600 {
		http.Error(w, "invalid command or timeout (1..3600 seconds)", http.StatusBadRequest)
		return
	}
	if input.Directory != "" && !filepath.IsAbs(input.Directory) {
		http.Error(w, "working directory must be absolute", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(input.Timeout)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, input.Args[0], input.Args[1:]...)
	command.Dir = input.Directory
	command.WaitDelay = time.Second
	stdout, stderr := &limitedOutput{}, &limitedOutput{}
	command.Stdout, command.Stderr = stdout, stderr
	err := runCommand(command)
	result := Result{Stdout: stdout.data, Stderr: stderr.data, Truncated: stdout.truncated || stderr.truncated}
	if err != nil {
		result.ExitCode = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.ExitCode = exit.ExitCode()
		}
		result.Error = err.Error()
		if ctx.Err() != nil {
			result.Error = ctx.Err().Error()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func filePath(w http.ResponseWriter, r *http.Request) (string, bool) {
	path := r.URL.Query().Get("path")
	if !filepath.IsAbs(path) {
		http.Error(w, "file path must be absolute", 400)
		return "", false
	}
	return filepath.Clean(path), true
}
func download(w http.ResponseWriter, r *http.Request) {
	path, ok := filePath(w, r)
	if !ok {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "source must be a regular file", 400)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
func upload(w http.ResponseWriter, r *http.Request) {
	path, ok := filePath(w, r)
	if !ok {
		return
	}
	if err := ReceiveFile(path, r.Body, r.URL.Query().Get("overwrite") == "true"); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReceiveFile publishes a complete file and preserves an existing destination unless explicitly replaced.
func ReceiveFile(path string, source io.Reader, overwrite bool) error {
	file, err := receiveTemporary(path, overwrite)
	if err != nil {
		return err
	}
	pending := file.Name()
	defer os.Remove(pending)
	_, err = io.Copy(file, source)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if overwrite {
		return os.Rename(pending, path)
	}
	return os.Link(pending, path)
}
