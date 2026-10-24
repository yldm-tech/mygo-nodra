package devtools

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed web/*
var web embed.FS

// Listen starts an authenticated inspector on an ephemeral IPv4 loopback port.
// The returned URL carries its capability token in the fragment (never sent in
// HTTP requests or referrers); the UI uses it as a bearer header for its API.
// Only one server can run per inspector. Close detaches stores and stops it.
func (i *Inspector) Listen() (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return "", ErrClosed
	}
	if i.server != nil {
		return "", errors.New("nodra devtools: already listening")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	i.server = &http.Server{Handler: i.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	i.done = make(chan struct{})
	go func() {
		defer close(i.done)
		_ = i.server.Serve(listener)
	}()
	return "http://" + listener.Addr().String() + "/#" + i.token, nil
}

// Handler serves the UI and authenticated API. Listen is the recommended entry
// point; custom servers must also bind only to loopback. Tokens are per inspector.
func (i *Inspector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "loopback host required", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "same origin required", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(supplied), []byte(i.token)) != 1 {
				http.Error(w, "inspector token required", http.StatusUnauthorized)
				return
			}
			i.serveAPI(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		assets := map[string]struct{ name, mime string }{
			"/":          {"web/index.html", "text/html; charset=utf-8"},
			"/app.js":    {"web/app.js", "text/javascript; charset=utf-8"},
			"/style.css": {"web/style.css", "text/css; charset=utf-8"},
		}
		asset, ok := assets[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, err := web.ReadFile(asset.name)
		if err != nil {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", asset.mime)
		_, _ = w.Write(data)
	})
}

func (i *Inspector) serveAPI(w http.ResponseWriter, r *http.Request) {
	method := http.MethodGet
	if r.URL.Path == "/api/restore" || r.URL.Path == "/api/clear" || r.URL.Path == "/api/capture" {
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/api/changes", "/api/events":
		values := r.URL.Query()
		parse := func(key string) (uint64, error) {
			if values.Get(key) == "" {
				return 0, nil
			}
			return strconv.ParseUint(values.Get(key), 10, 64)
		}
		revision, e1 := parse("revision")
		cursor, e2 := parse("cursor")
		before, e3 := parse("before")
		limit, e4 := parse("limit")
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || limit > 200 {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/changes" {
			_ = json.NewEncoder(w).Encode(i.Changes(revision, cursor, int(limit)))
		} else {
			_ = json.NewEncoder(w).Encode(i.EventsBefore(before, int(limit)))
		}
	case "/api/capture":
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		var policy struct {
			Paused           bool  `json:"paused"`
			SampleIntervalMS int64 `json:"sampleIntervalMS"`
		}
		if err != nil || decode(raw, &policy) != nil || policy.SampleIntervalMS < 0 || policy.SampleIntervalMS > 60000 {
			http.Error(w, "invalid capture policy", http.StatusBadRequest)
			return
		}
		if err := i.SetCapture(policy.Paused, time.Duration(policy.SampleIntervalMS)*time.Millisecond); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "/api/snapshot":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(i.Snapshot())
	case "/api/clear":
		i.Clear()
		w.WriteHeader(http.StatusNoContent)
	case "/api/restore":
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		var request struct {
			StoreID  string          `json:"storeId"`
			Sequence uint64          `json:"sequence"`
			State    json.RawMessage `json:"state"`
		}
		if err := decode(raw, &request); err != nil || (request.Sequence == 0) == (request.State == nil) {
			http.Error(w, "provide storeId and either sequence or state", http.StatusBadRequest)
			return
		}
		if request.Sequence != 0 {
			err = i.Restore(request.StoreID, request.Sequence)
		} else {
			err = i.Apply(request.StoreID, request.State)
		}
		if err != nil {
			status := http.StatusBadRequest
			switch {
			case errors.Is(err, ErrReadOnly):
				status = http.StatusForbidden
			case errors.Is(err, ErrNotFound):
				status = http.StatusNotFound
			case errors.Is(err, ErrClosed):
				status = http.StatusConflict
			}
			http.Error(w, fmt.Sprint(err), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}
