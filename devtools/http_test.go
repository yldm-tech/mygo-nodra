package devtools

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yldm-tech/nodra"
)

func request(i *Inspector, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:12345"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+i.token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	i.Handler().ServeHTTP(w, r)
	return w
}

func TestHTTPInspectionExportRestoreAndClear(t *testing.T) {
	i := New(Options{AllowRestore: true})
	defer func() { _ = i.Close() }()
	store := nodra.New(counterState{})
	mustAttach(t, i, "counter", store)
	response := request(i, "GET", "/api/snapshot", "")
	if response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	var snapshot Snapshot
	mustUpdate(t, json.Unmarshal(response.Body.Bytes(), &snapshot))
	if len(snapshot.Stores) != 1 || snapshot.Format != 1 {
		t.Fatalf("export = %+v", snapshot)
	}
	response = request(i, "POST", "/api/restore", `{"storeId":"counter","state":{"count":7}}`)
	if response.Code != http.StatusNoContent || store.Get().Count != 7 {
		t.Fatalf("apply = %d %s", response.Code, response.Body.String())
	}
	response = request(i, "POST", "/api/restore", `{"storeId":"counter","sequence":1}`)
	if response.Code != http.StatusNoContent || store.Get().Count != 0 {
		t.Fatalf("restore = %d %s", response.Code, response.Body.String())
	}
	if response := request(i, "POST", "/api/clear", "{}"); response.Code != http.StatusNoContent || len(i.Snapshot().Events) != 0 {
		t.Fatal("timeline did not clear")
	}
}

func TestHTTPRejectsUnauthorizedAndCrossOriginRequests(t *testing.T) {
	i := New(Options{})
	defer func() { _ = i.Close() }()
	for _, test := range []struct {
		name, host, origin, auth string
		status                   int
	}{
		{"missing-token", "127.0.0.1:12345", "", "", http.StatusUnauthorized},
		{"wrong-token", "127.0.0.1:12345", "", "Bearer wrong", http.StatusUnauthorized},
		{"missing-bearer", "127.0.0.1:12345", "", i.token, http.StatusUnauthorized},
		{"dns-rebinding", "attacker.invalid:12345", "", "Bearer " + i.token, http.StatusForbidden},
		{"cross-origin", "127.0.0.1:12345", "https://attacker.invalid", "Bearer " + i.token, http.StatusForbidden},
		{"same-origin", "127.0.0.1:12345", "http://127.0.0.1:12345", "Bearer " + i.token, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://127.0.0.1:12345/api/snapshot", nil)
			r.Host = test.host
			r.Header.Set("Authorization", test.auth)
			r.Header.Set("Origin", test.origin)
			w := httptest.NewRecorder()
			i.Handler().ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d", w.Code, test.status)
			}
		})
	}
}

func TestHTTPRejectsInvalidRestoresAndMethods(t *testing.T) {
	i := New(Options{AllowRestore: true})
	defer func() { _ = i.Close() }()
	store := nodra.New(counterState{})
	mustAttach(t, i, "counter", store)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/restore", "", http.StatusMethodNotAllowed},
		{"POST", "/api/snapshot", "{}", http.StatusMethodNotAllowed},
		{"GET", "/api/unknown", "", http.StatusNotFound},
		{"POST", "/api/restore", `{}`, http.StatusBadRequest},
		{"POST", "/api/restore", `{"storeId":"counter","state":{},"sequence":1}`, http.StatusBadRequest},
		{"POST", "/api/restore", `{"storeId":"counter","state":{"unknown":4}}`, http.StatusBadRequest},
		{"POST", "/api/restore", `{"storeId":"counter","state":{"count":"wrong"}}`, http.StatusBadRequest},
		{"POST", "/api/restore", `{"storeId":"counter","sequence":999}`, http.StatusNotFound},
		{"POST", "/api/restore", `{"storeId":"missing","state":{}}`, http.StatusNotFound},
		{"POST", "/api/restore", strings.Repeat("x", (1<<20)+1), http.StatusRequestEntityTooLarge},
	} {
		if w := request(i, test.method, test.path, test.body); w.Code != test.status {
			t.Errorf("%s %s status = %d, want %d", test.method, test.path, w.Code, test.status)
		}
	}
	if store.Get().Count != 0 || store.Version() != 0 {
		t.Fatal("invalid HTTP restore modified state")
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/restore", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+i.token)
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	i.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatal(w.Code)
	}
	readonly := New(Options{})
	defer func() { _ = readonly.Close() }()
	mustAttach(t, readonly, "counter", store)
	if w := request(readonly, "POST", "/api/restore", `{"storeId":"counter","state":{}}`); w.Code != http.StatusForbidden {
		t.Fatal("HTTP restore is not opt-in")
	}
}

func TestEmbeddedAssetsAndLoopbackServerLifecycle(t *testing.T) {
	i := New(Options{})
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		w := request(i, "GET", path, "")
		if w.Code != http.StatusOK || w.Body.Len() == 0 || w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("asset %s unavailable or missing headers: %d", path, w.Code)
		}
	}
	fullURL, err := i.Listen()
	mustUpdate(t, err)
	u, err := url.Parse(fullURL)
	mustUpdate(t, err)
	if u.Hostname() != "127.0.0.1" || u.Fragment == "" || u.RawQuery != "" {
		t.Fatal("server is not a loopback capability URL")
	}
	if _, err := i.Listen(); err == nil {
		t.Fatal("started a duplicate server")
	}
	u.Path, u.Fragment = "/api/snapshot", ""
	r, err := http.NewRequest("GET", u.String(), nil)
	mustUpdate(t, err)
	r.Header.Set("Authorization", "Bearer "+i.token)
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(r)
	mustUpdate(t, err)
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode)
	}
	mustUpdate(t, i.Close())
	if response, err := client.Do(r); err == nil {
		_ = response.Body.Close()
		t.Fatal("server remained reachable after Close")
	}
	if _, err := i.Listen(); err != ErrClosed {
		t.Fatalf("Listen after close = %v", err)
	}
}
