//go:build windows

package hcs

import "testing"

func TestEndpointSettings(t *testing.T) {
	for _, mac := range [][]string{nil, {""}, {"02:ab:00:00:00:01"}, {"02-AB-00-00-00-01"}} {
		settings, err := endpointSettings("switch", "owner", mac...)
		if err != nil {
			t.Fatal(err)
		}
		if settings["Owner"] != "owner" || settings["HostComputeNetwork"] != "switch" {
			t.Fatal("endpoint ownership changed", settings)
		}
		if len(mac) == 0 || mac[0] == "" {
			if _, exists := settings["MacAddress"]; exists {
				t.Fatal("automatic MAC must be left to HCN", settings)
			}
		} else if settings["MacAddress"] != "02-AB-00-00-00-01" {
			t.Fatal("MAC was not normalized for HCN", settings)
		}
	}
	for _, mac := range [][]string{{"invalid"}, {"01:00:00:00:00:01"}, {"ff:ff:ff:ff:ff:ff"}, {"00:00:00:00:00:00"}, {"02:00:00:00:00:00:00:01"}, {"", ""}} {
		if _, err := endpointSettings("switch", "owner", mac...); err == nil {
			t.Fatal("invalid MAC accepted", mac)
		}
	}
}
