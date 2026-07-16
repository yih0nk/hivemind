/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package agents

import (
	"encoding/json"
	"fmt"
	"strings"
)

// firstJSONValue extracts the first complete JSON value from s, ignoring
// whatever follows it. Local models sometimes emit two fenced JSON blocks
// back-to-back; decoding the whole string would fail on the second one.
func firstJSONValue(s string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return "", err
	}
	return string(raw), nil
}

// sanitizeLLMJSON repairs the JSON local chat models actually emit
// despite "JSON only" instructions: unwrap a markdown code fence, then
// escape raw control characters inside string literals (models write
// multi-line values with real newlines, which strict JSON forbids).
func sanitizeLLMJSON(s string) string {
	s = stripCodeFence(s)

	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
			b.WriteByte(c)
		case inString && c == '\\':
			escaped = true
			b.WriteByte(c)
		case c == '"':
			inString = !inString
			b.WriteByte(c)
		case inString && c < 0x20:
			switch c {
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				fmt.Fprintf(&b, `\u%04x`, c)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// stripCodeFence unwraps a ```json ... ``` fence, which chat models often
// add despite "JSON only" instructions.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:] // drop the language tag line
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}
