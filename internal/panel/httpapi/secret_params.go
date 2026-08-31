package httpapi

import (
	"encoding/json"

	"github.com/amyrm/antimage/internal/panel/nodes"
)

// Redaction of credential fields in adapter params.
//
// An outbound's params carry the credentials for an UPSTREAM provider -- a
// WireGuard private_key, a socks or http password. Those are the platform
// operator's secrets with a third party, and they were returned verbatim to
// anyone holding outbound:read on the node, which includes the reseller role.
// A tenant is not the platform operator and must never hold the platform's
// upstream keys.
//
// Which fields are secret comes from the SCHEMA THE NODE PUBLISHED, using JSON
// Schema's own writeOnly keyword -- "may be sent by the client, must not be
// returned by the server", which is exactly this. The panel keeps no list of
// credential field names, for the same reason the Inbound Studio keeps no list
// of protocols: a field the panel has not heard of is the adapter's business,
// and a hardcoded denylist would silently miss the next one.

// outboundSchemaFor returns the OutboundSchema an adapter kind publishes.
//
// Deliberately the same source validateOutbound reads. If redaction consulted
// a different schema than validation, a field could be validated as one shape
// and redacted as another, and the disagreement would show up as a credential
// that leaks or one that cannot be saved.
func outboundSchemaFor(adapterKind string) []byte {
	desc, ok := nodes.KnownAdapters()[adapterKind]
	if !ok {
		return nil
	}
	return desc.Caps.OutboundSchema
}

// RedactedValue replaces a secret on the way out.
//
// A sentinel rather than removing the key, because an operator has to be able
// to tell a configured credential from a missing one -- and because the editor
// round-trips the document, so the field must still be there to be sent back.
const RedactedValue = "__redacted__"

// secretFieldsOf returns the property names a schema marks writeOnly.
//
// A schema that cannot be parsed yields no names, which redacts nothing. That
// is the wrong direction for a security control, so callers must not rely on
// this alone for a node whose schema is missing -- see redactParams, which
// refuses to emit params it could not analyse.
func secretFieldsOf(schema []byte) (map[string]bool, bool) {
	if len(schema) == 0 {
		return nil, false
	}
	var parsed struct {
		Properties map[string]struct {
			WriteOnly bool `json:"writeOnly"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return nil, false
	}
	secrets := make(map[string]bool)
	for name, prop := range parsed.Properties {
		if prop.WriteOnly {
			secrets[name] = true
		}
	}
	return secrets, true
}

// redactParams returns params with every writeOnly field replaced.
//
// When the schema cannot be read the params are replaced ENTIRELY, not passed
// through. Failing open here would mean that a node which has not reported its
// schema -- or whose report was malformed -- discloses every credential it
// holds, and "we could not tell which fields were secret" is not a reason to
// assume none of them were.
func redactParams(schema, params []byte) json.RawMessage {
	secrets, ok := secretFieldsOf(schema)
	if !ok {
		return json.RawMessage(`{}`)
	}
	if len(secrets) == 0 {
		return json.RawMessage(params)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(params, &doc); err != nil {
		// Params that are not an object cannot be walked field by field, and
		// cannot be shown to be free of secrets either.
		return json.RawMessage(`{}`)
	}
	for name := range doc {
		if secrets[name] {
			doc[name] = json.RawMessage(`"` + RedactedValue + `"`)
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return out
}

// unredactParams restores the stored value of any field the client sent back
// still redacted.
//
// Without this the redaction destroys what it protects: the editor reads an
// outbound, gets "__redacted__" in place of the key, and submits the document
// unchanged on the next save -- overwriting a working upstream credential with
// the sentinel. The outbound would then fail to connect, and the original key
// would be gone.
//
// A client that genuinely wants to clear a field sends an empty string; only
// the exact sentinel is treated as "unchanged".
func unredactParams(incoming, stored []byte) (json.RawMessage, error) {
	var next map[string]json.RawMessage
	if err := json.Unmarshal(incoming, &next); err != nil {
		// Not an object: nothing to merge, and validation will reject it.
		return json.RawMessage(incoming), nil
	}

	var previous map[string]json.RawMessage
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &previous); err != nil {
			previous = nil
		}
	}

	changed := false
	for name, raw := range next {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || s != RedactedValue {
			continue
		}
		if old, ok := previous[name]; ok {
			next[name] = old
		} else {
			// The sentinel with nothing behind it is not a value any adapter
			// should receive; dropping it lets schema validation report a
			// missing required field rather than passing the literal through.
			delete(next, name)
		}
		changed = true
	}
	if !changed {
		return json.RawMessage(incoming), nil
	}
	return json.Marshal(next)
}
