package rdpauth

import "testing"

func TestLoginValidationAndVMIdentity(t *testing.T) {
	for _, login := range []*Login{{}, {Username: "myvm", Password: []byte("a\n")}, {Username: "bad\x00", Password: []byte("x")}, {Username: "myvm", Password: []byte{0xff}}} {
		if login.Validate() == nil {
			t.Fatal("invalid login accepted")
		}
	}
	password := []byte("fixture-only")
	login := &Login{Username: "myvm", Password: password}
	if err := login.Validate(); err != nil {
		t.Fatal(err)
	}
	login.Clear()
	for _, value := range password {
		if value != 0 {
			t.Fatal("secret was not erased")
		}
	}
	if Target("VM-A") != Target("vm-a") || Target("vm-a") == Target("vm-b") {
		t.Fatal("unstable or shared VM credential key")
	}
}
