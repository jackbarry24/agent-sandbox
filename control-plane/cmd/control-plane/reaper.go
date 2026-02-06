package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (s *server) reapIdleSandboxes(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapOnce(ctx)
		}
	}
}

func (s *server) reapOnce(ctx context.Context) {
	ttl := getenvDuration("SANDBOX_IDLE_TTL", defaultIdleTTL)
	if ttl <= 0 {
		return
	}
	nsList, err := s.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	now := time.Now()
	for _, ns := range nsList.Items {
		name := ns.Name
		if !strings.HasPrefix(name, "sbx-") {
			continue
		}
		labels := ns.Labels
		if labels != nil && labels["sbx.allocated"] == "false" {
			continue
		}
		last := ns.Annotations["sbx.last_exec_at"]
		var lastTime time.Time
		if last != "" && last != "0" {
			if ts, err := strconv.ParseInt(last, 10, 64); err == nil {
				lastTime = time.Unix(ts, 0)
			}
		}
		if lastTime.IsZero() {
			lastTime = ns.CreationTimestamp.Time
		}
		if now.Sub(lastTime) > ttl {
			if err := s.client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{}); err == nil {
				log.Printf("reaped sandbox namespace=%s idle=%s", name, now.Sub(lastTime))
			}
		}
	}
}
