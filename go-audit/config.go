// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"go/version"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"
)

// configName is the name of the config file in the store and in this
// directory, where it is the built-in config.
const configName = "go-audit.yml"

//go:embed go-audit.yml
var builtinConfig []byte

// builtinGolangci is the standard set of golangci-lint that config -init
// puts into the store.
//
//go:embed golangci.yml
var builtinGolangci []byte

// A config is what go-audit runs and how it links a report to the code.
type config struct {
	Editor string   `yaml:"editor"`
	Checks []*check `yaml:"checks"`

	file string // the file it was read from; empty for the built-in config
}

// A check is one program run over the module, with the format its output is
// read in.
type check struct {
	Name    string   `yaml:"name"`
	Cmd     []string `yaml:"cmd"`
	Format  string   `yaml:"format"`
	Install string   `yaml:"install"`
	Go      string   `yaml:"go"`
	Note    string   `yaml:"note"`
	Off     bool     `yaml:"off"`

	line int // where the check starts in its file
}

// loadConfig returns the config in use: go-audit.yml in the store if it is
// there, the built-in config otherwise.
func loadConfig(store string) (*config, error) {
	file := filepath.Join(store, configName)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return parseConfig(builtinConfig, "")
	}
	if err != nil {
		return nil, err
	}
	return parseConfig(data, file)
}

// source names where the config came from.
func (c *config) source() string {
	if c.file == "" {
		return "built-in"
	}
	return c.file
}

// yamlLine matches the line number in an error of the YAML package.
var yamlLine = regexp.MustCompile(`^(?:yaml: )?line (\d+): `)

// parseConfig reads a config from data, which came from file; an empty file
// stands for the built-in config. An error starts with file:line, like a
// compiler's, so that an editor can jump to it.
func parseConfig(data []byte, file string) (*config, error) {
	name := file
	if name == "" {
		name = "built-in " + configName
	}
	c := &config{file: file}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty", name)
		}
		return nil, fmt.Errorf("%s", yamlError(name, err))
	}

	// The decoding above has no line numbers; the nodes do.
	var nodes struct {
		Checks []yaml.Node `yaml:"checks"`
	}
	if err := yaml.Unmarshal(data, &nodes); err == nil {
		for i, n := range nodes.Checks {
			if i < len(c.Checks) && c.Checks[i] != nil {
				c.Checks[i].line = n.Line
			}
		}
	}
	if line, err := c.validate(); err != nil {
		if line > 0 {
			return nil, fmt.Errorf("%s:%d: %v", name, line, err)
		}
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	return c, nil
}

// yamlError rewrites an error of the YAML package as file:line: message
// lines, one per problem.
func yamlError(file string, err error) string {
	var msgs []string
	if te, ok := errors.AsType[*yaml.TypeError](err); ok {
		msgs = te.Errors
	} else {
		msgs = []string{err.Error()}
	}
	r := strings.NewReplacer("type main.check", "a check", "type main.config", "the config")
	for i, m := range msgs {
		m = r.Replace(m)
		if s := yamlLine.FindStringSubmatch(m); s != nil {
			m = file + ":" + s[1] + ": " + m[len(s[0]):]
		} else {
			m = file + ": " + strings.TrimPrefix(m, "yaml: ")
		}
		msgs[i] = m
	}
	return strings.Join(msgs, "\n")
}

// checkName is what a check may be called: the name goes into file names
// and into the lists of -only and -skip.
var checkName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// validate reports the first problem of the config and the line of the
// check it is in, 0 if it is in none.
func (c *config) validate() (int, error) {
	if len(c.Checks) == 0 {
		return 0, errors.New("no checks")
	}
	first := map[string]int{}
	for i, ch := range c.Checks {
		if ch == nil {
			return 0, fmt.Errorf("check %d is empty", i+1)
		}
		if ch.Name == "" {
			return ch.line, errors.New("a check with no name")
		}
		if err := ch.validate(); err != nil {
			return ch.line, fmt.Errorf("check %s: %v", ch.Name, err)
		}
		if line, ok := first[ch.Name]; ok {
			return ch.line, fmt.Errorf("check %s is described twice, first at line %d",
				ch.Name, line)
		}
		first[ch.Name] = ch.line
	}
	for _, m := range editorPart.FindAllStringSubmatch(c.Editor, -1) {
		switch m[1] {
		case "path", "line", "col":
		default:
			return 0, fmt.Errorf("editor: unknown {%s}; it takes {path}, {line} and {col}", m[1])
		}
	}
	return 0, nil
}

// editorPart matches what the editor template substitutes.
var editorPart = regexp.MustCompile(`\{([a-z]*)\}`)

func (ch *check) validate() error {
	if !checkName.MatchString(ch.Name) {
		return errors.New("a name takes lower-case letters, digits, '-', '_' and '.'")
	}
	if len(ch.Cmd) == 0 {
		return errors.New("no cmd")
	}
	if ch.Format == "" {
		return errors.New("no format")
	}
	if _, ok := formats[ch.Format]; !ok {
		return fmt.Errorf("unknown format %q (formats: %s)", ch.Format, formatNames())
	}
	for _, arg := range ch.Cmd {
		if err := validateArg(arg); err != nil {
			return err
		}
	}
	if ch.Go != "" && !version.IsValid("go"+ch.Go) {
		return fmt.Errorf("go: %q is no Go version, such as 1.26", ch.Go)
	}
	return nil
}

// substPart matches a substitution in an argument: {name} or {name:arg}.
// Braces around anything else, such as {{.Dir}} for go list, stay as they are.
var substPart = regexp.MustCompile(`\{([a-z][a-z-]*)(?::([^{}]*))?\}`)

// substitutions are the names a cmd may use; the value says whether the name
// takes an argument after a colon.
var substitutions = map[string]bool{
	"packages":         false,
	"files":            false,
	"module":           false,
	"gotooldir":        false,
	"bin":              true,
	"file":             true,
	"golangci-config":  false,
	"project-golangci": false,
}

// manyArgs are the substitutions that stand for a list of arguments.
var manyArgs = map[string]bool{"packages": true, "files": true}

func validateArg(arg string) error {
	for _, m := range substPart.FindAllStringSubmatchIndex(arg, -1) {
		part := arg[m[0]:m[1]]
		name := arg[m[2]:m[3]]
		hasArg := m[4] >= 0
		takesArg, ok := substitutions[name]
		switch {
		case !ok:
			return fmt.Errorf("unknown substitution %s", part)
		case takesArg && (!hasArg || m[4] == m[5]):
			return fmt.Errorf("%s needs a name: {%s:NAME}", part, name)
		case !takesArg && hasArg:
			return fmt.Errorf("%s takes no name: {%s}", part, name)
		case manyArgs[name] && part != arg:
			return fmt.Errorf("%s stands for several arguments; "+
				"give it an argument of its own", part)
		case name == "file" && !filepath.IsLocal(arg[m[4]:m[5]]):
			return fmt.Errorf("%s: the file is to be in the module root", part)
		}
	}
	return nil
}

// --------------------------------------------------------------------------
// go-audit config

func configCommand() *command {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	initFlag := fs.Bool("init", false,
		"put "+configName+" and "+golangciName+" into the store, unless they are there already")
	return &command{
		name:  "config",
		usage: "config [-init]",
		short: "Show the config in use; -init puts copies of the built-in ones in the store",
		flags: fs,
		run: func(_ context.Context, args []string) error {
			if len(args) > 0 {
				return usagef("config takes no arguments")
			}
			store, err := storeDir()
			if err != nil {
				return err
			}
			if *initFlag {
				return initConfig(stdout, store)
			}
			return showConfig(stdout, store)
		},
	}
}

// showConfig prints which config is in use and its checks; for a config of
// one's own, also what the built-in config has that it does not.
func showConfig(w io.Writer, store string) error {
	c, err := loadConfig(store)
	if err != nil {
		return err
	}
	golangci := filepath.Join(store, golangciName)
	if _, err := os.Stat(golangci); err != nil {
		golangci = "none: golangci-lint runs its standard set with --no-config"
	}
	editor := c.Editor
	if editor == "" {
		editor = "none: a file:line opens the file itself"
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if c.file == "" {
		_, _ = fmt.Fprintf(tw, "config:\tbuilt-in; go-audit config -init puts a copy in %s\n", store)
	} else {
		_, _ = fmt.Fprintf(tw, "config:\t%s\n", c.file)
	}
	_, _ = fmt.Fprintf(tw, "editor:\t%s\n", editor)
	_, _ = fmt.Fprintf(tw, "golangci:\t%s\n", golangci)
	if err := tw.Flush(); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "\nchecks, in order:\n")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, ch := range c.Checks {
		var when []string
		if ch.Off {
			when = append(when, "off")
		}
		if ch.Go != "" {
			when = append(when, "go "+ch.Go)
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
			ch.Name, ch.Format, strings.Join(when, ", "), strings.Join(ch.Cmd, " "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if c.file == "" {
		return nil
	}

	builtin, err := parseConfig(builtinConfig, "")
	if err != nil {
		return err
	}
	missing, changed := compareConfigs(c, builtin)
	if len(missing) > 0 || len(changed) > 0 {
		_, _ = fmt.Fprintf(w, "\nthe built-in config of this go-audit\n")
	}
	if len(missing) > 0 {
		_, _ = fmt.Fprintf(w, "  has checks yours lacks: %s\n", strings.Join(missing, ", "))
	}
	if len(changed) > 0 {
		_, err = fmt.Fprintf(w, "  describes otherwise: %s\n", strings.Join(changed, ", "))
	}
	return err
}

// compareConfigs returns the checks of builtin that mine lacks, and those
// mine describes otherwise, with the fields that differ: "vet (cmd, note)".
// They are in the order of builtin, so that what an update of go-audit
// brought is not lost silently.
func compareConfigs(mine, builtin *config) (missing, changed []string) {
	have := map[string]*check{}
	for _, ch := range mine.Checks {
		have[ch.Name] = ch
	}
	for _, b := range builtin.Checks {
		m := have[b.Name]
		if m == nil {
			missing = append(missing, b.Name)
			continue
		}
		var fields []string
		if !slices.Equal(m.Cmd, b.Cmd) {
			fields = append(fields, "cmd")
		}
		for _, f := range []struct {
			name      string
			mine, ref string
		}{
			{"format", m.Format, b.Format},
			{"install", m.Install, b.Install},
			{"go", m.Go, b.Go},
			{"note", m.Note, b.Note},
		} {
			if f.mine != f.ref {
				fields = append(fields, f.name)
			}
		}
		if m.Off != b.Off {
			fields = append(fields, "off")
		}
		if len(fields) > 0 {
			changed = append(changed, b.Name+" ("+strings.Join(fields, ", ")+")")
		}
	}
	return missing, changed
}

// initConfig puts the built-in config and the standard set of golangci-lint
// into the store, each unless a file of its name is there already: one's own
// is never overwritten.
func initConfig(w io.Writer, store string) error {
	if err := os.MkdirAll(store, 0o755); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		data []byte
	}{
		{configName, builtinConfig},
		{golangciName, builtinGolangci},
	} {
		path := filepath.Join(store, f.name)
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			if _, err := fmt.Fprintf(w, "kept %s: it is there already\n", path); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		_, err = out.Write(f.data)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "wrote %s\n", path); err != nil {
			return err
		}
	}
	return nil
}
