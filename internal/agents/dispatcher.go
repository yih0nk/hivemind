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
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
)

// DefaultAgentTimeout bounds a single agent's Run when no explicit
// timeout is configured. Exported so the controller can apply the same
// bound to agents it runs outside the dispatcher (the synthesizer).
const DefaultAgentTimeout = 30 * time.Second

// Dispatcher fans out a set of agents concurrently and collects their
// reports. One slow or failing agent must not sink the others, so agents
// are never cancelled because a sibling failed — only the caller's ctx
// cancels them.
type Dispatcher struct {
	Agents []Agent

	// MaxConcurrent bounds how many agents run at once; <= 0 means unbounded.
	MaxConcurrent int

	// Timeout bounds each agent's Run individually, so one stuck LLM
	// call cannot block the reconcile worker forever. <= 0 means
	// DefaultAgentTimeout.
	Timeout time.Duration
}

// Dispatch runs every agent and returns the outputs of those that
// succeeded, keyed by agent name. The returned error joins all per-agent
// failures; callers can persist the partial outputs and still mark the
// run Failed when err != nil.
func (d *Dispatcher) Dispatch(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (map[string]string, error) {
	var (
		mu      sync.Mutex
		outputs = make(map[string]string, len(d.Agents))
		errs    []error
	)

	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultAgentTimeout
	}

	g := new(errgroup.Group)
	if d.MaxConcurrent > 0 {
		g.SetLimit(d.MaxConcurrent)
	}

	for _, agent := range d.Agents {
		g.Go(func() error {
			// The deadline starts inside the goroutine: with a concurrency
			// limit an agent may wait for a slot, and queueing time must
			// not eat into its budget.
			agentCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			out, err := agent.Run(agentCtx, triage)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("agent %s: %w", agent.Name(), err))
				return nil
			}
			outputs[agent.Name()] = out
			return nil
		})
	}

	// Goroutines report failures via errs rather than their return value,
	// so Wait never short-circuits and every agent gets to finish.
	_ = g.Wait()

	return outputs, errors.Join(errs...)
}
