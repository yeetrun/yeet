// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSystemdExecStartRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "executable only",
			argv: []string{"/usr/bin/app"},
			want: "/usr/bin/app",
		},
		{
			name: "quoted arguments",
			argv: []string{"/usr/bin/app", "plain", "two words", "", `quote"here`, `slash\\here`},
			want: `/usr/bin/app plain "two words" "" "quote\"here" "slash\\\\here"`,
		},
		{
			name: "systemd expansion syntax and Unicode",
			argv: []string{"/usr/bin/app", "$HOME", "100%", "colon:value", "unicode-✓"},
			want: `/usr/bin/app $$HOME 100%% colon:value unicode-✓`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rendered, err := RenderSystemdExecStart(test.argv)
			if err != nil {
				t.Fatalf("RenderSystemdExecStart: %v", err)
			}
			if rendered != test.want {
				t.Fatalf("rendered ExecStart = %q, want %q", rendered, test.want)
			}

			parsed, err := ParseSystemdExecStart(rendered)
			if err != nil {
				t.Fatalf("ParseSystemdExecStart(%q): %v", rendered, err)
			}
			if diff := cmp.Diff(test.argv, parsed); diff != "" {
				t.Fatalf("argv mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSystemdExecStartEscapesExpansionSyntax(t *testing.T) {
	rendered, err := RenderSystemdExecStart([]string{"/usr/bin/app", "$HOME", "${TOKEN}", "%i", "100%"})
	if err != nil {
		t.Fatalf("RenderSystemdExecStart: %v", err)
	}
	const want = `/usr/bin/app $$HOME $${TOKEN} %%i 100%%`
	if rendered != want {
		t.Fatalf("rendered ExecStart = %q, want %q", rendered, want)
	}
	withoutDollarEscapes := strings.ReplaceAll(rendered, "$$", "")
	withoutPercentEscapes := strings.ReplaceAll(rendered, "%%", "")
	if strings.ContainsAny(withoutDollarEscapes, "$") || strings.ContainsAny(withoutPercentEscapes, "%") {
		t.Fatalf("rendered ExecStart retains unescaped expansion syntax: %q", rendered)
	}
}

func TestParseSystemdExecStartRejectsDecodedExpansionSyntax(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "environment", value: `/usr/bin/app \x24HOME`, wantErr: "systemd environment expansion"},
		{name: "specifier", value: `/usr/bin/app \x25i`, wantErr: "systemd specifier expansion"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseSystemdExecStart(test.value)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ParseSystemdExecStart(%q) error = %v, want error containing %q", test.value, err, test.wantErr)
			}
		})
	}
}

func TestParseSystemdExecStartCollapsesDecodedExpansionPairs(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  []string
	}{
		{name: "escaped dollars", value: `/usr/bin/app \x24\x24`, want: []string{"/usr/bin/app", "$"}},
		{name: "raw then escaped dollar", value: `/usr/bin/app $\x24`, want: []string{"/usr/bin/app", "$"}},
		{name: "escaped then raw dollar", value: `/usr/bin/app \x24$`, want: []string{"/usr/bin/app", "$"}},
		{name: "escaped dollar pair with suffix", value: `/usr/bin/app \x24\x24HOME`, want: []string{"/usr/bin/app", "$HOME"}},
		{name: "mixed dollar pair with suffix", value: `/usr/bin/app $\x24HOME`, want: []string{"/usr/bin/app", "$HOME"}},
		{name: "escaped percents", value: `/usr/bin/app \x25\x25`, want: []string{"/usr/bin/app", "%"}},
		{name: "raw then escaped percent", value: `/usr/bin/app %\x25`, want: []string{"/usr/bin/app", "%"}},
		{name: "escaped then raw percent", value: `/usr/bin/app \x25%`, want: []string{"/usr/bin/app", "%"}},
		{name: "escaped percent pair with suffix", value: `/usr/bin/app \x25\x25i`, want: []string{"/usr/bin/app", "%i"}},
		{name: "mixed percent pair with suffix", value: `/usr/bin/app \x25%i`, want: []string{"/usr/bin/app", "%i"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseSystemdExecStart(test.value)
			if err != nil {
				t.Fatalf("ParseSystemdExecStart(%q): %v", test.value, err)
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Fatalf("argv mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSystemdExecStartRejectsExecutableModifiers(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		unquoted   string
		quoted     string
		escaped    string
	}{
		{name: "shell pipe", executable: "|/usr/bin/app", unquoted: `|/usr/bin/app arg`, quoted: `"|/usr/bin/app" arg`, escaped: `\x7c/usr/bin/app arg`},
		{name: "full privileges", executable: "+/usr/bin/app", unquoted: `+/usr/bin/app arg`, quoted: `"+/usr/bin/app" arg`, escaped: `\x2b/usr/bin/app arg`},
		{name: "credential override", executable: "!/usr/bin/app", unquoted: `!/usr/bin/app arg`, quoted: `"!/usr/bin/app" arg`, escaped: `\x21/usr/bin/app arg`},
		{name: "double credential override", executable: "!!/usr/bin/app", unquoted: `!!/usr/bin/app arg`, quoted: `"!!/usr/bin/app" arg`, escaped: `\x21\x21/usr/bin/app arg`},
		{name: "argv zero override", executable: "@/usr/bin/app", unquoted: `@/usr/bin/app arg`, quoted: `"@/usr/bin/app" arg`, escaped: `\x40/usr/bin/app arg`},
		{name: "ignore failure", executable: "-/usr/bin/app", unquoted: `-/usr/bin/app arg`, quoted: `"-/usr/bin/app" arg`, escaped: `\x2d/usr/bin/app arg`},
		{name: "disable expansion", executable: ":/usr/bin/app", unquoted: `:/usr/bin/app arg`, quoted: `":/usr/bin/app" arg`, escaped: `\x3a/usr/bin/app arg`},
		{name: "combined ignore and argv zero", executable: "-@/usr/bin/app", unquoted: `-@/usr/bin/app arg`, quoted: `"-@/usr/bin/app" arg`, escaped: `\x2d\x40/usr/bin/app arg`},
		{name: "combined expansion and shell", executable: ":|/usr/bin/app", unquoted: `:|/usr/bin/app arg`, quoted: `":|/usr/bin/app" arg`, escaped: `\x3a\x7c/usr/bin/app arg`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RenderSystemdExecStart([]string{test.executable, "arg"})
			if err == nil || !strings.Contains(err.Error(), "unsupported systemd executable prefix") {
				t.Fatalf("RenderSystemdExecStart error = %v", err)
			}
			for _, value := range []string{test.unquoted, test.quoted, test.escaped} {
				_, err := ParseSystemdExecStart(value)
				if err == nil || !strings.Contains(err.Error(), "unsupported systemd executable prefix") {
					t.Fatalf("ParseSystemdExecStart(%q) error = %v", value, err)
				}
			}
		})
	}
}

func TestSystemdExecStartQuotesLiteralSemicolonArgument(t *testing.T) {
	argv := []string{"/usr/bin/app", ";", "/usr/bin/other"}
	const want = `/usr/bin/app ";" /usr/bin/other`

	rendered, err := RenderSystemdExecStart(argv)
	if err != nil {
		t.Fatalf("RenderSystemdExecStart: %v", err)
	}
	if rendered != want {
		t.Fatalf("rendered ExecStart = %q, want %q", rendered, want)
	}
	parsed, err := ParseSystemdExecStart(rendered)
	if err != nil {
		t.Fatalf("ParseSystemdExecStart: %v", err)
	}
	if diff := cmp.Diff(argv, parsed); diff != "" {
		t.Fatalf("argv mismatch (-want +got):\n%s", diff)
	}
}

func TestParseSystemdExecStartRejectsUnquotedCommandSeparator(t *testing.T) {
	for _, value := range []string{
		`/usr/bin/app ; /usr/bin/other`,
		`/usr/bin/app ;`,
		`;`,
	} {
		_, err := ParseSystemdExecStart(value)
		if err == nil || !strings.Contains(err.Error(), "unquoted systemd command separator") {
			t.Fatalf("ParseSystemdExecStart(%q) error = %v", value, err)
		}
	}
}

func TestSystemdExecStartAcceptsLiteralSemicolonForms(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  []string
	}{
		{name: "quoted", value: `/usr/bin/app ";"`, want: []string{"/usr/bin/app", ";"}},
		{name: "escaped", value: `/usr/bin/app \;`, want: []string{"/usr/bin/app", ";"}},
		{name: "embedded", value: `/usr/bin/app before;after`, want: []string{"/usr/bin/app", "before;after"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseSystemdExecStart(test.value)
			if err != nil {
				t.Fatalf("ParseSystemdExecStart(%q): %v", test.value, err)
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Fatalf("argv mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseSystemdExecStartRejectsGenericEscapedSemicolon(t *testing.T) {
	for _, value := range []string{
		`/usr/bin/app "\;"`,
		`/usr/bin/app before\;after`,
	} {
		_, err := ParseSystemdExecStart(value)
		if err == nil || !strings.Contains(err.Error(), "unsupported escape \\;") {
			t.Fatalf("ParseSystemdExecStart(%q) error = %v", value, err)
		}
	}
}

func TestSystemdExecStartRejectsSemicolonExecutable(t *testing.T) {
	if _, err := RenderSystemdExecStart([]string{";"}); err == nil || !strings.Contains(err.Error(), "semicolon executable") {
		t.Fatalf("RenderSystemdExecStart error = %v", err)
	}
	for _, value := range []string{`";"`, `\;`} {
		if _, err := ParseSystemdExecStart(value); err == nil || !strings.Contains(err.Error(), "semicolon executable") {
			t.Fatalf("ParseSystemdExecStart(%q) error = %v", value, err)
		}
	}
}

func TestRenderSystemdExecStartRejectsInvalidArgv(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		wantErr string
	}{
		{name: "nil argv", wantErr: "empty argv"},
		{name: "empty argv", argv: []string{}, wantErr: "empty argv"},
		{name: "empty executable", argv: []string{""}, wantErr: "empty executable"},
		{name: "blank executable", argv: []string{" \t"}, wantErr: "empty executable"},
		{name: "NUL", argv: []string{"/usr/bin/app", "bad\x00arg"}, wantErr: "NUL"},
		{name: "carriage return", argv: []string{"/usr/bin/app", "bad\rarg"}, wantErr: "line break"},
		{name: "line feed", argv: []string{"/usr/bin/app", "bad\narg"}, wantErr: "line break"},
		{name: "unsupported control", argv: []string{"/usr/bin/app", "bad\x01arg"}, wantErr: "unsupported control"},
		{name: "delete control", argv: []string{"/usr/bin/app", "bad\x7farg"}, wantErr: "unsupported control"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RenderSystemdExecStart(test.argv)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("RenderSystemdExecStart error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestParseSystemdExecStartRejectsMalformedValues(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "empty command", value: " \t", wantErr: "empty command"},
		{name: "empty executable", value: `"" argument`, wantErr: "empty executable"},
		{name: "NUL", value: "/usr/bin/app bad\x00arg", wantErr: "NUL"},
		{name: "carriage return", value: "/usr/bin/app bad\rarg", wantErr: "line break"},
		{name: "line feed", value: "/usr/bin/app bad\narg", wantErr: "line break"},
		{name: "unsupported control", value: "/usr/bin/app bad\x01arg", wantErr: "unsupported control"},
		{name: "escaped unsupported control", value: `/usr/bin/app "bad\x01arg"`, wantErr: "unsupported control"},
		{name: "escaped NUL", value: `/usr/bin/app "bad\x00arg"`, wantErr: "NUL"},
		{name: "escaped line feed", value: `/usr/bin/app "bad\narg"`, wantErr: "line break"},
		{name: "unterminated double quote", value: `/usr/bin/app "bad`, wantErr: "unterminated quote"},
		{name: "unterminated single quote", value: `/usr/bin/app 'bad`, wantErr: "unterminated quote"},
		{name: "unterminated escape", value: `/usr/bin/app bad\`, wantErr: "unterminated escape"},
		{name: "unsupported simple escape", value: `/usr/bin/app \q`, wantErr: "unsupported escape"},
		{name: "short hexadecimal escape", value: `/usr/bin/app \x1`, wantErr: "short hexadecimal escape"},
		{name: "invalid hexadecimal escape", value: `/usr/bin/app \xzz`, wantErr: "invalid hexadecimal escape"},
		{name: "systemd environment expansion", value: `/usr/bin/app $HOME`, wantErr: "systemd environment expansion"},
		{name: "systemd specifier expansion", value: `/usr/bin/app %i`, wantErr: "systemd specifier expansion"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseSystemdExecStart(test.value)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ParseSystemdExecStart error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestParseSystemdExecStartAcceptsAdjacentFragments(t *testing.T) {
	const value = `/usr/bin/app pre"two words"'post' empty"" "left"'right'`
	want := []string{"/usr/bin/app", "pretwo wordspost", "empty", "leftright"}

	got, err := ParseSystemdExecStart(value)
	if err != nil {
		t.Fatalf("ParseSystemdExecStart(%q): %v", value, err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("argv mismatch (-want +got):\n%s", diff)
	}
}

func TestSystemdExecStartSupportsQuotedControlEscapes(t *testing.T) {
	argv := []string{"/usr/bin/app", "tab\tvalue", "bell\a", "backspace\b", "form-feed\f", "vertical-tab\v"}
	const want = `/usr/bin/app "tab\tvalue" "bell\a" "backspace\b" "form-feed\f" "vertical-tab\v"`

	rendered, err := RenderSystemdExecStart(argv)
	if err != nil {
		t.Fatalf("RenderSystemdExecStart: %v", err)
	}
	if rendered != want {
		t.Fatalf("rendered ExecStart = %q, want %q", rendered, want)
	}
	parsed, err := ParseSystemdExecStart(rendered)
	if err != nil {
		t.Fatalf("ParseSystemdExecStart: %v", err)
	}
	if diff := cmp.Diff(argv, parsed); diff != "" {
		t.Fatalf("argv mismatch (-want +got):\n%s", diff)
	}
}

func FuzzSystemdExecStartRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"plain",
		"two words",
		"",
		"$HOME",
		"100%",
		"unicode-✓",
		"quote\"here",
		`slash\here`,
		";",
		"bad\x01control",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, argument string) {
		argv := []string{"/usr/bin/app", argument}
		rendered, err := RenderSystemdExecStart(argv)
		if err != nil {
			return
		}
		reparsed, err := ParseSystemdExecStart(rendered)
		if err != nil {
			t.Fatalf("reparse rendered value %q: %v", rendered, err)
		}
		if diff := cmp.Diff(argv, reparsed); diff != "" {
			t.Fatalf("round-trip mismatch (-want +got):\n%s", diff)
		}
		rerendered, err := RenderSystemdExecStart(reparsed)
		if err != nil {
			t.Fatalf("rerender reparsed argv %#v: %v", reparsed, err)
		}
		if rerendered != rendered {
			t.Fatalf("render is unstable: first %q, second %q", rendered, rerendered)
		}
	})
}

func FuzzParseSystemdExecStartCanonical(f *testing.F) {
	for _, seed := range []string{
		`/usr/bin/app plain`,
		`/usr/bin/app "two words"`,
		`/usr/bin/app ""`,
		`/usr/bin/app $$HOME`,
		`/usr/bin/app 100%%`,
		`/usr/bin/app ";"`,
		`/usr/bin/app \;`,
		`|/usr/bin/app value`,
		`/usr/bin/app ; /usr/bin/other`,
		`/usr/bin/app \q`,
		`/usr/bin/app \x1`,
		"/usr/bin/app bad\x01control",
		`/usr/bin/app "unterminated`,
		`/usr/bin/app trailing\`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		argv, err := ParseSystemdExecStart(value)
		if err != nil {
			return
		}
		rendered, err := RenderSystemdExecStart(argv)
		if err != nil {
			t.Fatalf("render parsed argv %#v: %v", argv, err)
		}
		reparsed, err := ParseSystemdExecStart(rendered)
		if err != nil {
			t.Fatalf("reparse rendered value %q: %v", rendered, err)
		}
		if diff := cmp.Diff(argv, reparsed); diff != "" {
			t.Fatalf("canonical round-trip mismatch (-want +got):\n%s", diff)
		}
	})
}
