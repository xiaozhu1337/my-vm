package sharing

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDAVVirtualParentsAndShareRoots(t *testing.T) {
	handler, closeRoots, err := Open([]Share{{Name: "Shared1", Path: t.TempDir(), ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRoots()
	mux := http.NewServeMux()
	mux.Handle("/", handler)
	for _, path := range []string{"/", "/dav", "/dav/", "/dav/Shared1", "/dav/Shared1/"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest("OPTIONS", path, nil))
			if response.Code != 200 || response.Header().Get("DAV") == "" || response.Header().Get("MS-Author-Via") != "DAV" {
				t.Fatalf("OPTIONS: %d %v", response.Code, response.Header())
			}
			for _, depth := range []string{"0", "1"} {
				response = httptest.NewRecorder()
				request := httptest.NewRequest("PROPFIND", path, nil)
				request.Header.Set("Depth", depth)
				mux.ServeHTTP(response, request)
				if response.Code != 207 {
					t.Fatalf("PROPFIND depth %s: %d %s", depth, response.Code, response.Body.String())
				}
				var result struct {
					Responses []struct {
						Href  string `xml:"href"`
						Props []struct {
							Type struct {
								Collection *struct{} `xml:"collection"`
							} `xml:"prop>resourcetype"`
						} `xml:"propstat"`
					} `xml:"response"`
				}
				if err := xml.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Responses) == 0 || result.Responses[0].Href == "" || len(result.Responses[0].Props) == 0 || result.Responses[0].Props[0].Type.Collection == nil {
					t.Fatalf("missing DAV collection: %s", response.Body.String())
				}
				if depth == "0" && len(result.Responses) != 1 {
					t.Fatal("depth zero returned children")
				}
				if depth == "1" && path == "/dav/" && !strings.Contains(response.Body.String(), "/dav/Shared1") {
					t.Fatal("share absent from parent listing")
				}
			}
		})
	}
	for _, method := range []string{"PUT", "DELETE", "MOVE", "MKCOL"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(method, "/dav/", nil))
		if response.Code != 405 {
			t.Fatalf("virtual parent mutation %s: %d", method, response.Code)
		}
	}
}
