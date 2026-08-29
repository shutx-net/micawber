package markdown

import (
	"bytes"

	"go.yaml.in/yaml/v3"

	"github.com/shutx-net/micawber/core"
)

// yamlCodec reads and writes YAML front matter, the "---" block that Jekyll,
// Hugo, Astro and Obsidian all understand.
//
// This is the only file that imports go.yaml.in/yaml/v3, and
// markdown/architecture_test.go fails if that changes. The path is unfamiliar
// because the gopkg.in module was frozen at v3.0.1 when its author retired the
// project in April 2025; the YAML organisation continues the same code at
// go.yaml.in/yaml, whose v3 line still receives fixes.
//
// The library is chosen for one specific reason: yaml.Node exposes a mapping as
// an ordered slice of key and value nodes carrying their comments and scalar
// styles, which is exactly what re-emitting an edited block without disturbing
// the untouched entries needs.
type yamlCodec struct{}

// decode reads a YAML mapping into fields. A block that holds nothing -- empty,
// or only comments -- decodes to an empty map rather than to nil, so that an
// empty block is distinguishable from no block at all.
func (yamlCodec) decode(raw []byte) (map[string]any, error) {
	mapping, err := yamlMapping(raw)
	if err != nil {
		return nil, err
	}
	if mapping == nil {
		return map[string]any{}, nil
	}
	var fields map[string]any
	if err := mapping.Decode(&fields); err != nil {
		return nil, yamlInvalid(err, "is not a mapping of string keys")
	}
	return nonNilFields(fields), nil
}

// encode writes fields as a YAML mapping.
//
// With a previously authored block the output is that block patched: every key
// the edit did not touch keeps its position, its comments and its original
// scalar text, so an edit to one field is a diff of one line. With no prev the
// output is canonical, keys sorted, because yaml.Marshal sorts map keys.
func (yamlCodec) encode(fields map[string]any, prev []byte) ([]byte, error) {
	if len(prev) == 0 {
		out, err := yaml.Marshal(fields)
		if err != nil {
			// The library's message may quote the offending value.
			return nil, invalidf(core.FrontMatterYAML, "cannot be encoded")
		}
		return out, nil
	}

	patched, err := yamlPatch(prev, fields)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(yamlIndent(prev))
	if err := encoder.Encode(patched); err != nil {
		return nil, invalidf(core.FrontMatterYAML, "cannot be encoded")
	}
	if err := encoder.Close(); err != nil {
		return nil, invalidf(core.FrontMatterYAML, "cannot be encoded")
	}
	return buf.Bytes(), nil
}

// yamlPatch rebuilds the authored block's mapping node against fields.
//
// An authored key whose value is unchanged keeps its own value node, which is
// what carries the quoting, the original scalar text and any nested formatting.
// A changed value gets a fresh node, with the line's comments moved onto it.
// Keys the edit removed are dropped, and keys it added are appended in sorted
// order, so a write is deterministic.
//
// A changed nested value has its whole value node replaced, so comments inside
// that subtree are lost while the rest of the block is untouched. Patching
// inside a subtree is deliberately not attempted: nothing can yet produce such
// an edit.
func yamlPatch(prev []byte, fields map[string]any) (*yaml.Node, error) {
	authored, err := yamlMapping(prev)
	if err != nil {
		return nil, err
	}

	patched := &yaml.Node{Kind: yaml.MappingNode}
	kept := make(map[string]bool, len(fields))
	for i := 0; authored != nil && i+1 < len(authored.Content); i += 2 {
		key, value := authored.Content[i], authored.Content[i+1]
		edited, ok := fields[key.Value]
		if !ok || kept[key.Value] {
			continue
		}
		kept[key.Value] = true

		var authoredValue any
		if err := value.Decode(&authoredValue); err != nil {
			return nil, yamlInvalid(err, "is not a mapping of string keys")
		}
		if valuesEqual(authoredValue, edited) {
			patched.Content = append(patched.Content, key, value)
			continue
		}

		replacement, err := yamlValue(edited)
		if err != nil {
			return nil, err
		}
		// The comments belong to the line rather than to the value, so they
		// survive an edit to it.
		replacement.HeadComment = value.HeadComment
		replacement.LineComment = value.LineComment
		replacement.FootComment = value.FootComment
		patched.Content = append(patched.Content, key, replacement)
	}

	for _, key := range orderedKeys(fields, nil) {
		if kept[key] {
			continue
		}
		value, err := yamlValue(fields[key])
		if err != nil {
			return nil, err
		}
		name := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
		patched.Content = append(patched.Content, name, value)
	}
	return patched, nil
}

// yamlValue encodes one value as a node.
func yamlValue(value any) (*yaml.Node, error) {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		// The library's message may quote the offending value.
		return nil, invalidf(core.FrontMatterYAML, "cannot be encoded")
	}
	return &node, nil
}

// yamlIndent returns the number of spaces prev indents a nested level by, or two.
//
// The emitter re-indents the whole block from its own setting, so reading the
// authored indentation is what keeps an untouched nested mapping or list off the
// diff.
func yamlIndent(prev []byte) int {
	for line := range bytes.Lines(normalizeLF(prev)) {
		spaces := 0
		for spaces < len(line) && line[spaces] == ' ' {
			spaces++
		}
		// An indented comment says nothing about the structural indentation.
		if spaces > 0 && spaces < len(line) && line[spaces] != '#' {
			return spaces
		}
	}
	return 2
}

// yamlMapping parses raw and returns its top-level mapping node, or nil for a
// block that holds no content at all.
//
// It returns the node rather than a map because the encoder needs the same
// parse: an edited block is re-emitted from the authored nodes, which is what
// carries key order, comments and quoting through a write.
func yamlMapping(raw []byte) (*yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(normalizeLF(raw), &document); err != nil {
		return nil, yamlInvalid(err, "is not well formed")
	}

	content := &document
	if document.Kind == yaml.DocumentNode {
		if len(document.Content) == 0 {
			return nil, nil
		}
		content = document.Content[0]
	}

	switch {
	case content.Kind == 0, content.Tag == "!!null":
		return nil, nil
	case content.Kind != yaml.MappingNode:
		return nil, invalidf(core.FrontMatterYAML, "is not a mapping")
	}
	return content, nil
}

// yamlInvalid wraps a yaml/v3 failure as a [core.ErrInvalid].
//
// The library's own message is deliberately dropped rather than wrapped: it can
// quote an anchor name, a duplicate key or another token from the block, and the
// block is where a credential would be. Only the line number survives, which is
// a number.
func yamlInvalid(err error, reason string) error {
	if line, ok := errorLine(err); ok {
		return invalidf(core.FrontMatterYAML, "%s at block line %d", reason, line)
	}
	return invalidf(core.FrontMatterYAML, "%s", reason)
}
