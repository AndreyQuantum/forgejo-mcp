package shared

import (
	"io"
	"os"
)

// FileRoots are the allowed roots of local file params (section 10.5), opened
// with os.Root so symlink and `..` escapes are refused on every OS.
type FileRoots struct {
	SaveDir   string // FORGEJO_SAVE_DIR (created 0700 on first use)
	AllowCWD  bool   // FORGEJO_SAVE_CWD
	TokenFile string // never readable or writable through these params
	Disabled  bool   // HTTP mode without FORGEJO_HTTP_FILES
}

// OpenLocal opens an existing local file for reading (uploads); files larger
// than 512 MiB are refused. It returns the file and its size.
func (r *FileRoots) OpenLocal(path string) (f *os.File, size int64, err error) { panic("WP6") }

// SaveFile writes r to a NEW local file (O_CREATE|O_EXCL; existing files are
// never overwritten: `error: exists: <path>`) and returns the final path and
// the number of bytes written.
func (r *FileRoots) SaveFile(path string, src io.Reader) (final string, n int64, err error) {
	panic("WP6")
}
