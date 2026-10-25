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
	"reflect"
	"sync"
	"time"

	"my-vm/internal/control"
	"my-vm/internal/guestops"
)

// Client calls the same Service used by embedded applications. State and Memory
// return the latest Refresh snapshot so GUI painting never performs I/O.
type Client struct {
	http           *http.Client
	mu             sync.RWMutex
	items          map[string]Item
	itemsRevision  uint64
	stateRevision  uint64
	configMu       sync.Mutex
	configuration  Config
	configRevision uint64
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
	c.configMu.Lock()
	defer c.configMu.Unlock()
	return c.loadConfiguration(ctx)
}

// RefreshConfiguration avoids retransmitting unchanged configuration during GUI polling.
func (c *Client) RefreshConfiguration(ctx context.Context) (Config, error) {
	if err := c.Refresh(ctx); err != nil {
		return Config{}, err
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	c.mu.RLock()
	revision := c.itemsRevision
	c.mu.RUnlock()
	if revision != 0 && revision == c.configRevision {
		return cloneConfig(c.configuration), nil
	}
	return c.loadConfiguration(ctx)
}

func (c *Client) loadConfiguration(ctx context.Context) (Config, error) {
	var result Config
	var revision uint64
	for offset, retries := 0, 0; ; {
		known := uint64(0)
		if offset == 0 {
			known = c.configRevision
		}
		data, err := c.Execute(ctx, Request{Action: "config", Offset: offset, Limit: 100, Revision: known})
		if err != nil {
			return result, err
		}
		var value ConfigurationPage
		if err = json.Unmarshal(data, &value); err != nil {
			return result, err
		}
		if value.Unchanged && value.Revision == c.configRevision {
			return cloneConfig(c.configuration), nil
		}
		if offset > 0 && revision != value.Revision {
			if retries >= 2 {
				return Config{}, fmt.Errorf("配置在分页期间持续变化，请重试")
			}
			result, offset, retries = Config{}, 0, retries+1
			continue
		}
		revision = value.Revision
		result.SideChannelMitigations, result.QEMUDir = value.Settings.SideChannelMitigations, value.Settings.QEMUDir
		result.Machines = append(result.Machines, value.Machines.Items...)
		offset += len(value.Machines.Items)
		if offset >= value.Machines.Total || len(value.Machines.Items) == 0 {
			c.configuration, c.configRevision = cloneConfig(result), revision
			return result, nil
		}
	}
}

func (c *Client) Refresh(ctx context.Context) error {
	items := map[string]Item{}
	var revision uint64
	for offset, retries := 0, 0; ; {
		data, err := c.Execute(ctx, Request{Action: "list", Offset: offset, Limit: 100})
		if err != nil {
			return err
		}
		var value Page[Item]
		if err = json.Unmarshal(data, &value); err != nil {
			return err
		}
		if offset > 0 && revision != value.Revision {
			if retries >= 2 {
				return fmt.Errorf("配置在状态分页期间持续变化，请重试")
			}
			items, offset, retries = map[string]Item{}, 0, retries+1
			continue
		}
		revision = value.Revision
		for _, item := range value.Items {
			items[item.ID] = item
		}
		offset += len(value.Items)
		if offset >= value.Total || len(value.Items) == 0 {
			break
		}
	}
	c.mu.Lock()
	if !reflect.DeepEqual(c.items, items) {
		c.stateRevision++
	}
	c.items, c.itemsRevision = items, revision
	c.mu.Unlock()
	return nil
}

// StateRevision changes when the cached list, runtime state, memory or progress changes.
func (c *Client) StateRevision() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stateRevision
}

func (c *Client) State(id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if item, ok := c.items[id]; ok {
		return item.State
	}
	return "已关机"
}
func (c *Client) RuntimeID(id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.items[id].RuntimeID
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
