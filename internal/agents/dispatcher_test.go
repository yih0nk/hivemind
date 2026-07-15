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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
)

type stubAgent struct {
	name   string
	output string
	err    error
	run    func(ctx context.Context) error // optional hook, called before returning
}

func (s *stubAgent) Name() string { return s.name }

func (s *stubAgent) Run(ctx context.Context, _ *incidentsv1alpha1.IncidentTriage) (string, error) {
	if s.run != nil {
		if err := s.run(ctx); err != nil {
			return "", err
		}
	}
	return s.output, s.err
}

func TestDispatchCollectsAllOutputs(t *testing.T) {
	d := &Dispatcher{Agents: []Agent{
		&stubAgent{name: logTriageName, output: "logs look bad"},
		&stubAgent{name: metricsCorrelatorName, output: "cpu spiked"},
	}}

	outputs, err := d.Dispatch(context.Background(), &incidentsv1alpha1.IncidentTriage{})
	if err != nil {
		t.Fatalf("Dispatch() error = %v, want nil", err)
	}
	if len(outputs) != 2 {
		t.Fatalf("got %d outputs, want 2: %v", len(outputs), outputs)
	}
	if outputs[logTriageName] != "logs look bad" {
		t.Errorf("logtriage output = %q", outputs[logTriageName])
	}
}

func TestDispatchPartialFailureKeepsSurvivors(t *testing.T) {
	boom := errors.New("prometheus unreachable")
	d := &Dispatcher{Agents: []Agent{
		&stubAgent{name: logTriageName, output: "ok"},
		&stubAgent{name: metricsCorrelatorName, err: boom},
	}}

	outputs, err := d.Dispatch(context.Background(), &incidentsv1alpha1.IncidentTriage{})
	if !errors.Is(err, boom) {
		t.Fatalf("Dispatch() error = %v, want wrapped %v", err, boom)
	}
	if !strings.Contains(err.Error(), metricsCorrelatorName) {
		t.Errorf("error should name the failing agent, got %q", err)
	}
	if outputs[logTriageName] != "ok" {
		t.Errorf("surviving agent output lost: %v", outputs)
	}
	if _, present := outputs[metricsCorrelatorName]; present {
		t.Errorf("failed agent should have no output entry: %v", outputs)
	}
}

func TestDispatchRespectsConcurrencyLimit(t *testing.T) {
	var current, peak atomic.Int32
	track := func(context.Context) error {
		now := current.Add(1)
		for {
			p := peak.Load()
			if now <= p || peak.CompareAndSwap(p, now) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond) // hold the slot so overlap is observable
		current.Add(-1)
		return nil
	}

	agents := make([]Agent, 8)
	for i := range agents {
		agents[i] = &stubAgent{name: string(rune('a' + i)), output: "x", run: track}
	}

	d := &Dispatcher{Agents: agents, MaxConcurrent: 2}
	if _, err := d.Dispatch(context.Background(), &incidentsv1alpha1.IncidentTriage{}); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if peak.Load() > 2 {
		t.Errorf("peak concurrency = %d, want <= 2", peak.Load())
	}
}

func TestDispatchCancelsAgentsExceedingTimeout(t *testing.T) {
	// The hung agent blocks until its context is cancelled -- the way a
	// well-behaved but stalled LLM HTTP call would.
	hang := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	d := &Dispatcher{
		Agents: []Agent{
			&stubAgent{name: logTriageName, output: "ok"},
			&stubAgent{name: metricsCorrelatorName, run: hang},
		},
		Timeout: 20 * time.Millisecond,
	}

	done := make(chan struct{})
	var outputs map[string]string
	var err error
	go func() {
		outputs, err = d.Dispatch(context.Background(), &incidentsv1alpha1.IncidentTriage{})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Dispatch did not return; timeout not enforced")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Dispatch() error = %v, want context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), metricsCorrelatorName) {
		t.Errorf("error should name the timed-out agent, got %q", err)
	}
	if outputs[logTriageName] != "ok" {
		t.Errorf("fast agent's output lost: %v", outputs)
	}
}
