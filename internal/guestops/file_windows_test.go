//go:build windows

package guestops

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestReceiveFilePreservesRestrictedDACL(t *testing.T) {
	for _, protected := range []bool{true, false} {
		t.Run(strconv.FormatBool(protected), func(t *testing.T) { testReceiveDACL(t, protected) })
	}
}

func testReceiveDACL(t *testing.T, protected bool) {
	target := filepath.Join(t.TempDir(), "restricted.txt")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	protection := windows.SECURITY_INFORMATION(windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if protected {
		protection = windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err = windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|protection, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	before, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := receiveTemporary(target, true)
	if err != nil {
		t.Fatal(err)
	}
	during, err := windows.GetSecurityInfo(windows.Handle(temporary.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	temporary.Close()
	os.Remove(temporary.Name())
	if err != nil || strings.ReplaceAll(during.String(), "AI", "") != strings.ReplaceAll(before.String(), "AI", "") {
		t.Fatalf("temporary DACL changed: %v, %v, %v", before, during, err)
	}
	if err = ReceiveFile(target, strings.NewReader("new"), true); err != nil {
		t.Fatal(err)
	}
	after, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || strings.ReplaceAll(after.String(), "AI", "") != strings.ReplaceAll(before.String(), "AI", "") {
		t.Fatalf("published DACL changed: %v, %v, %v", before, after, err)
	}
}
