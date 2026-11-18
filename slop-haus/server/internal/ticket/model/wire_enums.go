package model

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/wireschema"
)

// Each enum is one named schema, built from its Go values, so a field of
// the type is that enum in the spec and in the frontend's generated types
// -- and, on a request, huma refuses any other value.

func (Status) Schema(r huma.Registry) *huma.Schema {
	return wireschema.Enum(r, "Status", statuses)
}

func (Priority) Schema(r huma.Registry) *huma.Schema {
	return wireschema.Enum(r, "Priority", []Priority{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent})
}

func (PathSource) Schema(r huma.Registry) *huma.Schema {
	return wireschema.Enum(r, "PathSource", []PathSource{PathSourceFiled, PathSourceExtended})
}
