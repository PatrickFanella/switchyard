package main

import (
	"bufio"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type addAppOptions struct {
	ConfigPath    string
	ReloadCommand string
	NoReload      bool
}

type appKeySettings struct {
	Name            string
	Key             string
	Priority        int
	Deduplicate     bool
	CacheTTLSecs    int
	CacheMaxEntries int
}

func runAddApp(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("add-app", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := addAppOptions{}
	fs.StringVar(&opts.ConfigPath, "config", "config.yaml", "Path to config file")
	fs.StringVar(&opts.ReloadCommand, "reload-command", "systemctl restart llama-line", "Command to reload/restart llama-line after writing config")
	fs.BoolVar(&opts.NoReload, "no-reload", false, "Update config without running reload command")
	if err := fs.Parse(args); err != nil {
		return err
	}

	reader := bufio.NewReader(stdin)
	settings, err := promptAppKeySettings(reader, stdout)
	if err != nil {
		return err
	}
	if err := appendAppKeyToConfig(opts.ConfigPath, settings); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "added app %q to %s\n", settings.Name, opts.ConfigPath)
	fmt.Fprintf(stdout, "api key: %s\n", settings.Key)

	if opts.NoReload {
		fmt.Fprintln(stdout, "reload skipped")
		return nil
	}
	reload, err := promptBool(reader, stdout, fmt.Sprintf("Reload now with `%s`?", opts.ReloadCommand), true)
	if err != nil {
		return err
	}
	if !reload {
		fmt.Fprintln(stdout, "reload skipped")
		return nil
	}
	if err := runReloadCommand(opts.ReloadCommand); err != nil {
		return fmt.Errorf("reload failed: %w", err)
	}
	fmt.Fprintln(stdout, "reloaded")
	return nil
}

func promptAppKeySettings(reader *bufio.Reader, out io.Writer) (appKeySettings, error) {
	name, err := promptString(reader, out, "App name", "")
	if err != nil {
		return appKeySettings{}, err
	}
	key, err := generateUUIDv4()
	if err != nil {
		return appKeySettings{}, err
	}
	priority, err := promptInt(reader, out, "Priority", 0)
	if err != nil {
		return appKeySettings{}, err
	}
	deduplicate, err := promptBool(reader, out, "Enable request deduplication?", true)
	if err != nil {
		return appKeySettings{}, err
	}
	cacheTTL, err := promptInt(reader, out, "Cache TTL seconds", 0)
	if err != nil {
		return appKeySettings{}, err
	}
	cacheMax, err := promptInt(reader, out, "Cache max entries", 0)
	if err != nil {
		return appKeySettings{}, err
	}
	return appKeySettings{
		Name:            name,
		Key:             key,
		Priority:        priority,
		Deduplicate:     deduplicate,
		CacheTTLSecs:    cacheTTL,
		CacheMaxEntries: cacheMax,
	}, nil
}

func appendAppKeyToConfig(path string, settings appKeySettings) error {
	if strings.TrimSpace(settings.Name) == "" {
		return errors.New("app name is required")
	}
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
	keysNode, err := ensureSequenceField(root, "keys")
	if err != nil {
		return err
	}
	if keyNameExists(keysNode, settings.Name) {
		return fmt.Errorf("app %q already exists", settings.Name)
	}
	keysNode.Content = append(keysNode.Content, appKeyYAMLNode(settings))

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

func keyNameExists(keysNode *yaml.Node, name string) bool {
	for _, item := range keysNode.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(item.Content); i += 2 {
			if item.Content[i].Value == "name" && item.Content[i+1].Value == name {
				return true
			}
		}
	}
	return false
}

func appKeyYAMLNode(settings appKeySettings) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("name"), scalar(settings.Name),
		scalar("key"), scalar(settings.Key),
		scalar("priority"), intScalar(settings.Priority),
		scalar("deduplicate"), boolScalar(settings.Deduplicate),
		scalar("cache_ttl_secs"), intScalar(settings.CacheTTLSecs),
		scalar("cache_max_entries"), intScalar(settings.CacheMaxEntries),
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

func promptString(reader *bufio.Reader, out io.Writer, label, defaultValue string) (string, error) {
	for {
		value, err := prompt(reader, out, label, defaultValue)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(out, "value required")
	}
}

func promptInt(reader *bufio.Reader, out io.Writer, label string, defaultValue int) (int, error) {
	for {
		value, err := prompt(reader, out, label, strconv.Itoa(defaultValue))
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return n, nil
		}
		fmt.Fprintln(out, "enter an integer")
	}
}

func promptBool(reader *bufio.Reader, out io.Writer, label string, defaultValue bool) (bool, error) {
	def := "n"
	if defaultValue {
		def = "y"
	}
	for {
		value, err := prompt(reader, out, label, def)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "y", "yes", "true", "1":
			return true, nil
		case "n", "no", "false", "0":
			return false, nil
		default:
			fmt.Fprintln(out, "enter y or n")
		}
	}
}

func prompt(reader *bufio.Reader, out io.Writer, label, defaultValue string) (string, error) {
	if defaultValue == "" {
		fmt.Fprintf(out, "%s: ", label)
	} else {
		fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
	}
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && value == "" {
		return "", io.EOF
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func generateUUIDv4() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func runReloadCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("reload command is empty")
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
