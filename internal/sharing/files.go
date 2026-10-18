package sharing

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/net/webdav"
)

type Share struct {
	Name     string `yaml:"name" json:"name"`
	Path     string `yaml:"path" json:"-"`
	ReadOnly bool   `yaml:"read_only" json:"read_only"`
}

func (s *Share) UnmarshalYAML(node *yaml.Node) error {
	type plain Share
	value := plain{ReadOnly: true}
	if node.Kind != yaml.MappingNode {
		return errors.New("共享配置必须为映射")
	}
	for i := 0; i < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "name", "path", "read_only":
		default:
			return errors.New("未知共享配置字段: " + node.Content[i].Value)
		}
	}
	if err := node.Decode(&value); err != nil {
		return err
	}
	*s = Share(value)
	return nil
}

var validName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func Validate(shares []Share) error {
	if len(shares) > 16 {
		return errors.New("每台虚拟机最多映射 16 个文件夹")
	}
	names := map[string]bool{}
	for _, s := range shares {
		name := strings.ToLower(s.Name)
		if !validName.MatchString(s.Name) || names[name] {
			return errors.New("共享名称需以英文字母开头，只含字母、数字、下划线或连字符，且不能重复（最长 64 字符）")
		}
		if !filepath.IsAbs(s.Path) {
			return errors.New("共享文件夹必须使用绝对路径")
		}
		names[name] = true
	}
	return nil
}

type rootedFS struct {
	root     *os.Root
	readOnly bool
}

func (f rootedFS) Mkdir(_ context.Context, n string, p os.FileMode) error {
	if f.readOnly {
		return fs.ErrPermission
	}
	return f.root.Mkdir(strings.TrimPrefix(n, "/"), p)
}
func (f rootedFS) OpenFile(ctx context.Context, n string, flags int, p os.FileMode) (webdav.File, error) {
	if f.readOnly && flags&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		return nil, fs.ErrPermission
	}
	n = strings.TrimPrefix(n, "/")
	if n == "" {
		n = "."
	}
	if tx, ok := ctx.Value(uploadKey{}).(*upload); ok && flags&os.O_TRUNC != 0 {
		return tx.open(f.root, n, p)
	}
	if ctx.Value(copyKey{}) != nil && flags&(os.O_WRONLY|os.O_RDWR|os.O_TRUNC) != 0 {
		flags |= os.O_CREATE | os.O_EXCL
	}
	return f.root.OpenFile(n, flags, p)
}

// Clients remove children explicitly; host directory deletion never recurses.
func (f rootedFS) RemoveAll(_ context.Context, n string) error {
	if f.readOnly {
		return fs.ErrPermission
	}
	return f.root.Remove(strings.TrimPrefix(n, "/"))
}
func (f rootedFS) Rename(_ context.Context, a, b string) error {
	if f.readOnly {
		return fs.ErrPermission
	}
	return renameNoReplace(f.root, strings.TrimPrefix(a, "/"), strings.TrimPrefix(b, "/"))
}
func (f rootedFS) Stat(_ context.Context, n string) (os.FileInfo, error) {
	n = strings.TrimPrefix(n, "/")
	if n == "" {
		n = "."
	}
	return f.root.Stat(n)
}
func Open(shares []Share) (http.Handler, func(), error) {
	if err := Validate(shares); err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	var roots []*os.Root
	closeAll := func() {
		for _, r := range roots {
			_ = r.Close()
		}
	}
	for _, s := range shares {
		r, err := os.OpenRoot(s.Path)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		roots = append(roots, r)
		prefix := "/dav/" + s.Name
		h := &webdav.Handler{Prefix: prefix, FileSystem: rootedFS{r, s.ReadOnly}, LockSystem: webdav.NewMemLS()}
		handler := atomicUploads(h, r, prefix)
		if s.ReadOnly {
			readHandler := handler
			handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.Method {
				case "GET", "HEAD", "OPTIONS", "PROPFIND":
					readHandler.ServeHTTP(w, request)
				default:
					http.Error(w, "只读共享不能修改文件", http.StatusForbidden)
				}
			})
		}
		mux.Handle(prefix+"/", handler)
		mux.Handle(prefix, handler)
	}
	return mux, closeAll, nil
}
