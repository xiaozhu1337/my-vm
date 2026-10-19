//go:build windows

package rdpauth

import (
	"testing"
	"unicode/utf16"
	"unsafe"
)

func TestNativeCredentialLayout(t *testing.T) {
	var c nativeCredential
	if unsafe.Sizeof(c) != 80 || unsafe.Offsetof(c.Blob) != 40 || unsafe.Offsetof(c.UserName) != 72 {
		t.Fatal("CREDENTIALW layout does not match Windows x64")
	}
}
func TestManagedLoginIsStoredPerVMAndRotates(t *testing.T) {
	// Never use real VM records in tests.
	first, second := t.TempDir(), t.TempDir()
	defer Delete(Target(first))
	defer Delete(Target(second))
	for _, secret := range []string{"fixture-one", "fixture-two"} {
		login := &Login{Username: "myvm", Password: []byte(secret)}
		if err := Save(first, login); err != nil {
			t.Fatal(err)
		}
		login.Clear()
		name, password, err := Read(Target(first))
		if err != nil || name != "myvm" || string(utf16.Decode(password)) != secret {
			t.Fatal("incorrect saved credential", err)
		}
		clear(password)
		_, other, err := Read(Target(second))
		if err != nil || other != nil {
			t.Fatal("credentials crossed VM identities", err)
		}
	}
}
