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
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// maxLogBytes caps how much of one pod's log stream is read, protecting
// the operator from a pod that logs faster than TailLines can bound.
const maxLogBytes = 256 << 10

// PodLogReader is the seam for reading one pod's logs. The log
// subresource is streamed from the kubelet through the API server and is
// not served by controller-runtime's client, so agents depend on this
// interface and production wires in a client-go clientset.
type PodLogReader interface {
	PodLogs(ctx context.Context, namespace, pod string, opts *corev1.PodLogOptions) (string, error)
}

// ClientsetPodLogReader implements PodLogReader against a real cluster.
type ClientsetPodLogReader struct {
	Clientset kubernetes.Interface
}

var _ PodLogReader = (*ClientsetPodLogReader)(nil)

func (r *ClientsetPodLogReader) PodLogs(ctx context.Context, namespace, pod string, opts *corev1.PodLogOptions) (string, error) {
	stream, err := r.Clientset.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("opening log stream for %s/%s: %w", namespace, pod, err)
	}
	defer func() { _ = stream.Close() }()

	raw, err := io.ReadAll(io.LimitReader(stream, maxLogBytes))
	if err != nil {
		return "", fmt.Errorf("reading logs for %s/%s: %w", namespace, pod, err)
	}
	return string(raw), nil
}
