// errchk $G -e $D/$F.go

// Copyright 2011 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Each test is in a separate function just so that if the
// compiler stops processing after one error, we don't
// lose other ones.

package main

var (
	i, n int
	x    []int
	c    chan int
	m    map[int]int
	s    string
)

// goto after declaration okay
func _() {
	x := 1
	goto L
L:
	_ = x
}

// goto before declaration okay
func _() {
	goto L
L:
	x := 1
	_ = x
}

// goto across declaration not okay
func _() {
	goto L // ERROR "goto L jumps over declaration of x at LINE+1"
	x := 1
	_ = x
L:
}

// goto across declaration in inner scope okay
func _() {
	goto L
	{
		x := 1
		_ = x
	}
L:
}

// goto across declaration after inner scope not okay
func _() {
	goto L // ERROR "goto L jumps over declaration of x at LINE+5"
	{
		x := 1
		_ = x
	}
	x := 1
	_ = x
L:
}

// goto across declaration in reverse okay
func _() {
L:
	x := 1
	_ = x
	goto L
}

// error shows first offending variable
func _() {
	goto L // ERROR "goto L jumps over declaration of x at LINE+1"
	x := 1
	_ = x
	y := 1
	_ = y
L:
}

// goto not okay even if code path is dead
func _() {
	goto L // ERROR "goto L jumps over declaration of x at LINE+1"
	x := 1
	_ = x
	y := 1
	_ = y
	return
L:
}

// goto into outer block okay
func _() {
	{
		goto L
	}
L:
}

// goto backward into outer block okay
func _() {
L:
	{
		goto L
	}
}

// goto into inner block not okay
func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+1"
	{
	L:
	}
}

// goto backward into inner block still not okay
func _() {
	{
	L:
	}
	goto L // ERROR "goto L jumps into block starting at LINE-3"
}

// error shows first (outermost) offending block
func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+1"
	{
		{
			{
			L:
			}
		}
	}
}

// error prefers block diagnostic over declaration diagnostic
func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+3"
	x := 1
	_ = x
	{
	L:
	}
}

// many kinds of blocks, all invalid to jump into or among,
// but valid to jump out of

// if

func _() {
L:
	if true {
		goto L
	}
}

func _() {
L:
	if true {
		goto L
	} else {
	}
}

func _() {
L:
	if false {
	} else {
		goto L
	}
}

func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+1"
	if true {
	L:
	}
}

func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+1"
	if true {
	L:
	} else {
	}
}

func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+1"
	if true {
	} else {
	L:
	}
}

func _() {
	if false {
	L:
	} else {
		goto L // ERROR "goto L jumps into block starting at LINE-3"
	}
}

func _() {
	if true {
		goto L // ERROR "goto L jumps into block starting at LINE+1"
	} else {
	L:
	}
}

func _() {
	if true {
		goto L // ERROR "goto L jumps into block starting at LINE+1"
	} else if false {
	L:
	}
}

func _() {
	if true {
		goto L // ERROR "goto L jumps into block starting at LINE+1"
	} else if false {
	L:
	} else {
	}
}

func _() {
	// This one is tricky.  There is an implicit scope
	// starting at the second if statement, and it contains
	// the final else, so the outermost offending scope
	// really is LINE+1 (like in the previous test),
	// even though it looks like it might be LINE+3 instead.
	if true {
		goto L // ERROR "goto L jumps into block starting at LINE+1"
	} else if false {
	} else {
	L:
	}
}

/* Want to enable these tests but gofmt mangles them.  Issue 1972.

func _() {
	// This one is okay, because the else is in the
	// implicit whole-if block and has no inner block
	// (no { }) around it.
	if true {
		goto L
	} else
		L:
}

func _() {
	// Still not okay.
	if true {
	L:
	} else
		goto L //// ERROR "goto L jumps into block starting at LINE-3"
}

*/

// for

func _() {
	for {
		goto L
	}
L:
}

func _() {
	for {
		goto L
	L:
	}
}

func _() {
	for {
	L:
	}
	goto L // ERROR "goto L jumps into block starting at LINE-3"
}

func _() {
	for {
		goto L
	L1:
	}
L:
	goto L1 // ERROR "goto L1 jumps into block starting at LINE-5"
}

func _() {
	for i < n {
	L:
	}
	goto L // ERROR "goto L jumps into block starting at LINE-3"
}

func _() {
	for i = 0; i < n; i++ {
	L:
	}
	goto L // ERROR "goto L jumps into block starting at LINE-3"
}

func _() {
	for i = range x {
	L:
	}
	goto L // ERROR "goto L jumps into block starting at LINE-3"
}

func _() {
	for i = range c {
	L:
	}
	goto L // ERROR "goto L jumps into block starting at LINE-3"
}

func _() {
	for i = range m {
	L:
	}
	goto L // ERROR "goto L jumps into block starting at LINE-3"
}

func _() {
	for i = range s {
	L:
	}
	goto L // ERROR "goto L jumps into block starting at LINE-3"
}

// switch

func _() {
L:
	switch i {
	case 0:
		goto L
	}
}

func _() {
L:
	switch i {
	case 0:

	default:
		goto L
	}
}

func _() {
	switch i {
	case 0:

	default:
	L:
		goto L
	}
}

func _() {
	switch i {
	case 0:

	default:
		goto L
	L:
	}
}

func _() {
	switch i {
	case 0:
		goto L
	L:
		;
	default:
	}
}

func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+1"
	switch i {
	case 0:
	L:
	}
}

func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+1"
	switch i {
	case 0:
	L:
		;
	default:
	}
}

func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+1"
	switch i {
	case 0:
	default:
	L:
	}
}

func _() {
	switch i {
	default:
		goto L // ERROR "goto L jumps into block starting at LINE+1"
	case 0:
	L:
	}
}

func _() {
	switch i {
	case 0:
	L:
		;
	default:
		goto L // ERROR "goto L jumps into block starting at LINE-4"
	}
}

// select
// different from switch.  the statement has no implicit block around it.

func _() {
L:
	select {
	case <-c:
		goto L
	}
}

func _() {
L:
	select {
	case c <- 1:

	default:
		goto L
	}
}

func _() {
	select {
	case <-c:

	default:
	L:
		goto L
	}
}

func _() {
	select {
	case c <- 1:

	default:
		goto L
	L:
	}
}

func _() {
	select {
	case <-c:
		goto L
	L:
		;
	default:
	}
}

func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+2"
	select {
	case c <- 1:
	L:
	}
}

func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+2"
	select {
	case c <- 1:
	L:
		;
	default:
	}
}

func _() {
	goto L // ERROR "goto L jumps into block starting at LINE+3"
	select {
	case <-c:
	default:
	L:
	}
}

func _() {
	select {
	default:
		goto L // ERROR "goto L jumps into block starting at LINE+1"
	case <-c:
	L:
	}
}

func _() {
	select {
	case <-c:
	L:
		;
	default:
		goto L // ERROR "goto L jumps into block starting at LINE-4"
	}
}
