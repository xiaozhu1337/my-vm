//go:build linux

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

func installEnhancements() error {
	if os.Getenv("XDG_SESSION_TYPE") != "wayland" {
		return errors.New("请在需要增强能力的 GNOME/KDE Wayland 桌面终端中安装")
	}
	kind, err := waylandDesktop(os.Getenv("XDG_CURRENT_DESKTOP"))
	if err != nil {
		return err
	}
	if _, _, err = linuxPayload(mediaRoot); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Println("验证离线包完成，正在请求管理员授权安装系统组件；不访问在线仓库。")
	if err = guestCommand("pkexec", exe, "--prepare", "--media", mediaRoot, "--desktop", kind); err != nil {
		return err
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	directory := filepath.Join(root, "MyVMGuestTools", "tls")
	cert, key, err := desktopCertificate(directory)
	if err != nil {
		return err
	}
	if kind == "kde" {
		return configureKDE(root, cert, key)
	}
	fmt.Println("GNOME 增强会话将新增远程虚拟显示器，分辨率随窗口调整；原主屏应用不保证自动迁移。")
	// Display Control can resize GNOME's virtual monitor, but not a mirrored physical display.
	if err = guestCommand("gsettings", "set", "org.gnome.desktop.remote-desktop.rdp", "screen-share-mode", "extend"); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"rdp", "set-tls-cert", cert}, {"rdp", "set-tls-key", key}, {"rdp", "disable-view-only"},
	} {
		if err = guestCommand("grdctl", args...); err != nil {
			return err
		}
	}
	if err = configureGNOMELogin(); err != nil {
		return err
	}
	if err = guestCommand("grdctl", "rdp", "enable"); err != nil {
		return err
	}
	return guestCommand("systemctl", "--user", "enable", "--now", "gnome-remote-desktop.service")
}

func configureGNOMELogin() error {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	defer clear(secret[:])
	password := base64.RawURLEncoding.EncodeToString(secret[:])
	command := exec.Command("grdctl", "rdp", "set-credentials", "myvm")
	// One password line avoids grdctl's per-prompt buffered stdin; never pass secrets in argv.
	command.Stdin = strings.NewReader(password + "\n")
	if err := command.Run(); err != nil {
		return fmt.Errorf("配置 GNOME 专用连接凭据失败，请确认当前桌面密钥环已解锁: %w", err)
	}
	fmt.Println("已自动配置 GNOME 专用连接凭据，无需设置或提供 Linux 系统账户密码。")
	return nil
}

func desktopCertificate(directory string) (string, string, error) {
	cert, key := filepath.Join(directory, "desktop.crt"), filepath.Join(directory, "desktop.key")
	if pair, err := tls.LoadX509KeyPair(cert, key); err == nil {
		if parsed, err := x509.ParseCertificate(pair.Certificate[0]); err == nil && time.Until(parsed.NotAfter) > 30*24*time.Hour {
			return cert, key, nil
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", "", err
	}
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "MyVM desktop"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(2, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		return "", "", err
	}
	if err = os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)}), 0600); err != nil {
		return "", "", err
	}
	if err = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		return "", "", err
	}
	return cert, key, nil
}

func configureKDE(root, cert, key string) error {
	if _, err := exec.LookPath("krdpserver"); err != nil {
		return err
	}
	current, err := user.Current()
	if err != nil {
		return err
	}
	for _, item := range [][2]string{{"ListenPort", "43389"}, {"SystemUserEnabled", "true"}, {"Certificate", cert}, {"CertificateKey", key}} {
		if err = guestCommand("kwriteconfig6", "--file", "krdpserverrc", "--group", "General", "--key", item[0], item[1]); err != nil {
			return err
		}
	}
	directory := filepath.Join(root, "systemd", "user", "app-org.kde.krdpserver.service.d")
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	override := "[Service]\nExecStart=\nExecStart=/usr/bin/krdpserver --address 127.0.0.1\n"
	if err = os.WriteFile(filepath.Join(directory, "my-vm.conf"), []byte(override), 0600); err != nil {
		return err
	}
	if err = guestCommand("gdbus", "call", "--session", "--dest", "org.freedesktop.impl.portal.PermissionStore",
		"--object-path", "/org/freedesktop/impl/portal/PermissionStore", "--method", "org.freedesktop.impl.portal.PermissionStore.SetPermission",
		"kde-authorized", "true", "remote-desktop", "org.kde.krdpserver", "['yes']"); err != nil {
		return err
	}
	if err = guestCommand("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err = guestCommand("systemctl", "--user", "enable", "--now", "app-org.kde.krdpserver.service"); err != nil {
		return err
	}
	if err = guestCommand("systemctl", "--user", "restart", "app-org.kde.krdpserver.service"); err != nil {
		return err
	}
	fmt.Printf("KDE 增强桌面已配置；连接时使用当前系统账户 %s 及其密码。保持 Plasma 桌面已登录。\n", current.Username)
	return nil
}
