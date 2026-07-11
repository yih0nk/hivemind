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

// Package github provides the PR-publishing seam used by the synthesizer
// agent. Consumers depend on PRClient, never on the GitHub REST API.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// PRClient is everything Hivemind needs from a code host: a branch to
// put the root-cause report on, and a PR to publish it.
type PRClient interface {
	// CreateBranch creates branch in repo ("owner/name") pointing at the
	// current head of base. Creating a branch that already exists is not
	// an error, so retried reconciles stay idempotent.
	CreateBranch(ctx context.Context, repo, base, branch string) error

	// CreatePR opens a pull request from head into base and returns its URL.
	CreatePR(ctx context.Context, repo, title, body, head, base string) (string, error)
}

// RESTClient implements PRClient against the GitHub REST API v3.
type RESTClient struct {
	Token      string
	BaseURL    string // e.g. https://api.github.com
	HTTPClient *http.Client
}

var _ PRClient = (*RESTClient)(nil)

func NewRESTClient(token string) *RESTClient {
	return &RESTClient{
		Token:      token,
		BaseURL:    "https://api.github.com",
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *RESTClient) CreateBranch(ctx context.Context, repo, base, branch string) error {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/git/ref/heads/%s", repo, base), nil, &ref); err != nil {
		return fmt.Errorf("resolving base %s: %w", base, err)
	}

	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/git/refs", repo), map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": ref.Object.SHA,
	}, nil)
	// 422 means the ref already exists: a previous reconcile got here first.
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity {
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating branch %s: %w", branch, err)
	}
	return nil
}

func (c *RESTClient) CreatePR(ctx context.Context, repo, title, body, head, base string) (string, error) {
	var pr struct {
		HTMLURL string `json:"html_url"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls", repo), map[string]string{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}, &pr)
	if err != nil {
		return "", fmt.Errorf("creating PR for %s: %w", repo, err)
	}
	return pr.HTMLURL, nil
}

// APIError carries the GitHub status code so callers can branch on it.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github returned %d: %s", e.StatusCode, e.Body)
}

// do sends one API request, decoding a JSON response into out when out is
// non-nil. Non-2xx responses become *APIError.
func (c *RESTClient) do(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshaling payload: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling github: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{StatusCode: resp.StatusCode, Body: string(msg)}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}
