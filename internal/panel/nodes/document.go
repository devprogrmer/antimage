// Package nodes owns the node registry and the desired-state document.
//
// The document is derived from relational tables on demand, never stored as a
// blob (spec section 5). Its serialization is canonical per RFC 8785, and no
// field uses omitempty: every field is always present, and absent means an
// explicit null. Adding or removing a field changes every node's hash and so
// requires a migration that recomputes stored hashes.
package nodes

import "encoding/json"

// DocumentSchemaVersion is carried in every document. Bump it when the shape
// changes, and ship a migration that recomputes node_revisions.doc_sha256.
const DocumentSchemaVersion = 1

type Credential struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Subject is wired but stays empty in SP1. SP2 populates it.
type Subject struct {
	ID          int64        `json:"id"`
	Credentials []Credential `json:"credentials"`
}

type Service struct {
	ID      int64           `json:"id"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Params  json.RawMessage `json:"params"`
}

// Document is what an agent converges against.
//
// Every field is tagged without omitempty on purpose.
type Document struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      int64     `json:"revision"`
	NodeID        int64     `json:"node_id"`
	Services      []Service `json:"services"`
	Subjects      []Subject `json:"subjects"`
}

// Snapshot bundles the three values that must always travel together.
// Callers must never recompute any of them independently (invariant 5).
type Snapshot struct {
	Revision int64
	Document Document
	Bytes    []byte
	SHA256   string
}
