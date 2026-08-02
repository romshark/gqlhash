// Package allowlist holds the GraphQL documents a request may carry.
// An [Allowlist] reads them from a directory and answers whether a given
// document is one of them.
//
// A .graphqls file in that directory is read as a schema,
// and every document is then checked against it.
package allowlist

import (
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gqlparser "github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/parser"
)

// Allowlist is the set of documents a request may carry, read from a directory
// by [Allowlist.Reload]. Safe for concurrent use: a reload doesn't disturb the
// calls in flight, which are answered by the load before it.
type Allowlist struct {
	newHash func() hash.Hash
	options gqlhash.Options

	// current is nil until the first Reload: no allowlist rather than an empty one.
	current atomic.Pointer[list]

	// loading serializes [Allowlist.Reload], which several callers may reach at once.
	loading sync.Mutex
}

// list is one published set of hashes and when it was published.
// Immutable: a reload swaps a whole one.
type list struct {
	docs     map[string]struct{}
	loadedAt time.Time
}

// New returns an empty allowlist. It allows nothing until the first
// [Allowlist.Reload], which is what reads a directory.
func New(newHash func() hash.Hash, options gqlhash.Options) *Allowlist {
	return &Allowlist{newHash: newHash, options: options}
}

// Allowed reports whether a request may carry the document with key,
// the hash of its canonical form, so formatting makes no difference.
// The lookup allocates nothing, so key may be a subslice of a request body.
//
// Nothing is allowed before the first [Allowlist.Reload].
func (a *Allowlist) Allowed(key []byte) bool {
	l := a.current.Load()
	if l == nil {
		return false
	}
	_, ok := l.docs[string(key)]
	return ok
}

func (a *Allowlist) Len() int {
	l := a.current.Load()
	if l == nil {
		return 0
	}
	return len(l.docs)
}

// Stats returns what the allowlist holds and when it was loaded, both from the
// same load — reading them apart would let a reload in between pair one load's
// count with another's time.
//
// loadedAt is the zero time before the first [Allowlist.Reload].
func (a *Allowlist) Stats() (documents int, loadedAt time.Time) {
	l := a.current.Load()
	if l == nil {
		return 0, time.Time{}
	}
	return len(l.docs), l.loadedAt
}

// Result is what an [Allowlist.Reload] published. Reporting it is the caller's:
// which of it deserves an event, and at which level.
type Result struct {
	// Files is the file of every document on the allowlist, in the order they
	// were read. Empty rather than nil where a load took nothing.
	Files []string

	// Skipped is one error per file left out: a document that can't be read,
	// doesn't parse, isn't taken by the schema, or shares a hash with another.
	// Each one names the file, and a syntax error names the line and the column.
	Skipped []error

	// SchemaErr is set where the .graphqls files hold no readable schema,
	// which leaves every document unchecked rather than unserved.
	// No file was left out, so it isn't one of Skipped.
	SchemaErr error

	// Added and Removed count the documents against the list this one replaced.
	Added, Removed int
}

// Reload reads dir and publishes what it holds, replacing what the allowlist
// held before. Nothing remembers the last dir; concurrent callers queue.
//
// A document is skipped where it can't be read, doesn't parse, or the schema
// doesn't take it, so one broken file doesn't keep the rest from being served.
// Documents sharing a hash are all skipped: which one a request meant is
// unknowable. Every skip is named in the [Result].
//
// A directory holding no usable document publishes an empty allowlist, which
// rejects every request. A schema that can't be read leaves the documents
// unchecked rather than unserved, reported as [Result.SchemaErr].
func (a *Allowlist) Reload(dir string) (Result, error) {
	a.loading.Lock()
	defer a.loading.Unlock()

	var skipped []error

	files, schemaFiles, err := scanDir(dir)
	if err != nil {
		return Result{}, fmt.Errorf("scanning directory %s: %w", dir, err)
	}

	// A directory holding no schema is checked against none:
	// the documents are hashed as they are.
	schema, schemaErr := loadSchema(schemaFiles)
	if schemaErr != nil {
		schemaErr = fmt.Errorf("%s: %w", strings.Join(schemaFiles, ", "), schemaErr)
	}

	// byHash gathers the files under their document's hash,
	// so a shared one is seen before anything is published.
	byHash := make(map[string][]string, len(files))
	order := make([]string, 0, len(files))
	h := a.newHash()
	p := parser.NewParser[[]byte](0)

	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			skipped = append(skipped, fmt.Errorf("%s: %w", name, err))
			continue
		}

		h.Reset()
		if e := p.Parse(h, a.options, src); e.IsErr() {
			skipped = append(skipped, documentError(name, src, e))
			continue
		}
		if err := validate(schema, name, src); err != nil {
			skipped = append(skipped, err)
			continue
		}

		key := string(h.Sum(nil))

		if _, seen := byHash[key]; !seen {
			order = append(order, key)
		}
		byHash[key] = append(byHash[key], name)
	}

	docs := make(map[string]struct{}, len(byHash))
	loaded := make([]string, 0, len(byHash))
	for _, key := range order {
		names := byHash[key]
		if len(names) > 1 {
			// Which a request meant is unknowable, so none is served:
			// allowing the wrong one is worse than allowing neither.
			for i, name := range names {
				others := append(append([]string{}, names[:i]...), names[i+1:]...)
				skipped = append(skipped, fmt.Errorf(
					"%s: the same hash as %s, none of them is served",
					name, strings.Join(others, ", ")))
			}
			continue
		}
		docs[key] = struct{}{}
		loaded = append(loaded, names[0])
	}

	previous := a.current.Load()
	a.current.Store(&list{docs: docs, loadedAt: time.Now()})

	added, removed := diff(previous, docs)
	return Result{
		Files: loaded, Skipped: skipped, SchemaErr: schemaErr,
		Added: added, Removed: removed,
	}, nil
}

// scanDir returns the documents and the schema files under dir, sorted.
//
// The root is resolved through symlinks first, since -allowlist commonly names one:
// a deploy swaps an allowlist atomically by pointing a link at the new
// directory (ln -s v2 tmp; mv -T tmp current), and [filepath.WalkDir] lstats its root,
// so it would see the link and walk nothing at all — an allowlist holding no document,
// rejecting every request, on the deployment shape this is most likely run in.
//
// Only the root is resolved: what's reported is still the path as it was given,
// so a swap doesn't rewrite every entry of documents.files, and a symlinked
// directory inside the allowlist stays unwalked — following those invites a loop.
func scanDir(dir string) (docs, schemas []string, err error) {
	root := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		root = resolved
	}
	// given maps a path under the resolved root back to the argument,
	// so a caller reads the allowlist it named.
	given := func(path string) string {
		if root == dir {
			return path
		}
		rest, ok := strings.CutPrefix(path, root)
		if !ok {
			return path
		}
		return filepath.Join(dir, rest)
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() && path != root {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || strings.HasSuffix(name, "~") {
			return nil
		}
		switch {
		case strings.HasSuffix(name, schemaExt):
			schemas = append(schemas, given(path))
		case isDocument(name):
			docs = append(docs, given(path))
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return docs, schemas, nil
}

// schemaExt names a file holding the schema.
// A directory without one is checked against no schema.
const schemaExt = ".graphqls"

func isDocument(name string) bool {
	return strings.HasSuffix(name, ".graphql") || strings.HasSuffix(name, ".gql")
}

// loadSchema reads the schema files as one schema: nil where there is none,
// an error where they hold no valid one.
func loadSchema(files []string) (*ast.Schema, error) {
	if len(files) == 0 {
		return nil, nil
	}
	sources := make([]*ast.Source, 0, len(files))
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		sources = append(sources, &ast.Source{Name: name, Input: string(src)})
	}
	schema, err := gqlparser.LoadSchema(sources...)
	if err != nil {
		return nil, err
	}
	return schema, nil
}

// validate reports what the schema makes of the document in src,
// or nil if it takes it. The message names the file, the line and the column.
func validate(schema *ast.Schema, name string, src []byte) error {
	if schema == nil {
		return nil
	}
	if _, errs := gqlparser.LoadQueryWithRules(
		schema, string(src), rules.NewDefaultRules(),
	); len(errs) > 0 {
		e := errs[0]
		if len(e.Locations) > 0 {
			return fmt.Errorf("%s:%d:%d: %s",
				name, e.Locations[0].Line, e.Locations[0].Column, e.Message)
		}
		return fmt.Errorf("%s: %s", name, e.Message)
	}
	return nil
}

func diff(previous *list, docs map[string]struct{}) (added, removed int) {
	if previous == nil {
		return len(docs), 0
	}
	for key := range docs {
		if _, ok := previous.docs[key]; !ok {
			added++
		}
	}
	for key := range previous.docs {
		if _, ok := docs[key]; !ok {
			removed++
		}
	}
	return added, removed
}

// documentError points at the syntax error of a document in the allowlist.
func documentError(name string, src []byte, e gqlhash.Result) error {
	line, column := gqlhash.Position(src, e.ErrOffset)
	return fmt.Errorf("%s:%d:%d: %w", name, line, column, e.Err)
}
