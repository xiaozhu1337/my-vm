//go:build windows

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"my-vm/internal/control"
	"my-vm/internal/logging"
)

func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	if code != 0 {
		if exe, err := os.Executable(); err == nil {
			if log, err := logging.Open(filepath.Join(filepath.Dir(exe), "logs")); err == nil {
				fmt.Fprintf(log, "命令行退出码 %d\n", code)
				log.Close()
			}
		}
		os.Exit(code)
	}
}
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("my-vm-cli", flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Int("timeout", 60, "操作超时秒数（1..3600），选项放在命令前")
	offset := flags.Int("offset", 0, "列表起始位置")
	limit := flags.Int("limit", 20, "列表条数（最多100）")
	overwrite := flags.Bool("overwrite", false, "复制文件时允许覆盖指定目标")
	directory := flags.String("cwd", "", "客户机命令的绝对工作目录")
	wait := flags.Bool("wait", false, "启动/停止后等待目标状态")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "my-vm-cli [options] <command>\n  list\n  start|stop|kill|pause|resume <VM ID or name>\n  snapshot list <VM>\n  snapshot create|restore|delete <VM> <name>\n  exec <VM> <program> [arguments...]\n  copy-to <VM> <host-file> <absolute-guest-file>\n  copy-from <VM> <absolute-guest-file> <host-file>\n命令行会连接同用户的管理器；未运行时自动启动管理器。客户机操作需要已安装并运行增强工具。")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	args = flags.Args()
	if len(args) == 0 {
		flags.Usage()
		return 2
	}
	if *timeout < 1 || *timeout > 3600 {
		fmt.Fprintln(stderr, "timeout 必须为 1..3600")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()
	client, err := control.Client(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer client.CloseIdleConnections()
	command := &commands{ctx: ctx, client: client, stdout: stdout, stderr: stderr, timeout: *timeout, offset: *offset, limit: *limit, overwrite: *overwrite, directory: *directory, wait: *wait}
	code, err := command.execute(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return code
}
