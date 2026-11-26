/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package agent

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv/fake"
	pb "github.com/klotho-k8s/cluster-api-provider-hyperv/proto/hypervagent/v1"
)

const testToken = "s3cr3t-token"

// dialTestServer starts an in-memory gRPC agent server wrapping a fake Hyper-V
// client, with the token interceptor, and returns a connected Client. The
// caller gets the token it must present.
func dialTestServer(t *testing.T, clientToken string) *Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.UnaryInterceptor(TokenAuthInterceptor(testToken)))
	pb.RegisterHyperVAgentServer(srv, NewServer(fake.New()))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		TokenCredentials(clientToken, false),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return NewClient(pb.NewHyperVAgentClient(conn))
}

func TestRoundTripCreateGetDelete(t *testing.T) {
	c := dialTestServer(t, testToken)
	ctx := context.Background()

	vm, err := c.CreateVM(ctx, hyperv.CreateVMInput{
		Name: "node0", Generation: 2, MemoryStartupBytes: 2 << 30, CPUCount: 2,
		SwitchName: "Default Switch", VHDPath: `C:\vms\node0.vhdx`, Notes: "owned",
	})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if vm.Name != "node0" || vm.Generation != 2 || vm.CPUCount != 2 {
		t.Fatalf("CreateVM round trip wrong: %+v", vm)
	}

	ok, err := c.Exists(ctx, "node0")
	if err != nil || !ok {
		t.Fatalf("Exists: %v ok=%v", err, ok)
	}
	got, err := c.GetVM(ctx, "node0")
	if err != nil || got.Name != "node0" {
		t.Fatalf("GetVM: %v %+v", err, got)
	}
	if err := c.StartVM(ctx, "node0"); err != nil {
		t.Fatalf("StartVM: %v", err)
	}
	if err := c.SetVLAN(ctx, "node0", 10); err != nil {
		t.Fatalf("SetVLAN: %v", err)
	}
	if err := c.AttachCloudInit(ctx, "node0", []byte("#cloud-config\n")); err != nil {
		t.Fatalf("AttachCloudInit: %v", err)
	}
	if err := c.RemoveVM(ctx, "node0"); err != nil {
		t.Fatalf("RemoveVM: %v", err)
	}
}

func TestGetVMNotFoundMapsAcrossTransport(t *testing.T) {
	c := dialTestServer(t, testToken)
	_, err := c.GetVM(context.Background(), "ghost")
	if !errors.Is(err, hyperv.ErrVMNotFound) {
		t.Fatalf("want ErrVMNotFound across gRPC, got %v", err)
	}
}

func TestAuthRejectsWrongToken(t *testing.T) {
	c := dialTestServer(t, "wrong-token")
	_, err := c.Exists(context.Background(), "x")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

func TestAuthRejectsMissingToken(t *testing.T) {
	c := dialTestServer(t, "")
	_, err := c.Exists(context.Background(), "x")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated for empty token, got %v", err)
	}
}

// TestInterceptorDirect exercises the interceptor without a server.
func TestInterceptorDirect(t *testing.T) {
	interceptor := TokenAuthInterceptor("good")
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	cases := []struct {
		name string
		md   metadata.MD
		want codes.Code
	}{
		{"good", metadata.Pairs(authHeader, bearerPrefix+"good"), codes.OK},
		{"wrong", metadata.Pairs(authHeader, bearerPrefix+"bad"), codes.Unauthenticated},
		{"missing", metadata.MD{}, codes.Unauthenticated},
		{"bare-no-prefix", metadata.Pairs(authHeader, "good"), codes.OK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), tc.md)
			_, err := interceptor(ctx, nil, nil, handler)
			if status.Code(err) != tc.want {
				t.Fatalf("got %v, want %v", status.Code(err), tc.want)
			}
		})
	}
}

// TestEmptyExpectedTokenRejectsAll guards against a misconfigured agent.
func TestEmptyExpectedTokenRejectsAll(t *testing.T) {
	interceptor := TokenAuthInterceptor("")
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(authHeader, bearerPrefix+"anything"))
	_, err := interceptor(ctx, nil, nil, func(context.Context, any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("empty expected token must reject, got %v", err)
	}
}

func TestSelfSignedGenerates(t *testing.T) {
	cert, key, err := GenerateSelfSigned([]string{"127.0.0.1", "localhost", "host.example"}, 24*time.Hour)
	if err != nil || len(cert) == 0 || len(key) == 0 {
		t.Fatalf("GenerateSelfSigned: %v (cert %d key %d)", err, len(cert), len(key))
	}
	block, _ := pem.Decode(cert)
	if block == nil {
		t.Fatal("cert is not valid PEM")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if !parsed.NotAfter.After(time.Now()) {
		t.Fatalf("cert already expired: NotAfter=%v", parsed.NotAfter)
	}
	// DNS SANs carry the names; the IP lands in IPAddresses.
	if !slices.Contains(parsed.DNSNames, "localhost") || !slices.Contains(parsed.DNSNames, "host.example") {
		t.Fatalf("missing expected DNS SANs, got %v", parsed.DNSNames)
	}
	foundIP := false
	for _, ip := range parsed.IPAddresses {
		if ip.String() == "127.0.0.1" {
			foundIP = true
		}
	}
	if !foundIP {
		t.Fatalf("missing 127.0.0.1 IP SAN, got %v", parsed.IPAddresses)
	}
}

// TestClientTLSEmptyCAUsesSystemRoots guards review finding H2: ClientTLS("")
// must not error at construction (it previously tried to read the file "").
func TestClientTLSEmptyCAUsesSystemRoots(t *testing.T) {
	if _, err := ClientTLS("", ""); err != nil {
		t.Fatalf("ClientTLS with empty CA must succeed (system roots), got %v", err)
	}
}

// TestTLSRoundTripByHostname is the end-to-end guard for H2/H3: a self-signed
// cert generated for the wildcard-bind SAN set (hostname + loopback) must let a
// controller dial the agent BY NAME over real TLS, trusting the cert as CA, and
// make an authenticated RPC. It would have caught both the empty-CA bug (H2) and
// the loopback-only SAN bug (H3).
func TestTLSRoundTripByHostname(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, err := GenerateSelfSigned([]string{"localhost", "127.0.0.1"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	certFile := filepath.Join(dir, "agent.crt")
	keyFile := filepath.Join(dir, "agent.key")
	if err := WriteKeyPair(certFile, keyFile, certPEM, keyPEM); err != nil {
		t.Fatalf("WriteKeyPair: %v", err)
	}

	tlsOpt, err := ServerTLS(certFile, keyFile)
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(tlsOpt, grpc.UnaryInterceptor(TokenAuthInterceptor(testToken)))
	pb.RegisterHyperVAgentServer(srv, NewServer(fake.New()))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	_, port, _ := net.SplitHostPort(lis.Addr().String())
	// Trust the agent's cert as CA and dial by hostname: TLS verifies "localhost"
	// against the cert SANs.
	clientTLS, err := ClientTLS(certFile, "")
	if err != nil {
		t.Fatalf("ClientTLS: %v", err)
	}
	conn, err := grpc.NewClient("localhost:"+port, clientTLS, TokenCredentials(testToken, true))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	c := NewClient(pb.NewHyperVAgentClient(conn))
	if _, err := c.Exists(context.Background(), "node0"); err != nil {
		t.Fatalf("authenticated RPC over verified TLS failed: %v", err)
	}
}
