package guestops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusProtocol(t *testing.T) {
	expected := Status{Protocol: 1, OS: "windows", Desktop: DesktopStatus{Mode: "windows", State: "available", Reason: "service_trigger_start"}}
	mux := handler(func(ctx context.Context) Status {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing deadline")
		}
		return expected
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := &http.Client{Transport: statusTransport{base: server.URL}}
	actual, err := ReadStatus(context.Background(), client)
	if err != nil || actual != expected {
		t.Fatalf("status round trip: %+v %v", actual, err)
	}
	request := httptest.NewRequest("POST", "/v1/status", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != 405 {
		t.Fatal("status accepts mutations")
	}
	response = httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest("GET", "/v1/status", nil))
	if response.Code != 501 {
		t.Fatal("legacy provider falsely advertised readiness")
	}
}

type statusTransport struct{ base string }

func (s statusTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	url := *r.URL
	copy.URL = &url
	copy.URL.Scheme = "http"
	copy.URL.Host = strings.TrimPrefix(s.base, "http://")
	return http.DefaultTransport.RoundTrip(copy)
}

func TestStatusProtocolRejectsInvalidAndBoundsResponses(t *testing.T) {
	for _, mutate := range []func(*Status){func(s *Status) { s.Protocol = 2 }, func(s *Status) { s.OS = "other" }, func(s *Status) { s.Desktop.State = "okay" }, func(s *Status) { s.Desktop.Mode = "wayland" }, func(s *Status) { s.Desktop.Reason = "password secret" }, func(s *Status) { s.Desktop.DynamicResolution = true }} {
		status := Status{Protocol: 1, OS: "windows", Desktop: DesktopStatus{Mode: "windows", State: "ready", Reason: "services_running"}}
		mutate(&status)
		if status.Validate() == nil {
			t.Fatalf("accepted invalid status: %+v", status)
		}
	}
	for _, data := range []string{`{"protocol":2}`, strings.Repeat(" ", 4097), `{"protocol":1,"os":"windows","desktop":{"mode":"windows","state":"ready","reason":"services_running"}} {}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(data)) }))
		_, err := ReadStatus(context.Background(), &http.Client{Transport: statusTransport{base: server.URL}})
		server.Close()
		if err == nil {
			t.Fatal("invalid status body accepted")
		}
	}
	response := httptest.NewRecorder()
	statusHandler(func(context.Context) Status { return Status{Protocol: 100} })(response, httptest.NewRequest("GET", "/v1/status", nil))
	var data map[string]any
	json.Unmarshal(response.Body.Bytes(), &data)
	if response.Code != 503 {
		t.Fatal("invalid provider status published")
	}
}
