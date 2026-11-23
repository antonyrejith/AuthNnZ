// Package openapi embeds the OpenAPI specifications of Portun's APIs, so that
// a server can read the vendor extensions of its operations at startup
// (ADR-0018). The specifications are the source of truth: the Go server code
// is generated from them (make generate).
package openapi

import _ "embed"

// Platform is the Platform API specification (api/openapi/platform/openapi.yaml).
//
//go:embed platform/openapi.yaml
var Platform []byte
