// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type runWebFileList struct {
	Dir     string            `json:"dir"`
	Entries []runWebFileEntry `json:"entries"`
}

type runWebFileEntry struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Dir           bool   `json:"dir"`
	LikelyPayload bool   `json:"likelyPayload,omitempty"`
	LikelyEnv     bool   `json:"likelyEnv,omitempty"`
}

type runWebScoredFileEntry struct {
	entry runWebFileEntry
	score int
}

type runWebSubsequenceMatch struct {
	first      int
	last       int
	gaps       int
	contiguous int
	run        int
}

func (l runWebFileList) entry(name string) *runWebFileEntry {
	for i := range l.Entries {
		if l.Entries[i].Name == name {
			return &l.Entries[i]
		}
	}
	return nil
}

const maxRunWebFileSearchResults = 80

func listRunWebFiles(root, rel string) (runWebFileList, error) {
	rootReal, rel, targetReal, err := resolveRunWebListTarget(root, rel)
	if err != nil {
		return runWebFileList{}, err
	}

	entries, err := os.ReadDir(targetReal)
	if err != nil {
		return runWebFileList{}, err
	}

	out := runWebFileList{Dir: rel}
	for _, entry := range entries {
		name := entry.Name()
		entryPath := filepath.ToSlash(filepath.Join(rel, name))
		if rel == "." {
			entryPath = name
		}
		fileEntry, err := newRunWebFileEntry(rootReal, targetReal, entryPath, name)
		if err != nil {
			continue
		}
		out.Entries = append(out.Entries, fileEntry)
	}

	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Dir != out.Entries[j].Dir {
			return out.Entries[i].Dir
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})

	return out, nil
}

func searchRunWebFiles(root, query, field string) (runWebFileList, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return listRunWebFiles(root, ".")
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return runWebFileList{}, err
	}
	rootReal = filepath.Clean(rootReal)
	tokens := strings.Fields(strings.ToLower(query))
	matches, err := collectRunWebFileSearchMatches(rootReal, tokens, field)
	if err != nil {
		return runWebFileList{}, err
	}
	sortRunWebFileSearchMatches(matches)
	return runWebFileSearchList(matches), nil
}

func collectRunWebFileSearchMatches(rootReal string, tokens []string, field string) ([]runWebScoredFileEntry, error) {
	var matches []runWebScoredFileEntry
	err := filepath.WalkDir(rootReal, func(path string, d fs.DirEntry, err error) error {
		match, ok, walkErr := runWebFileSearchMatch(rootReal, path, d, err, tokens, field)
		if ok {
			matches = append(matches, match)
		}
		return walkErr
	})
	return matches, err
}

func runWebFileSearchMatch(rootReal, path string, d fs.DirEntry, walkErr error, tokens []string, field string) (runWebScoredFileEntry, bool, error) {
	if walkErr != nil || path == rootReal {
		return runWebScoredFileEntry{}, false, nil
	}
	if d.IsDir() {
		if runWebSearchSkipDir(d.Name()) {
			return runWebScoredFileEntry{}, false, filepath.SkipDir
		}
		return runWebScoredFileEntry{}, false, nil
	}
	entry, err := newRunWebSearchFileEntry(rootReal, path)
	if err != nil {
		return runWebScoredFileEntry{}, false, nil
	}
	if entry.Dir || !runWebFileSearchFieldAllows(entry, field) {
		return runWebScoredFileEntry{}, false, nil
	}
	score, ok := scoreRunWebFileSearch(entry, tokens, field)
	if !ok {
		return runWebScoredFileEntry{}, false, nil
	}
	return runWebScoredFileEntry{entry: entry, score: score}, true, nil
}

func sortRunWebFileSearchMatches(matches []runWebScoredFileEntry) {
	sort.Slice(matches, func(i, j int) bool {
		left := matches[i]
		right := matches[j]
		if left.score != right.score {
			return left.score > right.score
		}
		if left.entry.LikelyPayload != right.entry.LikelyPayload {
			return left.entry.LikelyPayload
		}
		if left.entry.LikelyEnv != right.entry.LikelyEnv {
			return left.entry.LikelyEnv
		}
		leftDepth := strings.Count(left.entry.Path, "/")
		rightDepth := strings.Count(right.entry.Path, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		if len(left.entry.Path) != len(right.entry.Path) {
			return len(left.entry.Path) < len(right.entry.Path)
		}
		return left.entry.Path < right.entry.Path
	})
}

func runWebFileSearchList(matches []runWebScoredFileEntry) runWebFileList {
	out := runWebFileList{Dir: "."}
	for i, match := range matches {
		if i >= maxRunWebFileSearchResults {
			break
		}
		out.Entries = append(out.Entries, match.entry)
	}
	return out
}

func runWebFileSearchFieldAllows(entry runWebFileEntry, field string) bool {
	return field != "envFile" || entry.LikelyEnv
}

func resolveRunWebListTarget(root, rel string) (string, string, string, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", err
	}
	rootReal = filepath.Clean(rootReal)

	rel, err = cleanRunWebRel(rel)
	if err != nil {
		return "", "", "", err
	}

	targetReal, err := filepath.EvalSymlinks(filepath.Join(rootReal, rel))
	if err != nil {
		return "", "", "", err
	}
	targetReal = filepath.Clean(targetReal)
	if !runWebPathInsideRoot(rootReal, targetReal) {
		return "", "", "", fmt.Errorf("path escapes project root")
	}
	return rootReal, rel, targetReal, nil
}

func newRunWebFileEntry(rootReal, targetReal, entryPath, name string) (runWebFileEntry, error) {
	entryReal, err := filepath.EvalSymlinks(filepath.Join(targetReal, name))
	if err != nil {
		return runWebFileEntry{}, err
	}
	entryReal = filepath.Clean(entryReal)
	if !runWebPathInsideRoot(rootReal, entryReal) {
		return runWebFileEntry{}, fmt.Errorf("path escapes project root")
	}
	info, err := os.Stat(entryReal)
	if err != nil {
		return runWebFileEntry{}, err
	}
	return runWebFileEntry{
		Name:          name,
		Path:          entryPath,
		Dir:           info.IsDir(),
		LikelyPayload: runWebLikelyPayload(name, info.Mode()),
		LikelyEnv:     runWebLikelyEnv(name),
	}, nil
}

func newRunWebSearchFileEntry(rootReal, path string) (runWebFileEntry, error) {
	entryReal, err := filepath.EvalSymlinks(path)
	if err != nil {
		return runWebFileEntry{}, err
	}
	entryReal = filepath.Clean(entryReal)
	if !runWebPathInsideRoot(rootReal, entryReal) {
		return runWebFileEntry{}, fmt.Errorf("path escapes project root")
	}
	info, err := os.Stat(entryReal)
	if err != nil {
		return runWebFileEntry{}, err
	}
	rel, err := filepath.Rel(rootReal, path)
	if err != nil {
		return runWebFileEntry{}, err
	}
	name := filepath.Base(path)
	return runWebFileEntry{
		Name:          name,
		Path:          filepath.ToSlash(rel),
		Dir:           info.IsDir(),
		LikelyPayload: runWebLikelyPayload(name, info.Mode()),
		LikelyEnv:     runWebLikelyEnv(name),
	}, nil
}

func runWebSearchSkipDir(name string) bool {
	switch name {
	case ".git", ".jj", ".hg", ".svn", ".direnv", "node_modules":
		return true
	default:
		return false
	}
}

func cleanRunWebRel(rel string) (string, error) {
	rel = filepath.Clean(strings.TrimSpace(rel))
	if rel == "" || rel == "." {
		return ".", nil
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes project root")
	}
	return rel, nil
}

func runWebPathInsideRoot(rootReal, targetReal string) bool {
	rel, err := filepath.Rel(rootReal, targetReal)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func runWebLikelyPayload(name string, mode os.FileMode) bool {
	if runWebKnownPayloadName(name) {
		return true
	}
	return mode.IsRegular() && mode&0o111 != 0
}

func runWebKnownPayloadName(name string) bool {
	base := strings.ToLower(name)
	if base == "compose.yml" || base == "compose.yaml" || base == "docker-compose.yml" || base == "docker-compose.yaml" || name == "Dockerfile" {
		return true
	}
	return false
}

func runWebLikelyEnv(name string) bool {
	base := strings.ToLower(name)
	return strings.Contains(base, ".env")
}

func scoreRunWebFileSearch(entry runWebFileEntry, tokens []string, field string) (int, bool) {
	path := strings.ToLower(entry.Path)
	base := strings.ToLower(entry.Name)
	score, ok := scoreRunWebFileSearchTokens(path, base, tokens)
	if !ok {
		return 0, false
	}
	score += runWebFileSearchKindBoost(entry, field)
	score -= strings.Count(entry.Path, "/") * 12
	score -= utf8.RuneCountInString(entry.Path)
	return score, true
}

func scoreRunWebFileSearchTokens(path, base string, tokens []string) (int, bool) {
	total := 0
	for _, token := range tokens {
		score, ok := bestRunWebFileTokenScore(path, base, token)
		if !ok {
			return 0, false
		}
		total += score
	}
	return total, true
}

func bestRunWebFileTokenScore(path, base, token string) (int, bool) {
	pathScore, pathOK := runWebFuzzyScore(path, token)
	baseScore, baseOK := runWebFuzzyScore(base, token)
	if !pathOK && !baseOK {
		return 0, false
	}
	if baseOK && baseScore+120 > pathScore {
		return baseScore + 120, true
	}
	return pathScore, true
}

func runWebFileSearchKindBoost(entry runWebFileEntry, field string) int {
	if field == "envFile" && entry.LikelyEnv {
		return 700
	}
	if field != "envFile" && runWebKnownPayloadName(entry.Name) {
		return 1100
	}
	if field != "envFile" && entry.LikelyPayload {
		return 700
	}
	return 0
}

func runWebFuzzyScore(haystack, needle string) (int, bool) {
	if needle == "" {
		return 0, true
	}
	if score, ok := runWebDirectFuzzyScore(haystack, needle); ok {
		return score, true
	}
	return runWebSubsequenceFuzzyScore(haystack, needle)
}

func runWebDirectFuzzyScore(haystack, needle string) (int, bool) {
	switch {
	case haystack == needle:
		return 1200, true
	case strings.HasPrefix(haystack, needle):
		return 1050 - utf8.RuneCountInString(haystack), true
	default:
		idx := strings.Index(haystack, needle)
		return 850 - idx - utf8.RuneCountInString(haystack), idx >= 0
	}
}

func runWebSubsequenceFuzzyScore(haystack, needle string) (int, bool) {
	hayRunes := []rune(haystack)
	match, ok := runWebSubsequenceMatchFor(hayRunes, needle)
	if !ok {
		return 0, false
	}
	return 420 + match.contiguous*18 - match.first*3 - match.gaps*5 - len(hayRunes), true
}

func runWebSubsequenceMatchFor(hayRunes []rune, needle string) (runWebSubsequenceMatch, bool) {
	match := runWebSubsequenceMatch{first: -1, last: -1}
	hayIndex := 0
	for _, char := range needle {
		found, next := runWebFindRune(hayRunes, hayIndex, char)
		if found < 0 {
			return runWebSubsequenceMatch{}, false
		}
		match.add(found)
		hayIndex = next
	}
	return match, true
}

func runWebFindRune(hayRunes []rune, start int, want rune) (int, int) {
	for i := start; i < len(hayRunes); i++ {
		if hayRunes[i] == want {
			return i, i + 1
		}
	}
	return -1, start
}

func (m *runWebSubsequenceMatch) add(position int) {
	if m.first < 0 {
		m.first = position
	}
	if m.last >= 0 {
		m.addGap(position - m.last - 1)
	} else {
		m.run = 1
	}
	m.last = position
	if m.run > m.contiguous {
		m.contiguous = m.run
	}
}

func (m *runWebSubsequenceMatch) addGap(gap int) {
	m.gaps += gap
	if gap == 0 {
		m.run++
		return
	}
	m.run = 1
}
