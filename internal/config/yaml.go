package config

import (
	"bytes"
	"errors"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// loadYAML loads bounded, strict YAML without including input values in errors.
func loadYAML(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return err
	}
	if len(data) > 65536 {
		return errors.New("configuration exceeds size limit")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return errors.New("invalid configuration yaml")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("configuration must be a mapping")
	}
	if err := validateNodes(&document); err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid configuration fields")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("configuration must contain one document")
	}
	return nil
}

func validateNodes(node *yaml.Node) error {
	if node.Tag == "!!null" || node.Kind == yaml.AliasNode {
		return errors.New("configuration cannot contain nulls or aliases")
	}
	for _, child := range node.Content {
		if err := validateNodes(child); err != nil {
			return err
		}
	}
	return nil
}
