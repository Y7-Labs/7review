package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const MaxConfigBytes = 2 << 20

func Load(path string) (ReviewConfigV2, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ReviewConfigV2{}, fmt.Errorf("policy: stat %s: %w", path, err)
	}
	if info.Size() > MaxConfigBytes {
		return ReviewConfigV2{}, fmt.Errorf("policy: %s exceeds %d bytes", path, MaxConfigBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReviewConfigV2{}, fmt.Errorf("policy: read %s: %w", path, err)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return DecodeYAML(data)
	case ".json":
		return DecodeJSON(data)
	default:
		return ReviewConfigV2{}, fmt.Errorf("policy: unsupported file extension %q", filepath.Ext(path))
	}
}

func DecodeJSON(data []byte) (ReviewConfigV2, error) {
	if len(data) > MaxConfigBytes {
		return ReviewConfigV2{}, fmt.Errorf("policy: JSON exceeds %d bytes", MaxConfigBytes)
	}
	var config ReviewConfigV2
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return ReviewConfigV2{}, fmt.Errorf("policy: decode JSON: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return ReviewConfigV2{}, err
	}
	if err := validateJSONShape(data); err != nil {
		return ReviewConfigV2{}, err
	}
	if err := Validate(config); err != nil {
		return ReviewConfigV2{}, err
	}
	return config, nil
}

func DecodeYAML(data []byte) (ReviewConfigV2, error) {
	if len(data) > MaxConfigBytes {
		return ReviewConfigV2{}, fmt.Errorf("policy: YAML exceeds %d bytes", MaxConfigBytes)
	}
	var config ReviewConfigV2
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return ReviewConfigV2{}, fmt.Errorf("policy: decode YAML: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ReviewConfigV2{}, fmt.Errorf("policy: multiple YAML documents are not allowed")
		}
		return ReviewConfigV2{}, fmt.Errorf("policy: decode trailing YAML: %w", err)
	}
	if err := validateYAMLShape(data); err != nil {
		return ReviewConfigV2{}, err
	}
	if err := Validate(config); err != nil {
		return ReviewConfigV2{}, err
	}
	return config, nil
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("policy: multiple JSON values are not allowed")
		}
		return fmt.Errorf("policy: decode trailing JSON: %w", err)
	}
	return nil
}
