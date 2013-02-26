// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bufio"
	"compress/gzip"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"url"
)

// DefaultTransport is the default implementation of Transport and is
// used by DefaultClient.  It establishes a new network connection for
// each call to Do and uses HTTP proxies as directed by the
// $HTTP_PROXY and $NO_PROXY (or $http_proxy and $no_proxy)
// environment variables.
var DefaultTransport RoundTripper = &Transport{Proxy: ProxyFromEnvironment}

// DefaultMaxIdleConnsPerHost is the default value of Transport's
// MaxIdleConnsPerHost.
const DefaultMaxIdleConnsPerHost = 2

// Transport is an implementation of RoundTripper that supports http,
// https, and http proxies (for either http or https with CONNECT).
// Transport can also cache connections for future re-use.
type Transport struct {
	lk       sync.Mutex
	idleConn map[string][]*persistConn
	altProto map[string]RoundTripper // nil or map of URI scheme => RoundTripper

	// TODO: tunable on global max cached connections
	// TODO: tunable on timeout on cached connections
	// TODO: optional pipelining

	// Proxy specifies a function to return a proxy for a given
	// Request. If the function returns a non-nil error, the
	// request is aborted with the provided error.
	// If Proxy is nil or returns a nil *URL, no proxy is used.
	Proxy func(*Request) (*url.URL, os.Error)

	// Dial specifies the dial function for creating TCP
	// connections.
	// If Dial is nil, net.Dial is used.
	Dial func(net, addr string) (c net.Conn, err os.Error)

	DisableKeepAlives  bool
	DisableCompression bool

	// MaxIdleConnsPerHost, if non-zero, controls the maximum idle
	// (keep-alive) to keep to keep per-host.  If zero,
	// DefaultMaxIdleConnsPerHost is used.
	MaxIdleConnsPerHost int
}

// ProxyFromEnvironment returns the URL of the proxy to use for a
// given request, as indicated by the environment variables
// $HTTP_PROXY and $NO_PROXY (or $http_proxy and $no_proxy).
// Either URL or an error is returned.
func ProxyFromEnvironment(req *Request) (*url.URL, os.Error) {
	proxy := getenvEitherCase("HTTP_PROXY")
	if proxy == "" {
		return nil, nil
	}
	if !useProxy(canonicalAddr(req.URL)) {
		return nil, nil
	}
	proxyURL, err := url.ParseRequest(proxy)
	if err != nil {
		return nil, os.NewError("invalid proxy address")
	}
	if proxyURL.Host == "" {
		proxyURL, err = url.ParseRequest("http://" + proxy)
		if err != nil {
			return nil, os.NewError("invalid proxy address")
		}
	}
	return proxyURL, nil
}

// ProxyURL returns a proxy function (for use in a Transport)
// that always returns the same URL.
func ProxyURL(fixedURL *url.URL) func(*Request) (*url.URL, os.Error) {
	return func(*Request) (*url.URL, os.Error) {
		return fixedURL, nil
	}
}

// RoundTrip implements the RoundTripper interface.
func (t *Transport) RoundTrip(req *Request) (resp *Response, err os.Error) {
	if req.URL == nil {
		if req.URL, err = url.Parse(req.RawURL); err != nil {
			return
		}
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		t.lk.Lock()
		var rt RoundTripper
		if t.altProto != nil {
			rt = t.altProto[req.URL.Scheme]
		}
		t.lk.Unlock()
		if rt == nil {
			return nil, &badStringError{"unsupported protocol scheme", req.URL.Scheme}
		}
		return rt.RoundTrip(req)
	}

	cm, err := t.connectMethodForRequest(req)
	if err != nil {
		return nil, err
	}

	// Get the cached or newly-created connection to either the
	// host (for http or https), the http proxy, or the http proxy
	// pre-CONNECTed to https server.  In any case, we'll be ready
	// to send it requests.
	pconn, err := t.getConn(cm)
	if err != nil {
		return nil, err
	}

	return pconn.roundTrip(req)
}

// RegisterProtocol registers a new protocol with scheme.
// The Transport will pass requests using the given scheme to rt.
// It is rt's responsibility to simulate HTTP request semantics.
//
// RegisterProtocol can be used by other packages to provide
// implementations of protocol schemes like "ftp" or "file".
func (t *Transport) RegisterProtocol(scheme string, rt RoundTripper) {
	if scheme == "http" || scheme == "https" {
		panic("protocol " + scheme + " already registered")
	}
	t.lk.Lock()
	defer t.lk.Unlock()
	if t.altProto == nil {
		t.altProto = make(map[string]RoundTripper)
	}
	if _, exists := t.altProto[scheme]; exists {
		panic("protocol " + scheme + " already registered")
	}
	t.altProto[scheme] = rt
}

// CloseIdleConnections closes any connections which were previously
// connected from previous requests but are now sitting idle in
// a "keep-alive" state. It does not interrupt any connections currently
// in use.
func (t *Transport) CloseIdleConnections() {
	t.lk.Lock()
	defer t.lk.Unlock()
	if t.idleConn == nil {
		return
	}
	for _, conns := range t.idleConn {
		for _, pconn := range conns {
			pconn.close()
		}
	}
	t.idleConn = nil
}

//
// Private implementation past this point.
//

func getenvEitherCase(k string) string {
	if v := os.Getenv(strings.ToUpper(k)); v != "" {
		return v
	}
	return os.Getenv(strings.ToLower(k))
}

func (t *Transport) connectMethodForRequest(req *Request) (*connectMethod, os.Error) {
	cm := &connectMethod{
		targetScheme: req.URL.Scheme,
		targetAddr:   canonicalAddr(req.URL),
	}
	if t.Proxy != nil {
		var err os.Error
		cm.proxyURL, err = t.Proxy(req)
		if err != nil {
			return nil, err
		}
	}
	return cm, nil
}

// proxyAuth returns the Proxy-Authorization header to set
// on requests, if applicable.
func (cm *connectMethod) proxyAuth() string {
	if cm.proxyURL == nil {
		return ""
	}
	proxyInfo := cm.proxyURL.RawUserinfo
	if proxyInfo != "" {
		return "Basic " + base64.URLEncoding.EncodeToString([]byte(proxyInfo))
	}
	return ""
}

func (t *Transport) putIdleConn(pconn *persistConn) {
	t.lk.Lock()
	defer t.lk.Unlock()
	if t.DisableKeepAlives || t.MaxIdleConnsPerHost < 0 {
		pconn.close()
		return
	}
	if pconn.isBroken() {
		return
	}
	key := pconn.cacheKey
	max := t.MaxIdleConnsPerHost
	if max == 0 {
		max = DefaultMaxIdleConnsPerHost
	}
	if len(t.idleConn[key]) >= max {
		pconn.close()
		return
	}
	t.idleConn[key] = append(t.idleConn[key], pconn)
}

func (t *Transport) getIdleConn(cm *connectMethod) (pconn *persistConn) {
	t.lk.Lock()
	defer t.lk.Unlock()
	if t.idleConn == nil {
		t.idleConn = make(map[string][]*persistConn)
	}
	key := cm.String()
	for {
		pconns, ok := t.idleConn[key]
		if !ok {
			return nil
		}
		if len(pconns) == 1 {
			pconn = pconns[0]
			t.idleConn[key] = nil, false
		} else {
			// 2 or more cached connections; pop last
			// TODO: queue?
			pconn = pconns[len(pconns)-1]
			t.idleConn[key] = pconns[0 : len(pconns)-1]
		}
		if !pconn.isBroken() {
			return
		}
	}
	return
}

func (t *Transport) dial(network, addr string) (c net.Conn, err os.Error) {
	if t.Dial != nil {
		return t.Dial(network, addr)
	}
	return net.Dial(network, addr)
}

// getConn dials and creates a new persistConn to the target as
// specified in the connectMethod.  This includes doing a proxy CONNECT
// and/or setting up TLS.  If this doesn't return an error, the persistConn
// is ready to write requests to.
func (t *Transport) getConn(cm *connectMethod) (*persistConn, os.Error) {
	if pc := t.getIdleConn(cm); pc != nil {
		return pc, nil
	}

	conn, err := t.dial("tcp", cm.addr())
	if err != nil {
		if cm.proxyURL != nil {
			err = fmt.Errorf("http: error connecting to proxy %s: %v", cm.proxyURL, err)
		}
		return nil, err
	}

	pa := cm.proxyAuth()

	pconn := &persistConn{
		t:        t,
		cacheKey: cm.String(),
		conn:     conn,
		reqch:    make(chan requestAndChan, 50),
	}
	newClientConnFunc := NewClientConn

	switch {
	case cm.proxyURL == nil:
		// Do nothing.
	case cm.targetScheme == "http":
		newClientConnFunc = NewProxyClientConn
		if pa != "" {
			pconn.mutateRequestFunc = func(req *Request) {
				if req.Header == nil {
					req.Header = make(Header)
				}
				req.Header.Set("Proxy-Authorization", pa)
			}
		}
	case cm.targetScheme == "https":
		connectReq := &Request{
			Method: "CONNECT",
			RawURL: cm.targetAddr,
			Host:   cm.targetAddr,
			Header: make(Header),
		}
		if pa != "" {
			connectReq.Header.Set("Proxy-Authorization", pa)
		}
		connectReq.Write(conn)

		// Read response.
		// Okay to use and discard buffered reader here, because
		// TLS server will not speak until spoken to.
		br := bufio.NewReader(conn)
		resp, err := ReadResponse(br, connectReq)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if resp.StatusCode != 200 {
			f := strings.SplitN(resp.Status, " ", 2)
			conn.Close()
			return nil, os.NewError(f[1])
		}
	}

	if cm.targetScheme == "https" {
		// Initiate TLS and check remote host name against certificate.
		conn = tls.Client(conn, nil)
		if err = conn.(*tls.Conn).Handshake(); err != nil {
			return nil, err
		}
		if err = conn.(*tls.Conn).VerifyHostname(cm.tlsHost()); err != nil {
			return nil, err
		}
		pconn.conn = conn
	}

	pconn.br = bufio.NewReader(pconn.conn)
	pconn.cc = newClientConnFunc(conn, pconn.br)
	go pconn.readLoop()
	return pconn, nil
}

// useProxy returns true if requests to addr should use a proxy,
// according to the NO_PROXY or no_proxy environment variable.
// addr is always a canonicalAddr with a host and port.
func useProxy(addr string) bool {
	if len(addr) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return false
		}
	}

	no_proxy := getenvEitherCase("NO_PROXY")
	if no_proxy == "*" {
		return false
	}

	addr = strings.ToLower(strings.TrimSpace(addr))
	if hasPort(addr) {
		addr = addr[:strings.LastIndex(addr, ":")]
	}

	for _, p := range strings.Split(no_proxy, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if len(p) == 0 {
			continue
		}
		if hasPort(p) {
			p = p[:strings.LastIndex(p, ":")]
		}
		if addr == p || (p[0] == '.' && (strings.HasSuffix(addr, p) || addr == p[1:])) {
			return false
		}
	}
	return true
}

// connectMethod is the map key (in its String form) for keeping persistent
// TCP connections alive for subsequent HTTP requests.
//
// A connect method may be of the following types:
//
// Cache key form                Description
// -----------------             -------------------------
// ||http|foo.com                http directly to server, no proxy
// ||https|foo.com               https directly to server, no proxy
// http://proxy.com|https|foo.com  http to proxy, then CONNECT to foo.com
// http://proxy.com|http           http to proxy, http to anywhere after that
//
// Note: no support to https to the proxy yet.
//
type connectMethod struct {
	proxyURL     *url.URL // nil for no proxy, else full proxy URL
	targetScheme string   // "http" or "https"
	targetAddr   string   // Not used if proxy + http targetScheme (4th example in table)
}

func (ck *connectMethod) String() string {
	proxyStr := ""
	if ck.proxyURL != nil {
		proxyStr = ck.proxyURL.String()
	}
	return strings.Join([]string{proxyStr, ck.targetScheme, ck.targetAddr}, "|")
}

// addr returns the first hop "host:port" to which we need to TCP connect.
func (cm *connectMethod) addr() string {
	if cm.proxyURL != nil {
		return canonicalAddr(cm.proxyURL)
	}
	return cm.targetAddr
}

// tlsHost returns the host name to match against the peer's
// TLS certificate.
func (cm *connectMethod) tlsHost() string {
	h := cm.targetAddr
	if hasPort(h) {
		h = h[:strings.LastIndex(h, ":")]
	}
	return h
}

type readResult struct {
	res *Response // either res or err will be set
	err os.Error
}

type writeRequest struct {
	// Set by client (in pc.roundTrip)
	req   *Request
	resch chan *readResult

	// Set by writeLoop if an error writing headers.
	writeErr os.Error
}

// persistConn wraps a connection, usually a persistent one
// (but may be used for non-keep-alive requests as well)
type persistConn struct {
	t                 *Transport
	cacheKey          string // its connectMethod.String()
	conn              net.Conn
	cc                *ClientConn
	br                *bufio.Reader
	reqch             chan requestAndChan // written by roundTrip(); read by readLoop()
	mutateRequestFunc func(*Request)      // nil or func to modify each outbound request

	lk                   sync.Mutex // guards numExpectedResponses and broken
	numExpectedResponses int
	broken               bool // an error has happened on this connection; marked broken so it's not reused.
}

func (pc *persistConn) isBroken() bool {
	pc.lk.Lock()
	defer pc.lk.Unlock()
	return pc.broken
}

func (pc *persistConn) expectingResponse() bool {
	pc.lk.Lock()
	defer pc.lk.Unlock()
	return pc.numExpectedResponses > 0
}

func (pc *persistConn) readLoop() {
	alive := true
	for alive {
		pb, err := pc.br.Peek(1)
		if err != nil {
			if (err == os.EOF || err == os.EINVAL) && !pc.expectingResponse() {
				// Remote side closed on us.  (We probably hit their
				// max idle timeout)
				pc.close()
				return
			}
		}
		if !pc.expectingResponse() {
			log.Printf("Unsolicited response received on idle HTTP channel starting with %q; err=%v",
				string(pb), err)
			pc.close()
			return
		}

		rc := <-pc.reqch
		resp, err := pc.cc.readUsing(rc.req, func(buf *bufio.Reader, forReq *Request) (*Response, os.Error) {
			resp, err := ReadResponse(buf, forReq)
			if err != nil || resp.ContentLength == 0 {
				return resp, err
			}
			if rc.addedGzip {
				forReq.Header.Del("Accept-Encoding")
			}
			if rc.addedGzip && resp.Header.Get("Content-Encoding") == "gzip" {
				resp.Header.Del("Content-Encoding")
				resp.Header.Del("Content-Length")
				resp.ContentLength = -1
				gzReader, err := gzip.NewReader(resp.Body)
				if err != nil {
					pc.close()
					return nil, err
				}
				resp.Body = &readFirstCloseBoth{&discardOnCloseReadCloser{gzReader}, resp.Body}
			}
			resp.Body = &bodyEOFSignal{body: resp.Body}
			return resp, err
		})

		if err == ErrPersistEOF {
			// Succeeded, but we can't send any more
			// persistent connections on this again.  We
			// hide this error to upstream callers.
			alive = false
			err = nil
		} else if err != nil || rc.req.Close {
			alive = false
		}

		hasBody := resp != nil && resp.ContentLength != 0
		var waitForBodyRead chan bool
		if alive {
			if hasBody {
				waitForBodyRead = make(chan bool)
				resp.Body.(*bodyEOFSignal).fn = func() {
					pc.t.putIdleConn(pc)
					waitForBodyRead <- true
				}
			} else {
				// When there's no response body, we immediately
				// reuse the TCP connection (putIdleConn), but
				// we need to prevent ClientConn.Read from
				// closing the Response.Body on the next
				// loop, otherwise it might close the body
				// before the client code has had a chance to
				// read it (even though it'll just be 0, EOF).
				pc.cc.lk.Lock()
				pc.cc.lastbody = nil
				pc.cc.lk.Unlock()

				pc.t.putIdleConn(pc)
			}
		}

		rc.ch <- responseAndError{resp, err}

		// Wait for the just-returned response body to be fully consumed
		// before we race and peek on the underlying bufio reader.
		if waitForBodyRead != nil {
			<-waitForBodyRead
		}
	}
}

type responseAndError struct {
	res *Response
	err os.Error
}

type requestAndChan struct {
	req *Request
	ch  chan responseAndError

	// did the Transport (as opposed to the client code) add an
	// Accept-Encoding gzip header? only if it we set it do
	// we transparently decode the gzip.
	addedGzip bool
}

func (pc *persistConn) roundTrip(req *Request) (resp *Response, err os.Error) {
	if pc.mutateRequestFunc != nil {
		pc.mutateRequestFunc(req)
	}

	// Ask for a compressed version if the caller didn't set their
	// own value for Accept-Encoding. We only attempted to
	// uncompress the gzip stream if we were the layer that
	// requested it.
	requestedGzip := false
	if !pc.t.DisableCompression && req.Header.Get("Accept-Encoding") == "" {
		// Request gzip only, not deflate. Deflate is ambiguous and 
		// as universally supported anyway.
		// See: http://www.gzip.org/zlib/zlib_faq.html#faq38
		requestedGzip = true
		req.Header.Set("Accept-Encoding", "gzip")
	}

	pc.lk.Lock()
	pc.numExpectedResponses++
	pc.lk.Unlock()

	err = pc.cc.Write(req)
	if err != nil {
		pc.close()
		return
	}

	ch := make(chan responseAndError, 1)
	pc.reqch <- requestAndChan{req, ch, requestedGzip}
	re := <-ch
	pc.lk.Lock()
	pc.numExpectedResponses--
	pc.lk.Unlock()

	return re.res, re.err
}

func (pc *persistConn) close() {
	pc.lk.Lock()
	defer pc.lk.Unlock()
	pc.broken = true
	pc.cc.Close()
	pc.conn.Close()
	pc.mutateRequestFunc = nil
}

var portMap = map[string]string{
	"http":  "80",
	"https": "443",
}

// canonicalAddr returns url.Host but always with a ":port" suffix
func canonicalAddr(url *url.URL) string {
	addr := url.Host
	if !hasPort(addr) {
		return addr + ":" + portMap[url.Scheme]
	}
	return addr
}

func responseIsKeepAlive(res *Response) bool {
	// TODO: implement.  for now just always shutting down the connection.
	return false
}

// bodyEOFSignal wraps a ReadCloser but runs fn (if non-nil) at most
// once, right before the final Read() or Close() call returns, but after
// EOF has been seen.
type bodyEOFSignal struct {
	body     io.ReadCloser
	fn       func()
	isClosed bool
}

func (es *bodyEOFSignal) Read(p []byte) (n int, err os.Error) {
	n, err = es.body.Read(p)
	if es.isClosed && n > 0 {
		panic("http: unexpected bodyEOFSignal Read after Close; see issue 1725")
	}
	if err == os.EOF && es.fn != nil {
		es.fn()
		es.fn = nil
	}
	return
}

func (es *bodyEOFSignal) Close() (err os.Error) {
	if es.isClosed {
		return nil
	}
	es.isClosed = true
	err = es.body.Close()
	if err == nil && es.fn != nil {
		es.fn()
		es.fn = nil
	}
	return
}

type readFirstCloseBoth struct {
	io.ReadCloser
	io.Closer
}

func (r *readFirstCloseBoth) Close() os.Error {
	if err := r.ReadCloser.Close(); err != nil {
		r.Closer.Close()
		return err
	}
	if err := r.Closer.Close(); err != nil {
		return err
	}
	return nil
}

// discardOnCloseReadCloser consumes all its input on Close.
type discardOnCloseReadCloser struct {
	io.ReadCloser
}

func (d *discardOnCloseReadCloser) Close() os.Error {
	io.Copy(ioutil.Discard, d.ReadCloser) // ignore errors; likely invalid or already closed
	return d.ReadCloser.Close()
}
