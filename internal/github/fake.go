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

package github

import (
	"context"
	"sync"
)

// PRRequest records one CreatePR invocation in full, so tests can
// assert on the title and body the client was asked to publish.
type PRRequest struct {
	Repo  string
	Title string
	Body  string
	Head  string
	Base  string
}

// FakePRClient is a test double recording every branch and PR request.
type FakePRClient struct {
	// PRURL is returned by CreatePR when Err is nil.
	PRURL string
	// Err, when set, is returned by both methods.
	Err error
	// BranchErr, when set, is returned by CreateBranch only, so tests
	// can fail branch creation while leaving CreatePR healthy.
	BranchErr error

	mu       sync.Mutex
	branches []string // "repo base branch"
	prs      []PRRequest
}

var _ PRClient = (*FakePRClient)(nil)

func (f *FakePRClient) CreateBranch(_ context.Context, repo, base, branch string) error {
	f.mu.Lock()
	f.branches = append(f.branches, repo+" "+base+" "+branch)
	f.mu.Unlock()
	if f.BranchErr != nil {
		return f.BranchErr
	}
	return f.Err
}

func (f *FakePRClient) CreatePR(_ context.Context, repo, title, body, head, base string) (string, error) {
	f.mu.Lock()
	f.prs = append(f.prs, PRRequest{Repo: repo, Title: title, Body: body, Head: head, Base: base})
	f.mu.Unlock()
	if f.Err != nil {
		return "", f.Err
	}
	return f.PRURL, nil
}

// Branches returns a copy of every recorded CreateBranch call.
func (f *FakePRClient) Branches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.branches))
	copy(out, f.branches)
	return out
}

// PRs returns a copy of every recorded CreatePR call.
func (f *FakePRClient) PRs() []PRRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]PRRequest, len(f.prs))
	copy(out, f.prs)
	return out
}
