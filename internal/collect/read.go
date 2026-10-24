package collect

import (
	"bytes"
	"os"
	"path/filepath"
)

// fileReader reads small /proc and /sys files into buffers that are reused
// across samples. /proc files report size 0, so os.ReadFile regrows its
// buffer on every call; at 1 s sampling that is most of hearthd's garbage.
// Not safe for concurrent use; the returned slice is valid until the next
// read of the same file.
type fileReader struct {
	bufs map[string]*bytes.Buffer
}

func newFileReader() *fileReader { return &fileReader{bufs: map[string]*bytes.Buffer{}} }

// read reads root/name. Roots come from Config (tests use fixtures), never
// from requests.
func (r *fileReader) read(root, name string) ([]byte, error) {
	p := filepath.Join(root, name)
	f, err := os.Open(p) // #nosec G304 -- fixed name under a configured root
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := r.bufs[p]
	if buf == nil {
		buf = &bytes.Buffer{}
		r.bufs[p] = buf
	}
	buf.Reset()
	if _, err := buf.ReadFrom(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
