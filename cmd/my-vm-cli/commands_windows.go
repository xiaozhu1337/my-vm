//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"my-vm/internal/guestops"
)

type commands struct {
	ctx                    context.Context
	client                 *http.Client
	stdout, stderr         io.Writer
	timeout, offset, limit int
	overwrite, wait        bool
	directory              string
}

func (c *commands) request(method, path string, query url.Values, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(c.ctx, method, "http://manager"+path+"?"+query.Encode(), body)
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
	response, err := c.client.Do(request)
	if err != nil {
		if method != http.MethodGet {
			return nil, fmt.Errorf("请求中断，操作结果尚未确认；查询客户机状态或文件后再重试: %w", err)
		}
		return nil, err
	}
	if response.StatusCode >= 300 {
		defer response.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return nil, fmt.Errorf("%s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	return response, nil
}
func (c *commands) show(query url.Values) error {
	response, err := c.request("GET", "/v1/action", query, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var value any
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value); err != nil {
		return err
	}
	encoder := yaml.NewEncoder(c.stdout)
	defer encoder.Close()
	encoder.SetIndent(2)
	return encoder.Encode(value)
}
func (c *commands) execute(args []string) (int, error) {
	if args[0] == "list" && len(args) == 1 {
		return 0, c.show(url.Values{"action": {"list"}, "offset": {strconv.Itoa(c.offset)}, "limit": {strconv.Itoa(c.limit)}})
	}
	if len(args) < 2 {
		return 2, errors.New("缺少虚拟机参数，使用 --help 查看用法")
	}
	switch args[0] {
	case "start", "stop", "kill", "pause", "resume":
		if len(args) != 2 {
			return 2, errors.New("控制命令需要一个虚拟机 ID 或名称")
		}
		query := url.Values{"action": {args[0]}, "vm": {args[1]}}
		response, err := c.request("POST", "/v1/action", query, nil)
		if err != nil {
			return 1, err
		}
		response.Body.Close()
		if c.wait {
			return 0, c.waitState(args[1], args[0])
		}
		fmt.Fprintln(c.stdout, "操作请求已完成")
		return 0, nil
	case "snapshot":
		if len(args) < 3 {
			return 2, errors.New("snapshot 需要动作和虚拟机")
		}
		if args[1] == "list" && len(args) == 3 {
			return 0, c.show(url.Values{"action": {"snapshots"}, "vm": {args[2]}})
		}
		if len(args) != 4 || args[1] != "create" && args[1] != "restore" && args[1] != "delete" {
			return 2, errors.New("snapshot create|restore|delete <VM> <name>")
		}
		response, err := c.request("POST", "/v1/action", url.Values{"action": {"snapshot-" + args[1]}, "vm": {args[2]}, "name": {args[3]}}, nil)
		if err != nil {
			return 1, err
		}
		response.Body.Close()
		fmt.Fprintln(c.stdout, "快照操作完成")
		return 0, nil
	case "exec":
		return c.exec(args)
	case "copy-to", "copy-from":
		return 0, c.copy(args)
	default:
		return 2, errors.New("未知命令，使用 --help 查看用法")
	}
}
func (c *commands) exec(args []string) (int, error) {
	if len(args) < 3 {
		return 2, errors.New("exec <VM> <program> [arguments...]")
	}
	input := guestops.Command{Args: args[2:], Directory: c.directory, Timeout: c.timeout}
	data, err := json.Marshal(input)
	if err != nil {
		return 1, err
	}
	response, err := c.request("POST", "/v1/guest/exec", url.Values{"vm": {args[1]}}, bytes.NewReader(data))
	if err != nil {
		return 1, err
	}
	defer response.Body.Close()
	var result guestops.Result
	if err = json.NewDecoder(io.LimitReader(response.Body, 12<<20)).Decode(&result); err != nil {
		return 1, err
	}
	c.stdout.Write(result.Stdout)
	c.stderr.Write(result.Stderr)
	if result.Truncated {
		fmt.Fprintln(c.stderr, "命令输出超过每个流 4 MiB，已截断")
	}
	if result.ExitCode < 0 {
		return 1, errors.New(result.Error)
	}
	if result.ExitCode > 255 {
		return 1, fmt.Errorf("客户机退出码 %d", result.ExitCode)
	}
	return result.ExitCode, nil
}
func (c *commands) copy(args []string) error {
	if len(args) != 4 {
		return errors.New("copy-to|copy-from <VM> <source-file> <destination-file>")
	}
	query := url.Values{"vm": {args[1]}, "overwrite": {strconv.FormatBool(c.overwrite)}}
	if args[0] == "copy-to" {
		query.Set("path", args[3])
		input, err := os.Open(args[2])
		if err != nil {
			return err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("源文件必须为普通文件")
		}
		response, err := c.request("PUT", "/v1/guest/file", query, input)
		if err != nil {
			return err
		}
		response.Body.Close()
	} else {
		query.Set("path", args[2])
		response, err := c.request("GET", "/v1/guest/file", query, nil)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		target, err := filepath.Abs(args[3])
		if err != nil {
			return err
		}
		if err = guestops.ReceiveFile(target, response.Body, c.overwrite); err != nil {
			return err
		}
	}
	fmt.Fprintln(c.stdout, "文件复制完成")
	return nil
}
func (c *commands) waitState(machine, action string) error {
	wanted := "已关机"
	if action == "start" || action == "resume" {
		wanted = "运行中"
	}
	if action == "pause" {
		wanted = "已暂停"
	}
	for {
		response, err := c.request("GET", "/v1/action", url.Values{"vm": {machine}, "action": {"state"}}, nil)
		if err != nil {
			return err
		}
		var state string
		err = json.NewDecoder(response.Body).Decode(&state)
		response.Body.Close()
		if err != nil {
			return err
		}
		if state == wanted {
			fmt.Fprintln(c.stdout, state)
			return nil
		}
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
