//go:build windows

package library

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPBoundaryValidationAndLegacyConsoleProtocol(t *testing.T) {
	service, _ := fixture(t, 1)
	cfg, _ := service.config()
	id := cfg.Machines[0].ID
	service.gate.Lock()
	defer service.gate.Unlock()
	cases := []struct {
		method, path, body string
		code               int
	}{
		{"POST", "/v1/action", `{"action":"list","unknown":1}`, 400},
		{"POST", "/v1/action", `{"action":"list"} {}`, 400},
		{"POST", "/v1/action", `{"action":"list"}`, 200},
		{"GET", "/v1/action?action=start&vm=" + id, "", 405},
		{"GET", "/v1/action?action=state&vm=" + id, "", 200},
		{"POST", "/v1/action?action=runtime&vm=" + id, "", 400},
		{"POST", "/v1/action", `{"action":"get","vm":"` + id + `"}`, 200},
		{"DELETE", "/v1/action", "", 405},
		{"GET", "/unknown", "", 404},
		{"GET", "/v1/guest-status?vm=missing&runtime=old", "", 409},
		{"POST", "/v1/guest-status?vm=missing&runtime=old", "", 405},
		{"DELETE", "/v1/guest/file?vm=" + id, "", 405},
	}
	for _, test := range cases {
		t.Run(test.method+test.path+test.body, func(t *testing.T) {
			response := httptest.NewRecorder()
			service.ServeHTTP(response, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
			if response.Code != test.code {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
}

func TestPublicClientPropagatesErrorsAndWaitCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient(&http.Client{Transport: localTransport(func(r *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})})
	if _, err := client.Execute(canceled, Request{Action: "create"}); err == nil || !strings.Contains(err.Error(), "操作结果尚未确认") {
		t.Fatal(err)
	}
	_, client = fixture(t, 1)
	if err := client.WaitState(canceled, "missing", "start"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	client = NewClient(&http.Client{Transport: localTransport(func(r *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		response.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(response, `{"ok":false,"error":"specific failure"}`)
		return response.Result(), nil
	})})
	if _, err := client.Execute(context.Background(), Request{Action: "start"}); err == nil || err.Error() != "specific failure" {
		t.Fatal(err)
	}
}

func TestPreparedMediaRetainsPublishedAndReclaimsOnlyOrphans(t *testing.T) {
	service, _ := fixture(t, 1)
	cfg, _ := service.config()
	root := t.TempDir()
	orphan := filepath.Join(root, "orphan.iso")
	published := filepath.Join(root, "published.iso")
	unrelated := filepath.Join(root, "user.iso")
	for _, path := range []string{orphan, published, unrelated} {
		if err := os.WriteFile(path, []byte("iso"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	service.preparedMedia[orphan] = false
	service.preparedMedia[published] = false
	cfg.Machines[0].DriverISO = published
	service.cleanupMedia(cfg)
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan not reclaimed", err)
	}
	cfg.Machines[0].DriverISO = ""
	service.cleanupMedia(cfg)
	for _, path := range []string{published, unrelated} {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, []byte("iso")) {
			t.Fatal("deleted draft or user image", path, err)
		}
	}
}

func TestCopyFromRejectsTruncatedHTTPBodyWithoutReplacingFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = io.WriteString(w, "partial")
	}))
	defer server.Close()
	client := NewClient(&http.Client{Transport: localTransport(func(r *http.Request) (*http.Response, error) {
		request := r.Clone(r.Context())
		request.URL.Scheme = "http"
		request.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return server.Client().Transport.RoundTrip(request)
	})})
	defer client.Close()
	root := t.TempDir()
	target := filepath.Join(root, "existing.bin")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.CopyFrom(context.Background(), "guest", "/source.bin", target, true); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("truncated response accepted", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "original" {
		t.Fatal("existing destination changed", string(data), err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal("partial download leaked", entries, err)
	}
}

func TestMachineSelectorRejectsAmbiguousNames(t *testing.T) {
	first, second := NewMachine(), NewMachine()
	first.Name = "Same"
	second.Name = "same"
	if _, err := SelectMachine([]Machine{first, second}, "SAME"); err == nil {
		t.Fatal("ambiguous name accepted")
	}
	if got, err := SelectMachine([]Machine{first, second}, first.ID); err != nil || got.ID != first.ID {
		t.Fatal(got, err)
	}
}
