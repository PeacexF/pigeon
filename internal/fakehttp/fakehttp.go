// Package fakehttp is a recording HTTP server for channel adapter tests.
package fakehttp

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Request is a request the server received.
type Request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// Server records every request and answers with the configured reply.
type Server struct {
	*httptest.Server

	mu     sync.Mutex
	reqs   []Request
	status int
	body   string
	header http.Header
}

// New starts a server replying status and body; it is closed when t ends.
func New(t testing.TB, status int, body string) *Server {
	t.Helper()
	s := &Server{status: status, body: body, header: http.Header{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.reqs = append(s.reqs, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
	status, reply := s.status, s.body
	maps.Copy(w.Header(), s.header)
	s.mu.Unlock()
	w.WriteHeader(status)
	_, _ = io.WriteString(w, reply)
}

// Reply changes the reply, with header given as key, value pairs.
func (s *Server) Reply(status int, body string, header ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body, s.header = status, body, http.Header{}
	for i := 0; i+1 < len(header); i += 2 {
		s.header.Add(header[i], header[i+1])
	}
}

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// Last returns the most recent request; it fails t if there was none.
func (s *Server) Last(t testing.TB) Request {
	t.Helper()
	reqs := s.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request received")
	}
	return reqs[len(reqs)-1]
}

// LastJSON decodes the most recent request body into a map.
func (s *Server) LastJSON(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(s.Last(t).Body, &m); err != nil {
		t.Fatalf("request body is not a JSON object: %v", err)
	}
	return m
}
