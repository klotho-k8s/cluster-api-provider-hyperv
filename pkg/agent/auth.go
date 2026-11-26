/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package agent

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// authHeader is the gRPC metadata key carrying the bearer token.
const authHeader = "authorization"

// bearerPrefix is the scheme prefix on the authorization value.
const bearerPrefix = "Bearer "

// tokenFromContext extracts the bearer token from incoming gRPC metadata.
func tokenFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(authHeader)
	if len(vals) == 0 {
		return ""
	}
	v := vals[0]
	if strings.HasPrefix(v, bearerPrefix) {
		return strings.TrimPrefix(v, bearerPrefix)
	}
	return v
}

// TokenAuthInterceptor returns a unary server interceptor that requires every
// RPC to present the expected bearer token, compared in constant time. A
// missing or wrong token is rejected with codes.Unauthenticated. An empty
// expected token is a configuration error and rejects all calls.
func TokenAuthInterceptor(expected string) grpc.UnaryServerInterceptor {
	want := []byte(expected)
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if len(want) == 0 {
			return nil, status.Error(codes.Unauthenticated, "agent token not configured")
		}
		got := []byte(tokenFromContext(ctx))
		if subtle.ConstantTimeEq(int32(len(got)), int32(len(want))) != 1 ||
			subtle.ConstantTimeCompare(got, want) != 1 {
			return nil, status.Error(codes.Unauthenticated, "invalid or missing agent token")
		}
		return handler(ctx, req)
	}
}

// tokenCreds is a PerRPCCredentials that attaches the bearer token to every
// outgoing call.
type tokenCreds struct {
	token      string
	requireTLS bool
}

func (t tokenCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{authHeader: bearerPrefix + t.token}, nil
}

func (t tokenCreds) RequireTransportSecurity() bool { return t.requireTLS }

// TokenCredentials returns a DialOption that sends the bearer token on every
// RPC. requireTLS mirrors PerRPCCredentials.RequireTransportSecurity: set it
// true in production so the token is never sent over an unencrypted channel;
// tests use false to run over an in-memory connection.
func TokenCredentials(token string, requireTLS bool) grpc.DialOption {
	return grpc.WithPerRPCCredentials(tokenCreds{token: token, requireTLS: requireTLS})
}
