package sharing

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"

	"golang.org/x/net/webdav"
)

type uploadKey struct{}
type copyKey struct{}
type upload struct {
	root              *os.Root
	temporary, target string
	file              *os.File
	closeError        error
}

func (u *upload) open(root *os.Root, name string, mode os.FileMode) (webdav.File, error) {
	if u.file != nil {
		return nil, errors.New("重复打开上传目标")
	}
	u.root = root
	u.target = name
	u.temporary = path.Join(path.Dir(name), ".myvm-upload-"+rand.Text())
	f, err := root.OpenFile(u.temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR, mode)
	if err != nil {
		return nil, err
	}
	u.file = f
	return syncedFile{f, u}, nil
}

type syncedFile struct {
	*os.File
	upload *upload
}

func (f syncedFile) Close() error {
	f.upload.closeError = errors.Join(f.Sync(), f.File.Close())
	return f.upload.closeError
}
func atomicUploads(next http.Handler, root *os.Root, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "COPY" || r.Method == "MOVE" {
			// The DAV library deletes overwrite targets before it can know that copying succeeds.
			r.Header.Set("Overwrite", "F")
			if r.Method == "COPY" {
				r = r.WithContext(context.WithValue(r.Context(), copyKey{}, true))
				source := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")
				if source == "" {
					source = "."
				}
				if info, err := root.Stat(source); err == nil && info.IsDir() {
					http.Error(w, "目录请通过客户端逐项复制", http.StatusMethodNotAllowed)
					return
				}
			}
		}
		if r.Method != "PUT" {
			next.ServeHTTP(w, r)
			return
		}
		tx := &upload{}
		defer func() {
			if tx.temporary != "" {
				_ = root.Remove(tx.temporary)
			}
		}()
		result := httptest.NewRecorder()
		next.ServeHTTP(result, r.WithContext(context.WithValue(r.Context(), uploadKey{}, tx)))
		if result.Code >= 200 && result.Code < 300 && tx.file != nil {
			if tx.closeError != nil {
				http.Error(w, "无法保存共享文件: "+tx.closeError.Error(), 500)
				return
			}
			if err := root.Rename(tx.temporary, tx.target); err != nil {
				http.Error(w, "无法提交共享文件: "+err.Error(), 500)
				return
			}
			tx.temporary = ""
		}
		for k, values := range result.Header() {
			w.Header()[k] = values
		}
		w.WriteHeader(result.Code)
		_, _ = w.Write(result.Body.Bytes())
	})
}
