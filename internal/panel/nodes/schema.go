package nodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/singbox"
	"github.com/amyrm/antimage/internal/node/adapter/stub"
	"github.com/amyrm/antimage/internal/node/adapter/xray"
)

// ErrSchemaViolation means service params failed their adapter's schema.
var ErrSchemaViolation = errors.New("service params violate the adapter schema")

// KnownAdapters returns the descriptors the panel can validate against.
//
// SP1 ships only the stub. SP2, SP5, and SP6 register their descriptors here,
// which is the whole extension point: the panel never learns protocol config
// formats, only how to fetch a schema and validate against it.
func KnownAdapters() map[string]adapter.Descriptor {
	// Constructed with empty paths and nil runtimes: only the Descriptor is
	// read here, and it must not touch the host. The panel never runs an
	// adapter, it only validates operator input against the schema each one
	// publishes.
	//
	// HotUserAdd in these descriptors is the CAPABILITY OF THE TYPE, not of any
	// particular node. A node reports its own at Hello, because whether Xray
	// can hot-add depends on that node having a management API configured.
	x := xray.New("", nil, true)
	sb := singbox.New("", nil)
	s := stub.New("")
	return map[string]adapter.Descriptor{
		string(stub.Kind):    s.Descriptor(),
		string(xray.Kind):    x.Descriptor(),
		string(singbox.Kind): sb.Descriptor(),
	}
}

// ValidateServiceParams checks params against an adapter's published schema.
//
// A malformed schema is the adapter's bug and surfaces as a plain error; only
// a params failure wraps ErrSchemaViolation, so a caller mapping that to 422
// cannot accidentally blame the client for the panel's own defect.
func ValidateServiceParams(schema json.RawMessage, params json.RawMessage) error {
	if len(schema) == 0 {
		return errors.New("adapter publishes no service schema")
	}
	compiler := jsonschema.NewCompiler()
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return fmt.Errorf("parse adapter schema: %w", err)
	}
	if err := compiler.AddResource("adapter.json", schemaDoc); err != nil {
		return fmt.Errorf("register adapter schema: %w", err)
	}
	compiled, err := compiler.Compile("adapter.json")
	if err != nil {
		return fmt.Errorf("compile adapter schema: %w", err)
	}

	paramsDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(params))
	if err != nil {
		return fmt.Errorf("%w: params are not valid JSON", ErrSchemaViolation)
	}
	if err := compiled.Validate(paramsDoc); err != nil {
		return fmt.Errorf("%w: %w", ErrSchemaViolation, err)
	}
	return nil
}
