package chroniclekit

import "strings"

// DiffOption declares the caller's document schema to the kit: which arrays
// have element identity, which fields are bookkeeping noise, which object
// shapes are value types, and how field names resolve to human labels. One
// vocabulary, two consumers — Diff uses the write-shaping options (array keys,
// value types); Explain uses the read-decoration options (array keys, labels,
// name fields, ignored fields). The kit hardcodes no domain content.
type DiffOption func(*diffConfig)

type diffConfig struct {
	arrayKeys    map[string]string // index-free dotted schema path of array → dot-path to identity field
	labels       func(path []string) (string, bool)
	ignoredNames map[string]struct{} // bare entries: field name, any depth
	ignoredPaths map[string]struct{} // dotted entries: index-free schema path
	valueTypes   []ValueType
	nameFields   []string
}

func newDiffConfig(opts []DiffOption) diffConfig {
	cfg := diffConfig{nameFields: []string{"name"}}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// WithArrayKeys declares element identity per array, keyed by the array's
// index-free dotted field path in the document ("" for a root-level array),
// mapping to a dot-path into each element, e.g.
// {"lines": "product.id", "approvers": "userId"}. Arrays without a usable
// declared key fall back to an "id" field on every element, then to
// positional (index) pairing.
func WithArrayKeys(keys map[string]string) DiffOption {
	return func(c *diffConfig) { c.arrayKeys = keys }
}

// WithLabels installs the caller's label resolver. It receives the index-free
// schema path of a field and returns its label (typically an i18n key), used
// verbatim — the kit never translates. Return ok=false to fall back to the
// default Title Case of the field name.
func WithLabels(resolve func(path []string) (label string, ok bool)) DiffOption {
	return func(c *diffConfig) { c.labels = resolve }
}

// WithIgnoredFields names bookkeeping fields — revision markers,
// created/updated stamps — to hide from the human read side. An entry is
// either a bare field name, matched at any depth, or an index-free dotted
// schema path (the WithArrayKeys vocabulary), matching that field and its
// subtree — "meta.rev" folds the bookkeeping marker without touching a data
// field also named rev elsewhere. Recording is UNAFFECTED: the changelog
// stays a full data record and replay reproduces these fields; Explain merely
// flags their changes as Bookkeeping so displays can fold them away. Appends
// to any earlier call; the kit ships no defaults.
func WithIgnoredFields(entries ...string) DiffOption {
	return func(c *diffConfig) {
		for _, e := range entries {
			set := &c.ignoredNames
			if strings.Contains(e, ".") {
				set = &c.ignoredPaths
			}
			if *set == nil {
				*set = make(map[string]struct{})
			}
			(*set)[e] = struct{}{}
		}
	}
}

// WithValueTypes declares value-object shapes to compare canonically.
func WithValueTypes(vts ...ValueType) DiffOption {
	return func(c *diffConfig) { c.valueTypes = append(c.valueTypes, vts...) }
}

// WithNameFields replaces the default {"name"} list of fields tried, in
// order, for an element's display name.
func WithNameFields(names ...string) DiffOption {
	return func(c *diffConfig) { c.nameFields = names }
}

// ValueType is a caller-declared value-object shape (e.g. a decimal or money
// encoding). An object whose key set exactly matches Fields is compared and
// recorded via Canon's canonical JSON scalar instead of being diffed
// field-by-field, so different encodings of the same value are not changes.
// Canon returning false declines the object (normal diff applies).
type ValueType struct {
	Fields []string
	Canon  func(obj map[string]any) (string, bool)
}

// labelFor resolves the label of the field at schemaPath: caller's resolver
// first, else Title Case of the final segment. Never called for array
// indices.
func labelFor(cfg *diffConfig, schemaPath []string) string {
	if cfg.labels != nil {
		if l, ok := cfg.labels(schemaPath); ok {
			return l
		}
	}
	return humanize(schemaPath[len(schemaPath)-1])
}

// humanize renders a field name as a Title Case label: word boundaries at
// '_', '-', and lower/digit→upper camelCase transitions.
func humanize(seg string) string {
	var words []string
	var w strings.Builder
	flush := func() {
		if w.Len() > 0 {
			words = append(words, w.String())
			w.Reset()
		}
	}
	prevLower := false
	for _, r := range seg {
		switch {
		case r == '_' || r == '-':
			flush()
			prevLower = false
		case r >= 'A' && r <= 'Z':
			if prevLower {
				flush()
			}
			w.WriteRune(r)
			prevLower = false
		default:
			w.WriteRune(r)
			prevLower = r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		}
	}
	flush()
	for i, word := range words {
		r := []rune(word)
		words[i] = strings.ToUpper(string(r[:1])) + strings.ToLower(string(r[1:]))
	}
	return strings.Join(words, " ")
}
