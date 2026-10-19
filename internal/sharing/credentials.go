package sharing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"my-vm/internal/rdpauth"
	"net"
	"net/http"
	"time"
)

func credentialHandler(save func(*rdpauth.Login) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if save == nil {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var login rdpauth.Login
		defer login.Clear()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&login)
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				err = errors.New("trailing data")
			}
		}
		if err != nil || login.Validate() != nil {
			http.Error(w, "invalid desktop credentials", 400)
			return
		}
		// Identity is captured by the host's VM-specific listener, never supplied in this body.
		if err = save(&login); err != nil {
			http.Error(w, "credential store unavailable", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func PublishLogin(ctx context.Context, login *rdpauth.Login) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dialHost(ctx) }, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	return publishLogin(ctx, &http.Client{Transport: transport, Timeout: 8 * time.Second}, login)
}
func publishLogin(ctx context.Context, client *http.Client, login *rdpauth.Login) error {
	if err := login.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(login)
	if err != nil {
		return errors.New("cannot encode desktop credentials")
	}
	defer clear(data)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://my-vm-host/rdp-credentials", bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("无法回传增强登录凭据，请使用新版虚拟机软件启动客户机后重新安装增强功能")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("宿主保存增强登录凭据失败 (HTTP %d)，请更新虚拟机软件并重新启动客户机后重试", response.StatusCode)
	}
	return nil
}
