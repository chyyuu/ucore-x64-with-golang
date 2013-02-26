// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rpc

import (
	"fmt"
	"http/httptest"
	"io"
	"log"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	newServer                 *Server
	serverAddr, newServerAddr string
	httpServerAddr            string
	once, newOnce, httpOnce   sync.Once
)

const (
	second      = 1e9
	newHttpPath = "/foo"
)

type Args struct {
	A, B int
}

type Reply struct {
	C int
}

type Arith int

// Some of Arith's methods have value args, some have pointer args. That's deliberate.

func (t *Arith) Add(args Args, reply *Reply) os.Error {
	reply.C = args.A + args.B
	return nil
}

func (t *Arith) Mul(args *Args, reply *Reply) os.Error {
	reply.C = args.A * args.B
	return nil
}

func (t *Arith) Div(args Args, reply *Reply) os.Error {
	if args.B == 0 {
		return os.NewError("divide by zero")
	}
	reply.C = args.A / args.B
	return nil
}

func (t *Arith) String(args *Args, reply *string) os.Error {
	*reply = fmt.Sprintf("%d+%d=%d", args.A, args.B, args.A+args.B)
	return nil
}

func (t *Arith) Scan(args string, reply *Reply) (err os.Error) {
	_, err = fmt.Sscan(args, &reply.C)
	return
}

func (t *Arith) Error(args *Args, reply *Reply) os.Error {
	panic("ERROR")
}

func listenTCP() (net.Listener, string) {
	l, e := net.Listen("tcp", "127.0.0.1:0") // any available address
	if e != nil {
		log.Fatalf("net.Listen tcp :0: %v", e)
	}
	return l, l.Addr().String()
}

func startServer() {
	Register(new(Arith))

	var l net.Listener
	l, serverAddr = listenTCP()
	log.Println("Test RPC server listening on", serverAddr)
	go Accept(l)

	HandleHTTP()
	httpOnce.Do(startHttpServer)
}

func startNewServer() {
	newServer = NewServer()
	newServer.Register(new(Arith))

	var l net.Listener
	l, newServerAddr = listenTCP()
	log.Println("NewServer test RPC server listening on", newServerAddr)
	go Accept(l)

	newServer.HandleHTTP(newHttpPath, "/bar")
	httpOnce.Do(startHttpServer)
}

func startHttpServer() {
	server := httptest.NewServer(nil)
	httpServerAddr = server.Listener.Addr().String()
	log.Println("Test HTTP RPC server listening on", httpServerAddr)
}

func TestRPC(t *testing.T) {
	once.Do(startServer)
	testRPC(t, serverAddr)
	newOnce.Do(startNewServer)
	testRPC(t, newServerAddr)
}

func testRPC(t *testing.T, addr string) {
	client, err := Dial("tcp", addr)
	if err != nil {
		t.Fatal("dialing", err)
	}

	// Synchronous calls
	args := &Args{7, 8}
	reply := new(Reply)
	err = client.Call("Arith.Add", args, reply)
	if err != nil {
		t.Errorf("Add: expected no error but got string %q", err.String())
	}
	if reply.C != args.A+args.B {
		t.Errorf("Add: expected %d got %d", reply.C, args.A+args.B)
	}

	// Nonexistent method
	args = &Args{7, 0}
	reply = new(Reply)
	err = client.Call("Arith.BadOperation", args, reply)
	// expect an error
	if err == nil {
		t.Error("BadOperation: expected error")
	} else if !strings.HasPrefix(err.String(), "rpc: can't find method ") {
		t.Errorf("BadOperation: expected can't find method error; got %q", err)
	}

	// Unknown service
	args = &Args{7, 8}
	reply = new(Reply)
	err = client.Call("Arith.Unknown", args, reply)
	if err == nil {
		t.Error("expected error calling unknown service")
	} else if strings.Index(err.String(), "method") < 0 {
		t.Error("expected error about method; got", err)
	}

	// Out of order.
	args = &Args{7, 8}
	mulReply := new(Reply)
	mulCall := client.Go("Arith.Mul", args, mulReply, nil)
	addReply := new(Reply)
	addCall := client.Go("Arith.Add", args, addReply, nil)

	addCall = <-addCall.Done
	if addCall.Error != nil {
		t.Errorf("Add: expected no error but got string %q", addCall.Error.String())
	}
	if addReply.C != args.A+args.B {
		t.Errorf("Add: expected %d got %d", addReply.C, args.A+args.B)
	}

	mulCall = <-mulCall.Done
	if mulCall.Error != nil {
		t.Errorf("Mul: expected no error but got string %q", mulCall.Error.String())
	}
	if mulReply.C != args.A*args.B {
		t.Errorf("Mul: expected %d got %d", mulReply.C, args.A*args.B)
	}

	// Error test
	args = &Args{7, 0}
	reply = new(Reply)
	err = client.Call("Arith.Div", args, reply)
	// expect an error: zero divide
	if err == nil {
		t.Error("Div: expected error")
	} else if err.String() != "divide by zero" {
		t.Error("Div: expected divide by zero error; got", err)
	}

	// Bad type.
	reply = new(Reply)
	err = client.Call("Arith.Add", reply, reply) // args, reply would be the correct thing to use
	if err == nil {
		t.Error("expected error calling Arith.Add with wrong arg type")
	} else if strings.Index(err.String(), "type") < 0 {
		t.Error("expected error about type; got", err)
	}

	// Non-struct argument
	const Val = 12345
	str := fmt.Sprint(Val)
	reply = new(Reply)
	err = client.Call("Arith.Scan", &str, reply)
	if err != nil {
		t.Errorf("Scan: expected no error but got string %q", err.String())
	} else if reply.C != Val {
		t.Errorf("Scan: expected %d got %d", Val, reply.C)
	}

	// Non-struct reply
	args = &Args{27, 35}
	str = ""
	err = client.Call("Arith.String", args, &str)
	if err != nil {
		t.Errorf("String: expected no error but got string %q", err.String())
	}
	expect := fmt.Sprintf("%d+%d=%d", args.A, args.B, args.A+args.B)
	if str != expect {
		t.Errorf("String: expected %s got %s", expect, str)
	}

	args = &Args{7, 8}
	reply = new(Reply)
	err = client.Call("Arith.Mul", args, reply)
	if err != nil {
		t.Errorf("Mul: expected no error but got string %q", err.String())
	}
	if reply.C != args.A*args.B {
		t.Errorf("Mul: expected %d got %d", reply.C, args.A*args.B)
	}
}

func TestHTTP(t *testing.T) {
	once.Do(startServer)
	testHTTPRPC(t, "")
	newOnce.Do(startNewServer)
	testHTTPRPC(t, newHttpPath)
}

func testHTTPRPC(t *testing.T, path string) {
	var client *Client
	var err os.Error
	if path == "" {
		client, err = DialHTTP("tcp", httpServerAddr)
	} else {
		client, err = DialHTTPPath("tcp", httpServerAddr, path)
	}
	if err != nil {
		t.Fatal("dialing", err)
	}

	// Synchronous calls
	args := &Args{7, 8}
	reply := new(Reply)
	err = client.Call("Arith.Add", args, reply)
	if err != nil {
		t.Errorf("Add: expected no error but got string %q", err.String())
	}
	if reply.C != args.A+args.B {
		t.Errorf("Add: expected %d got %d", reply.C, args.A+args.B)
	}
}

// CodecEmulator provides a client-like api and a ServerCodec interface.
// Can be used to test ServeRequest.
type CodecEmulator struct {
	server        *Server
	serviceMethod string
	args          *Args
	reply         *Reply
	err           os.Error
}

func (codec *CodecEmulator) Call(serviceMethod string, args *Args, reply *Reply) os.Error {
	codec.serviceMethod = serviceMethod
	codec.args = args
	codec.reply = reply
	codec.err = nil
	var serverError os.Error
	if codec.server == nil {
		serverError = ServeRequest(codec)
	} else {
		serverError = codec.server.ServeRequest(codec)
	}
	if codec.err == nil && serverError != nil {
		codec.err = serverError
	}
	return codec.err
}

func (codec *CodecEmulator) ReadRequestHeader(req *Request) os.Error {
	req.ServiceMethod = codec.serviceMethod
	req.Seq = 0
	return nil
}

func (codec *CodecEmulator) ReadRequestBody(argv interface{}) os.Error {
	if codec.args == nil {
		return io.ErrUnexpectedEOF
	}
	*(argv.(*Args)) = *codec.args
	return nil
}

func (codec *CodecEmulator) WriteResponse(resp *Response, reply interface{}) os.Error {
	if resp.Error != "" {
		codec.err = os.NewError(resp.Error)
	}
	*codec.reply = *(reply.(*Reply))
	return nil
}

func (codec *CodecEmulator) Close() os.Error {
	return nil
}

func TestServeRequest(t *testing.T) {
	once.Do(startServer)
	testServeRequest(t, nil)
	newOnce.Do(startNewServer)
	testServeRequest(t, newServer)
}

func testServeRequest(t *testing.T, server *Server) {
	client := CodecEmulator{server: server}

	args := &Args{7, 8}
	reply := new(Reply)
	err := client.Call("Arith.Add", args, reply)
	if err != nil {
		t.Errorf("Add: expected no error but got string %q", err.String())
	}
	if reply.C != args.A+args.B {
		t.Errorf("Add: expected %d got %d", reply.C, args.A+args.B)
	}

	err = client.Call("Arith.Add", nil, reply)
	if err == nil {
		t.Errorf("expected error calling Arith.Add with nil arg")
	}
}

type ReplyNotPointer int
type ArgNotPublic int
type ReplyNotPublic int
type local struct{}

func (t *ReplyNotPointer) ReplyNotPointer(args *Args, reply Reply) os.Error {
	return nil
}

func (t *ArgNotPublic) ArgNotPublic(args *local, reply *Reply) os.Error {
	return nil
}

func (t *ReplyNotPublic) ReplyNotPublic(args *Args, reply *local) os.Error {
	return nil
}

// Check that registration handles lots of bad methods and a type with no suitable methods.
func TestRegistrationError(t *testing.T) {
	err := Register(new(ReplyNotPointer))
	if err == nil {
		t.Errorf("expected error registering ReplyNotPointer")
	}
	err = Register(new(ArgNotPublic))
	if err == nil {
		t.Errorf("expected error registering ArgNotPublic")
	}
	err = Register(new(ReplyNotPublic))
	if err == nil {
		t.Errorf("expected error registering ReplyNotPublic")
	}
}

type WriteFailCodec int

func (WriteFailCodec) WriteRequest(*Request, interface{}) os.Error {
	// the panic caused by this error used to not unlock a lock.
	return os.NewError("fail")
}

func (WriteFailCodec) ReadResponseHeader(*Response) os.Error {
	time.Sleep(120e9)
	panic("unreachable")
}

func (WriteFailCodec) ReadResponseBody(interface{}) os.Error {
	time.Sleep(120e9)
	panic("unreachable")
}

func (WriteFailCodec) Close() os.Error {
	return nil
}

func TestSendDeadlock(t *testing.T) {
	client := NewClientWithCodec(WriteFailCodec(0))

	done := make(chan bool)
	go func() {
		testSendDeadlock(client)
		testSendDeadlock(client)
		done <- true
	}()
	select {
	case <-done:
		return
	case <-time.After(5e9):
		t.Fatal("deadlock")
	}
}

func testSendDeadlock(client *Client) {
	defer func() {
		recover()
	}()
	args := &Args{7, 8}
	reply := new(Reply)
	client.Call("Arith.Add", args, reply)
}

func dialDirect() (*Client, os.Error) {
	return Dial("tcp", serverAddr)
}

func dialHTTP() (*Client, os.Error) {
	return DialHTTP("tcp", httpServerAddr)
}

func countMallocs(dial func() (*Client, os.Error), t *testing.T) uint64 {
	once.Do(startServer)
	client, err := dial()
	if err != nil {
		t.Fatal("error dialing", err)
	}
	args := &Args{7, 8}
	reply := new(Reply)
	runtime.UpdateMemStats()
	mallocs := 0 - runtime.MemStats.Mallocs
	const count = 100
	for i := 0; i < count; i++ {
		err := client.Call("Arith.Add", args, reply)
		if err != nil {
			t.Errorf("Add: expected no error but got string %q", err.String())
		}
		if reply.C != args.A+args.B {
			t.Errorf("Add: expected %d got %d", reply.C, args.A+args.B)
		}
	}
	runtime.UpdateMemStats()
	mallocs += runtime.MemStats.Mallocs
	return mallocs / count
}

func TestCountMallocs(t *testing.T) {
	fmt.Printf("mallocs per rpc round trip: %d\n", countMallocs(dialDirect, t))
}

func TestCountMallocsOverHTTP(t *testing.T) {
	fmt.Printf("mallocs per HTTP rpc round trip: %d\n", countMallocs(dialHTTP, t))
}

func benchmarkEndToEnd(dial func() (*Client, os.Error), b *testing.B) {
	b.StopTimer()
	once.Do(startServer)
	client, err := dial()
	if err != nil {
		fmt.Println("error dialing", err)
		return
	}

	// Synchronous calls
	args := &Args{7, 8}
	reply := new(Reply)
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		err = client.Call("Arith.Add", args, reply)
		if err != nil {
			fmt.Printf("Add: expected no error but got string %q", err.String())
			break
		}
		if reply.C != args.A+args.B {
			fmt.Printf("Add: expected %d got %d", reply.C, args.A+args.B)
			break
		}
	}
}

func BenchmarkEndToEnd(b *testing.B) {
	benchmarkEndToEnd(dialDirect, b)
}

func BenchmarkEndToEndHTTP(b *testing.B) {
	benchmarkEndToEnd(dialHTTP, b)
}
