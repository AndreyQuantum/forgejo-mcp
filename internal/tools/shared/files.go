package shared

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// maxUpload is the largest file OpenLocal accepts.
const maxUpload = 512 << 20

// FileRoots are the allowed roots of local file params (section 10.5), opened
// with os.Root so symlink and `..` escapes are refused on every OS.
type FileRoots struct {
	SaveDir   string // FORGEJO_SAVE_DIR (created 0700 on first use)
	AllowCWD  bool   // FORGEJO_SAVE_CWD
	TokenFile string // never readable or writable through these params
	Disabled  bool   // HTTP mode without FORGEJO_HTTP_FILES
}

func (r *FileRoots) saveDir() string {
	if r.SaveDir != "" {
		return r.SaveDir
	}
	return filepath.Join(os.TempDir(), "forgejo-mcp")
}

// roots returns the absolute allowed roots, the save dir first.
func (r *FileRoots) roots() ([]string, error) {
	save, err := filepath.Abs(r.saveDir())
	if err != nil {
		return nil, err
	}
	out := []string{save}
	if r.AllowCWD {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		out = append(out, cwd)
	}
	return out, nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// relUnder returns p relative to root when p lies under it.
func relUnder(root, p string) (string, bool) {
	if samePath(root, p) {
		return "", false // the root itself is a directory, not a file
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if len(p) <= len(prefix) || !samePath(p[:len(prefix)], prefix) {
		return "", false
	}
	return p[len(prefix):], true
}

// resolve maps a user path to (root dir, path relative to it).
func (r *FileRoots) resolve(p string) (root, rel string, err error) {
	if r.Disabled {
		return "", "", errors.New("local file params are disabled in HTTP mode (set FORGEJO_HTTP_FILES=1)")
	}
	if p == "" || strings.ContainsRune(p, 0) {
		return "", "", errors.New("invalid: empty or malformed path")
	}
	roots, err := r.roots()
	if err != nil {
		return "", "", err
	}
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") {
			return "", "", errors.New("invalid: UNC paths are not allowed")
		}
		if filepath.VolumeName(p) != "" && !filepath.IsAbs(p) {
			return "", "", errors.New("invalid: drive-relative paths are not allowed")
		}
	}
	if filepath.IsAbs(p) {
		clean := filepath.Clean(p)
		for _, rt := range roots {
			if rl, ok := relUnder(rt, clean); ok {
				root, rel = rt, rl
				break
			}
		}
		if root == "" {
			return "", "", fmt.Errorf("path %s is outside the allowed directory %s", p, roots[0])
		}
	} else {
		root, rel = roots[0], p
	}
	// Dot components (.git, .claude, `..`) are refused in the part below the root.
	for _, comp := range strings.FieldsFunc(rel, func(c rune) bool { return c == '/' || c == '\\' }) {
		if comp == "." {
			continue
		}
		if strings.HasPrefix(comp, ".") {
			return "", "", fmt.Errorf("invalid: path component %q starts with a dot", comp)
		}
	}
	rel = filepath.Clean(filepath.FromSlash(strings.ReplaceAll(rel, `\`, "/")))
	if rel == "." || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", "", errors.New("invalid: path must name a file")
	}
	if r.TokenFile != "" {
		if tf, err := filepath.Abs(r.TokenFile); err == nil && samePath(tf, filepath.Join(root, rel)) {
			return "", "", errors.New("invalid: the token file cannot be used as a file param")
		}
	}
	return root, rel, nil
}

func (r *FileRoots) open(p string) (*os.Root, string, string, error) {
	root, rel, err := r.resolve(p)
	if err != nil {
		return nil, "", "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, "", "", err
	}
	rt, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", "", err
	}
	return rt, root, rel, nil
}

// OpenLocal opens an existing local file for reading (uploads); files larger
// than 512 MiB are refused. It returns the file and its size.
func (r *FileRoots) OpenLocal(path string) (f *os.File, size int64, err error) {
	rt, _, rel, err := r.open(path)
	if err != nil {
		return nil, 0, err
	}
	defer rt.Close()
	f, err = rt.Open(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, fmt.Errorf("not found: %s", path)
		}
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("invalid: %s is not a regular file", path)
	}
	if fi.Size() > maxUpload {
		f.Close()
		return nil, 0, fmt.Errorf("file too large: %d bytes (limit 512 MiB)", fi.Size())
	}
	return f, fi.Size(), nil
}

// SaveFile writes r to a NEW local file (O_CREATE|O_EXCL; existing files are
// never overwritten: `error: exists: <path>`) and returns the final path and
// the number of bytes written.
func (r *FileRoots) SaveFile(path string, src io.Reader) (final string, n int64, err error) {
	rt, root, rel, err := r.open(path)
	if err != nil {
		return "", 0, err
	}
	defer rt.Close()
	final = filepath.Join(root, rel)
	if dir := filepath.Dir(rel); dir != "." {
		if err := rt.MkdirAll(dir, 0o700); err != nil {
			return "", 0, err
		}
	}
	f, err := rt.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", 0, fmt.Errorf("exists: %s", final)
		}
		return "", 0, err
	}
	n, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = rt.Remove(rel)
		return "", 0, err
	}
	return final, n, nil
}
