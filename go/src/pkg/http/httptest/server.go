// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Implementation of Server

package httptest

import (
	"crypto/rand"
	"crypto/tls"
	"flag"
	"fmt"
	"http"
	"net"
	"os"
	"time"
)

// A Server is an HTTP server listening on a system-chosen port on the
// local loopback interface, for use in end-to-end HTTP tests.
type Server struct {
	URL      string // base URL of form http://ipaddr:port with no trailing slash
	Listener net.Listener
	TLS      *tls.Config // nil if not using using TLS
}

// historyListener keeps track of all connections that it's ever
// accepted.
type historyListener struct {
	net.Listener
	history []net.Conn
}

func (hs *historyListener) Accept() (c net.Conn, err os.Error) {
	c, err = hs.Listener.Accept()
	if err == nil {
		hs.history = append(hs.history, c)
	}
	return
}

func newLocalListener() net.Listener {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if l, err = net.Listen("tcp6", "[::1]:0"); err != nil {
			panic(fmt.Sprintf("httptest: failed to listen on a port: %v", err))
		}
	}
	return l
}

// When debugging a particular http server-based test,
// this flag lets you run
//	gotest -run=BrokenTest -httptest.serve=127.0.0.1:8000
// to start the broken server so you can interact with it manually.
var serve = flag.String("httptest.serve", "", "if non-empty, httptest.NewServer serves on this address and blocks")

// NewServer starts and returns a new Server.
// The caller should call Close when finished, to shut it down.
func NewServer(handler http.Handler) *Server {
	ts := new(Server)
	var l net.Listener
	if *serve != "" {
		var err os.Error
		l, err = net.Listen("tcp", *serve)
		if err != nil {
			panic(fmt.Sprintf("httptest: failed to listen on %v: %v", *serve, err))
		}
	} else {
		l = newLocalListener()
	}
	ts.Listener = &historyListener{l, make([]net.Conn, 0)}
	ts.URL = "http://" + l.Addr().String()
	server := &http.Server{Handler: handler}
	go server.Serve(ts.Listener)
	if *serve != "" {
		fmt.Println(os.Stderr, "httptest: serving on", ts.URL)
		select {}
	}
	return ts
}

// NewTLSServer starts and returns a new Server using TLS.
// The caller should call Close when finished, to shut it down.
func NewTLSServer(handler http.Handler) *Server {
	l := newLocalListener()
	ts := new(Server)

	cert, err := tls.X509KeyPair(localhostCert, localhostKey)
	if err != nil {
		panic(fmt.Sprintf("httptest: NewTLSServer: %v", err))
	}

	ts.TLS = &tls.Config{
		Rand:         rand.Reader,
		Time:         time.Seconds,
		NextProtos:   []string{"http/1.1"},
		Certificates: []tls.Certificate{cert},
	}
	tlsListener := tls.NewListener(l, ts.TLS)

	ts.Listener = &historyListener{tlsListener, make([]net.Conn, 0)}
	ts.URL = "https://" + l.Addr().String()
	server := &http.Server{Handler: handler}
	go server.Serve(ts.Listener)
	return ts
}

// Close shuts down the server.
func (s *Server) Close() {
	s.Listener.Close()
}

// CloseClientConnections closes any currently open HTTP connections
// to the test Server.
func (s *Server) CloseClientConnections() {
	hl, ok := s.Listener.(*historyListener)
	if !ok {
		return
	}
	for _, conn := range hl.history {
		conn.Close()
	}
}

// localhostCert is a PEM-encoded TLS cert with SAN DNS names
// "127.0.0.1" and "[::1]", expiring at the last second of 2049 (the end
// of ASN.1 time).
var localhostCert = []byte(`-----BEGIN CERTIFICATE-----
MIIBOTCB5qADAgECAgEAMAsGCSqGSIb3DQEBBTAAMB4XDTcwMDEwMTAwMDAwMFoX
DTQ5MTIzMTIzNTk1OVowADBaMAsGCSqGSIb3DQEBAQNLADBIAkEAsuA5mAFMj6Q7
qoBzcvKzIq4kzuT5epSp2AkcQfyBHm7K13Ws7u+0b5Vb9gqTf5cAiIKcrtrXVqkL
8i1UQF6AzwIDAQABo08wTTAOBgNVHQ8BAf8EBAMCACQwDQYDVR0OBAYEBAECAwQw
DwYDVR0jBAgwBoAEAQIDBDAbBgNVHREEFDASggkxMjcuMC4wLjGCBVs6OjFdMAsG
CSqGSIb3DQEBBQNBAJH30zjLWRztrWpOCgJL8RQWLaKzhK79pVhAx6q/3NrF16C7
+l1BRZstTwIGdoGId8BRpErK1TXkniFb95ZMynM=
-----END CERTIFICATE-----
`)

// localhostKey is the private key for localhostCert.
var localhostKey = []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIBPQIBAAJBALLgOZgBTI+kO6qAc3LysyKuJM7k+XqUqdgJHEH8gR5uytd1rO7v
tG+VW/YKk3+XAIiCnK7a11apC/ItVEBegM8CAwEAAQJBAI5sxq7naeR9ahyqRkJi
SIv2iMxLuPEHaezf5CYOPWjSjBPyVhyRevkhtqEjF/WkgL7C2nWpYHsUcBDBQVF0
3KECIQDtEGB2ulnkZAahl3WuJziXGLB+p8Wgx7wzSM6bHu1c6QIhAMEp++CaS+SJ
/TrU0zwY/fW4SvQeb49BPZUF3oqR8Xz3AiEA1rAJHBzBgdOQKdE3ksMUPcnvNJSN
poCcELmz2clVXtkCIQCLytuLV38XHToTipR4yMl6O+6arzAjZ56uq7m7ZRV0TwIh
AM65XAOw8Dsg9Kq78aYXiOEDc5DL0sbFUu/SlmRcCg93
-----END RSA PRIVATE KEY-----
`)
