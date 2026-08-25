package configfile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"go.yaml.in/yaml/v3"
)

// This file is the write half of the package: it edits a configuration file as a
// yaml.Node tree rather than by decoding into File and marshalling back.
//
// The reason is comments. A user's moraine.yaml is a hand-maintained file, and
// decode-then-marshal would silently delete every comment in it, along with any key
// this version of moraine does not know about. Editing the node tree keeps both, and
// keeps the keys in the order the user wrote them.
//
// What is NOT preserved: blank-line spacing and indentation width. yaml.v3 does not
// record blank lines between plain keys, and re-emits the tree with its own layout,
// so the first write normalises the file to indentWidth. Every write after that is a
// no-op on layout — the round trip is stable — which is why the first one is worth
// the trade.

// indentWidth is the nesting indentation a written file uses. Two spaces matches the
// example in README.md, and yaml.v3 would otherwise use four.
const indentWidth = 2

// header is the comment a file moraine creates itself opens with. A file the user
// already wrote keeps its own head comment instead.
const header = "moraine configuration — see `moraine config --help`.\n" +
	"Hand edits and comments are preserved by `moraine config set`."

// tmpPattern names the staging file a save publishes from. It mirrors the
// .moraine-*.tmp convention internal/organize uses on the copy path.
const tmpPattern = ".moraine-config-*.tmp"

// Document is one configuration file, held as a node tree so that an edit can
// preserve everything it does not touch. Open reads it, Set and Unset change it, and
// Save writes it back atomically.
type Document struct {
	path     string
	root     *yaml.Node // a DocumentNode whose single child is the top-level mapping
	existed  bool
	original []byte // what was read, so Changed can tell a real edit from a no-op
}

// Target reports the file a write should go to, resolved exactly like the file a run
// reads (see Load). The difference is what "there is no file" means: reading simply
// has no configuration, while writing has nowhere to put one, so the two cases Load
// treats as silence are errors here.
func Target(explicit string) (Location, error) {
	loc, _, err := candidate(explicit)
	return loc, err
}

// Open reads path into a Document. A file that does not exist yet is not an error —
// it yields an empty document carrying moraine's own header comment, so `config set`
// works before there is any configuration file at all.
func Open(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return blank(path), nil
	case err != nil:
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("config file %q: %w", path, err)
	}
	// A file that is empty, or holds nothing but comments, decodes to no node at all.
	// It is a valid configuration that sets nothing, so treat it as a starting point
	// rather than an error.
	if root.Kind == 0 {
		d := blank(path)
		d.existed = true
		return d, nil
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 { //nolint:nestif
		return nil, fmt.Errorf("config file %q does not hold a single YAML document", path)
	}
	if root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf(
			"config file %q must hold a mapping of settings at the top level, not %s",
			path, kindName(root.Content[0].Kind))
	}
	return &Document{path: path, root: &root, existed: true, original: data}, nil
}

// Changed reports whether saving would alter anything: whether the document now
// renders differently from the file it was read from, or there is no file yet and
// there is something to put in one.
//
// It is what keeps a command that changes nothing from touching the file — an "edit"
// where every question was accepted as it stood must not create a configuration file,
// nor bump the modification time of an existing one.
func (d *Document) Changed() (bool, error) {
	if !d.existed {
		return len(d.mapping().Content) > 0, nil
	}
	data, err := d.Bytes()
	if err != nil {
		return false, err
	}
	return !bytes.Equal(d.original, data), nil
}

// Path reports the file this document was read from and will be saved to.
func (d *Document) Path() string { return d.path }

// Existed reports whether the file was already on disk when it was opened.
func (d *Document) Existed() bool { return d.existed }

// Set stores value at keys, a path such as ["sort", "gap"] or ["output"], creating
// the intermediate mappings it needs.
//
// An existing setting is rewritten in place rather than replaced, because a comment
// on the same line ("gap: 6h  # a long day") belongs to the value node: swapping that
// node for a fresh one is exactly how such a comment would get lost.
func (d *Document) Set(keys []string, value *yaml.Node) error {
	if len(keys) == 0 {
		return errors.New("no setting named")
	}
	parent := d.mapping()
	for _, key := range keys[:len(keys)-1] {
		next, err := section(parent, key)
		if err != nil {
			return fmt.Errorf("config file %q: %w", d.path, err)
		}
		parent = next
	}

	leaf := keys[len(keys)-1]
	if _, existing := find(parent, leaf); existing != nil {
		existing.Kind = value.Kind
		existing.Tag = value.Tag
		existing.Value = value.Value
		existing.Style = value.Style
		existing.Content = value.Content
		return nil
	}
	insert(parent, scalar(leaf), value)
	return nil
}

// insert adds a key/value pair to a mapping, before the first pair whose value is a
// section. YAML does not care, but a reader does: a file that reads
//
//	log_level: warn
//	sort:
//	  gap: 8h
//
// is the shape README documents, while appending would put a new top-level setting
// below the section and make it look nested.
func insert(parent *yaml.Node, name, value *yaml.Node) {
	if value.Kind != yaml.MappingNode {
		for i := 0; i+1 < len(parent.Content); i += 2 {
			if parent.Content[i+1].Kind != yaml.MappingNode {
				continue
			}
			// The first section moves down, so the blank line moraine put before it
			// travels with it and the new setting does not inherit it.
			name.HeadComment, parent.Content[i].HeadComment =
				parent.Content[i].HeadComment, name.HeadComment
			parent.Content = slices.Insert(parent.Content, i, name, value)
			return
		}
	}
	parent.Content = append(parent.Content, name, value)
}

// Unset removes the setting at keys and reports whether it was there. A section left
// with no settings is removed too, so unsetting the last key of a section does not
// leave "sort: {}" behind.
func (d *Document) Unset(keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	return remove(d.mapping(), keys)
}

// Bytes renders the document as it would be saved.
func (d *Document) Bytes() ([]byte, error) {
	// An empty mapping encodes as "{}", which is valid YAML but reads as debris in a
	// file a user opens. Emit the comments alone: a file with no keys is one that
	// sets nothing, which is what read already does with it.
	if len(d.mapping().Content) == 0 {
		if d.root.HeadComment == "" {
			return nil, nil
		}
		// The head comment already carries its "# " markers, as every yaml.v3
		// comment does; it is emitted rather than re-commented.
		return []byte(d.root.HeadComment + "\n"), nil
	}

	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(indentWidth)
	if err := enc.Encode(d.root); err != nil {
		return nil, fmt.Errorf("rendering config file %q: %w", d.path, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("rendering config file %q: %w", d.path, err)
	}
	return b.Bytes(), nil
}

// Save writes the document back to its path atomically: a staging file in the same
// directory, fsynced, then renamed over the target, then the directory fsynced. That
// is the discipline internal/organize uses to publish a copy, with one deliberate
// difference — this renames rather than hard-linking, because replacing the existing
// file is the whole point here.
func (d *Document) Save() error {
	data, err := d.Bytes()
	if err != nil {
		return err
	}

	dir := filepath.Dir(d.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return fmt.Errorf("staging config file in %q: %w", dir, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once the rename has succeeded

	if err := writeAndSync(tmp, data); err != nil {
		return fmt.Errorf("writing config file %q: %w", name, err)
	}
	if err := os.Rename(name, d.path); err != nil {
		return fmt.Errorf("publishing config file %q: %w", d.path, err)
	}
	syncDir(dir)
	return nil
}

// mapping returns the top-level mapping, which Open and blank both guarantee.
func (d *Document) mapping() *yaml.Node { return d.root.Content[0] }

// blank builds an empty document for a file that is not there yet.
func blank(path string) *Document {
	return &Document{
		path: path,
		root: &yaml.Node{
			Kind:        yaml.DocumentNode,
			HeadComment: commentBlock(header),
			Content:     []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
		},
	}
}

// writeAndSync writes data to f and flushes it to the device before closing, so a
// crash between the write and the rename cannot leave a published-but-empty file.
func writeAndSync(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// syncDir flushes a directory entry so the rename survives a crash. A filesystem
// that cannot be opened as a directory (or refuses the fsync) is not worth failing a
// completed write over, so the error is dropped.
func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}

// find locates key in a mapping node, returning the index of its key node and the
// value node beside it. A mapping's Content alternates key, value, key, value.
func find(m *yaml.Node, key string) (int, *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i, m.Content[i+1]
		}
	}
	return -1, nil
}

// section returns the mapping stored under key, creating it when absent. A new
// section gets a blank line before it (yaml.v3 spells that as a head comment opening
// with a newline), so a generated file does not run its sections together.
func section(parent *yaml.Node, key string) (*yaml.Node, error) {
	if _, existing := find(parent, key); existing != nil {
		if existing.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%q holds %s, not a section of settings", key, kindName(existing.Kind))
		}
		return existing, nil
	}
	name := scalar(key)
	if len(parent.Content) > 0 {
		name.HeadComment = "\n"
	}
	created := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, name, created)
	return created, nil
}

// remove deletes the setting at keys from m, pruning any section it empties, and
// reports whether it found anything to delete.
func remove(m *yaml.Node, keys []string) bool {
	i, value := find(m, keys[0])
	if value == nil {
		return false
	}
	if len(keys) == 1 {
		m.Content = slices.Delete(m.Content, i, i+2)
		return true
	}
	if value.Kind != yaml.MappingNode || !remove(value, keys[1:]) {
		return false
	}
	if len(value.Content) == 0 {
		m.Content = slices.Delete(m.Content, i, i+2)
	}
	return true
}

// scalar builds a plain string node, which is what every key name is.
func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// commentBlock turns plain lines into a YAML comment block. yaml.v3 stores comments
// with their "# " markers included, so they have to be added rather than implied.
func commentBlock(text string) string {
	var b bytes.Buffer
	for i, line := range bytes.Split([]byte(text), []byte("\n")) {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("# ")
		b.Write(line)
	}
	return b.String()
}

// kindName names a node kind for an error message, so a user who put a list where a
// mapping belongs is told which is which.
func kindName(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "a document"
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "a mapping"
	case yaml.ScalarNode:
		return "a single value"
	case yaml.AliasNode:
		return "an alias"
	default:
		return "an unknown kind of node"
	}
}
