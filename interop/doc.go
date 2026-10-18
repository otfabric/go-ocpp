//go:build interop

// SPDX-License-Identifier: MIT

// Package interop tests go-ocpp against independent OCPP implementations:
// the reference images that otfabric/ocpp-interop publishes, pinned by
// digest. Nothing of that repository is cloned or built here; the images
// and their documented container contract are the whole interface.
//
// Both roles are tested, with CALLs from both ends of each connection:
//
//	go-ocpp client (station)   →  reference CSMS   (client_test.go)
//	reference station          →  go-ocpp server   (server_test.go)
//
// The expected answers come from the fixture baked into each image
// (print-fixture). A scenario an image does not support, according to its
// print-capabilities, is skipped and says why; a known upstream defect is
// skipped with a link to the evidence. Neither counts as a pass.
//
// Run with Docker available:
//
//	make interop
//
// or go test -tags=interop ./interop/... with the variables of harness.go.
package interop
