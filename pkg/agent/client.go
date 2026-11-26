/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package agent

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
	pb "github.com/klotho-k8s/cluster-api-provider-hyperv/proto/hypervagent/v1"
)

// Client implements hyperv.Client by calling a remote Hyper-V host agent over
// gRPC. It is what the controller uses when driving Hyper-V through the agent
// instead of local PowerShell. It satisfies hyperv.Client.
type Client struct {
	rpc  pb.HyperVAgentClient
	conn *grpc.ClientConn
}

var _ hyperv.Client = (*Client)(nil)

// NewClient wraps an existing gRPC connection. The caller owns dialing (see
// Dial) so the connection, TLS, and token credentials are configured in one
// place.
func NewClient(rpc pb.HyperVAgentClient) *Client {
	return &Client{rpc: rpc}
}

// Close closes the underlying connection when Client owns it (created via Dial).
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// fromStatus maps gRPC NotFound back to hyperv.ErrVMNotFound so callers that
// use errors.Is keep working across the transport.
func fromStatus(err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.NotFound {
		return hyperv.ErrVMNotFound
	}
	return err
}

func (c *Client) Exists(ctx context.Context, name string) (bool, error) {
	resp, err := c.rpc.Exists(ctx, &pb.ExistsRequest{Name: name})
	if err != nil {
		return false, fromStatus(err)
	}
	return resp.GetExists(), nil
}

func (c *Client) GetVM(ctx context.Context, name string) (*hyperv.VM, error) {
	resp, err := c.rpc.GetVM(ctx, &pb.GetVMRequest{Name: name})
	if err != nil {
		return nil, fromStatus(err)
	}
	return vmFromProto(resp.GetVm()), nil
}

func (c *Client) CreateVM(ctx context.Context, in hyperv.CreateVMInput) (*hyperv.VM, error) {
	resp, err := c.rpc.CreateVM(ctx, createInputToProto(in))
	if err != nil {
		return nil, fromStatus(err)
	}
	return vmFromProto(resp.GetVm()), nil
}

func (c *Client) StartVM(ctx context.Context, name string) error {
	_, err := c.rpc.StartVM(ctx, &pb.NameRequest{Name: name})
	return fromStatus(err)
}

func (c *Client) StopVM(ctx context.Context, name string, force bool) error {
	_, err := c.rpc.StopVM(ctx, &pb.StopVMRequest{Name: name, Force: force})
	return fromStatus(err)
}

func (c *Client) RemoveVM(ctx context.Context, name string) error {
	_, err := c.rpc.RemoveVM(ctx, &pb.NameRequest{Name: name})
	return fromStatus(err)
}

// DeleteVM invokes the single server-side teardown RPC. The controller makes
// this one call to delete a VM; the agent finishes the stop+destroy even if this
// connection drops the instant the VM (and the controller riding it) stops.
func (c *Client) DeleteVM(ctx context.Context, name string) error {
	_, err := c.rpc.DeleteVM(ctx, &pb.NameRequest{Name: name})
	return fromStatus(err)
}

func (c *Client) SetFirmware(ctx context.Context, in hyperv.FirmwareInput) error {
	_, err := c.rpc.SetFirmware(ctx, &pb.SetFirmwareRequest{
		Name:             in.Name,
		EnableSecureBoot: in.EnableSecureBoot,
	})
	return fromStatus(err)
}

func (c *Client) AttachCloudInit(ctx context.Context, name string, userData []byte) error {
	_, err := c.rpc.AttachCloudInit(ctx, &pb.AttachCloudInitRequest{Name: name, UserData: userData})
	return fromStatus(err)
}

func (c *Client) SetVLAN(ctx context.Context, name string, vlanID int) error {
	_, err := c.rpc.SetVLAN(ctx, &pb.SetVLANRequest{Name: name, VlanId: int32(vlanID)})
	return fromStatus(err)
}

func (c *Client) GetIPAddresses(ctx context.Context, name string) ([]string, error) {
	resp, err := c.rpc.GetIPAddresses(ctx, &pb.NameRequest{Name: name})
	if err != nil {
		return nil, fromStatus(err)
	}
	return resp.GetIpAddresses(), nil
}

// Dial connects to a Hyper-V agent at endpoint using the given dial options
// (TLS + token credentials; see TokenCredentials and ClientTLS) and returns a
// Client that owns and will Close the connection.
func Dial(endpoint string, opts ...grpc.DialOption) (*Client, error) {
	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("agent: dial %s: %w", endpoint, err)
	}
	return &Client{rpc: pb.NewHyperVAgentClient(conn), conn: conn}, nil
}
