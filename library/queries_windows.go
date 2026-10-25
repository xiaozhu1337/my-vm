//go:build windows

package library

import (
	"context"
	"encoding/json"
)

func clientPages[T any](ctx context.Context, c *Client, r Request) ([]T, error) {
	values := []T{}
	r.Limit = 100
	for {
		data, err := c.Execute(ctx, r)
		if err != nil {
			return nil, err
		}
		var value Page[T]
		if err = json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		values = append(values, value.Items...)
		r.Offset += len(value.Items)
		if r.Offset >= value.Total || len(value.Items) == 0 {
			return values, nil
		}
	}
}

func (c *Client) Snapshots(ctx context.Context, selector string) ([]Snapshot, error) {
	return clientPages[Snapshot](ctx, c, Request{Action: "snapshots", VM: selector})
}
func (c *Client) HostAdapters(ctx context.Context) ([]HostAdapter, error) {
	return clientPages[HostAdapter](ctx, c, Request{Action: "adapters"})
}
func (c *Client) HostNetworks(ctx context.Context) ([]Network, []NetworkRecord, error) {
	items, err := clientPages[NetworkItem](ctx, c, Request{Action: "networks"})
	if err != nil {
		return nil, nil, err
	}
	networks, records := []Network{}, []NetworkRecord{}
	for _, item := range items {
		if !item.Pending {
			networks = append(networks, item.Network)
		}
		if item.Managed {
			records = append(records, NetworkRecord{ID: item.ID, Name: item.Name, Subnet: item.Subnet, Pending: item.Pending})
		}
	}
	return networks, records, nil
}
