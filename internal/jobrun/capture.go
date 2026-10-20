package jobrun

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// capture.go implements the bounded capture of one job output stream
// (task 33). A stream is held in memory while it fits in memLimit; beyond
// that the buffer is spilled to a temporary file. The file is bounded by
// the stream's log cap: the first half of the cap is kept as is (the
// head), then the file keeps only the latest half of the cap, written as
// a ring. Total and omitted bytes are counted, so the report can say
// "[... N bytes omitted ...]".
//
// On Unix the temporary file is unlinked right after creation and used
// through the open handle, so not even a crash leaves it behind. On
// Windows the file cannot be removed while open, so Close removes it.

const (
	// memLimit is how much of a stream is held in memory. A stream that
	// stays within memLimit never touches the disk.
	memLimit = 10 * 1024
	// tailChunk is the read-back chunk size of the tail reader.
	tailChunk = 4096
	// Enough UTF-8 bytes for the report's 1,000 characters plus a
	// truncation indicator. Long raw lines must not accumulate in memory.
	tailLineBytes = 4004
)

// capture is the bounded capture of one stream. The zero fields are set
// by newCapture; Write never fails, recording the first I/O error in err
// instead (a full disk must not kill the job being watched).
type capture struct {
	label      string // "stdout" or "stderr", for the temporary file name
	cap        int64  // log cap: head cap/2 + ring cap/2
	mem        int64  // in-memory limit, memLimit; tests lower it
	dir        string // temporary-file directory; "": os.TempDir()
	buf        []byte // in-memory content while len ≤ mem
	high       int64  // buffer high-water mark, for tests
	file       captureFile
	createTemp func(string, string) (captureFile, error)
	head       int64 // bytes stored in the head region [0, cap/2)
	ring       int64 // bytes stored in the ring region
	ringW      int64 // next write offset within the ring region
	total      int64 // bytes the job wrote
	err        error // first I/O error
}

// The small file contract permits deterministic disk-error tests without
// global hooks, special devices, or filesystem-permission assumptions.
type captureFile interface {
	io.ReaderAt
	io.WriterAt
	io.Closer
	Name() string
	Chmod(os.FileMode) error
	Stat() (os.FileInfo, error)
}

func newCapture(label string, cap int64, dir string) *capture {
	return &capture{label: label, cap: cap, mem: min(int64(memLimit), cap), dir: dir,
		createTemp: func(dir, pattern string) (captureFile, error) { return os.CreateTemp(dir, pattern) }}
}

// headCap returns the size of the head region, half the log cap.
func (c *capture) headCap() int64 { return c.cap / 2 }

// ringCap returns the size of the ring region.
func (c *capture) ringCap() int64 { return c.cap - c.cap/2 }

// Total returns the number of bytes the job wrote to the stream.
func (c *capture) Total() int64 { return c.total }

// Omitted returns the number of bytes the log cap dropped between head
// and ring.
func (c *capture) Omitted() int64 {
	if c.total > c.Captured() {
		return c.total - c.Captured()
	}
	return 0
}

// Captured returns the number of bytes Content returns: head + ring.
func (c *capture) Captured() int64 { return c.head + c.ring + int64(len(c.buf)) }

// HighWater returns the largest in-memory buffer size seen. It never
// exceeds memLimit.
func (c *capture) HighWater() int64 { return c.high }

// Err returns the first capture I/O error, if any.
func (c *capture) Err() error { return c.err }

// Spilled reports whether the stream outgrew the in-memory buffer.
func (c *capture) Spilled() bool { return c.file != nil }

func (c *capture) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	if c.err != nil {
		return len(p), nil //nolint:nilerr // CaptureErr reports disk failures; keep draining the job.
	}
	if c.file == nil {
		if int64(len(c.buf))+int64(len(p)) <= c.mem {
			c.buf = append(c.buf, p...)
			if int64(len(c.buf)) > c.high {
				c.high = int64(len(c.buf))
			}
			return len(p), nil
		}
		if err := c.spill(); err != nil {
			c.setErr(err)
			return len(p), nil
		}
	}
	if err := c.writeFile(p); err != nil {
		c.setErr(err)
	}
	return len(p), nil
}

func (c *capture) setErr(err error) {
	if c.err == nil {
		c.err = err
	}
}

// spill moves the in-memory buffer into a fresh temporary file and
// switches to file capture. The file is owner-only and, on Unix, unlinked
// immediately. It is created in the capture's directory (Spec.TempDir;
// default: the system temporary directory).
func (c *capture) spill() error {
	f, err := c.createTemp(c.dir, "mailfire-run-*."+c.label+".log")
	if err != nil {
		return fmt.Errorf("creating temporary file for %s: %w", c.label, err)
	}
	if err := f.Chmod(0o600); err != nil {
		return errors.Join(fmt.Errorf("securing temporary file: %w", err), f.Close(), os.Remove(f.Name()))
	}
	c.file = f
	unlinkOnCreate(f)
	if len(c.buf) > 0 {
		buf := c.buf
		c.buf = nil
		if err := c.writeFile(buf); err != nil {
			return fmt.Errorf("writing %s capture: %w", c.label, err)
		}
	}
	return nil
}

// writeFile stores p in the head region while it fits, then in the ring.
func (c *capture) writeFile(p []byte) error {
	for len(p) > 0 {
		switch {
		case c.head < c.headCap():
			n := min(int64(len(p)), c.headCap()-c.head)
			written, err := c.file.WriteAt(p[:n], c.head)
			c.head += int64(written)
			if err != nil {
				return err
			}
			if int64(written) != n {
				return io.ErrShortWrite
			}
			p = p[n:]
		default:
			ring := c.ringCap()
			if int64(len(p)) >= ring {
				// Only the last ringCap bytes survive: write them
				// aligned to the end of the ring region.
				p = p[int64(len(p))-ring:]
				if _, err := c.file.WriteAt(p, c.headCap()); err != nil {
					return err
				}
				c.ring, c.ringW = ring, 0
				return nil
			}
			n := min(int64(len(p)), ring-c.ringW)
			if _, err := c.file.WriteAt(p[:n], c.headCap()+c.ringW); err != nil {
				return err
			}
			c.ringW = (c.ringW + n) % ring
			if c.ring += n; c.ring > ring {
				c.ring = ring
			}
			p = p[n:]
		}
	}
	return nil
}

// OmittedMarker is written between the head and the ring when the log cap
// dropped bytes.
func (c *capture) omittedMarker() []byte {
	return fmt.Appendf(nil, "[... %d bytes omitted ...]\n", c.Omitted())
}

// Content returns the captured stream: the whole stream when it fits the
// log cap, else the head, an omission marker, and the latest ring content.
// It reads the temporary file, never the (already reused) memory buffer of
// a spilled stream, so the memory cost is bounded by the log cap.
func (c *capture) Content() []byte {
	if c.file == nil {
		return append([]byte(nil), c.buf...)
	}
	var out []byte
	read := func(off, n int64) {
		if n <= 0 {
			return
		}
		p := make([]byte, n)
		if _, err := c.file.ReadAt(p, off); err != nil {
			c.setErr(fmt.Errorf("reading %s capture: %w", c.label, err))
			return
		}
		out = append(out, p...)
	}
	read(0, c.head)
	if c.Omitted() > 0 {
		out = append(out, c.omittedMarker()...)
	}
	for _, r := range c.ringRegions() {
		read(r.off, r.length)
	}
	return out
}

// region is a byte range of the capture file.
type region struct{ off, length int64 }

// ringRegions returns the ring content's file ranges, oldest first. When
// the ring wrapped, the oldest byte sits at the next write offset.
func (c *capture) ringRegions() []region {
	if c.ring == 0 {
		return nil
	}
	if c.ring == c.ringCap() && c.ringW > 0 {
		return []region{
			{c.headCap() + c.ringW, c.ringCap() - c.ringW},
			{c.headCap(), c.ringW},
		}
	}
	return []region{{c.headCap(), c.ring}}
}

// tail returns the last n lines of the captured stream, in order, without
// their line endings. Lines longer than the read-back chunks are cut. The
// raw bytes are returned unsanitized; the caller prepares them for the
// report (ANSI removal, \r collapse, UTF-8 replacement, length limit).
//
// The lines are read backwards from the end of the content, in tailChunk
// steps: the memory cost stays bounded however long the lines are.
func (c *capture) tail(n int) [][]byte {
	if n <= 0 {
		return nil
	}
	if c.file == nil {
		return lastLines(c.buf, n)
	}
	// The logical content regions, last first.
	regions := c.ringRegions()
	for i, j := 0, len(regions)-1; i < j; i, j = i+1, j-1 {
		regions[i], regions[j] = regions[j], regions[i]
	}
	if c.head > 0 {
		regions = append(regions, region{0, c.head})
	}
	scan := tailScanner{limit: n, first: true}
	var chunk [tailChunk]byte
	for _, r := range regions {
		for pos := r.off + r.length; pos > r.off && len(scan.lines) < n; {
			size := min(int64(tailChunk), pos-r.off)
			pos -= size
			if _, err := c.file.ReadAt(chunk[:size], pos); err != nil {
				c.setErr(fmt.Errorf("reading %s capture: %w", c.label, err))
				return reverse(scan.lines)
			}
			scan.feed(chunk[:size])
		}
	}
	return scan.finish()
}

// lastLines splits an in-memory buffer like tail does for a file.
func lastLines(buf []byte, n int) [][]byte {
	scan := tailScanner{limit: n, first: true}
	scan.feed(buf)
	return scan.finish()
}

// Scan backwards, retaining only the prefix of each displayed line in a
// fixed-size ring. A carriage return discards earlier progress updates.
type tailScanner struct {
	limit, start, size       int
	first, seen, overwritten bool
	line                     [tailLineBytes]byte
	lines                    [][]byte
}

func (s *tailScanner) feed(chunk []byte) {
	for i := len(chunk) - 1; i >= 0 && len(s.lines) < s.limit; i-- {
		b := chunk[i]
		if s.first {
			s.first = false
			if b == '\n' {
				continue
			}
		}
		if b == '\n' {
			s.flush()
			continue
		}
		s.seen = true
		if b == '\r' {
			if s.size > 0 {
				s.overwritten = true
			}
			continue
		}
		if s.overwritten {
			continue
		}
		s.start = (s.start + tailLineBytes - 1) % tailLineBytes
		s.line[s.start] = b
		s.size = min(s.size+1, tailLineBytes)
	}
}

func (s *tailScanner) flush() {
	line := make([]byte, s.size)
	n := copy(line, s.line[s.start:])
	copy(line[n:], s.line[:])
	s.lines = append(s.lines, line)
	s.start, s.size, s.seen, s.overwritten = 0, 0, false, false
}

func (s *tailScanner) finish() [][]byte {
	if s.seen && len(s.lines) < s.limit {
		s.flush()
	}
	return reverse(s.lines)
}

func reverse(lines [][]byte) [][]byte {
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

// Close releases the capture's resources. On Windows it also removes the
// temporary file (on Unix it was unlinked at creation).
func (c *capture) Close() error {
	if c.file == nil {
		return nil
	}
	err := c.file.Close()
	name := c.file.Name()
	c.file = nil
	return errors.Join(err, removeTempFile(name, err))
}
