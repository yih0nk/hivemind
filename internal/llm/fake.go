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

package llm

import (
	"context"
	"sync"
)

// FakeCall records one Complete invocation for later assertions.
type FakeCall struct {
	SystemPrompt string
	UserPrompt   string
}

// FakeLLMClient is a test double: it returns canned output and records
// every prompt it receives. Safe for concurrent use, so it can back
// multiple agents dispatched in parallel.
type FakeLLMClient struct {
	// Response is returned by every Complete call when Err is nil.
	Response string
	// Err, when set, is returned by every Complete call.
	Err error

	mu    sync.Mutex
	calls []FakeCall
}

var _ LLMClient = (*FakeLLMClient)(nil)

func (f *FakeLLMClient) Complete(_ context.Context, systemPrompt, userPrompt string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, FakeCall{SystemPrompt: systemPrompt, UserPrompt: userPrompt})
	f.mu.Unlock()

	if f.Err != nil {
		return "", f.Err
	}
	return f.Response, nil
}

// Calls returns a copy of every recorded invocation, in order.
func (f *FakeLLMClient) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}
