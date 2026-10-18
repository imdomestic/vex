package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/pflag"
)

type Options struct {
	Args         []string
	Environ      []string
	DefaultPath  string
	AllowMissing bool
}

type Origin struct {
	Layer string `toml:"layer" json:"layer"`
	Name  string `toml:"name" json:"name"`
}

type Resolved[T any] struct {
	Config  T
	Path    string
	Sources map[string]Origin
}

func (r Resolved[T]) Render(withSources bool) ([]byte, error) {
	data, err := toml.Marshal(r.Config)
	if err != nil || !withSources {
		return data, err
	}
	var output bytes.Buffer
	keys := make([]string, 0, len(r.Sources))
	for key := range r.Sources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		source := r.Sources[key]
		fmt.Fprintf(&output, "# source %s = %s (%s)\n", key, source.Layer, strings.ReplaceAll(strings.ReplaceAll(source.Name, "\n", " "), "\r", " "))
	}
	output.Write(data)
	return output.Bytes(), nil
}

func Defaults() Config {
	hostname, err := os.Hostname()
	if err != nil {
		panic(err)
	}
	return Config{
		Server: Server{
			Name:   hostname,
			Listen: []string{"127.0.0.1:8790"}, PublicBaseURL: "http://127.0.0.1:8790",
			InternalBaseURL: "http://127.0.0.1:8790", DataDir: "/var/lib/vex",
			Zone: "vex.internal", RouterAddress: "127.0.0.1:25565",
		},
		Runtime: Runtime{Backend: "kubernetes", Kubernetes: Kubernetes{
			NamespacePrefix: "vex-p-", SystemNamespace: "vex-system",
			KubeconfigFile: "/var/lib/vex/kubeconfig", StorageClass: "local-path",
			WebhookSources: []string{}, Nodes: map[string]Node{},
		}},
		Runtimes: map[string]JavaRuntime{}, Agents: map[string]Agent{},
		Archive: Archive{Backend: "local", Local: LocalArchive{Directory: "/var/lib/vex/archive"}},
		Auth:    Auth{SigningKeyFile: "/var/lib/vex/secrets/signing.key", SessionTTL: "2160h", InviteTTL: "24h", AdminSocket: "/run/vex/admin.sock"},
		Users:   map[string]User{}, Projects: map[string]Project{},
		Build: Build{MaxConcurrent: 1, LockFile: "/var/lib/vex/build.lock"},
		Rooms: Rooms{Idle: Idle{Commit: "10m", Branch: "10m", World: "10m"}, JoinLease: "3m", StartTimeout: "5m", Heap: "4G", DefaultSeed: "empty"},
	}
}

func defaultProject() Project {
	return Project{
		DefaultBranch: "main", Trust: "untrusted", Git: ProjectGit{PollInterval: "60s"},
		Build:   ProjectBuild{Source: "host", Timeout: "20m", GradleArgs: []string{}},
		Loaders: map[string]Loader{}, Members: map[string]Role{}, Whitelist: []string{},
		Quota:  Quota{MaxRooms: 4, RoomMemoryMax: "5Gi", BuildCache: 20, SnapshotKeep: 5, SnapshotKeepDays: 14},
		Worlds: ProjectWorlds{ExtraRootEntries: []string{}},
	}
}

func LoadDaemon(options Options) (Resolved[Config], error) {
	if options.DefaultPath == "" {
		options.DefaultPath = "/etc/vex/vexd.toml"
	}
	result, err := resolve(options, Defaults())
	if err != nil {
		return result, err
	}
	result.Config.Path = result.Path
	if len(result.Config.Server.Listeners) != 0 && result.Sources["server.listen"].Layer == "default" {
		result.Config.Server.Listen = nil
	}
	return result, result.Config.Validate()
}

func LoadClient(options Options) (Resolved[Client], error) {
	if options.DefaultPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Resolved[Client]{}, err
		}
		options.DefaultPath = filepath.Join(home, ".config", "vex", "config.toml")
	}
	result, err := resolve(options, Client{URL: "http://127.0.0.1:8790", Timeout: "20m", Servers: map[string]ClientServer{}})
	if err != nil {
		return result, err
	}
	result.Config.Path = result.Path
	return result, result.Config.Validate()
}

type layer struct {
	values  map[string]any
	origins map[string]Origin
}

func resolve[T any](options Options, defaults T) (Resolved[T], error) {
	var result Resolved[T]
	schema := reflect.TypeOf(defaults)
	if options.Environ == nil {
		options.Environ = os.Environ()
	}
	environment, envPath, err := environmentLayer(schema, options.Environ)
	if err != nil {
		return result, err
	}
	command, cliPath, err := commandLayer(schema, options.Args)
	if err != nil {
		return result, err
	}
	path := options.DefaultPath
	explicit := false
	if envPath != nil {
		path, explicit = *envPath, true
	}
	if cliPath != nil {
		path, explicit = *cliPath, true
	}
	if path == "" || strings.ContainsAny(path, "\x00\r\n") {
		return result, fieldError("config", "requires a file path")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return result, fieldError("config", "cannot resolve file path")
	}
	file := layer{values: map[string]any{}, origins: map[string]Origin{}}
	data, err := os.ReadFile(path)
	if err != nil && !(errors.Is(err, os.ErrNotExist) && (!explicit || options.AllowMissing)) {
		return result, fieldError("config", "cannot read configuration file")
	}
	if err == nil {
		if err = toml.Unmarshal(data, &file.values); err != nil {
			return result, fieldError("config", "invalid TOML document")
		}
		if err = validateLayer(schema, file.values, ""); err != nil {
			return result, err
		}
		collectOrigins(file.values, "", Origin{Layer: "file", Name: path}, file.origins)
	}
	merged, err := asMap(defaults)
	if err != nil {
		return result, err
	}
	layers := []layer{file, environment, command}
	for _, layer := range layers {
		if err = materializeDefaults(merged, layer.values, schema, ""); err != nil {
			return result, err
		}
	}
	result.Sources = map[string]Origin{}
	collectOrigins(merged, "", Origin{Layer: "default", Name: "built-in"}, result.Sources)
	for _, layer := range layers {
		merge(merged, layer.values)
		for path, origin := range layer.origins {
			result.Sources[path] = origin
		}
	}
	encoded, err := toml.Marshal(merged)
	if err != nil {
		return result, fieldError("config", "cannot encode merged configuration")
	}
	decoder := toml.NewDecoder(bytes.NewReader(encoded)).DisallowUnknownFields()
	if err = decoder.Decode(&result.Config); err != nil {
		return result, fieldError("config", "configuration contains incompatible value types")
	}
	result.Path = path
	return result, nil
}

func asMap(value any) (map[string]any, error) {
	data, err := toml.Marshal(value)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	return result, toml.Unmarshal(data, &result)
}

func member(schema reflect.Type, name string) (reflect.Type, bool) {
	if schema.Kind() == reflect.Map {
		return schema.Elem(), ValidID(name)
	}
	if schema.Kind() != reflect.Struct {
		return nil, false
	}
	for i := 0; i < schema.NumField(); i++ {
		field := schema.Field(i)
		if field.Tag.Get("toml") == name && name != "-" {
			return field.Type, true
		}
	}
	return nil, false
}

func validateLayer(schema reflect.Type, value any, path string) error {
	if value == nil {
		return fieldError(path, "null values are not supported")
	}
	switch schema.Kind() {
	case reflect.Struct, reflect.Map:
		table, ok := value.(map[string]any)
		if !ok {
			return fieldError(path, "requires a table")
		}
		for key, child := range table {
			childType, ok := member(schema, key)
			childPath := joinKey(path, key)
			if !ok {
				return fieldError(childPath, "unknown key or invalid ID")
			}
			if err := validateLayer(childType, child, childPath); err != nil {
				return err
			}
		}
	case reflect.Slice:
		values := reflect.ValueOf(value)
		if values.Kind() != reflect.Slice {
			return fieldError(path, "requires a list")
		}
		for i := 0; i < values.Len(); i++ {
			if err := validateLayer(schema.Elem(), values.Index(i).Interface(), path); err != nil {
				return err
			}
		}
	case reflect.String:
		if _, ok := value.(string); !ok {
			return fieldError(path, "requires a string")
		}
	case reflect.Bool:
		if _, ok := value.(bool); !ok {
			return fieldError(path, "requires a boolean")
		}
	case reflect.Int:
		if reflect.TypeOf(value).Kind() != reflect.Int64 && reflect.TypeOf(value).Kind() != reflect.Int {
			return fieldError(path, "requires an integer")
		}
	default:
		return fieldError(path, "unsupported configuration type")
	}
	return nil
}

func canonicalPath(schema reflect.Type, segments []string, environment bool) ([]string, reflect.Type, error) {
	path := make([]string, 0, len(segments))
	for _, segment := range segments {
		name := segment
		if environment {
			name = strings.ToLower(name)
			if schema.Kind() == reflect.Map {
				name = strings.ReplaceAll(name, "_", "-")
			}
		} else if schema.Kind() == reflect.Struct {
			name = strings.ReplaceAll(name, "-", "_")
		}
		next, ok := member(schema, name)
		if !ok {
			return nil, nil, fieldError(strings.Join(append(path, name), "."), "unknown key or invalid ID")
		}
		path = append(path, name)
		schema = next
	}
	if schema.Kind() == reflect.Struct || schema.Kind() == reflect.Map {
		return nil, nil, fieldError(strings.Join(path, "."), "requires a leaf configuration key")
	}
	return path, schema, nil
}

func parseValue(schema reflect.Type, raw, path string) (any, error) {
	var value any
	switch schema.Kind() {
	case reflect.String:
		value = raw
	case reflect.Slice:
		if !strings.HasPrefix(strings.TrimSpace(raw), "[") {
			if schema.Elem().Kind() != reflect.String {
				return nil, fieldError(path, "requires a TOML array")
			}
			values := []string{}
			if raw != "" {
				for _, part := range strings.Split(raw, ",") {
					values = append(values, strings.TrimSpace(part))
				}
			}
			value = values
			break
		}
		fallthrough
	default:
		var parsed map[string]any
		if err := toml.Unmarshal([]byte("value = "+raw), &parsed); err != nil || len(parsed) != 1 {
			return nil, fieldError(path, "invalid TOML literal")
		}
		value = parsed["value"]
	}
	return value, validateLayer(schema, value, path)
}

func environmentLayer(schema reflect.Type, environ []string) (layer, *string, error) {
	result := layer{values: map[string]any{}, origins: map[string]Origin{}}
	var configPath *string
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, "VEX_") {
			continue
		}
		if key == "VEX_CONFIG" {
			copied := value
			configPath = &copied
			continue
		}
		path, typ, err := canonicalPath(schema, strings.Split(strings.TrimPrefix(key, "VEX_"), "__"), true)
		if err != nil {
			return result, nil, err
		}
		canonical := strings.Join(path, ".")
		parsed, err := parseValue(typ, value, canonical)
		if err != nil {
			return result, nil, err
		}
		if _, exists := result.origins[canonical]; exists {
			return result, nil, fieldError(canonical, "duplicate environment setting")
		}
		setValue(result.values, path, parsed)
		result.origins[canonical] = Origin{Layer: "environment", Name: key}
	}
	return result, configPath, nil
}

func commandLayer(schema reflect.Type, args []string) (layer, *string, error) {
	result := layer{values: map[string]any{}, origins: map[string]Origin{}}
	flags := pflag.NewFlagSet("configuration", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.String("config", "", "configuration file")
	paths := map[string][]string{}
	types := map[string]reflect.Type{}
	if schema == reflect.TypeOf(Client{}) {
		flags.StringP("project", "p", "", "project ID")
		paths["project"] = []string{"project"}
		types["project"] = reflect.TypeOf("")
	}
	for _, arg := range args {
		if arg == "-p" && schema == reflect.TypeOf(Client{}) {
			arg = "--project"
		}
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if name == "config" || flags.Lookup(name) != nil {
			continue
		}
		path, typ, err := canonicalPath(schema, strings.Split(name, "."), false)
		if err != nil {
			return result, nil, err
		}
		paths[name], types[name] = path, typ
		flags.String(name, "", "configuration value")
		if typ.Kind() == reflect.Bool {
			flags.Lookup(name).NoOptDefVal = "true"
		}
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return result, nil, fieldError("command line", "invalid configuration arguments")
	}
	var configPath *string
	var parseErr error
	flags.Visit(func(flag *pflag.Flag) {
		if parseErr != nil {
			return
		}
		if flag.Name == "config" {
			value := flag.Value.String()
			configPath = &value
			return
		}
		path := paths[flag.Name]
		canonical := strings.Join(path, ".")
		if _, exists := result.origins[canonical]; exists {
			parseErr = fieldError(canonical, "duplicate command line aliases")
			return
		}
		value, err := parseValue(types[flag.Name], flag.Value.String(), canonical)
		if err != nil {
			parseErr = err
			return
		}
		setValue(result.values, path, value)
		result.origins[canonical] = Origin{Layer: "command", Name: "--" + flag.Name}
	})
	return result, configPath, parseErr
}

func materializeDefaults(dst, src map[string]any, schema reflect.Type, path string) error {
	for key, value := range src {
		childType, ok := member(schema, key)
		if !ok {
			return fieldError(joinKey(path, key), "unknown key or invalid ID")
		}
		child, table := value.(map[string]any)
		if !table {
			continue
		}
		current, ok := dst[key].(map[string]any)
		if !ok {
			current = map[string]any{}
			if childType.Kind() == reflect.Struct {
				defaults := reflect.New(childType).Elem().Interface()
				if childType == reflect.TypeOf(Project{}) {
					defaults = defaultProject()
				}
				var err error
				current, err = asMap(defaults)
				if err != nil {
					return err
				}
			}
			dst[key] = current
		}
		if err := materializeDefaults(current, child, childType, joinKey(path, key)); err != nil {
			return err
		}
	}
	return nil
}

func merge(dst, src map[string]any) {
	for key, value := range src {
		if child, ok := value.(map[string]any); ok {
			current, ok := dst[key].(map[string]any)
			if !ok {
				current = map[string]any{}
				dst[key] = current
			}
			merge(current, child)
		} else {
			dst[key] = value
		}
	}
}

func setValue(dst map[string]any, path []string, value any) {
	for _, key := range path[:len(path)-1] {
		child, ok := dst[key].(map[string]any)
		if !ok {
			child = map[string]any{}
			dst[key] = child
		}
		dst = child
	}
	dst[path[len(path)-1]] = value
}

func collectOrigins(table map[string]any, path string, origin Origin, result map[string]Origin) {
	for key, value := range table {
		childPath := joinKey(path, key)
		if child, ok := value.(map[string]any); ok {
			collectOrigins(child, childPath, origin, result)
		} else {
			result[childPath] = origin
		}
	}
}

func joinKey(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}
