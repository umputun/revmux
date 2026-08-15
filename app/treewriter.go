package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// treeWriter materializes prompt files under one destination directory, skipping whatever is already
// there. It is the single implementation `revmux init` and `--dump-defaults` both write through. Every
// write goes through an os.Root on the destination: a joined path contains its last component alone,
// since os.Lstat dereferences every directory above it.
type treeWriter struct {
	dir  string
	root *os.Root
}

// newTreeWriter creates dir when it is absent and opens it as the root every write is contained in.
func newTreeWriter(dir string) (*treeWriter, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return &treeWriter{dir: dir, root: root}, nil
}

// close releases the destination handle.
func (w *treeWriter) close() error {
	if err := w.root.Close(); err != nil {
		return fmt.Errorf("close %s: %w", w.dir, err)
	}
	return nil
}

// write creates relPath — slash-separated and relative to the destination — reporting whether this call
// created it. An entry already there is left exactly as it is, a dangling symlink included: Lstat rather
// than Stat is what makes such a link an entry rather than a missing file, and O_EXCL refuses one planted
// between the look and the write.
func (w *treeWriter) write(relPath string, data []byte) (bool, error) {
	if dir := path.Dir(relPath); dir != "." {
		// an escaping link occupying a directory name is refused here rather than at the entry check
		// below, so the failure names the entry: the directory alone does not say which file was
		// refused. A link landing back inside the destination is followed and reports ErrExist.
		if err := w.root.MkdirAll(dir, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return false, fmt.Errorf("create %s: %w", w.dest(relPath), err)
		}
	}

	dst := w.dest(relPath)
	_, err := w.root.Lstat(relPath)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", dst, err)
	}

	f, err := w.root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("write %s: %w", dst, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("write %s: %w", dst, err)
	}
	return true, nil
}

// checkRegular reports whether relPath is there, refusing anything that is not a regular file. os.Root
// refuses a link leaving the destination but follows one landing back inside it, and the
// read-then-overwrite pair below is where that bites: an aliased config is read and then truncated
// through the alias.
func (w *treeWriter) checkRegular(relPath string) (bool, error) {
	fi, err := w.root.Lstat(relPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", w.dest(relPath), err)
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("read %s: not a regular file", w.dest(relPath))
	}
	return true, nil
}

// read returns what relPath holds, or nil when it is absent — an absent entry is not an error, since
// the caller's next move is to create it.
func (w *treeWriter) read(relPath string) ([]byte, error) {
	found, err := w.checkRegular(relPath)
	if err != nil || !found {
		return nil, err
	}
	f, err := w.root.Open(relPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", w.dest(relPath), err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", w.dest(relPath), err)
	}
	return data, nil
}

// replace writes data over relPath, creating it when absent. It is what the config needs and write
// cannot give it: a comments-only config is rewritten so an upgrade can move a default nobody set, which
// is the O_TRUNC write's O_EXCL exists to refuse. Prompt files take write instead.
func (w *treeWriter) replace(relPath string, data []byte) error {
	if _, err := w.checkRegular(relPath); err != nil {
		return err
	}
	f, err := w.root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", w.dest(relPath), err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", w.dest(relPath), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", w.dest(relPath), err)
	}
	return nil
}

// dest renders one entry the way every message and every reported path names it.
func (w *treeWriter) dest(relPath string) string {
	return filepath.Join(w.dir, filepath.FromSlash(relPath))
}
