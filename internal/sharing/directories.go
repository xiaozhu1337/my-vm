package sharing

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/net/webdav"
)

// directoryFS exposes only the virtual DAV parents; physical files use rootedFS.
type directoryFS struct{ shares []Share }

func (directoryFS) Mkdir(context.Context, string, os.FileMode) error { return fs.ErrPermission }
func (directoryFS) RemoveAll(context.Context, string) error          { return fs.ErrPermission }
func (directoryFS) Rename(context.Context, string, string) error     { return fs.ErrPermission }
func (f directoryFS) Stat(_ context.Context, name string) (os.FileInfo, error) {
	clean := path.Clean("/" + strings.TrimPrefix(name, "/"))
	if clean == "/" || clean == "/dav" {
		return directoryInfo(path.Base(clean)), nil
	}
	for _, s := range f.shares {
		if clean == "/dav/"+s.Name {
			return directoryInfo(s.Name), nil
		}
	}
	return nil, fs.ErrNotExist
}
func (f directoryFS) OpenFile(ctx context.Context, name string, flags int, _ os.FileMode) (webdav.File, error) {
	if flags&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		return nil, fs.ErrPermission
	}
	info, err := f.Stat(ctx, name)
	if err != nil {
		return nil, err
	}
	file := &directoryFile{info: info}
	switch path.Clean("/" + strings.TrimPrefix(name, "/")) {
	case "/":
		file.children = []os.FileInfo{directoryInfo("dav")}
	case "/dav":
		for _, s := range f.shares {
			file.children = append(file.children, directoryInfo(s.Name))
		}
	}
	return file, nil
}

type directoryInfo string

func (i directoryInfo) Name() string     { return string(i) }
func (directoryInfo) Size() int64        { return 0 }
func (directoryInfo) Mode() os.FileMode  { return os.ModeDir | 0555 }
func (directoryInfo) ModTime() time.Time { return time.Time{} }
func (directoryInfo) IsDir() bool        { return true }
func (directoryInfo) Sys() any           { return nil }

type directoryFile struct {
	info     os.FileInfo
	children []os.FileInfo
	offset   int
}

func (f *directoryFile) Stat() (os.FileInfo, error) { return f.info, nil }
func (*directoryFile) Close() error                 { return nil }
func (*directoryFile) Read([]byte) (int, error)     { return 0, io.EOF }
func (*directoryFile) Write([]byte) (int, error)    { return 0, fs.ErrPermission }
func (*directoryFile) Seek(int64, int) (int64, error) {
	return 0, errors.New("cannot seek a virtual directory")
}
func (f *directoryFile) Readdir(count int) ([]os.FileInfo, error) {
	if f.offset == len(f.children) && count > 0 {
		return nil, io.EOF
	}
	end := len(f.children)
	if count > 0 && end-f.offset > count {
		end = f.offset + count
	}
	result := f.children[f.offset:end]
	f.offset = end
	return result, nil
}
