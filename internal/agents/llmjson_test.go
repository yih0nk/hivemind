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
	"testing"
)

// Shared expectations for the soft error report decodeReport emits when
// an LLM response is malformed; every agent's test exercises this path.
const (
	softErrorCaseName    = "malformed LLM JSON degrades to a soft error report"
	wantSoftErrorStatus  = `"status":"error"`
	wantSoftErrorSummary = "llm returned malformed JSON"
)

// tinyJSON is a minimal valid JSON value reused across sanitize and
// extraction cases.
const tinyJSON = `{"a": "b"}`

func TestSanitizeLLMJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "valid JSON passes through untouched",
			in:   `{"a": "b", "n": 1}`,
			want: `{"a": "b", "n": 1}`,
		},
		{
			name: "code fence is unwrapped",
			in:   "```json\n{\"a\": \"b\"}\n```",
			want: tinyJSON,
		},
		{
			name: "raw newline inside a string is escaped",
			in:   "{\"fix\": \"step 1\nstep 2\"}",
			want: `{"fix": "step 1\nstep 2"}`,
		},
		{
			name: "raw tab and carriage return inside a string are escaped",
			in:   "{\"a\": \"x\ty\r\"}",
			want: `{"a": "x\ty\r"}`,
		},
		{
			name: "newlines between members stay as whitespace",
			in:   "{\n  \"a\": \"b\"\n}",
			want: "{\n  \"a\": \"b\"\n}",
		},
		{
			name: "escaped quote does not end the string",
			in:   "{\"a\": \"he said \\\"hi\\\"\nbye\"}",
			want: `{"a": "he said \"hi\"` + `\n` + `bye"}`,
		},
		{
			name: "already-escaped newline is left alone",
			in:   `{"a": "line\nline"}`,
			want: `{"a": "line\nline"}`,
		},
		{
			name: "other control chars become unicode escapes",
			in:   "{\"a\": \"x\x01y\"}",
			want: `{"a": "x\u0001y"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeLLMJSON(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeLLMJSON(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFirstJSONValue(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "single value passes through untouched",
			in:   tinyJSON,
			want: tinyJSON,
		},
		{
			name: "trailing second value is ignored",
			in:   "{\"a\": \"b\"}\n```\n\n```json\n{\"c\": \"d\"}",
			want: tinyJSON,
		},
		{
			name: "surrounding whitespace is not part of the value",
			in:   "\n  {\"a\": \"b\"}  \n",
			want: tinyJSON,
		},
		{
			name:    "prose is an error",
			in:      "Sure! The problem is probably DNS.",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := firstJSONValue(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("firstJSONValue(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("firstJSONValue(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("firstJSONValue(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The synthesizer output from a live llama3.2-vision run: fenced JSON
// whose recommendedFix contains real newlines. The sanitized form must
// unmarshal.
func TestSanitizeLLMJSONRepairsLiveSynthesizerOutput(t *testing.T) {
	raw := "```json\n{\n  \"rootCause\": \"Insufficient resource allocation for pod\",\n" +
		"  \"recommendedFix\": \"1. Restart the pod. \n" +
		"                    2. Increase pod resource requests. \n" +
		"                    3. Check for other high-CPU processes.\",\n" +
		"  \"confidence\": 0.9,\n  \"severity\": \"critical\",\n  \"estimatedImpact\": \"High\"\n}\n```"

	var parsed SynthesisReport
	if err := json.Unmarshal([]byte(sanitizeLLMJSON(raw)), &parsed); err != nil {
		t.Fatalf("sanitized output does not parse: %v", err)
	}
	if parsed.RootCause != "Insufficient resource allocation for pod" {
		t.Errorf("rootCause = %q", parsed.RootCause)
	}
	if parsed.Confidence != 0.9 {
		t.Errorf("confidence = %v, want 0.9", parsed.Confidence)
	}
}
