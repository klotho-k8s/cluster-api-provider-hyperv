/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ServerTLS loads a certificate and key from disk and returns a gRPC
// ServerOption enabling TLS with it.
func ServerTLS(certFile, keyFile string) (grpc.ServerOption, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("agent: load server keypair: %w", err)
	}
	return grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})), nil
}

// ClientTLS returns a DialOption trusting the CA in caFile. With an empty
// caFile the host's system root CAs are used (NewClientTLSFromFile cannot be
// called with an empty path: it would try to read the file "" and fail).
func ClientTLS(caFile, serverNameOverride string) (grpc.DialOption, error) {
	if caFile == "" {
		return grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverNameOverride,
		})), nil
	}
	creds, err := credentials.NewClientTLSFromFile(caFile, serverNameOverride)
	if err != nil {
		return nil, fmt.Errorf("agent: load CA %s: %w", caFile, err)
	}
	return grpc.WithTransportCredentials(creds), nil
}

// ClientTLSFromPEM returns a DialOption trusting the CA certificate(s) in
// caPEM. It is the in-memory counterpart of ClientTLS: the controller runs as a
// Linux pod and receives the agent CA inline (klotho hands it through the
// provider configSecret as the HYPERV_AGENT_CA variable), so there is no file
// to point NewClientTLSFromFile at.
func ClientTLSFromPEM(caPEM []byte, serverNameOverride string) (grpc.DialOption, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("agent: no CA certificate found in the provided PEM")
	}
	return grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		ServerName: serverNameOverride,
	})), nil
}

// GenerateSelfSigned creates a self-signed ECDSA certificate valid for the
// given DNS names and IP addresses, for first-run bootstrapping of the agent.
// The returned PEM blocks are the certificate and its private key.
func GenerateSelfSigned(hosts []string, validFor time.Duration) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("agent: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("agent: serial: %w", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "hyperv-agent"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("agent: create cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("agent: marshal key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// WriteKeyPair writes a certificate and key PEM to the given paths, creating
// them 0600. It is used by the agent's first-run self-signed bootstrap.
func WriteKeyPair(certPath, keyPath string, certPEM, keyPEM []byte) error {
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return fmt.Errorf("agent: write cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("agent: write key: %w", err)
	}
	return nil
}
