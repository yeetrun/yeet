// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"fmt"
	"strconv"
	"strings"
)

// RenderSystemdExecStart renders argv using systemd's native command-line
// syntax. It does not introduce a shell command line.
func RenderSystemdExecStart(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty argv")
	}
	if strings.TrimSpace(argv[0]) == "" {
		return "", fmt.Errorf("empty executable")
	}
	if err := validateSystemdExecExecutable(argv[0]); err != nil {
		return "", err
	}

	rendered := make([]string, len(argv))
	for i, value := range argv {
		if err := validateSystemdExecValue(value); err != nil {
			return "", fmt.Errorf("argument %d %w", i, err)
		}
		rendered[i] = renderSystemdExecWord(value)
	}
	return strings.Join(rendered, " "), nil
}

func renderSystemdExecWord(value string) string {
	var rendered strings.Builder
	for i := 0; i < len(value); i++ {
		if escaped, ok := renderedSystemdExecByte(value[i]); ok {
			rendered.WriteString(escaped)
		} else {
			rendered.WriteByte(value[i])
		}
	}
	if systemdExecWordNeedsQuotes(value) {
		return `"` + rendered.String() + `"`
	}
	return rendered.String()
}

func systemdExecWordNeedsQuotes(value string) bool {
	return value == "" || value == ";" || strings.ContainsAny(value, " \t\a\b\v\f'\"\\")
}

func renderedSystemdExecByte(value byte) (string, bool) {
	const escapedBytes = "$%\\\"\a\b\t\v\f"
	escapes := [...]string{"$$", "%%", `\\`, `\"`, `\a`, `\b`, `\t`, `\v`, `\f`}
	index := strings.IndexByte(escapedBytes, value)
	if index < 0 {
		return "", false
	}
	return escapes[index], true
}

// ParseSystemdExecStart parses the supported systemd command-line syntax into
// argv. Shell operators are ordinary argument bytes; no shell is invoked.
func ParseSystemdExecStart(value string) ([]string, error) {
	parser := systemdExecParser{value: value}
	return parser.parse()
}

type systemdExecParser struct {
	value  string
	args   []string
	word   strings.Builder
	inWord bool
	quote  byte
	index  int
}

func (parser *systemdExecParser) parse() ([]string, error) {
	for parser.index < len(parser.value) {
		if err := parser.consume(); err != nil {
			return nil, err
		}
	}
	if parser.quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	parser.finishWord()
	for i, value := range parser.args {
		collapsed, err := collapseSystemdExecExpansions(value)
		if err != nil {
			return nil, err
		}
		parser.args[i] = collapsed
	}
	if len(parser.args) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	if strings.TrimSpace(parser.args[0]) == "" {
		return nil, fmt.Errorf("empty executable")
	}
	if err := validateSystemdExecExecutable(parser.args[0]); err != nil {
		return nil, err
	}
	return parser.args, nil
}

func (parser *systemdExecParser) consume() error {
	value := parser.value[parser.index]
	if err := validateSystemdExecByte(value); err != nil {
		return err
	}
	if parser.consumeUnquotedWhitespace(value) {
		return nil
	}
	if parser.isUnquotedCommandSeparator(value) {
		return fmt.Errorf("unquoted systemd command separator")
	}
	if parser.consumeStandaloneEscapedSemicolon() {
		return nil
	}

	switch value {
	case '\'', '"':
		if !parser.consumeQuote(value) {
			parser.writeByte(value)
		}
	case '\\':
		return parser.consumeEscape()
	default:
		parser.writeByte(value)
	}
	return nil
}

func (parser *systemdExecParser) consumeStandaloneEscapedSemicolon() bool {
	if parser.quote != 0 || parser.inWord || parser.value[parser.index] != '\\' ||
		parser.index+1 >= len(parser.value) || parser.value[parser.index+1] != ';' {
		return false
	}
	next := parser.index + 2
	if next < len(parser.value) && parser.value[next] != ' ' && parser.value[next] != '\t' {
		return false
	}
	parser.word.WriteByte(';')
	parser.inWord = true
	parser.index = next
	return true
}

func (parser *systemdExecParser) isUnquotedCommandSeparator(value byte) bool {
	if value != ';' || parser.quote != 0 || parser.inWord {
		return false
	}
	next := parser.index + 1
	return next == len(parser.value) || parser.value[next] == ' ' || parser.value[next] == '\t'
}

func (parser *systemdExecParser) consumeUnquotedWhitespace(value byte) bool {
	if parser.quote != 0 || value != ' ' && value != '\t' {
		return false
	}
	parser.finishWord()
	parser.index++
	return true
}

func (parser *systemdExecParser) finishWord() {
	if !parser.inWord {
		return
	}
	parser.args = append(parser.args, parser.word.String())
	parser.word.Reset()
	parser.inWord = false
}

func (parser *systemdExecParser) consumeQuote(value byte) bool {
	if parser.quote == 0 {
		parser.quote = value
		parser.inWord = true
		parser.index++
		return true
	}
	if parser.quote != value {
		return false
	}
	parser.quote = 0
	parser.index++
	return true
}

func (parser *systemdExecParser) consumeEscape() error {
	decoded, consumed, err := decodeSystemdExecEscape(parser.value[parser.index:])
	if err != nil {
		return err
	}
	for i := 0; i < len(decoded); i++ {
		if err := validateSystemdExecByte(decoded[i]); err != nil {
			return err
		}
	}
	parser.word.WriteString(decoded)
	parser.inWord = true
	parser.index += consumed
	return nil
}

func (parser *systemdExecParser) writeByte(value byte) {
	parser.word.WriteByte(value)
	parser.inWord = true
	parser.index++
}

func decodeSystemdExecEscape(value string) (string, int, error) {
	if len(value) < 2 {
		return "", 0, fmt.Errorf("unterminated escape")
	}
	if decoded, ok := simpleSystemdExecEscape(value[1]); ok {
		return decoded, 2, nil
	}
	if value[1] != 'x' {
		return "", 0, fmt.Errorf("unsupported escape \\%c", value[1])
	}
	if len(value) < 4 {
		return "", 0, fmt.Errorf("short hexadecimal escape")
	}
	decoded, err := strconv.ParseUint(value[2:4], 16, 8)
	if err != nil {
		return "", 0, fmt.Errorf("invalid hexadecimal escape")
	}
	return string([]byte{byte(decoded)}), 4, nil
}

func simpleSystemdExecEscape(value byte) (string, bool) {
	const escapedBytes = "abfnrstv\\\"'"
	escapes := [...]string{"\a", "\b", "\f", "\n", "\r", " ", "\t", "\v", "\\", `"`, "'"}
	index := strings.IndexByte(escapedBytes, value)
	if index < 0 {
		return "", false
	}
	return escapes[index], true
}

func collapseSystemdExecExpansions(value string) (string, error) {
	var collapsed strings.Builder
	for index := 0; index < len(value); {
		current := value[index]
		if current != '$' && current != '%' {
			collapsed.WriteByte(current)
			index++
			continue
		}
		if index+1 >= len(value) || value[index+1] != current {
			if current == '$' {
				return "", fmt.Errorf("unresolved systemd environment expansion")
			}
			return "", fmt.Errorf("unresolved systemd specifier expansion")
		}
		collapsed.WriteByte(current)
		index += 2
	}
	return collapsed.String(), nil
}

func validateSystemdExecExecutable(value string) error {
	if value == ";" {
		return fmt.Errorf("semicolon executable is unsupported")
	}
	if strings.ContainsRune("|+!@-:", rune(value[0])) {
		return fmt.Errorf("unsupported systemd executable prefix %q", value[0])
	}
	return nil
}

func validateSystemdExecValue(value string) error {
	for i := 0; i < len(value); i++ {
		if err := validateSystemdExecByte(value[i]); err != nil {
			return err
		}
	}
	return nil
}

func validateSystemdExecByte(value byte) error {
	switch value {
	case 0:
		return fmt.Errorf("contains NUL")
	case '\r', '\n':
		return fmt.Errorf("contains line break")
	case '\a', '\b', '\t', '\v', '\f':
		return nil
	}
	if value < 0x20 || value == 0x7f {
		return fmt.Errorf("contains unsupported control character 0x%02x", value)
	}
	return nil
}
