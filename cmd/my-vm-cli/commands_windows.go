//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	vm "my-vm/library"
)

type commands struct {
	ctx                    context.Context
	client                 *vm.Client
	stdout, stderr         io.Writer
	timeout, offset, limit int
	overwrite, wait        bool
	directory, format      string
}

func output(writer io.Writer, format string, value any) error {
	if format == "yaml" {
		encoder := yaml.NewEncoder(writer)
		defer encoder.Close()
		encoder.SetIndent(2)
		return encoder.Encode(value)
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if format == "text" {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(value)
}
func (c *commands) show(value any) error {
	return output(c.stdout, c.format, map[string]any{"ok": true, "data": value})
}

func (c *commands) execute(args []string) (int, error) {
	if len(args) == 0 {
		return 2, errors.New("缺少命令")
	}
	switch args[0] {
	case "exec":
		if len(args) < 3 {
			return 2, errors.New("exec <VM> <program> [arguments...]")
		}
		if err := c.connect(); err != nil {
			return 1, err
		}
		result, err := c.client.Exec(c.ctx, args[1], vm.Command{Args: args[2:], Directory: c.directory, Timeout: c.timeout})
		if err != nil {
			return 1, err
		}
		if c.format == "text" || c.format == "" {
			c.stdout.Write(result.Stdout)
			c.stderr.Write(result.Stderr)
			if result.Truncated {
				fmt.Fprintln(c.stderr, "命令输出超过每个流 4 MiB，已截断")
			}
		} else {
			err = output(c.stdout, c.format, map[string]any{"ok": result.ExitCode == 0, "data": map[string]any{"exit_code": result.ExitCode, "stdout": string(result.Stdout), "stderr": string(result.Stderr), "truncated": result.Truncated, "error": result.Error}})
			if err != nil {
				return 1, err
			}
		}
		if result.ExitCode < 0 {
			return 1, nil
		}
		if result.ExitCode > 255 {
			return 1, nil
		}
		return result.ExitCode, nil
	case "copy-to", "copy-from":
		if len(args) != 4 {
			return 2, errors.New("copy-to|copy-from <VM> <source-file> <destination-file>")
		}
		if err := c.connect(); err != nil {
			return 1, err
		}
		var err error
		if args[0] == "copy-to" {
			err = c.client.CopyTo(c.ctx, args[1], args[2], args[3], c.overwrite)
		} else {
			err = c.client.CopyFrom(c.ctx, args[1], args[2], args[3], c.overwrite)
		}
		if err != nil {
			return 1, err
		}
		return 0, c.show(map[string]string{"destination": args[3]})
	case "template":
		if len(args) != 1 {
			return 2, errors.New("template 不接受参数")
		}
		return 0, c.show(vm.NewMachine())
	}
	request, err := c.action(args)
	if err != nil {
		return 2, err
	}
	if err = c.connect(); err != nil {
		return 1, err
	}
	value, err := c.client.Execute(c.ctx, request)
	if err != nil {
		return 1, err
	}
	if c.wait {
		switch request.Action {
		case "start", "stop", "kill", "pause", "resume":
			if err = c.client.WaitState(c.ctx, request.VM, request.Action); err != nil {
				return 1, err
			}
		}
	}
	var data any
	if err = json.Unmarshal(value, &data); err != nil {
		return 1, err
	}
	return 0, c.show(data)
}

func readMachine(reader io.Reader, machine *vm.Machine) error {
	data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("配置文件不能超过 1 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err = decoder.Decode(&document); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("配置文件只接受一份 YAML 文档")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("虚拟机配置必须为映射")
	}
	root := document.Content[0]
	wrapped, hasData := false, false
	for i := 0; i < len(root.Content); i += 2 {
		hasData = hasData || root.Content[i].Value == "data"
		wrapped = wrapped || root.Content[i].Value == "ok" || root.Content[i].Value == "data"
	}
	if wrapped {
		if !hasData {
			return errors.New("配置导出必须包含 ok: true 和 data")
		}
		// Accept exported CLI results as well as hand-written machine YAML.
		value := struct {
			OK   bool        `yaml:"ok"`
			Data *vm.Machine `yaml:"data"`
		}{Data: machine}
		decoder = yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err = decoder.Decode(&value); err != nil {
			return err
		}
		if !value.OK || value.Data == nil {
			return errors.New("配置导出必须包含 ok: true 和 data")
		}
		return nil
	}
	decoder = yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(machine)
}

func (c *commands) connect() error {
	if c.client != nil {
		return nil
	}
	client, err := vm.Connect(c.ctx)
	c.client = client
	return err
}

func (c *commands) action(args []string) (vm.Request, error) {
	request := vm.Request{Action: args[0], Offset: c.offset, Limit: c.limit}
	bad := func(usage string) (vm.Request, error) { return request, errors.New(usage) }
	switch args[0] {
	case "list", "status", "shutdown-all":
		if len(args) != 1 {
			return bad(args[0] + " 不接受参数")
		}
	case "get", "state", "start", "stop", "kill", "pause", "resume", "remove", "enhancements":
		if len(args) != 2 {
			return bad(args[0] + " <VM>")
		}
		request.VM = args[1]
	case "create", "update":
		want := 2
		if args[0] == "update" {
			want = 3
		}
		if len(args) != want {
			return bad("create <machine.yaml> | update <VM> <machine.yaml>")
		}
		machine := vm.NewMachine()
		if args[0] == "update" {
			request.VM = args[1]
			if err := c.connect(); err != nil {
				return request, err
			}
			data, err := c.client.Execute(c.ctx, vm.Request{Action: "get", VM: request.VM})
			if err != nil {
				return request, err
			}
			if err = json.Unmarshal(data, &machine); err != nil {
				return request, err
			}
			request.Expected = new(vm.Machine)
			if err = json.Unmarshal(data, request.Expected); err != nil {
				return request, err
			}
		}
		file, err := os.Open(args[len(args)-1])
		if err != nil {
			return request, err
		}
		defer file.Close()
		if err = readMachine(file, &machine); err != nil {
			return request, err
		}
		request.Machine = &machine
	case "clone":
		if len(args) != 3 && len(args) != 4 {
			return bad("clone <VM> <new-name> [@current|snapshot-name-or-ID]")
		}
		request.VM, request.Name = args[1], args[2]
		if len(args) == 4 {
			request.Snapshot = args[3]
			if request.Snapshot == "" {
				return bad("复制来源不能为空")
			}
		}
	case "snapshot":
		if len(args) < 3 {
			return bad("snapshot list|create|restore|delete <VM> [name]")
		}
		request.VM = args[2]
		if args[1] == "list" && len(args) == 3 {
			request.Action = "snapshots"
			break
		}
		if len(args) != 4 || args[1] != "create" && args[1] != "restore" && args[1] != "delete" {
			return bad("snapshot list|create|restore|delete <VM> [name]")
		}
		request.Action, request.Name = "snapshot-"+args[1], args[3]
	case "memory":
		if len(args) != 2 {
			return bad("memory <VM>")
		}
		request.VM = args[1]
	case "media":
		if len(args) != 4 || args[1] != "mount" {
			return bad("media mount <VM> <host-iso>")
		}
		request.Action, request.VM, request.Path = "media-mount", args[2], args[3]
	case "network":
		if len(args) == 2 && args[1] == "list" {
			request.Action = "networks"
			break
		}
		if len(args) == 4 && args[1] == "create" {
			request.Action, request.Name, request.CIDR = "network-create", args[2], args[3]
			break
		}
		if len(args) == 3 && args[1] == "delete" {
			request.Action, request.Name = "network-delete", args[2]
			break
		}
		return bad("network list | network create <name> <CIDR> | network delete <ID>")
	case "adapters":
		if len(args) != 1 {
			return bad("adapters 不接受参数")
		}
	case "settings":
		if len(args) == 1 {
			break
		}
		if len(args) != 4 || args[1] != "set" || args[2] != "side-channel-mitigations" {
			return bad("settings | settings set side-channel-mitigations <true|false>")
		}
		enabled, err := strconv.ParseBool(strings.ToLower(args[3]))
		if err != nil {
			return bad("设置值必须为 true 或 false")
		}
		request.Action, request.Enabled = "settings-update", &enabled
	default:
		return bad("未知命令，使用 --help 查看用法")
	}
	return request, nil
}
