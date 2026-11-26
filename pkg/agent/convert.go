/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package agent

import (
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
	pb "github.com/klotho-k8s/cluster-api-provider-hyperv/proto/hypervagent/v1"
)

// vmToProto converts a hyperv.VM to its protobuf form.
func vmToProto(v *hyperv.VM) *pb.VM {
	if v == nil {
		return nil
	}
	return &pb.VM{
		Name:               v.Name,
		Id:                 v.ID,
		State:              v.State,
		Generation:         int32(v.Generation),
		Notes:              v.Notes,
		CpuCount:           int32(v.CPUCount),
		MemoryStartupBytes: v.MemoryStartupBytes,
	}
}

// vmFromProto converts a protobuf VM to hyperv.VM.
func vmFromProto(v *pb.VM) *hyperv.VM {
	if v == nil {
		return nil
	}
	return &hyperv.VM{
		Name:               v.GetName(),
		ID:                 v.GetId(),
		State:              v.GetState(),
		Generation:         int(v.GetGeneration()),
		Notes:              v.GetNotes(),
		CPUCount:           int(v.GetCpuCount()),
		MemoryStartupBytes: v.GetMemoryStartupBytes(),
	}
}

// createInputToProto converts a hyperv.CreateVMInput to a CreateVMRequest.
func createInputToProto(in hyperv.CreateVMInput) *pb.CreateVMRequest {
	return &pb.CreateVMRequest{
		Name:               in.Name,
		Generation:         int32(in.Generation),
		MemoryStartupBytes: in.MemoryStartupBytes,
		CpuCount:           int32(in.CPUCount),
		SwitchName:         in.SwitchName,
		VhdPath:            in.VHDPath,
		Notes:              in.Notes,
	}
}

// createInputFromProto converts a CreateVMRequest to hyperv.CreateVMInput.
func createInputFromProto(r *pb.CreateVMRequest) hyperv.CreateVMInput {
	return hyperv.CreateVMInput{
		Name:               r.GetName(),
		Generation:         int(r.GetGeneration()),
		MemoryStartupBytes: r.GetMemoryStartupBytes(),
		CPUCount:           int(r.GetCpuCount()),
		SwitchName:         r.GetSwitchName(),
		VHDPath:            r.GetVhdPath(),
		Notes:              r.GetNotes(),
	}
}
