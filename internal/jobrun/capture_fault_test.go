package jobrun

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
)

func assertCaptureModel(t *testing.T, c *capture, source []byte) {
	t.Helper()
	want := source
	if int64(len(source)) > c.cap {
		head := c.cap / 2
		want = append([]byte(nil), source[:head]...)
		want = fmt.Appendf(want, "[... %d bytes omitted ...]\n", int64(len(source))-c.cap)
		want = append(want, source[int64(len(source))-(c.cap-head):]...)
	}
	if got := c.Content(); !bytes.Equal(got, want) {
		t.Fatalf("cap %d, total %d: content differs from head/tail model", c.cap, len(source))
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
	if c.Total() != int64(len(source)) || c.Captured() != min(int64(len(source)), c.cap) || c.Omitted() != max(int64(len(source))-c.cap, 0) {
		t.Fatalf("incorrect counts: total=%d captured=%d omitted=%d", c.Total(), c.Captured(), c.Omitted())
	}
	if c.HighWater() > c.mem {
		t.Fatalf("memory high-water %d > %d", c.HighWater(), c.mem)
	}
	if c.file != nil {
		fi, err := c.file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > c.cap {
			t.Fatalf("file size %d > cap %d", fi.Size(), c.cap)
		}
	}
}

func TestCaptureRandomizedBoundaries(t *testing.T) {
	rng := rand.New(rand.NewPCG(33, 34))
	for trial := 0; trial < 100; trial++ {
		c := newCapture("stdout", int64(1+rng.IntN(2048)))
		c.mem = min(c.mem, int64(1+rng.IntN(1024)))
		var source []byte
		for write := 0; write < 20; write++ {
			chunk := make([]byte, rng.IntN(2048))
			for i := range chunk {
				chunk[i] = byte(rng.Uint32())
			}
			source = append(source, chunk...)
			if _, err := c.Write(chunk); err != nil {
				t.Fatal(err)
			}
			assertCaptureModel(t, c, source)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzCaptureRing(f *testing.F) {
	f.Add([]byte("head and tail\n"), uint16(7), uint16(5))
	f.Add(bytes.Repeat([]byte("abc\n"), 4096), uint16(12000), uint16(10000))
	f.Add([]byte("x"), uint16(1), uint16(1))
	f.Fuzz(func(t *testing.T, data []byte, capValue, chunkValue uint16) {
		if len(data) > 64*1024 {
			t.Skip()
		}
		c := newCapture("stdout", max(int64(capValue), 1))
		defer closeTest(t, c)
		step := max(int(chunkValue), 1)
		for pos := 0; pos < len(data); {
			end := min(pos+step, len(data))
			if _, err := c.Write(data[pos:end]); err != nil {
				t.Fatal(err)
			}
			pos = end
		}
		assertCaptureModel(t, c, data)
	})
}

type faultyCaptureFile struct {
	captureFile
	op  string
	err error
}

func (f *faultyCaptureFile) WriteAt(p []byte, off int64) (int, error) {
	if f.op == "write" {
		n, err := f.captureFile.WriteAt(p[:len(p)/2], off)
		return n, errors.Join(err, f.err)
	}
	return f.captureFile.WriteAt(p, off)
}

func (f *faultyCaptureFile) ReadAt(p []byte, off int64) (int, error) {
	if f.op == "read" {
		return 0, f.err
	}
	return f.captureFile.ReadAt(p, off)
}

func (f *faultyCaptureFile) Chmod(mode os.FileMode) error {
	if f.op == "chmod" {
		return f.err
	}
	return f.captureFile.Chmod(mode)
}

func (f *faultyCaptureFile) Close() error {
	err := f.captureFile.Close()
	if f.op == "close" {
		return errors.Join(err, f.err)
	}
	return err
}

func TestCaptureDiskFailures(t *testing.T) {
	for _, op := range []string{"create", "chmod", "write", "read", "close"} {
		t.Run(op, func(t *testing.T) {
			injected := errors.New("injected " + op + " failure")
			c := newCapture("stdout", 128)
			c.mem = 8
			var name string
			c.createTemp = func(_, pattern string) (captureFile, error) {
				if op == "create" {
					return nil, injected
				}
				file, err := os.CreateTemp(t.TempDir(), pattern)
				if err != nil {
					return nil, err
				}
				name = file.Name()
				return &faultyCaptureFile{captureFile: file, op: op, err: injected}, nil
			}
			for _, data := range [][]byte{[]byte("prefix"), bytes.Repeat([]byte("x"), 50), bytes.Repeat([]byte("y"), 1000)} {
				if n, err := c.Write(data); n != len(data) || err != nil {
					t.Fatalf("capture stopped draining: %d, %v", n, err)
				}
			}
			_ = c.Content()
			if op != "close" && !errors.Is(c.Err(), injected) {
				t.Errorf("capture error = %v", c.Err())
			}
			if c.Total() != 1056 {
				t.Errorf("lost byte count: %d", c.Total())
			}
			err := c.Close()
			if op == "close" && !errors.Is(err, injected) {
				t.Errorf("close error = %v", err)
			}
			if op != "close" && err != nil {
				t.Fatal(err)
			}
			if name != "" {
				if _, err := os.Stat(name); !os.IsNotExist(err) {
					t.Fatalf("temporary file survived %s failure: %v", op, err)
				}
			}
		})
	}
}
