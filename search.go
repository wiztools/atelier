package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	// defaultSearchConversationLimit caps how many matching conversations a
	// search returns when SearchOptions.Limit is unset.
	defaultSearchConversationLimit = 50
	// maxSearchMatchesPerConversation keeps one heavyweight conversation from
	// dominating the result payload; the UI opens the conversation to see the
	// rest.
	maxSearchMatchesPerConversation = 20
	// maxSearchMatchesPerTurn caps matches across a single turn's content
	// entries (text plus, when enabled, thinking).
	maxSearchMatchesPerTurn = 3
	// searchSnippetRadius is how many runes of context flank the matched text
	// in a result snippet.
	searchSnippetRadius = 48
	// searchWorkers bounds concurrent conversation scans.
	searchWorkers = 8
)

// SearchOptions tunes a history search.
type SearchOptions struct {
	// CaseSensitive makes literal matching case-sensitive. Regex mode is
	// case-sensitive unless the pattern itself uses (?i).
	CaseSensitive bool `json:"caseSensitive"`
	// Regex interprets the query as a regular expression.
	Regex bool `json:"regex"`
	// IncludeThinking searches model thinking blocks in addition to visible
	// text.
	IncludeThinking bool `json:"includeThinking"`
	// Limit caps the number of conversations returned (0 = default).
	Limit int `json:"limit"`
}

// TurnMatch is one matched turn. The snippet is a single-line window around
// the first match, pre-split into Before/Match/After so the frontend can
// highlight it without offset math across string encodings.
type TurnMatch struct {
	TurnID    string `json:"turnId"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	Before    string `json:"before"`
	Match     string `json:"match"`
	After     string `json:"after"`
}

// ConversationSearchResult groups a conversation summary with its matches.
type ConversationSearchResult struct {
	Conversation ConversationSummary `json:"conversation"`
	TitleMatched bool                `json:"titleMatched"`
	Matches      []TurnMatch         `json:"matches"`
}

// SearchResponse is the payload returned by SearchConversations.
type SearchResponse struct {
	Results   []ConversationSearchResult `json:"results"`
	Truncated bool                       `json:"truncated"`
}

// searchHistory greps stored conversation history — turn text and titles —
// for the query, scanning turn files on demand rather than maintaining an
// index. Conversations come back newest-first with matches in turn order.
// Unreadable files are skipped, grep-style, rather than failing the search.
func searchHistory(storage ConfigStorage, query string, options SearchOptions) (SearchResponse, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return SearchResponse{Results: []ConversationSearchResult{}}, nil
	}
	var pattern *regexp.Regexp
	if options.Regex {
		compiled, err := regexp.Compile(trimmed)
		if err != nil {
			return SearchResponse{}, fmt.Errorf("invalid search pattern: %w", err)
		}
		pattern = compiled
	}
	matcher := newSearchMatcher(trimmed, options.CaseSensitive, pattern)

	root := filepath.Join(storage.History, "conversations")
	dirs := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		// Skip soft-deleted conversations without parsing, mirroring
		// listConversations' tombstone check.
		if _, statErr := os.Stat(filepath.Join(path, "tombstone.json")); statErr == nil {
			return filepath.SkipDir
		}
		if _, statErr := os.Stat(filepath.Join(path, "conversation.json")); statErr != nil {
			return nil
		}
		dirs = append(dirs, path)
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SearchResponse{Results: []ConversationSearchResult{}}, nil
		}
		return SearchResponse{}, err
	}

	results := make([]ConversationSearchResult, len(dirs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, searchWorkers)
	for i, dir := range dirs {
		wg.Add(1)
		go func(slot int, conversationDir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[slot] = searchConversationDir(conversationDir, matcher, options)
		}(i, dir)
	}
	wg.Wait()

	matched := []ConversationSearchResult{}
	for _, result := range results {
		if result.TitleMatched || len(result.Matches) > 0 {
			matched = append(matched, result)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Conversation.UpdatedAt == matched[j].Conversation.UpdatedAt {
			return matched[i].Conversation.ID < matched[j].Conversation.ID
		}
		return matched[i].Conversation.UpdatedAt > matched[j].Conversation.UpdatedAt
	})
	limit := options.Limit
	if limit <= 0 {
		limit = defaultSearchConversationLimit
	}
	truncated := len(matched) > limit
	if truncated {
		matched = matched[:limit]
	}
	return SearchResponse{Results: matched, Truncated: truncated}, nil
}

func searchConversationDir(dir string, matcher searchMatcher, options SearchOptions) ConversationSearchResult {
	result := ConversationSearchResult{Matches: []TurnMatch{}}
	var conversation HistoryConversation
	if err := readJSONFile(filepath.Join(dir, "conversation.json"), &conversation); err != nil {
		return result
	}
	if conversation.DeletedAt != "" {
		return result
	}
	result.Conversation = conversationSummaryFrom(conversation)
	if start, _ := matcher(conversation.Title); start >= 0 {
		result.TitleMatched = true
	}

	entries, err := os.ReadDir(filepath.Join(dir, "turns"))
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if len(result.Matches) >= maxSearchMatchesPerConversation {
			break
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var turn HistoryTurn
		if err := readJSONFile(filepath.Join(dir, "turns", entry.Name()), &turn); err != nil {
			continue
		}
		if turn.DeletedAt != "" {
			continue
		}
		result.Matches = append(result.Matches, searchTurnMatches(turn, matcher, options)...)
	}
	return result
}

func searchTurnMatches(turn HistoryTurn, matcher searchMatcher, options SearchOptions) []TurnMatch {
	matches := []TurnMatch{}
	for _, content := range turn.Content {
		if len(matches) >= maxSearchMatchesPerTurn {
			break
		}
		if content.Type != "text" && (content.Type != "thinking" || !options.IncludeThinking) {
			continue
		}
		if strings.TrimSpace(content.Text) == "" {
			continue
		}
		start, end := matcher(content.Text)
		if start < 0 {
			continue
		}
		before, matched, after := searchSnippet(content.Text, start, end)
		matches = append(matches, TurnMatch{
			TurnID:    turn.ID,
			Role:      turn.Role,
			CreatedAt: turn.CreatedAt,
			Before:    before,
			Match:     matched,
			After:     after,
		})
	}
	return matches
}

// searchMatcher reports the byte range of the first match in text, or (-1,-1)
// when there is none.
type searchMatcher func(text string) (start, end int)

func newSearchMatcher(needle string, caseSensitive bool, pattern *regexp.Regexp) searchMatcher {
	if pattern != nil {
		return func(text string) (int, int) {
			loc := pattern.FindStringIndex(text)
			if loc == nil {
				return -1, -1
			}
			return loc[0], loc[1]
		}
	}
	if caseSensitive {
		return func(text string) (int, int) {
			index := strings.Index(text, needle)
			if index < 0 {
				return -1, -1
			}
			return index, index + len(needle)
		}
	}
	return func(text string) (int, int) {
		return indexFold(text, needle)
	}
}

// indexFold finds needle in haystack ignoring case, returning byte offsets.
// Lowercasing both sides and searching the fold is wrong whenever folding
// changes byte length (e.g. "İ" → "i̇"), so compare rune pairs through the
// unicode.SimpleFold cycle instead.
func indexFold(haystack, needle string) (int, int) {
	needleRunes := []rune(needle)
	if len(needleRunes) == 0 {
		return -1, -1
	}
	haystackRunes := []rune(haystack)
	offset := 0
	for i := 0; i+len(needleRunes) <= len(haystackRunes); i++ {
		matched := true
		for j, needleRune := range needleRunes {
			if !runeEqualFold(haystackRunes[i+j], needleRune) {
				matched = false
				break
			}
		}
		if matched {
			return offset, offset + len(string(haystackRunes[i:i+len(needleRunes)]))
		}
		offset += utf8.RuneLen(haystackRunes[i])
	}
	return -1, -1
}

func runeEqualFold(a, b rune) bool {
	if a == b {
		return true
	}
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}

// searchSnippet builds a single-line snippet around text[start:end], split
// into before/match/after. Interior whitespace runs collapse to single
// spaces, but a separator flanking the match survives collapsing — otherwise
// the highlighted word jams against its neighbours ("cartooncharacter").
// Window edges snap to whole words so context never starts or ends mid-word
// ("…ith this input file"); ellipses mark truncated context.
func searchSnippet(text string, start, end int) (before, match, after string) {
	runes := []rune(text)
	startRune := utf8.RuneCountInString(text[:start])
	endRune := utf8.RuneCountInString(text[:end])
	lo := max(0, startRune-searchSnippetRadius)
	hi := min(len(runes), endRune+searchSnippetRadius)
	// Snap clipped window edges outward to word boundaries: skip the partial
	// word the radius cut into. A window that is one long token collapses to
	// no context at all (the loops stop at the match edges).
	if lo > 0 {
		for lo < startRune && !unicode.IsSpace(runes[lo-1]) {
			lo++
		}
	}
	if hi < len(runes) {
		for hi > endRune && !unicode.IsSpace(runes[hi]) {
			hi--
		}
	}
	before = collapseSnippetSpaces(string(runes[lo:startRune]))
	match = collapseSnippetSpaces(string(runes[startRune:endRune]))
	after = collapseSnippetSpaces(string(runes[endRune:hi]))
	if spaceBeforeMatch(runes, startRune, endRune) {
		before += " "
	}
	if spaceAfterMatch(runes, startRune, endRune) {
		after = " " + after
	}
	if lo > 0 {
		before = "…" + before
	}
	if hi < len(runes) {
		after += "…"
	}
	return before, match, after
}

// spaceBeforeMatch reports whether a whitespace separator sits at the match's
// left edge — either just outside it or consumed by the match itself (a
// regex like " cat " can swallow the flanking spaces).
func spaceBeforeMatch(runes []rune, startRune, endRune int) bool {
	if startRune < endRune && unicode.IsSpace(runes[startRune]) {
		return true
	}
	return startRune > 0 && unicode.IsSpace(runes[startRune-1])
}

func spaceAfterMatch(runes []rune, startRune, endRune int) bool {
	if endRune > startRune && unicode.IsSpace(runes[endRune-1]) {
		return true
	}
	return endRune < len(runes) && unicode.IsSpace(runes[endRune])
}

func collapseSnippetSpaces(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
