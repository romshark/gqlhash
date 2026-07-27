package proxy

import (
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/parser"
)

// entry is one allowed document.
type entry struct {
	// Name is the path of the file the document came from, for logs and metrics.
	Name string
}

// allowlist maps the key of every allowed document to its entry. The key is the
// hash sum of the canonical form, or the canonical form itself under -exact.
//
// An allowlist is immutable once published. [Store] swaps a whole one at a time.
type allowlist struct {
	docs     map[string]*entry
	loadedAt time.Time
}

// Len returns the number of allowed documents.
func (a *allowlist) Len() int {
	if a == nil {
		return 0
	}
	return len(a.docs)
}

// Lookup returns the entry of the document with key, or nil. A []byte key is
// looked up without allocating a string.
func (a *allowlist) Lookup(key []byte) *entry {
	return a.docs[string(key)]
}

// Store holds the allowlist in use. Reads take the current one without locking,
// a write publishes a whole new one.
type Store struct{ current atomic.Pointer[allowlist] }

// Load returns the allowlist in use.
func (s *Store) Load() *allowlist { return s.current.Load() }

// Loader reads documents from a directory and publishes them to a [Store].
type Loader struct {
	dir     string
	exact   bool
	newHash func() hash.Hash
	log     zerolog.Logger
	store   *Store
	options gqlhash.Options

	// seen is the state of the last scan, which a poll compares against.
	seen map[string]fileState
}

// fileState notices a change without reading the file.
type fileState struct {
	size    int64
	modTime time.Time
}

// NewLoader returns a loader for dir that publishes to store.
func NewLoader(
	store *Store,
	dir string,
	exact bool,
	newHash func() hash.Hash,
	options gqlhash.Options,
	log zerolog.Logger,
) *Loader {
	return &Loader{
		dir: dir, exact: exact, newHash: newHash, log: log, store: store,
		options: options, seen: map[string]fileState{},
	}
}

// Load reads the directory and publishes the result.
//
// A document that can't be read or doesn't parse is skipped with an error, so
// one broken file doesn't keep the rest from being served. A directory holding
// no usable document publishes an empty allowlist, which rejects everything, and
// says so.
func (l *Loader) Load() error {
	files, states, err := scanDir(l.dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", l.dir, err)
	}

	docs := make(map[string]*entry, len(files))
	var skipped int
	h := l.newHash()
	p := parser.NewParser[[]byte](0, 0)
	var canon appender

	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			l.log.Error().Err(err).Str("file", name).
				Msg("skipping a document that can't be read")
			skipped++
			continue
		}

		var key []byte
		if l.exact {
			canon.buf = canon.buf[:0]
			if e := p.Parse(&canon, l.options, src); e.IsErr() {
				l.log.Error().Err(documentError(name, src, e)).
					Msg("skipping a document that doesn't parse")
				skipped++
				continue
			}
			key = canon.buf
		} else {
			h.Reset()
			if e := p.Parse(h, l.options, src); e.IsErr() {
				l.log.Error().Err(documentError(name, src, e)).
					Msg("skipping a document that doesn't parse")
				skipped++
				continue
			}
			key = h.Sum(nil)
		}

		if dup, ok := docs[string(key)]; ok {
			l.log.Warn().
				Str("file", name).
				Str("duplicate_of", dup.Name).
				Msg("two files hold the same document")
			continue
		}
		docs[string(key)] = &entry{Name: name}
	}

	previous := l.store.Load()
	l.store.current.Store(&allowlist{docs: docs, loadedAt: time.Now()})
	l.seen = states

	if len(docs) == 0 {
		// Serving an empty allowlist rejects every request, which is loud in the
		// counters but silent otherwise. This is the one line that says why.
		l.log.Error().Str("dir", l.dir).Int("skipped", skipped).
			Msg("no documents on the allowlist, every request is rejected")
	}

	added, removed := diff(previous, docs)
	l.log.Info().
		Int("documents", len(docs)).
		Int("added", added).
		Int("removed", removed).
		Int("skipped", skipped).
		Str("dir", l.dir).
		Msg("allowlist loaded")
	return nil
}

// Watch reloads the allowlist when the directory changes. It returns when done
// is closed.
//
// Why a poll and not filesystem events: a Kubernetes ConfigMap mount is a
// directory of symlinks that an update renames, which events don't report, and a
// lost event would leave a stale allowlist for good. A poll bounds staleness by
// interval.
//
// A change is applied once the directory stayed unchanged for one interval, so a
// file still being written doesn't fail on every poll.
func (l *Loader) Watch(interval time.Duration, done <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()

	var pending bool
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}

		_, states, err := scanDir(l.dir)
		if err != nil {
			l.log.Error().Err(err).Str("dir", l.dir).Msg("reading the allowlist")
			continue
		}
		if sameFiles(l.seen, states) {
			if pending {
				// The directory settled, so the change is complete.
				pending = false
				if err := l.Load(); err != nil {
					l.log.Error().Err(err).Msg("reloading the allowlist")
				}
			}
			continue
		}
		// Something changed. Wait for the next poll to find it unchanged.
		l.seen, pending = states, true
	}
}

// scanDir returns the documents under dir, sorted, and their file states.
func scanDir(dir string) ([]string, map[string]fileState, error) {
	var files []string
	states := map[string]fileState{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() && path != dir {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !isDocument(name) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, path)
		states[path] = fileState{size: info.Size(), modTime: info.ModTime()}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return files, states, nil
}

// isDocument reports whether name is a GraphQL document. A backup file isn't.
func isDocument(name string) bool {
	if strings.HasSuffix(name, "~") {
		return false
	}
	return strings.HasSuffix(name, ".graphql") || strings.HasSuffix(name, ".gql")
}

func sameFiles(a, b map[string]fileState) bool {
	if len(a) != len(b) {
		return false
	}
	for name, sa := range a {
		sb, ok := b[name]
		if !ok || sa.size != sb.size || !sa.modTime.Equal(sb.modTime) {
			return false
		}
	}
	return true
}

func diff(previous *allowlist, docs map[string]*entry) (added, removed int) {
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
func documentError(name string, src []byte, e gqlhash.Error) error {
	line, column := gqlhash.Position(src, e.Offset)
	return fmt.Errorf("%s:%d:%d: %w", name, line, column, e.Err)
}

// appender is the [io.Writer] the canonical form of a document is written to.
type appender struct{ buf []byte }

func (a *appender) Write(p []byte) (int, error) {
	a.buf = append(a.buf, p...)
	return len(p), nil
}
