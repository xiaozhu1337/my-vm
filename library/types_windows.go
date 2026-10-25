//go:build windows

// Package library provides the application's business workflows without a GUI dependency.
package library

import (
	"my-vm/internal/guestops"
	"my-vm/internal/hcs"
	"my-vm/internal/networkmgr"
	"my-vm/internal/vm"
)

type Machine = vm.Machine
type Config = vm.Config
type Share = vm.Share
type NetworkAdapter = vm.NetworkAdapter
type COMPort = vm.COMPort
type Snapshot = vm.Snapshot
type Network = networkmgr.Network
type NetworkRecord = networkmgr.Record
type Switch = hcs.Network
type Command = guestops.Command
type CommandResult = guestops.Result

const CloneCurrent = vm.CloneCurrent

var GuestSystems = vm.GuestSystems
var NewMachine = vm.NewMachine
var NewClone = vm.NewClone
var Load = vm.Load
var Save = vm.Save
var DiskPath = vm.DiskPath
var SameMediaFile = vm.SameMediaFile

// Request is shared by embedded callers and the local process transport.
// Machine input is a complete YAML-compatible configuration, never a partial patch.
type Request struct {
	resolvedID string
	Action     string   `json:"action"`
	VM         string   `json:"vm,omitempty"`
	Expected   *Machine `json:"expected,omitempty"`
	Machine    *Machine `json:"machine,omitempty"`
	Snapshot   string   `json:"snapshot,omitempty"`
	Name       string   `json:"name,omitempty"`
	CIDR       string   `json:"cidr,omitempty"`
	Path       string   `json:"path,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	Size       int      `json:"size,omitempty"`
	Enabled    *bool    `json:"enabled,omitempty"`
	Offset     int      `json:"offset,omitempty"`
	Limit      int      `json:"limit,omitempty"`
	Revision   uint64   `json:"revision,omitempty"`
}

type Item struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	State     string    `json:"state"`
	Guest     string    `json:"guest"`
	Progress  *Progress `json:"progress,omitempty"`
	MemoryMiB int       `json:"memory_mib"`
	RuntimeID string    `json:"runtime_id,omitempty"`
}

type Progress struct {
	Action    string `json:"action"`
	Target    string `json:"target,omitempty"`
	Completed int64  `json:"completed"`
	Total     int64  `json:"total"`
}

type Page[T any] struct {
	Total    int    `json:"total"`
	Items    []T    `json:"items"`
	Revision uint64 `json:"revision,omitempty"`
}

type Settings struct {
	Root                   string `json:"root"`
	SideChannelMitigations bool   `json:"side_channel_mitigations"`
}

type HostAdapter struct {
	Name, MAC, Addresses, State string
	MTU                         int
}

type NetworkItem struct {
	Network
	Managed bool `json:"managed"`
	Pending bool `json:"pending"`
}

type DisplayInfo struct {
	Machine   Machine `json:"machine"`
	RuntimeID string  `json:"runtime_id"`
	Paused    bool    `json:"paused"`
}

type ConfigurationPage struct {
	Settings  Config        `json:"settings"`
	Machines  Page[Machine] `json:"machines"`
	Revision  uint64        `json:"revision"`
	Unchanged bool          `json:"unchanged,omitempty"`
}

type HostNetworks struct {
	Networks []Network       `json:"networks"`
	Records  []NetworkRecord `json:"records"`
	Adapters []HostAdapter   `json:"adapters"`
}

func page[T any](items []T, offset, limit int) Page[T] {
	if offset < 0 {
		offset = 0
	}
	if offset > len(items) {
		offset = len(items)
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	end := min(len(items), offset+limit)
	values := append([]T{}, items[offset:end]...)
	return Page[T]{Total: len(items), Items: values}
}
