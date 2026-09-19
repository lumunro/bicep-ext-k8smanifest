// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

// Package main implements the "k8smanifest" Bicep Local extension: a gRPC server
// that renders Kubernetes manifests authored in Bicep into YAML files.
package main

import (
	"bicep-ext-k8smanifest/bicep.azure.com/protos/extension"
	"bicep-ext-k8smanifest/internal/version"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var socket = flag.String("socket", "", "The path to the domain socket to connect on")

var printVersion = flag.Bool("version", false, "print the extension version and exit")

func main() {
	flag.Parse()

	if *printVersion {
		fmt.Println(version.Version)
		return
	}

	if *socket == "" {
		log.Fatalf("missing required -%s argument\n", "--socket")
	}

	// Remove a stale socket from a previous run (e.g. a kill -9 before the
	// cleanup in serve ran). Only NotExist and nil mean "nothing to remove";
	// any other Stat error is surfaced explicitly.
	if _, err := os.Stat(*socket); err != nil && !os.IsNotExist(err) {
		log.Fatalf("cannot stat socket %s: %v", *socket, err)
	}

	if err := os.RemoveAll(*socket); err != nil {
		log.Fatalf("failed to remove existing socket %s: %v", *socket, err)
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(context.Background(), "unix", *socket)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	log.Printf("k8smanifest extension %s listening on %s", version.Version, *socket)

	if err := serve(listener); err != nil {
		log.Fatalf("%v", err)
	}
}

// serve runs the gRPC server until a failure or SIGINT/SIGTERM, then
// gracefully stops it and removes the socket file. Errors are returned
// (not logged/exited here) so the signal-handler restore defer always runs.
func serve(listener net.Listener) error {
	grpcServer := grpc.NewServer()
	extension.RegisterBicepExtensionServer(grpcServer, newServer())

	// Graceful stop on SIGINT/SIGTERM: an interrupted write can leave a
	// truncated combined.yaml that Flux would sync.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)

	go func() {
		errCh <- grpcServer.Serve(listener)
	}()

	srvErr := error(nil)

	select {
	case srvErr = <-errCh:
	case <-ctx.Done():
		log.Printf("shutting down: %v", ctx.Err())
	}

	stopCh := make(chan struct{})

	go func() {
		grpcServer.GracefulStop()
		close(stopCh)
	}()

	select {
	case <-stopCh:
	case <-time.After(10 * time.Second):
		log.Println("graceful stop timed out; stopping now")

		grpcServer.Stop()
	}

	if err := os.RemoveAll(*socket); err != nil {
		return fmt.Errorf("removing socket %s: %w", *socket, err)
	}

	return srvErr
}

func newServer() *bicepExtensionServer {
	return &bicepExtensionServer{}
}

type bicepExtensionServer struct {
	extension.UnimplementedBicepExtensionServer
}

// resourceRequest carries the parsed Bicep resource specification.
type resourceRequest struct {
	Type       string
	APIVersion string
	Properties string
	config     *extensionConfig
}

func parseRequest(req *extension.ResourceSpecification) (*resourceRequest, error) {
	cfg := &extensionConfig{}

	if req.Config != nil && *req.Config != "" {
		// Strict parsing: the config has exactly two known keys, so a typo
		// ("outputdirector") must fail loudly, not silently default to
		// "manifests".
		dec := json.NewDecoder(strings.NewReader(*req.Config))
		dec.DisallowUnknownFields()

		if err := dec.Decode(cfg); err != nil {
			return nil, errors.New("failed to parse extension configuration: " + err.Error())
		}
	}

	apiVersion := ""
	if req.ApiVersion != nil {
		apiVersion = *req.ApiVersion
	}

	return &resourceRequest{
		Type:       req.Type,
		APIVersion: apiVersion,
		Properties: req.Properties,
		config:     cfg,
	}, nil
}

func buildError(err error) *extension.LocalExtensibilityOperationResponse {
	return &extension.LocalExtensibilityOperationResponse{
		ErrorData: &extension.ErrorData{
			Error: &extension.Error{
				Code:    "ManifestError",
				Message: err.Error(),
			},
		},
	}
}

// CreateOrUpdate renders the manifest and writes it to the output directory.
func (s *bicepExtensionServer) CreateOrUpdate(_ context.Context, req *extension.ResourceSpecification) (*extension.LocalExtensibilityOperationResponse, error) {
	r, err := parseRequest(req)
	if err != nil {
		return buildError(err), nil
	}

	result, err := computeManifest(r)
	if err != nil {
		return buildError(err), nil
	}

	path, err := writeManifest(r.config, result)
	if err != nil {
		return buildError(err), nil
	}

	props, err := responseProperties(result, path)
	if err != nil {
		return buildError(err), nil
	}

	succeededStatus := "Succeeded"

	return &extension.LocalExtensibilityOperationResponse{
		Resource: &extension.Resource{
			Type:        req.Type,
			ApiVersion:  req.ApiVersion,
			Identifiers: "{}",
			Properties:  props,
			Status:      &succeededStatus,
		},
	}, nil
}

// Preview validates the manifest and reports what would be written, without
// touching the file system.
func (s *bicepExtensionServer) Preview(_ context.Context, req *extension.ResourceSpecification) (*extension.LocalExtensibilityOperationResponse, error) {
	r, err := parseRequest(req)
	if err != nil {
		return buildError(err), nil
	}

	result, err := computeManifest(r)
	if err != nil {
		return buildError(err), nil
	}

	props, err := responseProperties(result, resultPath(r.config, result))
	if err != nil {
		return buildError(err), nil
	}

	succeededStatus := "Succeeded"

	return &extension.LocalExtensibilityOperationResponse{
		Resource: &extension.Resource{
			Type:        req.Type,
			ApiVersion:  req.ApiVersion,
			Identifiers: "{}",
			Properties:  props,
			Status:      &succeededStatus,
		},
	}, nil
}

// responseProperties echoes the manifest body back and adds the computed
// file path, rendered YAML and resolved apiVersion/kind, so Bicep outputs
// can reference them.
func responseProperties(result *manifestResult, path string) (string, error) {
	out := make(map[string]any, len(result.body)+4)
	maps.Copy(out, result.body)
	out["filePath"] = path
	out["content"] = result.yaml
	out["apiVersion"] = result.meta.apiVersion
	out["kind"] = result.meta.kind

	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}

	return string(b), nil
}

func (s *bicepExtensionServer) Get(_ context.Context, _ *extension.ResourceReference) (*extension.LocalExtensibilityOperationResponse, error) {
	return nil, status.Error(codes.Unimplemented, "the k8smanifest extension does not support Get")
}

func (s *bicepExtensionServer) Delete(_ context.Context, _ *extension.ResourceReference) (*extension.LocalExtensibilityOperationResponse, error) {
	return nil, status.Error(codes.Unimplemented, "the k8smanifest extension does not support Delete")
}

func (s *bicepExtensionServer) Ping(_ context.Context, req *extension.Empty) (*extension.Empty, error) {
	return req, nil
}

func (s *bicepExtensionServer) GetTypeFiles(_ context.Context, _ *extension.Empty) (*extension.TypeFilesResponse, error) {
	indexContent, typeFiles := loadTypeFiles()

	return &extension.TypeFilesResponse{
		IndexFile: indexContent,
		TypeFiles: typeFiles,
	}, nil
}
