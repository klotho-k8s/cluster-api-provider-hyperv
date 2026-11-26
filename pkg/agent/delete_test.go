/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package agent

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv/fake"
	pb "github.com/klotho-k8s/cluster-api-provider-hyperv/proto/hypervagent/v1"
)

// blockingDeleteBackend wraps the in-memory fake so a test can pause inside
// DeleteVM, cancel the inbound request while the teardown is "in flight", and
// then observe the context the backend was actually handed. It is the stand-in
// for the real WMI/PowerShell host work: a stop+destroy that must run to
// completion once started.
type blockingDeleteBackend struct {
	*fake.Client

	started chan struct{} // closed when DeleteVM has begun
	proceed chan struct{} // DeleteVM blocks until this is closed

	// ctxErrOnResume is the inbound context's error observed by the backend
	// after it resumes — it must be nil, proving the teardown runs on a context
	// detached from the cancelled request.
	ctxErrOnResume error
}

func (b *blockingDeleteBackend) DeleteVM(ctx context.Context, name string) error {
	close(b.started)
	<-b.proceed
	b.ctxErrOnResume = ctx.Err()
	return b.Client.DeleteVM(ctx, name)
}

// TestDeleteVMCompletesAfterClientDisconnect is the regression guard for the
// self-managed-cluster deadlock: once the teardown is underway, cancelling the
// inbound request (the controller dying as its own VM stops) must NOT abort the
// destroy. The server runs DeleteVM on a context detached from the request, so
// the backend sees no cancellation and the VM is fully removed.
func TestDeleteVMCompletesAfterClientDisconnect(t *testing.T) {
	backend := &blockingDeleteBackend{
		Client:  fake.New(),
		started: make(chan struct{}),
		proceed: make(chan struct{}),
	}
	backend.VMs["node0"] = &hyperv.VM{Name: "node0", ID: "g", State: hyperv.StateRunning}
	srv := NewServer(backend)

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		_, err := srv.DeleteVM(ctx, &pb.NameRequest{Name: "node0"})
		resCh <- result{err: err}
	}()

	// Wait until the teardown has committed (the backend is mid-destroy), then
	// simulate the controller's VM stopping: the gRPC client disconnects and the
	// inbound request context is cancelled.
	select {
	case <-backend.started:
	case <-time.After(5 * time.Second):
		t.Fatal("DeleteVM never started")
	}
	cancel()
	close(backend.proceed)

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("DeleteVM must finish despite client disconnect, got %v", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DeleteVM did not return after resume")
	}

	if backend.ctxErrOnResume != nil {
		t.Fatalf("teardown saw a cancelled context (%v): it was not detached from the request",
			backend.ctxErrOnResume)
	}
	if backend.DeleteCalls != 1 {
		t.Fatalf("want exactly one backend DeleteVM call, got %d", backend.DeleteCalls)
	}
	if _, stillThere := backend.VMs["node0"]; stillThere {
		t.Fatal("VM must be destroyed even though the client disconnected mid-delete")
	}
}

// TestDeleteVMRoundTrip exercises the full gRPC path: the controller-side
// Client.DeleteVM calling the server's DeleteVM over the wire, against the fake
// backend. It confirms the single RPC is wired end to end and is idempotent.
func TestDeleteVMRoundTrip(t *testing.T) {
	c := dialTestServer(t, testToken)
	ctx := context.Background()

	if _, err := c.CreateVM(ctx, hyperv.CreateVMInput{Name: "node0", Generation: 2}); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if err := c.DeleteVM(ctx, "node0"); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
	ok, err := c.Exists(ctx, "node0")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if ok {
		t.Fatal("VM should be gone after DeleteVM")
	}
	// Idempotent: deleting an already-absent VM succeeds.
	if err := c.DeleteVM(ctx, "node0"); err != nil {
		t.Fatalf("DeleteVM on absent VM must succeed, got %v", err)
	}
}

// TestDeleteVMHonorsEarlyCancel checks the other side of the contract: a request
// already cancelled before any teardown work begins is aborted, not silently
// run. Nothing is committed yet, so early cancellation is legitimate and the
// backend is never touched.
func TestDeleteVMHonorsEarlyCancel(t *testing.T) {
	backend := fake.New()
	backend.VMs["node0"] = &hyperv.VM{Name: "node0", ID: "g", State: hyperv.StateRunning}
	srv := NewServer(backend)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already gone before the call

	_, err := srv.DeleteVM(ctx, &pb.NameRequest{Name: "node0"})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("want Canceled for a pre-cancelled request, got %v", err)
	}
	if backend.DeleteCalls != 0 {
		t.Fatalf("backend must not be touched on early cancel, got %d calls", backend.DeleteCalls)
	}
	if _, stillThere := backend.VMs["node0"]; !stillThere {
		t.Fatal("VM must be untouched when the request is cancelled before work begins")
	}
}
