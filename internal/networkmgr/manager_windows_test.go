//go:build windows

package networkmgr

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testManager(t *testing.T) (*Manager, *[]Network, *int) {
	t.Helper()
	m := New(t.TempDir())
	networks := []Network{}
	deletes := 0
	m.api = backend{
		list:         func() ([]Network, error) { return append([]Network(nil), networks...), nil },
		hostPrefixes: func() ([]netip.Prefix, error) { return nil, nil },
		create: func(id, name, cidr string) error {
			networks = append(networks, Network{ID: id, Name: name, Type: "NAT", Subnet: cidr})
			return nil
		},
		remove: func(id string) error {
			deletes++
			for i, n := range networks {
				if n.ID == id {
					networks = append(networks[:i], networks[i+1:]...)
					break
				}
			}
			return nil
		},
		hasEndpoints: func(string) (bool, error) { return false, nil },
	}
	return m, &networks, &deletes
}
func TestCreateRegistersBeforeNativeCallAndRecoversFailure(t *testing.T) {
	m, networks, _ := testManager(t)
	native := m.api.create
	m.api.create = func(id, name, cidr string) error {
		records, err := m.records()
		if err != nil || len(records) != 1 || !records[0].Pending || records[0].ID != id {
			t.Fatalf("creation was not journaled: %+v %v", records, err)
		}
		return native(id, name, cidr)
	}
	if err := m.Create("Lab", "192.168.240.0/24"); err != nil {
		t.Fatal(err)
	}
	records, err := m.records()
	if err != nil || len(records) != 1 || records[0].Pending || len(*networks) != 1 {
		t.Fatalf("records=%+v networks=%+v err=%v", records, *networks, err)
	}
	if err := m.Create("lab", "192.168.241.0/24"); err == nil {
		t.Fatal("accepted duplicate name")
	}
	m.api.create = func(string, string, string) error { return errors.New("native failure") }
	if err := m.Create("Failed", "192.168.241.0/24"); err == nil {
		t.Fatal("accepted native failure")
	}
	records, err = m.records()
	if err != nil || len(records) != 2 || !records[1].Pending {
		t.Fatalf("failed creation lost record: %+v %v", records, err)
	}
	if err = m.Delete(records[1].ID, nil); err != nil {
		t.Fatal(err)
	}
	records, err = m.records()
	if err != nil || len(records) != 1 {
		t.Fatalf("pending recovery failed: %+v %v", records, err)
	}
}
func TestNetworkDeleteProtectsIdentityReferencesAndEndpoints(t *testing.T) {
	m, networks, deletes := testManager(t)
	if err := m.Create("Lab", "192.168.240.0/24"); err != nil {
		t.Fatal(err)
	}
	id := (*networks)[0].ID
	for _, bad := range []string{defaultSwitch, "00000000-0000-0000-0000-000000000001"} {
		if m.Delete(bad, nil) == nil {
			t.Fatal("accepted foreign network")
		}
	}
	if m.Delete(id, []string{"{" + strings.ToUpper(id) + "}"}) == nil {
		t.Fatal("accepted configured reference")
	}
	(*networks)[0].Type = "ICS"
	if m.Delete(id, nil) == nil {
		t.Fatal("accepted changed identity")
	}
	(*networks)[0].Type = "NAT"
	(*networks)[0].Subnet = "192.168.241.0/24"
	if m.Delete(id, nil) == nil {
		t.Fatal("accepted changed subnet")
	}
	(*networks)[0].Subnet = "192.168.240.0/24"
	m.api.hasEndpoints = func(string) (bool, error) { return true, nil }
	if m.Delete(id, nil) == nil {
		t.Fatal("accepted active endpoint")
	}
	m.api.hasEndpoints = func(string) (bool, error) { return false, errors.New("enumeration failed") }
	if m.Delete(id, nil) == nil {
		t.Fatal("ignored query failure")
	}
	if *deletes != 0 {
		t.Fatal("called native delete for protected network")
	}
	m.api.hasEndpoints = func(string) (bool, error) { return false, nil }
	if err := m.Delete(id, nil); err != nil {
		t.Fatal(err)
	}
	records, err := m.records()
	if err != nil || len(records) != 0 || *deletes != 1 {
		t.Fatalf("deletion incomplete: %+v %d %v", records, *deletes, err)
	}
}
func TestNetworkCreationRejectsOverlapAndInvalidInput(t *testing.T) {
	for _, cidr := range []string{"0.0.0.0/0", "8.8.8.0/24", "192.168.1.1/24", "192.168.1.0/31", "fd00::/64"} {
		if _, err := validateNetwork("Lab", cidr); err == nil {
			t.Fatalf("accepted %s", cidr)
		}
	}
	m, networks, _ := testManager(t)
	m.api.hostPrefixes = func() ([]netip.Prefix, error) { return []netip.Prefix{netip.MustParsePrefix("192.168.240.0/24")}, nil }
	if m.Create("Lab", "192.168.240.0/24") == nil {
		t.Fatal("accepted host subnet overlap")
	}
	m.api.hostPrefixes = func() ([]netip.Prefix, error) { return nil, nil }
	*networks = append(*networks, Network{ID: defaultSwitch, Name: "Default", Type: "ICS", Subnet: "192.168.240.0/24"})
	if m.Create("Lab", "192.168.240.0/24") == nil {
		t.Fatal("accepted virtual network overlap")
	}
	if _, err := os.Stat(m.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid operation wrote registry: %v", err)
	}
	data, err := networkSettings("Lab", "192.168.241.0/24")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil || doc["Type"] != "NAT" || !strings.Contains(string(data), `"NextHop":"192.168.241.1"`) {
		t.Fatalf("wrong NAT settings %s", data)
	}
}
func TestCorruptRegistryIsNeverOverwritten(t *testing.T) {
	m, _, deletes := testManager(t)
	for _, input := range []string{"invalid: [", "- id: broken\n", "[]\n---\n[]\n", "- unexpected: value\n"} {
		if err := os.WriteFile(m.path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if m.Create("Lab", "192.168.240.0/24") == nil {
			t.Fatal("accepted invalid registry")
		}
		data, err := os.ReadFile(m.path)
		if err != nil || string(data) != input {
			t.Fatal("overwrote invalid registry")
		}
	}
	if *deletes != 0 {
		t.Fatal("unexpected native deletion")
	}
	files, err := os.ReadDir(filepath.Dir(m.path))
	if err != nil || len(files) != 1 {
		t.Fatalf("leaked temporary files: %+v %v", files, err)
	}
}
