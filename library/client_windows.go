//go:build windows

package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"my-vm/internal/control"
	"my-vm/internal/guestops"
)

// Client calls the same Service used by embedded applications. State and Memory
// return the latest Refresh snapshot so GUI painting never performs I/O.
type Client struct {
	http  *http.Client
	mu    sync.RWMutex
	items map[string]Item
}

func NewClient(transport *http.Client) *Client {
	return &Client{http: transport, items: map[string]Item{}}
}
func Connect(ctx context.Context) (*Client, error) {
	transport, err := control.Client(ctx)
	if err != nil {
		return nil, err
	}
	return NewClient(transport), nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, "http://manager"+path+"?"+query.Encode(), body)
	if err != nil {
		return nil, err
	}
	if file, ok := body.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		request.ContentLength = info.Size()
	}
	response, err := c.http.Do(request)
	if err != nil {
		if method != http.MethodGet {
			return nil, fmt.Errorf("请求中断，操作结果尚未确认；查询状态后再重试: %w", err)
		}
		return nil, err
	}
	if response.StatusCode >= 300 {
		defer response.Body.Close()
		var failure struct {
			Error string `json:"error"`
		}
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" {
			return nil, fmt.Errorf("%s", failure.Error)
		}
		return nil, fmt.Errorf("%s: %s", response.Status, string(data))
	}
	return response, nil
}

// Execute uses typed Request input and returns the operation's JSON value.
func (c *Client) Execute(ctx context.Context, input Request) (json.RawMessage, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	response, err := c.request(ctx, http.MethodPost, "/v1/action", nil, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var value json.RawMessage
	err = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&value)
	return value, err
}

func (c *Client) Configuration(ctx context.Context) (Config, error) {
	var result Config
	for offset := 0; ; {
		data, err := c.Execute(ctx, Request{Action: "config", Offset: offset, Limit: 100})
		if err != nil {
			return result, err
		}
		var value struct {
			Settings Config        `json:"settings"`
			Machines Page[Machine] `json:"machines"`
		}
		if err = json.Unmarshal(data, &value); err != nil {
			return result, err
		}
		result.SideChannelMitigations, result.QEMUDir = value.Settings.SideChannelMitigations, value.Settings.QEMUDir
		result.Machines = append(result.Machines, value.Machines.Items...)
		offset += len(value.Machines.Items)
		if offset >= value.Machines.Total || len(value.Machines.Items) == 0 {
			return result, nil
		}
	}
}

func (c *Client) Refresh(ctx context.Context) error {
	items := map[string]Item{}
	for offset := 0; ; {
		data, err := c.Execute(ctx, Request{Action: "list", Offset: offset, Limit: 100})
		if err != nil {
			return err
		}
		var value Page[Item]
		if err = json.Unmarshal(data, &value); err != nil {
			return err
		}
		for _, item := range value.Items {
			items[item.ID] = item
		}
		offset += len(value.Items)
		if offset >= value.Total || len(value.Items) == 0 {
			break
		}
	}
	c.mu.Lock()
	c.items = items
	c.mu.Unlock()
	return nil
}
func (c *Client) State(id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if item, ok := c.items[id]; ok {
		return item.State
	}
	return "已关机"
}
func (c *Client) Memory(id string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.items[id].MemoryMiB
}
func (c *Client) Progress(id string) *Progress {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for machine, item := range c.items {
		if item.Progress != nil && (machine == id || item.Progress.Target == id) {
			copy := *item.Progress
			return &copy
		}
	}
	return nil
}
func (c *Client) Active() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, item := range c.items {
		if item.State != "已关机" {
			return true
		}
	}
	return false
}

func (c *Client) WaitState(ctx context.Context, selector, action string) error {
	wanted := "已关机"
	if action == "start" || action == "resume" {
		wanted = "运行中"
	}
	if action == "pause" {
		wanted = "已暂停"
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := c.Execute(ctx, Request{Action: "state", VM: selector})
		if err != nil {
			return err
		}
		var state string
		if err = json.Unmarshal(data, &state); err != nil {
			return err
		}
		if state == wanted {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Client) Exec(ctx context.Context, selector string, input Command) (CommandResult, error) {
	var result CommandResult
	body, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	response, err := c.request(ctx, http.MethodPost, "/v1/guest/exec", url.Values{"vm": {selector}}, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	err = json.NewDecoder(io.LimitReader(response.Body, 12<<20)).Decode(&result)
	return result, err
}

// CopyTo and CopyFrom stream binary data and preserve existing files by default.
func (c *Client) CopyTo(ctx context.Context, selector, source, target string, overwrite bool) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("源文件必须为普通文件")
	}
	response, err := c.request(ctx, http.MethodPut, "/v1/guest/file", url.Values{"vm": {selector}, "path": {target}, "overwrite": {fmt.Sprint(overwrite)}}, input)
	if err != nil {
		return err
	}
	return response.Body.Close()
}
func (c *Client) CopyFrom(ctx context.Context, selector, source, target string, overwrite bool) error {
	response, err := c.request(ctx, http.MethodGet, "/v1/guest/file", url.Values{"vm": {selector}, "path": {source}}, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	return guestops.ReceiveFile(absolute, response.Body, overwrite)
}
