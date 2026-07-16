//go:build dependency_anchors

// Package proto keeps the generated protobuf module's direct dependencies
// visible to module tooling when generated bindings are absent.
package proto

import (
	_ "google.golang.org/grpc"
	_ "google.golang.org/protobuf/proto"
)
