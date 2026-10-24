package collect

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// fileReader reads small /proc and /sys files into buffers that are reused
// across samples. /proc files report size 0, so os.ReadFile regrows its
// buffer on every call; at 1 s sampling that is most of hearthd's garbage.
// Not safe for concurrent use; the returned slice is valid until the next
// read of the same file.
type fileReader struct {
	bufs map[string][]byte
}

func newFileReader() *fileReader { return &fileReader{bufs: map[string][]byte{}} }

// read reads root/name. Roots come from Config (tests use fixtures), never
// from requests.
func (r *fileReader) read(root, name string) ([]byte, error) {
	p := filepath.Join(root, name)
	f, err := os.Open(p) // #nosec G304 -- fixed name under a configured root
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf, err := readInto(f, r.bufs[p][:0])
	if err != nil {
		return nil, err
	}
	r.bufs[p] = buf
	return buf, nil
}

// readInto reads f to EOF, appending to buf. It replaces os.ReadFile and
// bytes.Buffer.ReadFrom for sysfs, which both panic if read(2) returns a
// negative count: a kernel driver bug seen on amdgpu power1_input for an old
// GCN chip (Linux 6.12), whose read returns the sensor value as the length.
func readInto(f *os.File, buf []byte) ([]byte, error) {
	if cap(buf) == 0 {
		buf = make([]byte, 0, 512)
	}
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := f.Read(buf[len(buf):cap(buf)])
		if n > 0 {
			buf = buf[:len(buf)+n]
		}
		if errors.Is(err, io.EOF) {
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			return nil, io.ErrNoProgress
		}
	}
}

// readFile is os.ReadFile built on readInto.
func readFile(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- callers pass fixed sysfs/procfs paths under a configured root
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readInto(f, nil)
}
