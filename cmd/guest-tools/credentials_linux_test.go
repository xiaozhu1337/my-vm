//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"my-vm/internal/rdpauth"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

type fakeWallet struct {
	testing        *testing.T
	fail, readback string
	secret         []byte
	wrote          bool
	writes         int
}

func (f *fakeWallet) CallWithContext(_ context.Context, method string, _ dbus.Flags, args ...any) *dbus.Call {
	method = strings.TrimPrefix(method, "org.kde.KWallet.")
	if method == f.fail {
		return &dbus.Call{Err: errors.New("rejected")}
	}
	var result any
	switch method {
	case "networkWallet":
		result = "fixture-wallet"
	case "open":
		if len(args) != 3 || args[0] != "fixture-wallet" || args[1] != int64(0) || args[2] != "KRDP" {
			f.testing.Fatal("wrong wallet identity or DBus types")
		}
		result = int32(42)
	case "hasFolder":
		result = false
	case "createFolder":
		result = true
	case "writePassword":
		if len(args) != 5 || args[0] != int32(42) || args[1] != "KRDP" || args[2] != "myvm" || args[4] != "KRDP" {
			f.testing.Fatal("wrong QtKeychain storage")
		}
		f.secret = []byte(args[3].(string))
		f.wrote = true
		f.writes++
		result = int32(0)
	case "readPassword":
		result = string(f.secret)
		if f.readback != "" && f.wrote {
			result = f.readback
		}
	default:
		f.testing.Fatal("unexpected wallet method", method)
	}
	return &dbus.Call{Body: []any{result}}
}
func TestKDEManagedCredentialsUseQtKeychainWallet(t *testing.T) {
	login, err := newDesktopLogin()
	if err != nil {
		t.Fatal(err)
	}
	defer login.Clear()
	second, err := newDesktopLogin()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Clear()
	if len(login.Password) != 43 || bytes.Equal(login.Password, second.Password) {
		t.Fatal("weak or reused random credentials")
	}
	wallet := &fakeWallet{testing: t}
	if err = writeKDELogin(context.Background(), wallet, login); err != nil {
		t.Fatal(err)
	}
	defer clear(wallet.secret)
	if !wallet.wrote || !bytes.Equal(wallet.secret, login.Password) {
		t.Fatal("wallet did not receive independent password")
	}
	for _, method := range []string{"networkWallet", "open", "hasFolder", "createFolder", "writePassword", "readPassword"} {
		failed := &fakeWallet{testing: t, fail: method}
		err = writeKDELogin(context.Background(), failed, login)
		clear(failed.secret)
		if err == nil || strings.Contains(err.Error(), string(login.Password)) {
			t.Fatal("wallet failure hidden or password logged", method)
		}
	}
	wrong := &fakeWallet{testing: t, readback: "wrong"}
	err = writeKDELogin(context.Background(), wrong, login)
	clear(wrong.secret)
	if err == nil {
		t.Fatal("wallet write was not checked")
	}
	if err = writeKDELogin(context.Background(), wallet, &rdpauth.Login{}); err == nil {
		t.Fatal("empty login reached wallet")
	}
}

func TestFailedKDERegistrationRetainsInstalledPassword(t *testing.T) {
	wallet := &fakeWallet{testing: t, secret: []byte("existing-managed-secret")}
	defer clear(wallet.secret)
	for range 2 {
		login, err := newDesktopLogin()
		if err != nil {
			t.Fatal(err)
		}
		failure := errors.New("host store unavailable")
		err = registerKDELogin(context.Background(), wallet, login, func(got *rdpauth.Login) error {
			if !bytes.Equal(got.Password, wallet.secret) {
				t.Fatal("retry generated another password")
			}
			return failure
		})
		login.Clear()
		if !errors.Is(err, failure) || wallet.writes != 0 || string(wallet.secret) != "existing-managed-secret" {
			t.Fatal("failed registration changed installed credentials", err)
		}
	}
}
