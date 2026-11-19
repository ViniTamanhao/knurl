package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ViniTamanhao/knurl/schemas"
)

const manifestSchemaResourceID = "https://knurl.dev/schemas/manifest.schema.json"

var (
	manifestSchemaOnce sync.Once
	manifestSchema     *jsonschema.Schema
	manifestSchemaErr  error
)

func compiledManifestSchema() (*jsonschema.Schema, error) {
	manifestSchemaOnce.Do(func() {
		manifestSchema, manifestSchemaErr = compileSchemaBytes(manifestSchemaResourceID, schemas.ManifestJSON)
	})
	return manifestSchema, manifestSchemaErr
}

func compileSchemaBytes(id string, data []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unmarshal schema %s: %w", id, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if addErr := compiler.AddResource(id, doc); addErr != nil {
		return nil, fmt.Errorf("add schema resource %s: %w", id, addErr)
	}
	compiled, compileErr := compiler.Compile(id)
	if compileErr != nil {
		return nil, fmt.Errorf("compile schema %s: %w", id, compileErr)
	}
	return compiled, nil
}

// ValidateManifestJSON validates raw manifest.json bytes against the frozen
// Draft 2020-12 manifest JSON Schema, decodes it strictly into Manifest, and
// enforces semantic invariants.
func ValidateManifestJSON(data []byte) (Manifest, error) {
	sch, err := compiledManifestSchema()
	if err != nil {
		return Manifest{}, &ToolError{
			Code:    ErrInternalError,
			Message: fmt.Sprintf("compile embedded manifest schema: %v", err),
		}
	}

	val, unmarshalErr := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if unmarshalErr != nil {
		return Manifest{}, &ToolError{
			Code:    ErrInvalidManifest,
			Message: fmt.Sprintf("manifest.json is not valid JSON: %v", unmarshalErr),
		}
	}
	if valErr := sch.Validate(val); valErr != nil {
		return Manifest{}, &ToolError{
			Code:    ErrInvalidManifest,
			Message: fmt.Sprintf("manifest.json failed schema validation: %v", valErr),
		}
	}

	var manifest Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if decErr := dec.Decode(&manifest); decErr != nil {
		return Manifest{}, &ToolError{
			Code:    ErrInvalidManifest,
			Message: fmt.Sprintf("decode manifest.json: %v", decErr),
		}
	}
	var trailing any
	if trailErr := dec.Decode(&trailing); !errors.Is(trailErr, io.EOF) {
		return Manifest{}, &ToolError{
			Code:    ErrInvalidManifest,
			Message: "manifest.json contains trailing JSON data",
		}
	}

	if semErr := ValidateManifestSemantic(manifest); semErr != nil {
		return Manifest{}, &ToolError{
			Code:    ErrInvalidManifest,
			Message: fmt.Sprintf("manifest.json failed semantic validation: %v", semErr),
		}
	}
	return manifest, nil
}

// ValidateInput validates caller input against manifest.InputSchema.
func ValidateInput(manifest Manifest, input map[string]any) error {
	if input == nil {
		input = map[string]any{}
	}
	return validateMapWithSchema(
		ErrInvalidInput,
		"input",
		"knurl://input/"+manifest.Name,
		manifest.InputSchema,
		input,
	)
}

// ValidateOutput validates tool output against manifest.OutputSchema.
func ValidateOutput(manifest Manifest, output map[string]any) error {
	if output == nil {
		return &ToolError{
			Code:    ErrInvalidOutput,
			Message: "tool output must be a JSON object, got null",
		}
	}
	return validateMapWithSchema(
		ErrInvalidOutput,
		"output",
		"knurl://output/"+manifest.Name,
		manifest.OutputSchema,
		output,
	)
}

func validateMapWithSchema(
	errCode ErrorCode,
	kind string,
	schemaID string,
	schemaMap map[string]any,
	data map[string]any,
) error {
	schemaBytes, err := json.Marshal(schemaMap)
	if err != nil {
		return &ToolError{
			Code:    ErrInvalidManifest,
			Message: fmt.Sprintf("marshal %s_schema: %v", kind, err),
		}
	}
	sch, compileErr := compileSchemaBytes(schemaID, schemaBytes)
	if compileErr != nil {
		return &ToolError{
			Code:    ErrInvalidManifest,
			Message: fmt.Sprintf("invalid %s_schema: %v", kind, compileErr),
		}
	}

	dataBytes, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		return &ToolError{
			Code:    errCode,
			Message: fmt.Sprintf("marshal %s for validation: %v", kind, marshalErr),
		}
	}
	val, unmarshalErr := jsonschema.UnmarshalJSON(bytes.NewReader(dataBytes))
	if unmarshalErr != nil {
		return &ToolError{
			Code:    errCode,
			Message: fmt.Sprintf("decode %s for validation: %v", kind, unmarshalErr),
		}
	}
	if valErr := sch.Validate(val); valErr != nil {
		return &ToolError{
			Code:    errCode,
			Message: fmt.Sprintf("%s failed schema validation: %v", kind, valErr),
		}
	}
	return nil
}
