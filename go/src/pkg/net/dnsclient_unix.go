// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// DNS client: see RFC 1035.
// Has to be linked into package net for Dial.

// TODO(rsc):
//	Check periodically whether /etc/resolv.conf has changed.
//	Could potentially handle many outstanding lookups faster.
//	Could have a small cache.
//	Random UDP source port (net.Dial should do that for us).
//	Random request IDs.

package net

import (
	"os"
	"rand"
	"sync"
	"time"
)

// Send a request on the connection and hope for a reply.
// Up to cfg.attempts attempts.
func exchange(cfg *dnsConfig, c Conn, name string, qtype uint16) (*dnsMsg, os.Error) {
	if len(name) >= 256 {
		return nil, &DNSError{Error: "name too long", Name: name}
	}
	out := new(dnsMsg)
	out.id = uint16(rand.Int()) ^ uint16(time.Nanoseconds())
	out.question = []dnsQuestion{
		{name, qtype, dnsClassINET},
	}
	out.recursion_desired = true
	msg, ok := out.Pack()
	if !ok {
		return nil, &DNSError{Error: "internal error - cannot pack message", Name: name}
	}

	for attempt := 0; attempt < cfg.attempts; attempt++ {
		n, err := c.Write(msg)
		if err != nil {
			return nil, err
		}

		c.SetReadTimeout(int64(cfg.timeout) * 1e9) // nanoseconds

		buf := make([]byte, 2000) // More than enough.
		n, err = c.Read(buf)
		if err != nil {
			if e, ok := err.(Error); ok && e.Timeout() {
				continue
			}
			return nil, err
		}
		buf = buf[0:n]
		in := new(dnsMsg)
		if !in.Unpack(buf) || in.id != out.id {
			continue
		}
		return in, nil
	}
	var server string
	if a := c.RemoteAddr(); a != nil {
		server = a.String()
	}
	return nil, &DNSError{Error: "no answer from server", Name: name, Server: server, IsTimeout: true}
}

// Do a lookup for a single name, which must be rooted
// (otherwise answer will not find the answers).
func tryOneName(cfg *dnsConfig, name string, qtype uint16) (cname string, addrs []dnsRR, err os.Error) {
	if len(cfg.servers) == 0 {
		return "", nil, &DNSError{Error: "no DNS servers", Name: name}
	}
	for i := 0; i < len(cfg.servers); i++ {
		// Calling Dial here is scary -- we have to be sure
		// not to dial a name that will require a DNS lookup,
		// or Dial will call back here to translate it.
		// The DNS config parser has already checked that
		// all the cfg.servers[i] are IP addresses, which
		// Dial will use without a DNS lookup.
		server := cfg.servers[i] + ":53"
		c, cerr := Dial("udp", server)
		if cerr != nil {
			err = cerr
			continue
		}
		msg, merr := exchange(cfg, c, name, qtype)
		c.Close()
		if merr != nil {
			err = merr
			continue
		}
		cname, addrs, err = answer(name, server, msg, qtype)
		if err == nil || err.(*DNSError).Error == noSuchHost {
			break
		}
	}
	return
}

func convertRR_A(records []dnsRR) []IP {
	addrs := make([]IP, len(records))
	for i, rr := range records {
		a := rr.(*dnsRR_A).A
		addrs[i] = IPv4(byte(a>>24), byte(a>>16), byte(a>>8), byte(a))
	}
	return addrs
}

func convertRR_AAAA(records []dnsRR) []IP {
	addrs := make([]IP, len(records))
	for i, rr := range records {
		a := make(IP, 16)
		copy(a, rr.(*dnsRR_AAAA).AAAA[:])
		addrs[i] = a
	}
	return addrs
}

var cfg *dnsConfig
var dnserr os.Error

func loadConfig() { cfg, dnserr = dnsReadConfig() }

var onceLoadConfig sync.Once

func lookup(name string, qtype uint16) (cname string, addrs []dnsRR, err os.Error) {
	if !isDomainName(name) {
		return name, nil, &DNSError{Error: "invalid domain name", Name: name}
	}
	onceLoadConfig.Do(loadConfig)
	if dnserr != nil || cfg == nil {
		err = dnserr
		return
	}
	// If name is rooted (trailing dot) or has enough dots,
	// try it by itself first.
	rooted := len(name) > 0 && name[len(name)-1] == '.'
	if rooted || count(name, '.') >= cfg.ndots {
		rname := name
		if !rooted {
			rname += "."
		}
		// Can try as ordinary name.
		cname, addrs, err = tryOneName(cfg, rname, qtype)
		if err == nil {
			return
		}
	}
	if rooted {
		return
	}

	// Otherwise, try suffixes.
	for i := 0; i < len(cfg.search); i++ {
		rname := name + "." + cfg.search[i]
		if rname[len(rname)-1] != '.' {
			rname += "."
		}
		cname, addrs, err = tryOneName(cfg, rname, qtype)
		if err == nil {
			return
		}
	}

	// Last ditch effort: try unsuffixed.
	rname := name
	if !rooted {
		rname += "."
	}
	cname, addrs, err = tryOneName(cfg, rname, qtype)
	if err == nil {
		return
	}
	return
}

// goLookupHost is the native Go implementation of LookupHost.
// Used only if cgoLookupHost refuses to handle the request
// (that is, only if cgoLookupHost is the stub in cgo_stub.go).
// Normally we let cgo use the C library resolver instead of
// depending on our lookup code, so that Go and C get the same
// answers.
func goLookupHost(name string) (addrs []string, err os.Error) {
	// Use entries from /etc/hosts if they match.
	addrs = lookupStaticHost(name)
	if len(addrs) > 0 {
		return
	}
	onceLoadConfig.Do(loadConfig)
	if dnserr != nil || cfg == nil {
		err = dnserr
		return
	}
	ips, err := goLookupIP(name)
	if err != nil {
		return
	}
	addrs = make([]string, 0, len(ips))
	for _, ip := range ips {
		addrs = append(addrs, ip.String())
	}
	return
}

// goLookupIP is the native Go implementation of LookupIP.
// Used only if cgoLookupIP refuses to handle the request
// (that is, only if cgoLookupIP is the stub in cgo_stub.go).
// Normally we let cgo use the C library resolver instead of
// depending on our lookup code, so that Go and C get the same
// answers.
func goLookupIP(name string) (addrs []IP, err os.Error) {
	onceLoadConfig.Do(loadConfig)
	if dnserr != nil || cfg == nil {
		err = dnserr
		return
	}
	var records []dnsRR
	var cname string
	cname, records, err = lookup(name, dnsTypeA)
	if err != nil {
		return
	}
	addrs = convertRR_A(records)
	if cname != "" {
		name = cname
	}
	_, records, err = lookup(name, dnsTypeAAAA)
	if err != nil && len(addrs) > 0 {
		// Ignore error because A lookup succeeded.
		err = nil
	}
	if err != nil {
		return
	}
	addrs = append(addrs, convertRR_AAAA(records)...)
	return
}

// goLookupCNAME is the native Go implementation of LookupCNAME.
// Used only if cgoLookupCNAME refuses to handle the request
// (that is, only if cgoLookupCNAME is the stub in cgo_stub.go).
// Normally we let cgo use the C library resolver instead of
// depending on our lookup code, so that Go and C get the same
// answers.
func goLookupCNAME(name string) (cname string, err os.Error) {
	onceLoadConfig.Do(loadConfig)
	if dnserr != nil || cfg == nil {
		err = dnserr
		return
	}
	_, rr, err := lookup(name, dnsTypeCNAME)
	if err != nil {
		return
	}
	cname = rr[0].(*dnsRR_CNAME).Cname
	return
}
