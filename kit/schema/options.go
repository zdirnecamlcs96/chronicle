package chronicleschema

import (
	"strings"

	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
)

// Option declares the caller's document schema to the kit: which arrays have
// element identity, which fields are bookkeeping noise, which object shapes are
// value types, and how field names resolve to human labels. One vocabulary, two
// consumers — chroniclediff.Diff uses the write-shaping options (array keys,
// identity fields, value types); chronicleexplain.Explain uses the
// read-decoration options (array keys, identity fields, value types, labels,
// name fields, names, ignored fields). The kit hardcodes no domain content.
type Option func(*Config)

// Config is a resolved set of Options. Build one with New — the zero Config is
// not usable, because it lacks the default name field. Its state is read
// through methods rather than exported fields so a caller cannot construct one
// that bypasses those defaults.
type Config struct {
	arrayKeys      map[string]string // index-free dotted schema path of array → dot-path to identity field
	identityFields []string          // generic element identity, tried in order; no default
	labels         func(path []string) (string, bool)
	ignoredNames   map[string]struct{} // bare entries: field name, any depth
	ignoredPaths   map[string]struct{} // dotted entries: index-free schema path
	valueTypes     []ValueType
	nameFields     []string
	names          map[string]string // caller-seeded id→name, canonical-JSON keys
	strictIdentity bool
}

// New resolves opts into a Config.
func New(opts ...Option) Config {
	cfg := Config{nameFields: []string{"name"}}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// WithArrayKeys declares element identity per array, keyed by the array's
// index-free dotted field path in the document ("" for a root-level array),
// mapping to a dot-path into each element, e.g.
// {"lines": "product.id", "approvers": "userId"}. Arrays without a usable
// declared key fall back to WithIdentityFields, then to positional (index)
// pairing.
func WithArrayKeys(keys map[string]string) Option {
	return func(c *Config) { c.arrayKeys = keys }
}

// WithIdentityFields declares the fields tried, in order, as an array's
// generic element identity — the fallback for arrays WithArrayKeys does not
// name, e.g. WithIdentityFields("id"). The kit ships NO default: it knows no
// field names, so an array with neither a declared key nor a usable generic
// field pairs positionally. Identity shapes what is RECORDED, so a declaration
// made after the fact cannot re-key commits already sealed; declare it before
// the first write and keep it stable. Replaces any earlier call.
func WithIdentityFields(fields ...string) Option {
	return func(c *Config) { c.identityFields = fields }
}

// WithStrictIdentity makes an array of objects with no usable element identity
// a write-time error instead of a silent fall back to positional pairing.
// Positional pairing is a legitimate choice — it is the right one where order
// is the data — but it is the wrong one by ACCIDENT when WithArrayKeys or
// WithIdentityFields was simply forgotten, and identity shapes what is
// recorded, so no later declaration can re-key the commits already sealed. This
// turns that silent, permanent divergence into a failed write. Opt-in: without
// it, Diff pairs positionally as before.
func WithStrictIdentity() Option {
	return func(c *Config) { c.strictIdentity = true }
}

// WithLabels installs the caller's label resolver. It receives the index-free
// schema path of a field and returns its label (typically an i18n key), used
// verbatim — the kit never translates. Return ok=false to fall back to the
// default Title Case of the field name.
func WithLabels(resolve func(path []string) (label string, ok bool)) Option {
	return func(c *Config) { c.labels = resolve }
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
func WithIgnoredFields(entries ...string) Option {
	return func(c *Config) {
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

// WithValueTypes declares value-object shapes to compare canonically. Diff
// records a declared shape as its canonical scalar; Explain keeps the shape a
// scalar leaf when it appears inside a container value.
func WithValueTypes(vts ...ValueType) Option {
	return func(c *Config) { c.valueTypes = append(c.valueTypes, vts...) }
}

// WithNameFields replaces the default {"name"} list of fields tried, in
// order, for an element's display name.
func WithNameFields(names ...string) Option {
	return func(c *Config) { c.nameFields = names }
}

// WithNames seeds the id→name index with pairs the replayed document cannot
// supply — ids referencing entities stored outside it. Keys are ids as the
// caller holds them, plain or already canonical-JSON; both normalise to the
// same entry. Names found in the replayed revisions WIN, so a stale dictionary
// can never override the document. Read-side only: nothing here is recorded,
// and Diff ignores it. Data, not a resolver — a schema declared in one process
// must survive serialisation to another. Appends to any earlier call.
func WithNames(names map[string]string) Option {
	return func(c *Config) {
		if c.names == nil {
			c.names = make(map[string]string, len(names))
		}
		for id, n := range names {
			c.names[canonID(id)] = n
		}
	}
}

// canonID encodes a caller-supplied id the way the id→name index keys it:
// canonical JSON. A plain "u1" and an already-encoded `"u1"` land on the same
// key, so callers never have to guess the encoding.
func canonID(id string) string {
	if v := docmodel.ParseJSON(id); v != nil && !docmodel.IsContainer(v) {
		return docmodel.CanonJSON(v)
	}
	return docmodel.CanonJSON(id)
}

// ValueType is a caller-declared value-object shape (e.g. a decimal or money
// encoding). An object whose key set exactly matches Fields is compared and
// recorded via Canon's canonical JSON scalar instead of being diffed
// field-by-field, so different encodings of the same value are not changes.
// Canon returning false declines the object (normal diff applies). Canon must
// return a JSON scalar — it is stored verbatim and replay parses it: Diff
// refuses anything else (chroniclediff.ErrBadCanon), Explain treats it as
// declined.
type ValueType struct {
	Fields []string
	Canon  func(obj map[string]any) (string, bool)
}

// Label resolves the label of the field at schemaPath: caller's resolver
// first, else Title Case of the final segment. Never called for array
// indices.
func (c *Config) Label(schemaPath []string) string {
	if c.labels != nil {
		if l, ok := c.labels(schemaPath); ok {
			return l
		}
	}
	return humanize(schemaPath[len(schemaPath)-1])
}

// Bookkeeping reports whether the field at schema (whose final segment is
// seg) is a WithIgnoredFields entry: its bare name, or its schema path.
// Subtree coverage needs no prefix walk here — the caller checks every segment
// as it descends, and marks the ancestor node itself.
func (c *Config) Bookkeeping(seg string, schema []string) bool {
	if _, ok := c.ignoredNames[seg]; ok {
		return true
	}
	_, ok := c.ignoredPaths[docmodel.JoinPath(schema)]
	return ok
}

// Canon returns the canonical scalar for v when v is an object whose key set
// exactly matches a declared ValueType and its Canon accepts. The first key-set
// match decides; a declined Canon means normal field-wise diffing.
func (c *Config) Canon(v any) (string, bool) {
	obj, isObj := v.(map[string]any)
	if !isObj {
		return "", false
	}
outer:
	for _, vt := range c.valueTypes {
		if len(vt.Fields) != len(obj) {
			continue
		}
		for _, f := range vt.Fields {
			if _, present := obj[f]; !present {
				continue outer
			}
		}
		return vt.Canon(obj)
	}
	return "", false
}

// IdentityKey walks the identity chain for the array at schema:
// caller-configured key → each WithIdentityFields entry in order → none
// (positional). A candidate holds only when every element of every side given
// is an object with a unique scalar at the key path; an empty side passes
// vacuously.
//
// Sides is the only thing the write and read sides disagree about. The write
// side passes both revisions — a key that shapes what is RECORDED must hold for
// before and after alike. The read side passes the one revision in hand. With
// no sides at all the first declared candidate is accepted unchecked.
func (c *Config) IdentityKey(schema []string, sides ...[]any) ([]string, bool) {
	usable := func(kp []string) bool {
		for _, side := range sides {
			if !docmodel.UsableKey(kp, side) {
				return false
			}
		}
		return true
	}
	if cfgPath, ok := c.arrayKeys[docmodel.JoinPath(schema)]; ok {
		if kp := docmodel.SplitPath(cfgPath); usable(kp) {
			return kp, true
		}
	}
	for _, f := range c.identityFields {
		if kp := []string{f}; usable(kp) {
			return kp, true
		}
	}
	return nil, false
}

// ArrayKey returns the declared key path for the array at schema, without the
// usability check IdentityKey applies. ok is false if none was declared.
func (c *Config) ArrayKey(schema []string) ([]string, bool) {
	p, ok := c.arrayKeys[docmodel.JoinPath(schema)]
	if !ok {
		return nil, false
	}
	return docmodel.SplitPath(p), true
}

// IdentityFields returns the declared generic identity fields, in order.
func (c *Config) IdentityFields() []string { return c.identityFields }

// StrictIdentity reports whether WithStrictIdentity was declared.
func (c *Config) StrictIdentity() bool { return c.strictIdentity }

// NameFields returns the fields tried, in order, for an element's display name.
func (c *Config) NameFields() []string { return c.nameFields }

// Names returns the caller-seeded id→name pairs, keyed by canonical JSON.
func (c *Config) Names() map[string]string { return c.names }

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
