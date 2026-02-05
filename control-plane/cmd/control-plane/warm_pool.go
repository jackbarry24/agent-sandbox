package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultWarmControlNamespace = "sbx-warm-control"
	warmWindow                  = 60 * time.Second
	defaultIdleTTL              = 15 * time.Minute
)

type warmPoolConfig struct {
	size     int
	min      int
	max      int
	autosize bool
	idleTTL  time.Duration
}

type warmPool struct {
	client    *kubernetes.Clientset
	cfg       warmPoolConfig
	cache     cacheConfig
	controlNS string
	mu        sync.Mutex
	next      int
	recent    []time.Time
}

func warmPoolConfigFromEnv() warmPoolConfig {
	cfg := warmPoolConfig{
		size:     getenvInt("SANDBOX_WARM_POOL_SIZE", 0),
		min:      getenvInt("SANDBOX_WARM_POOL_MIN", 0),
		max:      getenvInt("SANDBOX_WARM_POOL_MAX", 0),
		autosize: getenvBool("SANDBOX_WARM_POOL_AUTOSIZE", false),
		idleTTL:  getenvDuration("SANDBOX_IDLE_TTL", defaultIdleTTL),
	}
	if cfg.autosize && cfg.max == 0 {
		cfg.max = 10
	}
	return cfg
}

func newWarmPool(client *kubernetes.Clientset, cfg warmPoolConfig, cacheCfg cacheConfig) *warmPool {
	return &warmPool{
		client:    client,
		cfg:       cfg,
		cache:     cacheCfg,
		controlNS: getenv("SANDBOX_WARM_CONTROL_NAMESPACE", defaultWarmControlNamespace),
	}
}

func (w *warmPool) enabled() bool {
	if w == nil {
		return false
	}
	if w.cfg.autosize {
		return w.cfg.max > 0 || w.cfg.min > 0
	}
	return w.cfg.size > 0
}

func (w *warmPool) allocateID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.next++
	return fmt.Sprintf("warm-%d", w.next)
}

func (w *warmPool) recordCreate() {
	if w == nil || !w.cfg.autosize {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recent = append(w.recent, time.Now())
	w.pruneLocked(time.Now())
}

func (w *warmPool) desiredSize() int {
	if w == nil {
		return 0
	}
	if !w.cfg.autosize {
		return w.cfg.size
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	w.pruneLocked(now)
	desired := len(w.recent)
	if desired < w.cfg.min {
		desired = w.cfg.min
	}
	if w.cfg.max > 0 && desired > w.cfg.max {
		desired = w.cfg.max
	}
	return desired
}

func (w *warmPool) pruneLocked(now time.Time) {
	cut := now.Add(-warmWindow)
	idx := 0
	for _, t := range w.recent {
		if t.After(cut) {
			w.recent[idx] = t
			idx++
		}
	}
	w.recent = w.recent[:idx]
}

func (w *warmPool) run(ctx context.Context, image string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	_ = w.ensureNamespace(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = w.ensureWarmNamespaces(ctx, image)
			_ = w.reapIdle(ctx)
		}
	}
}

func (w *warmPool) rebuildFromCluster(ctx context.Context, image string) error {
	if w == nil {
		return nil
	}
	_ = w.ensureNamespace(ctx)
	if err := w.ensureWarmNamespaces(ctx, image); err != nil {
		return err
	}
	if !w.cfg.autosize {
		return nil
	}
	nsList, err := w.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recent = w.recent[:0]
	for _, ns := range nsList.Items {
		last := ns.Annotations["sbx.last_exec_at"]
		if last == "" {
			continue
		}
		lastUnix, err := strconv.ParseInt(last, 10, 64)
		if err != nil {
			continue
		}
		t := time.Unix(lastUnix, 0)
		if now.Sub(t) <= warmWindow {
			w.recent = append(w.recent, t)
		}
	}
	return nil
}

func (w *warmPool) ensureNamespace(ctx context.Context) error {
	_, err := w.client.CoreV1().Namespaces().Get(ctx, w.controlNS, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	_, err = w.client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: w.controlNS},
	}, metav1.CreateOptions{})
	return err
}

// claimWarmNamespace finds an unclaimed warm namespace, marks it with session labels, and returns it.
func (w *warmPool) claimWarmNamespace(ctx context.Context, externalID string) (string, bool, error) {
	selector := labels.SelectorFromSet(map[string]string{
		"sbx.pool":  "warm",
		"sbx.state": "ready",
	})
	nsList, err := w.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return "", false, err
	}
	if len(nsList.Items) == 0 {
		return "", false, nil
	}
	sort.Slice(nsList.Items, func(i, j int) bool { return nsList.Items[i].Name < nsList.Items[j].Name })
	for _, candidate := range nsList.Items {
		ready, err := w.isPodReady(ctx, candidate.Name, "sandbox")
		if err != nil || !ready {
			continue
		}
		if candidate.Labels == nil {
			candidate.Labels = map[string]string{}
		}
		if candidate.Annotations == nil {
			candidate.Annotations = map[string]string{}
		}
		candidate.Labels["sbx.state"] = "claimed"
		if externalID != "" {
			candidate.Labels["sbx.external_id"] = externalID
		}
		candidate.Annotations["sbx.last_exec_at"] = strconv.FormatInt(time.Now().Unix(), 10)
		_, err = w.client.CoreV1().Namespaces().Update(ctx, &candidate, metav1.UpdateOptions{})
		if err != nil {
			return "", false, err
		}
		return candidate.Name, true, nil
	}
	return "", false, nil
}

func (w *warmPool) ensureWarmNamespaces(ctx context.Context, image string) error {
	desired := w.desiredSize()
	readySelector := labels.SelectorFromSet(map[string]string{
		"sbx.pool":  "warm",
		"sbx.state": "ready",
	})
	readyList, err := w.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: readySelector.String()})
	if err == nil {
		metricWarmPoolReady.Set(int64(len(readyList.Items)))
	}
	metricWarmPoolDesired.Set(int64(desired))
	selector := labels.SelectorFromSet(map[string]string{"sbx.pool": "warm"})
	nsList, err := w.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return err
	}
	for i := range nsList.Items {
		ns := nsList.Items[i]
		if ns.Labels["sbx.state"] != "creating" {
			continue
		}
		ready, err := w.isPodReady(ctx, ns.Name, "sandbox")
		if err != nil || !ready {
			continue
		}
		ns.Labels["sbx.state"] = "ready"
		_, _ = w.client.CoreV1().Namespaces().Update(ctx, &ns, metav1.UpdateOptions{})
	}
	if len(nsList.Items) >= desired {
		return nil
	}
	for i := len(nsList.Items); i < desired; i++ {
		name := fmt.Sprintf("sbx-warm-%d", i+1)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					"sbx.pool":  "warm",
					"sbx.state": "creating",
				},
				Annotations: map[string]string{
					"sbx.last_exec_at": "0",
				},
			},
		}
		_, _ = w.client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		_ = ensureCachePVC(ctx, w.client, name, "cache", w.cache)
		envVars := defaultSandboxEnv()
		allowed, disallowed := configAllowedHosts()
		if len(allowed) == 0 {
			allowed = splitCSV(getenv("SANDBOX_ALLOWED_HOSTS", ""))
		}
		if len(disallowed) == 0 {
			disallowed = splitCSV(getenv("SANDBOX_DISALLOWED_HOSTS", ""))
		}
		if len(allowed) > 0 {
			envVars["SBX_ALLOWED_HOSTS"] = joinCSV(allowed)
		}
		if len(disallowed) > 0 {
			envVars["SBX_DISALLOWED_HOSTS"] = joinCSV(disallowed)
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "sandbox",
				Labels: map[string]string{
					"sbx.warm": "true",
				},
			},
			Spec: sandboxPodSpec(image, []string{"sleep", "infinity"}, "emptydir", "", w.cache, mapToEnvVars(envVars)),
		}
		_, _ = w.client.CoreV1().Pods(name).Create(ctx, pod, metav1.CreateOptions{})
	}
	return nil
}

func (w *warmPool) reapIdle(ctx context.Context) error {
	selector := labels.SelectorFromSet(map[string]string{
		"sbx.pool":  "warm",
		"sbx.state": "claimed",
	})
	nsList, err := w.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return err
	}
	now := time.Now()
	for _, ns := range nsList.Items {
		last := ns.Annotations["sbx.last_exec_at"]
		if last == "" {
			continue
		}
		lastUnix, err := strconv.ParseInt(last, 10, 64)
		if err != nil {
			continue
		}
		if lastUnix == 0 {
			continue
		}
		if now.Sub(time.Unix(lastUnix, 0)) > w.cfg.idleTTL {
			_ = w.client.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{})
		}
	}
	return nil
}

func (w *warmPool) isPodReady(ctx context.Context, ns, name string) (bool, error) {
	pod, err := w.client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if pod.Status.Phase != corev1.PodRunning {
		return false, nil
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true, nil
		}
	}
	return false, nil
}
