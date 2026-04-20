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

// gwb-workload launches a deterministic burst of sleep-pods for the
// bench harness. It is intentionally narrow: every pod gets the same
// label, runtime, and GPU request, and the only knob the benchmark
// cares about — arrival rate — is honoured with a simple ticker.
//
// The pods are minimal on purpose: busybox + `sleep`. Heavier images
// bias the benchmark with image-pull time, which the accounting engine
// already excludes (see earliestContainerStart in pod_conversion.go);
// a lightweight sleeper keeps "wall-clock consumed" and "kubelet
// observed running" within the same second.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

// options mirrors the CLI flags 1:1. Exposed as a struct so tests can
// drive buildPod without reaching through flag.* global state.
type options struct {
	Namespace   string
	Label       string
	Count       int
	RatePerSec  float64
	Runtime     time.Duration
	GPUs        string
	GPUResource string
	Image       string
	Prefix      string
	Kubeconfig  string
}

func main() {
	opts := parseFlags()
	if err := run(context.Background(), opts); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.Namespace, "namespace", "default",
		"namespace to create pods in")
	flag.StringVar(&o.Label, "label", "app=gwb-bench-worker",
		"single label key=value applied to every pod; budgets match on it")
	flag.IntVar(&o.Count, "count", 10,
		"total number of pods to create")
	flag.Float64Var(&o.RatePerSec, "rate", 1.0,
		"arrival rate in pods per second")
	flag.DurationVar(&o.Runtime, "runtime", 30*time.Second,
		"how long each pod sleeps before exiting Succeeded")
	flag.StringVar(&o.GPUs, "gpus", "1",
		"resource quantity each pod requests (e.g. '1', '100m', '500m')")
	flag.StringVar(&o.GPUResource, "gpu-resource", "nvidia.com/gpu",
		"resource name counted against the budget; use 'cpu' on GPU-less kind")
	flag.StringVar(&o.Image, "image", "busybox:1.36",
		"container image — must provide /bin/sh and sleep")
	flag.StringVar(&o.Prefix, "prefix", "bench-",
		"pod name prefix; index is zero-padded to 4 digits")
	flag.StringVar(&o.Kubeconfig, "kubeconfig", "",
		"kubeconfig path override; falls back to KUBECONFIG / ~/.kube/config / in-cluster")
	flag.Parse()
	return o
}

// run is the entry point split out of main so it can return errors
// cleanly and be driven from tests or embedding callers later.
func run(ctx context.Context, o options) error {
	labelKey, labelVal, err := parseLabel(o.Label)
	if err != nil {
		return err
	}
	gpuQty, err := resource.ParseQuantity(o.GPUs)
	if err != nil {
		return fmt.Errorf("parse --gpus: %w", err)
	}
	if o.RatePerSec <= 0 {
		return fmt.Errorf("--rate must be > 0, got %v", o.RatePerSec)
	}
	if o.Count <= 0 {
		return fmt.Errorf("--count must be > 0, got %d", o.Count)
	}

	cfg, err := loadKubeconfig(o.Kubeconfig)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build clientset: %w", err)
	}

	interval := intervalFromRate(o.RatePerSec)
	fmt.Fprintf(os.Stderr, "launching %d pods at %.2f/s (interval %v) into ns=%s label=%s=%s\n",
		o.Count, o.RatePerSec, interval, o.Namespace, labelKey, labelVal)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	start := time.Now()
	for i := 0; i < o.Count; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
		pod := buildPod(o, labelKey, labelVal, gpuQty, i)
		if _, err := cs.CoreV1().Pods(o.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create pod %s: %w", pod.Name, err)
		}
	}
	fmt.Fprintf(os.Stderr, "launched %d pods in %v\n",
		o.Count, time.Since(start).Round(time.Millisecond))
	return nil
}

// parseLabel splits "key=value" into two non-empty strings. An empty
// key or a missing '=' is rejected early so the bench harness sees a
// fast failure rather than creating unlabelled pods the budget can't
// see.
func parseLabel(raw string) (string, string, error) {
	key, val, ok := strings.Cut(raw, "=")
	if !ok || key == "" || val == "" {
		return "", "", fmt.Errorf("--label must be key=value with non-empty parts, got %q", raw)
	}
	return key, val, nil
}

// intervalFromRate turns a pods-per-second rate into a tick interval.
// Keeps float division in one place so the rounding behaviour is easy
// to reason about (sub-nanosecond jitter is ignored as insignificant
// against a kind cluster's per-pod scheduling cost of ~10ms+).
func intervalFromRate(rps float64) time.Duration {
	return time.Duration(float64(time.Second) / rps)
}

// loadKubeconfig honours --kubeconfig first, then falls back to the
// controller-runtime chain (KUBECONFIG → ~/.kube/config → in-cluster).
// Reusing the controller-runtime loader keeps the generator's config
// discovery identical to the operator's, which matters when both are
// run from the same CI shell.
func loadKubeconfig(override string) (*rest.Config, error) {
	if override != "" {
		return clientcmd.BuildConfigFromFlags("", override)
	}
	return ctrlconfig.GetConfig()
}

func buildPod(o options, labelKey, labelVal string, gpuQty resource.Quantity, idx int) *corev1.Pod {
	one := int64(1)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s%04d", o.Prefix, idx),
			Namespace: o.Namespace,
			Labels: map[string]string{
				labelKey:                       labelVal,
				"app.kubernetes.io/managed-by": "gwb-workload",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: &one,
			Containers: []corev1.Container{{
				Name:    "worker",
				Image:   o.Image,
				Command: []string{"sh", "-c", fmt.Sprintf("sleep %d", int(o.Runtime.Seconds()))},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceName(o.GPUResource): gpuQty,
					},
				},
			}},
		},
	}
}
