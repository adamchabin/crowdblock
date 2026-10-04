package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"time"
)

const tailPollInterval = time.Second

// Follow calls onLine for every line appended to path, like `tail -F`: it
// starts at the end of the file (history is not reported again after a
// restart), survives rotation (path replaced by a new file) and truncation,
// and waits for the file if it doesn't exist yet. Returns when ctx is done.
func Follow(ctx context.Context, path string, onLine func(string)) error {
	var (
		f         *os.File
		r         *bufio.Reader
		partial   string // line without '\n' yet: the writer is in the middle of it
		atStart   = true // first open: skip existing content
		switching bool   // f was rotated/truncated: drain it, then reopen path
	)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	for {
		if f == nil {
			var err error
			f, err = os.Open(path)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					log.Printf("cannot open %s: %v", path, err)
				}
			}
			if f != nil {
				if atStart {
					f.Seek(0, io.SeekEnd)
				}
				r = bufio.NewReader(f)
				partial = ""
			}
			atStart = false
		}

		// Read everything there is.
		for f != nil {
			s, err := r.ReadString('\n')
			if err == nil {
				onLine(partial + s[:len(s)-1])
				partial = ""
				continue
			}
			partial += s
			break
		}

		if switching {
			// The old file has been read to its end above; continue with
			// the new one right away (from its start).
			f.Close()
			f = nil
			switching = false
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(tailPollInterval):
		}

		if f != nil && (rotated(f, path) || truncated(f)) {
			switching = true
		}
	}
}

// rotated: path now points to a different file (or none).
func rotated(f *os.File, path string) bool {
	cur, err := os.Stat(path)
	if err != nil {
		return true
	}
	old, err := f.Stat()
	return err != nil || !os.SameFile(old, cur)
}

// truncated: the file is shorter than what we've read (copytruncate).
func truncated(f *os.File) bool {
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return true
	}
	st, err := f.Stat()
	return err != nil || st.Size() < pos
}
