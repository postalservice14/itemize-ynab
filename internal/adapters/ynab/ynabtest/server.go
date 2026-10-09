// Package ynabtest is an in-process fake YNAB API server for tests. It never
// talks to the network: tests script responses per route and then assert on the
// requests the code under test sent, including how many write calls it made.
package ynabtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Request is one request the fake server received.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

// IsWrite reports whether the request mutates YNAB state.
func (r Request) IsWrite() bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// Response is a scripted reply.
type Response struct {
	Status int
	// Body is JSON-encoded unless it is a string or []byte, which is sent as is.
	Body   any
	Header http.Header
}

// Server records every request and replies from scripted routes.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	routes   map[string][]Response
	requests []Request
}

// New starts a fake server and registers its shutdown with t.Cleanup.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{routes: map[string][]Response{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL to hand to the client under test; it includes the /v1
// prefix the real API uses.
func (s *Server) URL() string { return s.srv.URL + "/v1" }

// On scripts the replies for "METHOD /path" (the path excludes the /v1 prefix
// and the query string). Replies are served in order and the last one repeats,
// so a single Response is a constant route and several model a sequence such
// as "200, then 429".
func (s *Server) On(method, path string, responses ...Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[method+" "+path] = append([]Response(nil), responses...)
}

// Requests returns a copy of every request received so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Calls returns the requests matching method and path.
func (s *Server) Calls(method, path string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// WriteCount is the number of mutating requests received.
func (s *Server) WriteCount() int {
	n := 0
	for _, r := range s.Requests() {
		if r.IsWrite() {
			n++
		}
	}
	return n
}

// Reset forgets recorded requests but keeps the scripted routes.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/v1")

	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method: r.Method,
		Path:   path,
		Query:  r.URL.Query(),
		Header: r.Header.Clone(),
		Body:   body,
	})
	key := r.Method + " " + path
	resp, ok := s.next(key)
	s.mu.Unlock()

	if !ok {
		resp = APIError(http.StatusNotFound, "404.2", "resource_not_found", "ynabtest: no route scripted for "+key)
	}
	write(w, resp)
}

// next pops the next scripted response for key; the last one is kept.
func (s *Server) next(key string) (Response, bool) {
	queue := s.routes[key]
	if len(queue) == 0 {
		return Response{}, false
	}
	resp := queue[0]
	if len(queue) > 1 {
		s.routes[key] = queue[1:]
	}
	return resp, true
}

func write(w http.ResponseWriter, resp Response) {
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	var payload []byte
	switch b := resp.Body.(type) {
	case nil:
	case string:
		payload = []byte(b)
	case []byte:
		payload = b
	default:
		var err error
		payload, err = json.Marshal(b)
		if err != nil {
			http.Error(w, "ynabtest: cannot encode body: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(payload)
}
