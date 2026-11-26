/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

// Package agent provides the gRPC transport between the Cluster API Hyper-V
// controller and the Hyper-V host agent. Server wraps a hyperv.Client (the
// PowerShell implementation, on the host) and exposes it over gRPC; Client
// implements hyperv.Client by calling a remote agent, so the controller can
// drive Hyper-V without executing PowerShell itself.
package agent

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
	pb "github.com/klotho-k8s/cluster-api-provider-hyperv/proto/hypervagent/v1"
)

// Server implements pb.HyperVAgentServer by delegating to a hyperv.Client. It
// runs inside the host agent daemon, where the client is a PowerShellClient.
type Server struct {
	pb.UnimplementedHyperVAgentServer
	client hyperv.Client
}

// NewServer returns a Server backed by the given Hyper-V client.
func NewServer(client hyperv.Client) *Server {
	return &Server{client: client}
}

// toStatus maps hyperv errors to gRPC status codes so the client can
// reconstruct them (ErrVMNotFound -> NotFound).
func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, hyperv.ErrVMNotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func (s *Server) Exists(ctx context.Context, r *pb.ExistsRequest) (*pb.ExistsResponse, error) {
	ok, err := s.client.Exists(ctx, r.GetName())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ExistsResponse{Exists: ok}, nil
}

func (s *Server) GetVM(ctx context.Context, r *pb.GetVMRequest) (*pb.VMResponse, error) {
	vm, err := s.client.GetVM(ctx, r.GetName())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.VMResponse{Vm: vmToProto(vm)}, nil
}

func (s *Server) CreateVM(ctx context.Context, r *pb.CreateVMRequest) (*pb.VMResponse, error) {
	vm, err := s.client.CreateVM(ctx, createInputFromProto(r))
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.VMResponse{Vm: vmToProto(vm)}, nil
}

func (s *Server) StartVM(ctx context.Context, r *pb.NameRequest) (*pb.Empty, error) {
	return &pb.Empty{}, toStatus(s.client.StartVM(ctx, r.GetName()))
}

func (s *Server) StopVM(ctx context.Context, r *pb.StopVMRequest) (*pb.Empty, error) {
	return &pb.Empty{}, toStatus(s.client.StopVM(ctx, r.GetName(), r.GetForce()))
}

func (s *Server) RemoveVM(ctx context.Context, r *pb.NameRequest) (*pb.Empty, error) {
	return &pb.Empty{}, toStatus(s.client.RemoveVM(ctx, r.GetName()))
}

func (s *Server) SetFirmware(ctx context.Context, r *pb.SetFirmwareRequest) (*pb.Empty, error) {
	return &pb.Empty{}, toStatus(s.client.SetFirmware(ctx, hyperv.FirmwareInput{
		Name:             r.GetName(),
		EnableSecureBoot: r.GetEnableSecureBoot(),
	}))
}

func (s *Server) AttachCloudInit(ctx context.Context, r *pb.AttachCloudInitRequest) (*pb.Empty, error) {
	return &pb.Empty{}, toStatus(s.client.AttachCloudInit(ctx, r.GetName(), r.GetUserData()))
}

func (s *Server) SetVLAN(ctx context.Context, r *pb.SetVLANRequest) (*pb.Empty, error) {
	return &pb.Empty{}, toStatus(s.client.SetVLAN(ctx, r.GetName(), int(r.GetVlanId())))
}

func (s *Server) GetIPAddresses(ctx context.Context, r *pb.NameRequest) (*pb.IPAddressesResponse, error) {
	ips, err := s.client.GetIPAddresses(ctx, r.GetName())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.IPAddressesResponse{IpAddresses: ips}, nil
}

// ListVMs is best-effort: it is not part of hyperv.Client, so it currently
// returns Unimplemented. It exists in the contract for discovery/debugging and
// can be backed once the client grows a List method.
func (s *Server) ListVMs(context.Context, *pb.Empty) (*pb.ListVMsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListVMs is not implemented")
}
