package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/umputun/revmux/app/task"
	"github.com/umputun/revmux/app/ui"
)

const (
	inputFileLimit  int64 = 1 << 20
	inputTotalLimit int64 = 8 << 20
	inputCountLimit       = 128
	inputEntryLimit       = 1024
)

type inputLimits struct {
	fileBytes  int64
	totalBytes int64
	contexts   int
	entries    int
}

type inputSnapshotter struct {
	limits inputLimits
	used   int64
}

type inputFile struct {
	label    string
	relative string
	path     string
}

type contextWalk struct {
	docs    []ui.InputDocument
	entries int
	limited bool
}

func (s *inputSnapshotter) load(rc reviewContext) []ui.InputDocument {
	docs := make([]ui.InputDocument, 0, 3)
	docs = append(docs,
		s.file(inputFile{label: "scope", relative: filepath.Join(task.InputDir, task.ScopeFile), path: rc.Scope}))
	docs = append(docs, s.optional(inputFile{label: "goal", relative: filepath.Join(task.InputDir, task.GoalFile),
		path: s.inputPath(rc, task.GoalFile, rc.Goal)})...)
	docs = append(docs, s.optional(s.profileFile(rc))...)
	return append(docs, s.context(rc.Context)...)
}

// profileFile labels the profile tab by where the bytes actually came from. Under the project fallback
// the round holds no non-empty input/profile.md, so reconstructing that path would report the tab
// absent and show a calibrated run as an uncalibrated one — the snapshot is what the agents read and
// is what the reader must see.
func (s *inputSnapshotter) profileFile(rc reviewContext) inputFile {
	if rc.ProfileSource != "" {
		return inputFile{label: "profile", relative: task.ProfileSnapshotFile, path: rc.Profile}
	}
	return inputFile{label: "profile", relative: filepath.Join(task.InputDir, task.ProfileFile),
		path: s.inputPath(rc, task.ProfileFile, rc.Profile)}
}

// inputPath preserves an optional file's existence for the display snapshot without changing the
// prompt-facing resolved value, where a missing file and an empty file both intentionally become the
// placeholder.
func (*inputSnapshotter) inputPath(rc reviewContext, name, resolved string) string {
	if rc.InputDir == "" {
		return resolved
	}
	path := filepath.Join(rc.InputDir, name)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return path
}

// optional is one document when the caller supplied the file and none at all when he did not, the same
// rule context follows: goal.md and profile.md are optional by design, so their absence is ordinary and a
// tab reading "not provided" is one a reader learns to skip. A file that exists and could not be read
// still gets its document, since that is a failure to show what the agents saw rather than an absence.
func (s *inputSnapshotter) optional(file inputFile) []ui.InputDocument {
	if file.path == "" {
		return nil
	}
	return []ui.InputDocument{s.file(file)}
}

// context is one document per context file, and no document at all when the caller supplied none: an
// absent context/ is the ordinary case, and a tab that always says "not provided" is one a reader learns
// to skip. A directory that could not be read, or a symlinked one left untraversed, still gets its
// document — that is a failure to show what the agents saw rather than an honest absence.
func (s *inputSnapshotter) context(dir string) []ui.InputDocument {
	if dir == "" {
		return nil
	}

	rootInfo, err := os.Lstat(dir)
	if err != nil {
		return []ui.InputDocument{{
			Label: "context", Path: filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir)),
			Notice: "cannot read context directory: " + err.Error(),
		}}
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return []ui.InputDocument{{
			Label: "context/", Path: filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir)),
			Notice: "symlinked directory not traversed",
		}}
	}

	state := &contextWalk{docs: []ui.InputDocument{}}
	s.walkContext(dir, dir, state)
	if len(state.docs) == 0 && !state.limited {
		return nil // an empty directory is the same absence as no directory
	}
	if state.limited {
		state.docs = append(state.docs, ui.InputDocument{
			Label: "more", Path: filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir)),
			Notice: fmt.Sprintf("more context entries omitted after the %d-file display or %d-entry traversal limit",
				s.limits.contexts, s.limits.entries),
		})
	}
	return state.docs
}

// walkContext incrementally reads at most the remaining entry budget from each directory. WalkDir
// cannot provide this bound: it reads and sorts a whole directory before its callback sees one entry.
func (s *inputSnapshotter) walkContext(root, dir string, state *contextWalk) {
	if len(state.docs) >= s.limits.contexts {
		state.limited = true
		return
	}
	if state.entries >= s.limits.entries {
		hasEntries, err := s.contextDirHasEntries(dir)
		if err != nil {
			s.contextNotice(root, dir, state, "cannot read: "+err.Error())
			return
		}
		state.limited = state.limited || hasEntries
		return
	}

	handle, err := os.Open(dir) //nolint:gosec // dir is intentionally the caller's resolved context tree
	if err != nil {
		s.contextNotice(root, dir, state, "cannot read: "+err.Error())
		return
	}
	remaining := s.limits.entries - state.entries
	entries, readErr := handle.ReadDir(remaining + 1)
	closeErr := handle.Close()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	if len(entries) > remaining {
		entries = entries[:remaining]
		state.limited = true
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		s.contextNotice(root, dir, state, "cannot read: "+readErr.Error())
	}
	if closeErr != nil {
		s.contextNotice(root, dir, state, "close failed: "+closeErr.Error())
	}

	for _, entry := range entries {
		if len(state.docs) >= s.limits.contexts || state.entries >= s.limits.entries {
			state.limited = true
			return
		}
		state.entries++
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			s.walkContext(root, path, state)
			continue
		}
		s.contextEntry(root, path, state)
	}
}

func (*inputSnapshotter) contextDirHasEntries(dir string) (bool, error) {
	handle, err := os.Open(dir) //nolint:gosec // dir is intentionally the caller's resolved context tree
	if err != nil {
		return false, fmt.Errorf("open context directory: %w", err)
	}
	entries, readErr := handle.ReadDir(1)
	closeErr := handle.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, fmt.Errorf("read context directory: %w", readErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close context directory: %w", closeErr)
	}
	return len(entries) > 0, nil
}

func (s *inputSnapshotter) contextEntry(root, path string, state *contextWalk) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		s.contextNotice(root, path, state, "cannot make path relative: "+err.Error())
		return
	}
	rel = filepath.ToSlash(rel)
	docPath := filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir, rel))

	info, err := os.Stat(path) // follows a file symlink, matching what an agent opening the path sees
	if err != nil {
		state.docs = append(state.docs, ui.InputDocument{Label: rel, Path: docPath, Notice: "cannot read: " + err.Error()})
		return
	}
	if info.IsDir() {
		state.docs = append(state.docs, ui.InputDocument{
			Label: rel + "/", Path: docPath, Notice: "symlinked directory not traversed",
		})
		return
	}
	if !info.Mode().IsRegular() {
		state.docs = append(state.docs, ui.InputDocument{Label: rel, Path: docPath, Notice: "not a regular file"})
		return
	}
	state.docs = append(state.docs, s.file(inputFile{label: rel, relative: docPath, path: path}))
}

func (s *inputSnapshotter) contextNotice(root, path string, state *contextWalk, notice string) {
	if len(state.docs) >= s.limits.contexts {
		state.limited = true
		return
	}
	label, docPath := "context", filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir))
	if rel, err := filepath.Rel(root, path); err == nil && rel != "." {
		label = filepath.ToSlash(rel) + "/"
		docPath = filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir, rel))
	}
	state.docs = append(state.docs, ui.InputDocument{Label: label, Path: docPath, Notice: notice})
}

func (s *inputSnapshotter) file(file inputFile) ui.InputDocument {
	doc := ui.InputDocument{
		Label: file.label, Path: filepath.ToSlash(file.relative),
		Markdown: strings.EqualFold(filepath.Ext(file.path), ".md"),
	}
	info, err := os.Stat(file.path)
	if err != nil {
		doc.Notice = "cannot read: " + err.Error()
		return doc
	}
	if !info.Mode().IsRegular() {
		doc.Notice = "not a regular file"
		return doc
	}
	if info.Size() == 0 {
		return doc
	}

	remaining := s.limits.totalBytes - s.used
	allowed := min(s.limits.fileBytes, remaining)
	if allowed <= 0 {
		doc.Notice = fmt.Sprintf("not captured: the %d-byte snapshot display limit was reached", s.limits.totalBytes)
		return doc
	}

	f, err := os.Open(file.path)
	if err != nil {
		doc.Notice = "cannot read: " + err.Error()
		return doc
	}
	data, readErr := io.ReadAll(io.LimitReader(f, allowed+utf8.UTFMax))
	closeErr := f.Close()

	truncated := int64(len(data)) > allowed || info.Size() > allowed
	if int64(len(data)) > allowed {
		cut := int(allowed)
		if cut > 0 && data[cut-1] == '\r' && data[cut] == '\n' {
			cut--
		}
		data = data[:cut]
	}
	if truncated {
		data = s.trimIncompleteUTF8(data)
	}
	captured := len(data)
	s.used += int64(captured)

	data, ok := s.displayText(data)
	if !ok {
		doc.Notice = fmt.Sprintf("binary or unsafe text (%d bytes); contents not shown", info.Size())
		return doc
	}
	doc.Content = string(data)
	if truncated {
		doc.Notice = fmt.Sprintf("truncated: showing %d of %d bytes", captured, info.Size())
	}
	if readErr != nil {
		if doc.Notice != "" {
			doc.Notice += "; "
		}
		doc.Notice += "read stopped: " + readErr.Error()
	}
	if closeErr != nil {
		if doc.Notice != "" {
			doc.Notice += "; "
		}
		doc.Notice += "close failed: " + closeErr.Error()
	}
	return doc
}

func (*inputSnapshotter) trimIncompleteUTF8(data []byte) []byte {
	for range utf8.UTFMax {
		if utf8.Valid(data) {
			return data
		}
		if len(data) == 0 {
			return data
		}
		data = data[:len(data)-1]
	}
	return data
}

func (*inputSnapshotter) displayText(data []byte) ([]byte, bool) {
	if !utf8.Valid(data) {
		return nil, false
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return nil, false
		}
	}
	return []byte(text), true
}
