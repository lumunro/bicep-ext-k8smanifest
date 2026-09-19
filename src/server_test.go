// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

// Server-level tests: drive the generated gRPC client against the extension
// server in-process over a unix socket, covering the wire surface that
// `bicep local-deploy` talks to (main.go).
package main

import (
	"bicep-ext-k8smanifest/bicep.azure.com/protos/extension"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const (
	manifestErrorCode   = "ManifestError"
	deploymentBicepType = "apps/Deployment"
	deploymentProps     = `{"metadata":{"name":"web","namespace":"apps"},"spec":{"replicas":2}}`
	v1APIVersion        = "v1"
)

// newGRPCClient registers the extension server on a unix socket under t.TempDir
// and returns a connected client. The server is stopped on test cleanup.
func newGRPCClient(t *testing.T) extension.BicepExtensionClient {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "ext.sock")

	lc := net.ListenConfig{}

	l, err := lc.Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatalf("listen on %s: %v", sock, err)
	}

	grpcServer := grpc.NewServer()
	extension.RegisterBicepExtensionServer(grpcServer, newServer())

	go func() {
		_ = grpcServer.Serve(l)
	}()

	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough://ext-test", grpc.WithContextDialer(
		func(ctx context.Context, _ string) (net.Conn, error) {
			var dl net.Dialer

			return dl.DialContext(ctx, "unix", sock)
		}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return extension.NewBicepExtensionClient(conn)
}

// deploymentRequest builds a CreateOrUpdate/Preview request for a Deployment
// into the given output directory.
func deploymentRequest(outDir, props string) *extension.ResourceSpecification {
	cfg := `{"outputDir":` + jsonString(outDir) + `}`
	apiVersion := v1APIVersion

	return &extension.ResourceSpecification{
		Type:       deploymentBicepType,
		Config:     &cfg,
		ApiVersion: &apiVersion,
		Properties: props,
	}
}

func jsonString(s string) string {
	return fmt.Sprintf("%q", s)
}

// assertErrorData asserts the response carries the extension error (not a gRPC
// error) with the given code and message substring.
func assertErrorData(t *testing.T, resp *extension.LocalExtensibilityOperationResponse, wantMessage string) {
	t.Helper()

	if resp.ErrorData == nil || resp.ErrorData.Error == nil {
		t.Fatalf("expected error data, got %+v", resp)
	}

	if got := resp.ErrorData.Error.Code; got != manifestErrorCode {
		t.Errorf("error code = %q, want %q", got, manifestErrorCode)
	}

	if !strings.Contains(resp.ErrorData.Error.Message, wantMessage) {
		t.Errorf("error message %q does not contain %q", resp.ErrorData.Error.Message, wantMessage)
	}
}

func TestGRPCPing(t *testing.T) {
	t.Parallel()

	client := newGRPCClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := client.Ping(ctx, &extension.Empty{}); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestGRPCCreateOrUpdate(t *testing.T) {
	t.Parallel()

	client := newGRPCClient(t)
	outDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.CreateOrUpdate(ctx, deploymentRequest(outDir, deploymentProps))
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}

	if resp.ErrorData != nil {
		t.Fatalf("unexpected error data: %+v", resp.ErrorData)
	}

	if resp.Resource == nil {
		t.Fatal("response has no resource")
	}

	if got := *resp.Resource.Status; got != "Succeeded" {
		t.Errorf("status = %q, want Succeeded", got)
	}

	if resp.Resource.Type != deploymentBicepType {
		t.Errorf("type = %q, want %q", resp.Resource.Type, deploymentBicepType)
	}

	var props map[string]any

	if err := json.Unmarshal([]byte(resp.Resource.Properties), &props); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}

	wantPath := filepath.Join(outDir, "apps-deployment-apps-web.yaml")

	if props["filePath"] != wantPath {
		t.Errorf("filePath = %v, want %q", props["filePath"], wantPath)
	}

	if content, ok := props["content"].(string); !ok || !strings.Contains(content, "apiVersion: apps/v1") || !strings.Contains(content, "kind: Deployment") {
		t.Errorf("content = %v, want rendered YAML with apiVersion and kind", props["content"])
	}

	b, err := os.ReadFile(wantPath) //nolint:gosec // path built from the test fixture dir
	if err != nil {
		t.Fatalf("manifest file not written: %v", err)
	}

	if !strings.Contains(string(b), "apiVersion: apps/v1") {
		t.Errorf("written manifest missing apiVersion:\n%s", b)
	}
}

func TestGRPCCreateOrUpdateUnknownConfigKey(t *testing.T) {
	t.Parallel()

	client := newGRPCClient(t)

	cfg := `{"outputdirector":"oops"}`
	apiVersion := v1APIVersion

	resp, err := client.CreateOrUpdate(context.Background(), &extension.ResourceSpecification{
		Type:       deploymentBicepType,
		Config:     &cfg,
		ApiVersion: &apiVersion,
		Properties: deploymentProps,
	})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}

	assertErrorData(t, resp, "failed to parse extension configuration")
}

func TestGRPCCreateOrUpdateInvalidProperties(t *testing.T) {
	t.Parallel()

	client := newGRPCClient(t)

	resp, err := client.CreateOrUpdate(context.Background(), deploymentRequest(t.TempDir(), `{}`))
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}

	assertErrorData(t, resp, "metadata.name is required")
}

func TestGRPCPreviewDoesNotWrite(t *testing.T) {
	t.Parallel()

	client := newGRPCClient(t)
	outDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Preview(ctx, deploymentRequest(outDir, deploymentProps))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if resp.ErrorData != nil {
		t.Fatalf("unexpected error data: %+v", resp.ErrorData)
	}

	var props map[string]any

	if err := json.Unmarshal([]byte(resp.Resource.Properties), &props); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}

	// Preview reports the planned path but must not touch the file system.
	wantPath := filepath.Join(outDir, "apps-deployment-apps-web.yaml")

	if props["filePath"] != wantPath {
		t.Errorf("filePath = %v, want %q", props["filePath"], wantPath)
	}

	if entries, err := os.ReadDir(outDir); err != nil {
		t.Fatalf("read outDir: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("preview wrote %d entries to %s", len(entries), outDir)
	}
}

func TestGRPCGetTypeFiles(t *testing.T) {
	t.Parallel()

	client := newGRPCClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.GetTypeFiles(ctx, &extension.Empty{})
	if err != nil {
		t.Fatalf("GetTypeFiles: %v", err)
	}

	if strings.TrimSpace(resp.IndexFile) == "" {
		t.Error("IndexFile is empty")
	}

	if types, ok := resp.TypeFiles[typesFileName]; !ok || strings.TrimSpace(types) == "" {
		t.Errorf("TypeFiles[%q] missing or empty (keys: %v)", typesFileName, typeFileKeys(resp.TypeFiles))
	}

	var index map[string]any

	if err := json.Unmarshal([]byte(resp.IndexFile), &index); err != nil {
		t.Errorf("IndexFile is not valid JSON: %v", err)
	}
}

func typeFileKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))

	for k := range m {
		out = append(out, k)
	}

	return out
}

func TestGRPCGetAndDeleteUnimplemented(t *testing.T) {
	t.Parallel()

	client := newGRPCClient(t)

	ref := &extension.ResourceReference{}

	_, err := client.Get(context.Background(), ref)
	assertUnimplemented(t, "Get", err)

	_, err = client.Delete(context.Background(), ref)
	assertUnimplemented(t, "Delete", err)
}

func assertUnimplemented(t *testing.T, op string, err error) {
	t.Helper()

	if err == nil {
		t.Fatalf("%s: expected an error, got nil", op)
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unimplemented {
		t.Errorf("%s: gRPC status = %v (%v), want codes.Unimplemented", op, st, err)
	}
}

// TestServeGracefulShutdown drives serve() over a real unix socket: one
// client round-trip, then SIGTERM must stop the server gracefully and remove
// the socket file. Not parallel: it signals the test process.
func TestServeGracefulShutdown(t *testing.T) { //nolint:paralleltest // signals the test process with SIGTERM
	sock := filepath.Join(t.TempDir(), "ext.sock")

	oldSocket := *socket
	*socket = sock

	t.Cleanup(func() { *socket = oldSocket })

	lc := net.ListenConfig{}

	l, err := lc.Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	done := make(chan error, 1)

	go func() {
		done <- serve(l)
	}()

	conn, err := grpc.NewClient("passthrough://serve-test", grpc.WithContextDialer(
		func(ctx context.Context, _ string) (net.Conn, error) {
			var dl net.Dialer

			return dl.DialContext(ctx, "unix", sock)
		}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := extension.NewBicepExtensionClient(conn).Ping(ctx, &extension.Empty{}); err != nil {
		t.Fatalf("Ping before shutdown: %v", err)
	}

	// The signal handler must be registered by now (serve registers it before
	// starting the gRPC server), so the process survives the SIGTERM.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not return after SIGTERM")
	}

	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket file still present after shutdown (stat err = %v)", err)
	}
}
