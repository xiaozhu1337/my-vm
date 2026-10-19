package sharing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"my-vm/internal/rdpauth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInstallCredentialHandoffValidatesAndErases(t *testing.T) {
	login := &rdpauth.Login{Username: "myvm", Password: []byte("fixture-only-secret")}
	defer login.Clear()
	var retained *rdpauth.Login
	calls := 0
	handler := credentialHandler(func(got *rdpauth.Login) error {
		calls++
		if got.Username != login.Username || !bytes.Equal(got.Password, login.Password) {
			t.Fatal("wrong credentials")
		}
		retained = got
		return nil
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Transport: testCredentialTransport{destination: server.URL, transport: http.DefaultTransport}}
	if err := publishLogin(context.Background(), client, login); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || retained == nil || retained.Password != nil {
		t.Fatal("secret retained by host handler")
	}
	valid, _ := json.Marshal(login)
	for _, body := range []string{"{}", string(valid) + "{}", `{"username":"myvm","password":"eA==","vm":"other"}`, strings.Repeat("x", 5000)} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/rdp-credentials", strings.NewReader(body)))
		if response.Code != 400 || strings.Contains(response.Body.String(), string(login.Password)) {
			t.Fatal("invalid handoff accepted or secret echoed", response.Code)
		}
	}
	if calls != 1 {
		t.Fatal("invalid request changed a VM credential")
	}
	for _, save := range []func(*rdpauth.Login) error{nil, func(*rdpauth.Login) error { return errors.New("fixture-only-secret") }} {
		response := httptest.NewRecorder()
		credentialHandler(save).ServeHTTP(response, httptest.NewRequest("POST", "/rdp-credentials", bytes.NewReader(valid)))
		if response.Code == 204 || strings.Contains(response.Body.String(), string(login.Password)) {
			t.Fatal("store failure hidden or secret leaked")
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/rdp-credentials", nil))
	if response.Code != 405 {
		t.Fatal("credentials exposed for reads")
	}
}

type testCredentialTransport struct {
	destination string
	transport   http.RoundTripper
}

func (t testCredentialTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	parsed := httptest.NewRequest("GET", t.destination, nil).URL
	copy.URL.Host = parsed.Host
	copy.URL.Scheme = parsed.Scheme
	return t.transport.RoundTrip(copy)
}
