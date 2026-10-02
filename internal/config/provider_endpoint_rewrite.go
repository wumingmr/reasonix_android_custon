package config

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"reasonix/internal/fileutil"
	fileencoding "reasonix/internal/fileutil/encoding"
)

// repairProviderEndpointContractsFileLocked performs a narrow lexical edit for
// a caller that already owns LockConfigFileEdits. It preserves comments,
// unknown provider fields, inline tables, encoding and file permissions.
func repairProviderEndpointContractsFileLocked(path string) ([]ProviderEndpointRepair, error) {
	resolved, exists, err := statConfigPath(path)
	if err != nil || !exists {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	rawBytes, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	encoding, detected := fileencoding.Detect(rawBytes)
	raw := fileencoding.Decode(detected, encoding)
	next, repairs, err := rewriteProviderEndpointContracts(string(raw))
	if err != nil || len(repairs) == 0 {
		return repairs, err
	}
	encoded, err := fileencoding.Encode(next, encoding)
	if err != nil {
		return nil, err
	}
	if err := fileutil.AtomicWriteFile(resolved, encoded, info.Mode().Perm()); err != nil {
		return nil, err
	}
	return repairs, nil
}

func rewriteProviderEndpointContracts(raw string) (string, []ProviderEndpointRepair, error) {
	var decoded struct {
		Providers []ProviderEntry `toml:"providers"`
	}
	if _, err := toml.Decode(raw, &decoded); err != nil {
		return raw, nil, err
	}
	repairs := make([]ProviderEndpointRepair, 0)
	repaired := make([]bool, len(decoded.Providers))
	for i := range decoded.Providers {
		if repair, changed := RepairProviderEndpointContract(&decoded.Providers[i]); changed {
			repairs = append(repairs, *repair)
			repaired[i] = true
		}
	}
	if len(repairs) == 0 {
		return raw, nil, nil
	}

	lines := strings.Split(raw, "\n")
	blocks := providerTOMLBlocks(lines)
	if len(blocks) == len(decoded.Providers) {
		var err error
		for i := range slices.Backward(decoded.Providers) {
			if !repaired[i] {
				continue
			}
			lines, err = rewriteProviderEndpointBlock(lines, blocks[i], decoded.Providers[i])
			if err != nil {
				return raw, nil, err
			}
		}
		return strings.Join(lines, "\n"), repairs, nil
	}

	inlineBlocks, err := providerTOMLInlineBlocks(raw)
	if err != nil || len(inlineBlocks) != len(decoded.Providers) {
		return raw, nil, fmt.Errorf("repair provider endpoint: could not map provider tables safely")
	}
	replacements := make([]tomlReplacement, 0, len(repairs)*7)
	for i := range decoded.Providers {
		if !repaired[i] {
			continue
		}
		blockReplacements, err := providerEndpointInlineReplacements(raw, inlineBlocks[i], decoded.Providers[i])
		if err != nil {
			return raw, nil, err
		}
		replacements = append(replacements, blockReplacements...)
	}
	return applyTOMLReplacements(raw, replacements), repairs, nil
}

func rewriteProviderEndpointBlock(lines []string, block providerTOMLBlock, entry ProviderEntry) ([]string, error) {
	foundKind, foundBase := false, false
	foundAuth, foundMode := false, false
	state := tomlOutside
	for i := block.start + 1; i < block.end; i++ {
		if state != tomlOutside {
			state = advanceTOMLStringState(state, lines[i])
			continue
		}
		nextState := advanceTOMLStringState(tomlOutside, lines[i])
		if nextState != tomlOutside {
			state = nextState
			continue
		}
		key, _, ok := tomlKeyValue(lines[i])
		if !ok {
			state = nextState
			continue
		}
		switch strings.Trim(key, `"'`) {
		case "kind":
			lines[i] = replaceTOMLStringAssignment(lines[i], entry.Kind)
			foundKind = true
		case "base_url":
			lines[i] = replaceTOMLStringAssignment(lines[i], entry.BaseURL)
			foundBase = true
		case "request_url", "chat_url":
			lines[i] = replaceTOMLStringAssignment(lines[i], "")
		case "auth_header":
			lines[i] = replaceTOMLScalarAssignment(lines[i], strconv.FormatBool(entry.AuthHeader))
			foundAuth = true
		case "responses_mode":
			foundMode = true
			if entry.Kind == "responses" && entry.ResponsesMode != "" {
				lines[i] = replaceTOMLStringAssignment(lines[i], entry.ResponsesMode)
			} else {
				lines[i] = preservedTOMLLineComment(lines[i])
			}
		case "responses_stateful":
			lines[i] = preservedTOMLLineComment(lines[i])
		}
		state = nextState
	}
	insert := make([]string, 0, 4)
	if !foundKind {
		insert = append(insert, "kind = "+strconv.Quote(entry.Kind))
	}
	if !foundBase {
		insert = append(insert, "base_url = "+strconv.Quote(entry.BaseURL))
	}
	if entry.AuthHeader && !foundAuth {
		insert = append(insert, "auth_header = true")
	}
	if entry.Kind == "responses" && entry.ResponsesMode != "" && !foundMode {
		insert = append(insert, "responses_mode = "+strconv.Quote(entry.ResponsesMode))
	}
	if len(insert) == 0 {
		return lines, nil
	}
	if block.end > 0 && strings.HasSuffix(lines[block.end-1], "\r") {
		for i := range insert {
			insert[i] += "\r"
		}
	}
	lines = append(lines, make([]string, len(insert))...)
	copy(lines[block.end+len(insert):], lines[block.end:len(lines)-len(insert)])
	copy(lines[block.end:], insert)
	return lines, nil
}

func preservedTOMLLineComment(line string) string {
	carriageReturn := strings.HasSuffix(line, "\r")
	line = strings.TrimSuffix(line, "\r")
	comment := tomlInlineCommentIndex(line)
	if comment < 0 {
		if carriageReturn {
			return "\r"
		}
		return ""
	}
	indentLen := len(line) - len(strings.TrimLeft(line, " \t"))
	next := line[:indentLen] + strings.TrimLeft(line[comment:], " \t")
	if carriageReturn {
		next += "\r"
	}
	return next
}

func providerEndpointInlineReplacements(raw string, block providerTOMLInlineBlock, entry ProviderEntry) ([]tomlReplacement, error) {
	removeKeys := map[string]bool{"responses_stateful": true}
	if entry.Kind != "responses" || entry.ResponsesMode == "" {
		removeKeys["responses_mode"] = true
	}
	replacements := make([]tomlReplacement, 0, 8)
	if kind, ok := block.fields["kind"]; ok {
		replacements = append(replacements, tomlReplacement{start: kind.valueStart, end: kind.valueEnd, value: strconv.Quote(entry.Kind)})
	}
	if base, ok := block.fields["base_url"]; ok {
		replacements = append(replacements, tomlReplacement{start: base.valueStart, end: base.valueEnd, value: strconv.Quote(entry.BaseURL)})
	}
	for _, key := range []string{"request_url", "chat_url"} {
		if field, ok := block.fields[key]; ok {
			replacements = append(replacements, tomlReplacement{start: field.valueStart, end: field.valueEnd, value: strconv.Quote("")})
		}
	}
	if field, ok := block.fields["auth_header"]; ok {
		replacements = append(replacements, tomlReplacement{start: field.valueStart, end: field.valueEnd, value: strconv.FormatBool(entry.AuthHeader)})
	}
	if field, ok := block.fields["responses_mode"]; ok && !removeKeys["responses_mode"] {
		replacements = append(replacements, tomlReplacement{start: field.valueStart, end: field.valueEnd, value: strconv.Quote(entry.ResponsesMode)})
	}
	replacements = append(replacements, inlineProviderFieldRemovals(block, removeKeys)...)

	var additions []string
	if _, ok := block.fields["kind"]; !ok {
		additions = append(additions, "kind = "+strconv.Quote(entry.Kind))
	}
	if _, ok := block.fields["base_url"]; !ok {
		additions = append(additions, "base_url = "+strconv.Quote(entry.BaseURL))
	}
	if entry.AuthHeader {
		if _, ok := block.fields["auth_header"]; !ok {
			additions = append(additions, "auth_header = true")
		}
	}
	if entry.Kind == "responses" && entry.ResponsesMode != "" {
		if _, ok := block.fields["responses_mode"]; !ok {
			additions = append(additions, "responses_mode = "+strconv.Quote(entry.ResponsesMode))
		}
	}
	if len(additions) > 0 {
		replacements = append(replacements, tomlReplacement{
			start: block.end,
			end:   block.end,
			value: ", " + strings.Join(additions, ", "),
		})
	}
	return replacements, nil
}

func inlineProviderFieldRemovals(block providerTOMLInlineBlock, removeKeys map[string]bool) []tomlReplacement {
	indexSet := make(map[int]bool)
	for key := range removeKeys {
		if field, ok := block.fields[key]; ok {
			indexSet[field.segment] = true
		}
	}
	if len(indexSet) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(indexSet))
	for index := range indexSet {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	var replacements []tomlReplacement
	for pos := 0; pos < len(indexes); {
		first, last := indexes[pos], indexes[pos]
		for pos+1 < len(indexes) && indexes[pos+1] == last+1 {
			pos++
			last = indexes[pos]
		}
		var start, end int
		if last < len(block.segments)-1 {
			start = block.segments[first][0]
			end = block.segments[last+1][0]
		} else if first > 0 {
			start = block.segments[first-1][1]
			end = block.segments[last][1]
		}
		if end > start {
			replacements = append(replacements, tomlReplacement{start: start, end: end})
		}
		pos++
	}
	return replacements
}
