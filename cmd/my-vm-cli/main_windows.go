//go:build windows

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"my-vm/internal/logging"
	vm "my-vm/library"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "serve" {
		if err := serve(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

func serve() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := logging.Open(filepath.Join(filepath.Dir(executable), "logs"))
	if err != nil {
		return err
	}
	defer log.Close()
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	service := vm.NewService(filepath.Join(root, "my-vm"), log)
	server, err := service.Listen()
	if err != nil {
		fmt.Fprintln(log, err)
		return err
	}
	defer server.Close()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	for {
		select {
		case <-server.Done():
			return nil
		case <-signals:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			_, err = service.Execute(ctx, vm.Request{Action: "shutdown-all"})
			cancel()
			if err == nil {
				return nil
			}
			fmt.Fprintln(log, err)
		}
	}
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("my-vm-cli", flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Int("timeout", 60, "操作超时秒数（1..3600），选项放在命令前")
	offset := flags.Int("offset", 0, "列表起始位置")
	limit := flags.Int("limit", 20, "列表条数（1..100）")
	overwrite := flags.Bool("overwrite", false, "允许覆盖文件复制的指定目标")
	directory := flags.String("cwd", "", "客户机命令的绝对工作目录")
	wait := flags.Bool("wait", false, "等待生命周期操作到达目标状态")
	format := flags.String("format", "json", "输出格式：json、yaml、text；exec 的 text 模式直接输出客户机文本")
	flags.Usage = func() {
		fmt.Fprintln(stderr, `my-vm-cli [options] <command>
  list | status | template
  create <machine.yaml> | get <VM> | update <VM> <machine.yaml>
  clone <VM> <new-name> [@current|snapshot-name-or-ID] | remove <VM>（保留磁盘）
  state|start|stop|kill|pause|resume <VM> | shutdown-all
  memory <VM>（只读查询）
  snapshot list <VM> | snapshot create|restore|delete <VM> <name>
  enhancements <VM> | media mount <VM> <host-iso>
  network list | network create <name> <CIDR> | network delete <ID>
  adapters | settings | settings set side-channel-mitigations <true|false>
  exec <VM> <program> [arguments...]
  copy-to <VM> <host-file> <absolute-guest-file>
  copy-from <VM> <absolute-guest-file> <host-file>
  serve（独立运行核心服务，无 GUI）
默认返回 JSON，失败返回非零退出码。VM 支持 ID 或唯一名称。自动连接或启动同用户的后台核心服务。`)
		flags.PrintDefaults()
	}
	failure := func(code int, err error) int {
		selected := *format
		if selected != "yaml" && selected != "text" {
			selected = "json"
		}
		if selected == "text" {
			fmt.Fprintln(stderr, err)
		} else {
			_ = output(stdout, selected, map[string]any{"ok": false, "error": err.Error(), "exit_code": code})
		}
		if executable, e := os.Executable(); e == nil {
			if log, e := logging.Open(filepath.Join(filepath.Dir(executable), "logs")); e == nil {
				fmt.Fprintln(log, err)
				log.Close()
			}
		}
		return code
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return failure(2, err)
	}
	args = flags.Args()
	if len(args) == 0 {
		return failure(2, fmt.Errorf("缺少命令，使用 --help 查看用法"))
	}
	if *timeout < 1 || *timeout > 3600 {
		return failure(2, fmt.Errorf("timeout 必须为 1..3600"))
	}
	if *offset < 0 || *limit < 1 || *limit > 100 {
		return failure(2, fmt.Errorf("offset 必须非负，limit 必须为 1..100"))
	}
	if *format != "json" && *format != "yaml" && *format != "text" {
		return failure(2, fmt.Errorf("format 必须为 json、yaml 或 text"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()
	command := &commands{ctx: ctx, stdout: stdout, stderr: stderr, timeout: *timeout, offset: *offset, limit: *limit, overwrite: *overwrite, directory: *directory, wait: *wait, format: *format}
	code, err := command.execute(args)
	if command.client != nil {
		command.client.Close()
	}
	if err != nil {
		if code == 0 {
			code = 1
		}
		return failure(code, err)
	}
	return code
}
