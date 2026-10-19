//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"my-vm/internal/rdpauth"
	"my-vm/internal/sharing"
	"time"

	"github.com/godbus/dbus/v5"
)

func newDesktopLogin() (*rdpauth.Login, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	defer clear(random[:])
	password := make([]byte, base64.RawURLEncoding.EncodedLen(len(random)))
	base64.RawURLEncoding.Encode(password, random[:])
	return &rdpauth.Login{Username: "myvm", Password: password}, nil
}

type walletCaller interface {
	CallWithContext(context.Context, string, dbus.Flags, ...any) *dbus.Call
}

func configureKDELogin(login *rdpauth.Login) error {
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return errors.New("无法连接当前桌面的钱包服务")
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return registerKDELogin(ctx, connection.Object("org.kde.kwalletd6", "/modules/kwalletd6"), login, func(login *rdpauth.Login) error {
		return sharing.PublishLogin(ctx, login)
	})
}
func registerKDELogin(ctx context.Context, wallet walletCaller, login *rdpauth.Login, publish func(*rdpauth.Login) error) error {
	if err := writeKDELogin(ctx, wallet, login); err != nil {
		return err
	}
	return publish(login)
}
func writeKDELogin(ctx context.Context, wallet walletCaller, login *rdpauth.Login) error {
	if err := login.Validate(); err != nil {
		return err
	}
	call := func(method string, values []any, args ...any) error {
		if err := wallet.CallWithContext(ctx, "org.kde.KWallet."+method, 0, args...).Store(values...); err != nil {
			return errors.New("KDE 钱包操作失败，请确认钱包已启用、已解锁并允许 KRDP 访问")
		}
		return nil
	}
	var name string
	if err := call("networkWallet", []any{&name}); err != nil {
		return err
	}
	if name == "" {
		return errors.New("KDE 没有可用的钱包，请先创建并解锁桌面钱包")
	}
	var handle int32
	if err := call("open", []any{&handle}, name, int64(0), "KRDP"); err != nil {
		return err
	}
	if handle < 0 {
		return errors.New("KDE 钱包授权被取消或钱包未解锁")
	}
	var exists bool
	if err := call("hasFolder", []any{&exists}, handle, "KRDP", "KRDP"); err != nil {
		return err
	}
	if !exists {
		if err := call("createFolder", []any{&exists}, handle, "KRDP", "KRDP"); err != nil {
			return err
		}
		if !exists {
			return errors.New("无法创建 KRDP 钱包文件夹")
		}
	}
	var saved string
	if err := call("readPassword", []any{&saved}, handle, "KRDP", login.Username, "KRDP"); err != nil {
		return err
	}
	if saved != "" {
		previous := &rdpauth.Login{Username: login.Username, Password: []byte(saved)}
		if err := previous.Validate(); err != nil {
			previous.Clear()
			return err
		}
		// Retain the installed password so a failed re-registration cannot break existing login.
		login.Clear()
		login.Password = previous.Password
		return nil
	}
	var code int32
	if err := call("writePassword", []any{&code}, handle, "KRDP", login.Username, string(login.Password), "KRDP"); err != nil {
		return err
	}
	if code != 0 {
		return errors.New("无法保存 KRDP 专用密码")
	}
	if err := call("readPassword", []any{&saved}, handle, "KRDP", login.Username, "KRDP"); err != nil {
		return err
	}
	if saved != string(login.Password) {
		return errors.New("KRDP 钱包密码校验失败")
	}
	return nil
}
