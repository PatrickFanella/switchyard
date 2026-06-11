package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	// ErrKeyNotFound is returned when a key name does not exist in the config file.
	ErrKeyNotFound = errors.New("key not found")
	// ErrKeyExists is returned when writing a key would duplicate another config key name.
	ErrKeyExists = errors.New("key name already exists")
)

// UpsertAPIKeyInFile adds or replaces one key entry in a YAML config file.
func UpsertAPIKeyInFile(path string, key APIKey) error {
	if strings.TrimSpace(key.Name) == "" {
		return errors.New("key name is required")
	}
	return mutateKeyFile(path, func(keys *yaml.Node) error {
		idx := keyIndex(keys, key.Name)
		if idx >= 0 {
			keys.Content[idx] = apiKeyYAMLNode(key)
			return nil
		}
		keys.Content = append(keys.Content, apiKeyYAMLNode(key))
		return nil
	})
}

// RenameAPIKeyInFile renames and updates priority/settings for one key entry.
func RenameAPIKeyInFile(path, oldName string, key APIKey) error {
	if strings.TrimSpace(oldName) == "" || strings.TrimSpace(key.Name) == "" {
		return errors.New("key name is required")
	}
	return mutateKeyFile(path, func(keys *yaml.Node) error {
		idx := keyIndex(keys, oldName)
		if idx < 0 {
			return ErrKeyNotFound
		}
		if key.Name != oldName && keyIndex(keys, key.Name) >= 0 {
			return ErrKeyExists
		}
		keys.Content[idx] = apiKeyYAMLNode(key)
		return nil
	})
}

// RemoveAPIKeyFromFile removes one key entry from a YAML config file.
func RemoveAPIKeyFromFile(path, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("key name is required")
	}
	return mutateKeyFile(path, func(keys *yaml.Node) error {
		idx := keyIndex(keys, name)
		if idx < 0 {
			return ErrKeyNotFound
		}
		keys.Content = append(keys.Content[:idx], keys.Content[idx+1:]...)
		return nil
	})
}

func mutateKeyFile(path string, mutate func(*yaml.Node) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	var doc yaml.Node
	if len(strings.TrimSpace(string(data))) == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	} else if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse config YAML: %w", err)
	}
	root := ensureMappingRoot(&doc)
	keys, err := ensureSequenceField(root, "keys")
	if err != nil {
		return err
	}
	if err := mutate(keys); err != nil {
		return err
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("render config YAML: %w", err)
	}
	if err := os.WriteFile(path, out, mode); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func ensureMappingRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	return doc.Content[0]
}

func ensureSequenceField(root *yaml.Node, field string) (*yaml.Node, error) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == field {
			if root.Content[i+1].Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("config field %q must be a list", field)
			}
			return root.Content[i+1], nil
		}
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: field}
	value := &yaml.Node{Kind: yaml.SequenceNode}
	root.Content = append(root.Content, key, value)
	return value, nil
}

func keyIndex(keys *yaml.Node, name string) int {
	for idx, item := range keys.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(item.Content); i += 2 {
			if item.Content[i].Value == "name" && item.Content[i+1].Value == name {
				return idx
			}
		}
	}
	return -1
}

func apiKeyYAMLNode(key APIKey) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("name"), scalar(key.Name),
		scalar("key"), scalar(key.Key),
		scalar("priority"), intScalar(key.Priority),
		scalar("deduplicate"), boolScalar(key.Deduplicate),
		scalar("cache_ttl_secs"), intScalar(key.CacheTTLSecs),
		scalar("cache_max_entries"), intScalar(key.CacheMaxEntries),
	}}
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func intScalar(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

func boolScalar(value bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(value)}
}
