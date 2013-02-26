// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
Package zip provides support for reading and writing ZIP archives.

See: http://www.pkware.com/documents/casestudies/APPNOTE.TXT

This package does not support ZIP64 or disk spanning.
*/
package zip

import "os"
import "time"

// Compression methods.
const (
	Store   uint16 = 0
	Deflate uint16 = 8
)

const (
	fileHeaderSignature      = 0x04034b50
	directoryHeaderSignature = 0x02014b50
	directoryEndSignature    = 0x06054b50
	fileHeaderLen            = 30 // + filename + extra
	directoryHeaderLen       = 46 // + filename + extra + comment
	directoryEndLen          = 22 // + comment
	dataDescriptorLen        = 12
)

type FileHeader struct {
	Name             string
	CreatorVersion   uint16
	ReaderVersion    uint16
	Flags            uint16
	Method           uint16
	ModifiedTime     uint16 // MS-DOS time
	ModifiedDate     uint16 // MS-DOS date
	CRC32            uint32
	CompressedSize   uint32
	UncompressedSize uint32
	Extra            []byte
	Comment          string
}

type directoryEnd struct {
	diskNbr            uint16 // unused
	dirDiskNbr         uint16 // unused
	dirRecordsThisDisk uint16 // unused
	directoryRecords   uint16
	directorySize      uint32
	directoryOffset    uint32 // relative to file
	commentLen         uint16
	comment            string
}

func recoverError(err *os.Error) {
	if e := recover(); e != nil {
		if osErr, ok := e.(os.Error); ok {
			*err = osErr
			return
		}
		panic(e)
	}
}

// msDosTimeToTime converts an MS-DOS date and time into a time.Time.
// The resolution is 2s.
// See: http://msdn.microsoft.com/en-us/library/ms724247(v=VS.85).aspx
func msDosTimeToTime(dosDate, dosTime uint16) time.Time {
	return time.Time{
		// date bits 0-4: day of month; 5-8: month; 9-15: years since 1980
		Year:  int64(dosDate>>9 + 1980),
		Month: int(dosDate >> 5 & 0xf),
		Day:   int(dosDate & 0x1f),

		// time bits 0-4: second/2; 5-10: minute; 11-15: hour
		Hour:   int(dosTime >> 11),
		Minute: int(dosTime >> 5 & 0x3f),
		Second: int(dosTime & 0x1f * 2),
	}
}

// Mtime_ns returns the modified time in ns since epoch.
// The resolution is 2s.
func (h *FileHeader) Mtime_ns() int64 {
	t := msDosTimeToTime(h.ModifiedDate, h.ModifiedTime)
	return t.Seconds() * 1e9
}
