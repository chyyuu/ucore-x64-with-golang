// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package time

import "os"

// Seconds reports the number of seconds since the Unix epoch,
// January 1, 1970 00:00:00 UTC.
func Seconds() int64 {
	sec, _, err := os.Time()
	if err != nil {
		panic(err)
	}
	return sec
}

// Nanoseconds reports the number of nanoseconds since the Unix epoch,
// January 1, 1970 00:00:00 UTC.
func Nanoseconds() int64 {
	sec, nsec, err := os.Time()
	if err != nil {
		panic(err)
	}
	return sec*1e9 + nsec
}

// Sleep pauses the current goroutine for at least ns nanoseconds.
// Higher resolution sleeping may be provided by syscall.Nanosleep 
// on some operating systems.
func Sleep(ns int64) os.Error {
	_, err := sleep(Nanoseconds(), ns)
	return err
}

// sleep takes the current time and a duration,
// pauses for at least ns nanoseconds, and
// returns the current time and an error.
func sleep(t, ns int64) (int64, os.Error) {
	// TODO(cw): use monotonic-time once it's available
	end := t + ns
	for t < end {
		err := sysSleep(end - t)
		if err != nil {
			return 0, err
		}
		t = Nanoseconds()
	}
	return t, nil
}
