package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"expvar"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"sandbox/control-plane/internal/k8s"
	"sandbox/pkg/api"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	defaultImage      = "sandbox-base:dev"
	defaultVolumeMode = "emptydir"
	defaultWaitReady  = 20 * time.Second
	defaultCacheMode  = "emptydir"
)

var _ = expvar.NewInt

type server struct {
	client *kubernetes.Clientset
	cfg    *rest.Config
	warm   *warmPool
	stream *streamHub
}

func main() {
	var addr string
	flag.StringVar(&addr, "addr", ":8080", "listen address")
	flag.Parse()

	client, cfg, err := k8s.NewClient()
	if err != nil {
		log.Fatalf("k8s client: %v", err)
	}
	if _, err := getConfig(); err != nil {
		log.Fatalf("config: %v", err)
	}
	if path := os.Getenv("SANDBOX_CONFIG"); path != "" {
		log.Printf("config loaded: %s", path)
	} else {
		log.Printf("config loaded: <none>")
	}

	s := &server{
		client: client,
		cfg:    cfg,
		warm:   nil,
		stream: newStreamHub(getenvInt("SANDBOX_STREAM_BUFFER", 200)),
	}
	metricCacheMode.Set(getenv("SANDBOX_CACHE_MODE", defaultCacheMode))
	metricStreamBuffer.Set(int64(getenvInt("SANDBOX_STREAM_BUFFER", 200)))
	s.warm = newWarmPool(client, warmPoolConfigFromEnv(), cacheConfigFromEnv())
	log.Printf("warm pool enabled=%t autosize=%t size=%d min=%d max=%d",
		s.warm.enabled(), s.warm.cfg.autosize, s.warm.cfg.size, s.warm.cfg.min, s.warm.cfg.max)
	if s.warm.enabled() {
		if err := s.warm.rebuildFromCluster(context.Background(), getenv("SANDBOX_IMAGE", defaultImage)); err != nil {
			log.Printf("warm pool rebuild: %v", err)
		}
		go s.warm.run(context.Background(), getenv("SANDBOX_IMAGE", defaultImage))
	}
	go s.reapIdleSandboxes(context.Background())

	router := gin.New()
	router.Use(requestIDMiddleware(), ginLogger())
	router.GET("/healthz", s.handleHealth)
	router.GET("/metrics", gin.WrapH(expvar.Handler()))
	router.POST("/sandboxes", s.handleSandboxes)
	router.GET("/sandboxes", s.listSandboxes)
	router.GET("/sandboxes/:id", s.getSandbox)
	router.POST("/sandboxes/:id/exec", s.execSandbox)
	router.GET("/sandboxes/:id/stream", s.streamSandbox)
	router.GET("/sandboxes/:id/ingest", s.ingestSandbox)
	router.DELETE("/sandboxes/:id", s.deleteSandbox)

	log.Printf("control-plane listening on %s", addr)
	if err := router.Run(addr); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func (s *server) handleHealth(c *gin.Context) {
	c.String(200, "ok")
}

func (s *server) handleSandboxes(c *gin.Context) {
	var req api.CreateSandboxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, err.Error())
		return
	}
	requestedID := req.ID
	if req.ID == "" {
		req.ID = generateID()
	}
	if !validID(req.ID) {
		writeError(c, 400, "id must be DNS-1123 compatible (lowercase letters, numbers, '-')")
		return
	}
	image := req.Image
	if image == "" {
		image = getenv("SANDBOX_IMAGE", defaultImage)
	}
	volumeMode := req.VolumeMode
	if volumeMode == "" {
		volumeMode = getenv("SANDBOX_VOLUME_MODE", defaultVolumeMode)
	}
	cacheCfg := cacheConfigFromRequest(req)
	envVars := defaultSandboxEnv()
	envVars = mergeEnv(envVars, req.Env)
	allowedHosts, disallowedHosts := normalizeAllowedHosts(req)
	if len(allowedHosts) > 0 {
		if _, ok := envVars["SBX_ALLOWED_HOSTS"]; !ok {
			envVars["SBX_ALLOWED_HOSTS"] = joinCSV(allowedHosts)
		}
	}
	if len(disallowedHosts) > 0 {
		if _, ok := envVars["SBX_DISALLOWED_HOSTS"]; !ok {
			envVars["SBX_DISALLOWED_HOSTS"] = joinCSV(disallowedHosts)
		}
	}

	ns := req.ID
	warmClaimed := false
	if requestedID == "" && s.warm != nil && s.warm.enabled() {
		if claimed, ok, err := s.warm.claimWarmNamespace(c.Request.Context()); err == nil && ok {
			// warm namespace already has a ready pod; reuse it
			ns = claimed
			warmClaimed = true
		} else if err != nil {
			writeError(c, 500, err.Error())
			return
		}
	}
	if !warmClaimed {
		ns = sandboxNamespace(req.ID)
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	nsAnnotations := map[string]string{}
	if len(allowedHosts) > 0 {
		nsAnnotations["sbx.allowed_hosts"] = joinCSV(allowedHosts)
	}
	if len(disallowedHosts) > 0 {
		nsAnnotations["sbx.disallowed_hosts"] = joinCSV(disallowedHosts)
	}
	if err := s.ensureNamespace(ctx, ns, nil, nsAnnotations); err != nil {
		writeError(c, 500, err.Error())
		return
	}

	var pvcName string
	if volumeMode == "pvc" {
		pvcName = "workspace"
		if err := s.ensurePVC(ctx, ns, pvcName); err != nil {
			writeError(c, 500, err.Error())
			return
		}
	}
	if err := ensureCachePVC(ctx, s.client, ns, "cache", cacheCfg); err != nil {
		writeError(c, 500, err.Error())
		return
	}

	podName := "sandbox"
	podAnnotations := map[string]string{}
	if len(allowedHosts) > 0 {
		podAnnotations["sbx.allowed_hosts"] = joinCSV(allowedHosts)
	}
	if len(disallowedHosts) > 0 {
		podAnnotations["sbx.disallowed_hosts"] = joinCSV(disallowedHosts)
	}
	if err := s.ensurePod(ctx, ns, podName, image, req.Command, volumeMode, pvcName, cacheCfg, mapToEnvVars(envVars), podAnnotations); err != nil {
		writeError(c, 500, err.Error())
		return
	}

	resp := api.CreateSandboxResponse{ID: ns, Namespace: ns, PodName: podName}
	metricCreates.Add(1)
	if warmClaimed {
		metricCreateWarmHit.Add(1)
	} else {
		metricCreateCold.Add(1)
	}
	if s.warm.enabled() {
		s.warm.recordCreate()
	}
	s.trackReadyAsync(ns, podName)
	writeJSON(c, 200, resp)
}

func (s *server) execSandbox(c *gin.Context) {
	id := c.Param("id")
	var req api.ExecRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, err.Error())
		return
	}
	if len(req.Command) == 0 {
		writeError(c, 400, "command is required")
		return
	}

	ns := id
	podName := "sandbox"

	ctx, cancel := context.WithTimeout(c.Request.Context(), defaultWaitReady)
	defer cancel()
	if err := s.waitForPodReady(ctx, ns, podName); err != nil {
		writeError(c, 409, "sandbox not ready: "+err.Error())
		return
	}
	streamCfg := streamConfigFromEnv()
	useAsync := getenvBool("SANDBOX_ASYNC_EXEC", true)
	if req.Async != nil {
		useAsync = *req.Async
	}
	if useAsync {
		execID := generateExecID()
		if streamCfg.sidecarImage != "" {
			cmd := wrapCommandForSidecar(execID, req.Command, streamCfg.eventsDir)
			go s.execCommandStream(context.Background(), ns, podName, "sandbox", execID, cmd)
		} else {
			go s.execCommandStream(context.Background(), ns, podName, "sandbox", execID, req.Command)
		}
		metricExecs.Add(1)
		writeJSON(c, 200, api.ExecResponse{ExecID: execID, Status: "running"})
		return
	}

	stdout, stderr, err := s.execCommand(c.Request.Context(), ns, podName, "sandbox", req.Command)
	if err != nil {
		writeError(c, 500, err.Error())
		return
	}
	_ = s.updateLastExec(c.Request.Context(), ns)
	metricExecs.Add(1)
	writeJSON(c, 200, api.ExecResponse{Stdout: stdout, Stderr: stderr, Status: "completed"})
}

func (s *server) deleteSandbox(c *gin.Context) {
	id := c.Param("id")
	ns := id
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	if err := s.client.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{}); err != nil {
		writeError(c, 500, err.Error())
		return
	}
	metricDeletes.Add(1)
	writeJSON(c, 200, map[string]string{"status": "deleted"})
}

func (s *server) getSandbox(c *gin.Context) {
	id := c.Param("id")
	ns := id
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	pod, err := s.client.CoreV1().Pods(ns).Get(ctx, "sandbox", metav1.GetOptions{})
	if err != nil {
		writeError(c, 404, err.Error())
		return
	}
	writeJSON(c, 200, map[string]string{
		"id":        id,
		"namespace": ns,
		"pod_name":  pod.Name,
		"phase":     string(pod.Status.Phase),
	})
}

func (s *server) listSandboxes(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	nsList, err := s.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		writeError(c, 500, err.Error())
		return
	}
	now := time.Now()
	statuses := make([]api.SandboxStatus, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		if !strings.HasPrefix(ns.Name, "sbx-") {
			continue
		}
		allocated := "true"
		if ns.Labels != nil && ns.Labels["sbx.allocated"] != "" {
			allocated = ns.Labels["sbx.allocated"]
		}
		lastExec := "-"
		if ts := ns.Annotations["sbx.last_exec_at"]; ts != "" && ts != "0" {
			if unix, err := strconv.ParseInt(ts, 10, 64); err == nil {
				lastExec = time.Unix(unix, 0).UTC().Format(time.RFC3339)
			}
		}
		age := now.Sub(ns.CreationTimestamp.Time)
		if age < 0 {
			age = 0
		}
		statuses = append(statuses, api.SandboxStatus{
			ID:           ns.Name,
			Namespace:    ns.Name,
			Age:          formatAge(age),
			State:        string(ns.Status.Phase),
			Allocated:    allocated,
			LastExecTime: lastExec,
		})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	writeJSON(c, 200, statuses)
}

func (s *server) ensureNamespace(ctx context.Context, name string, labels, annotations map[string]string) error {
	ns, err := s.client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		updated := false
		if len(labels) > 0 {
			if ns.Labels == nil {
				ns.Labels = map[string]string{}
			}
			for k, v := range labels {
				if ns.Labels[k] != v {
					ns.Labels[k] = v
					updated = true
				}
			}
		}
		if len(annotations) > 0 {
			if ns.Annotations == nil {
				ns.Annotations = map[string]string{}
			}
			for k, v := range annotations {
				if ns.Annotations[k] != v {
					ns.Annotations[k] = v
					updated = true
				}
			}
		}
		if updated {
			_, err = s.client.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
		}
		return err
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = s.client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (s *server) ensurePVC(ctx context.Context, ns, name string) error {
	_, err := s.client.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("10Gi"),
				},
			},
		},
	}
	_, err = s.client.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{})
	return err
}

func (s *server) ensurePod(ctx context.Context, ns, name, image string, cmd []string, volumeMode, pvcName string, cacheCfg cacheConfig, envVars []corev1.EnvVar, annotations map[string]string) error {
	_, err := s.client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
		Spec: sandboxPodSpec(image, cmd, volumeMode, pvcName, cacheCfg, envVars),
	}
	_, err = s.client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	return err
}

func (s *server) waitForPodReady(ctx context.Context, ns, name string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pod, err := s.client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if pod.Status.Phase == corev1.PodRunning {
				for _, cond := range pod.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						return nil
					}
				}
			}
		}
	}
}

func (s *server) trackReadyAsync(ns, podName string) {
	go func() {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), getenvDuration("SANDBOX_CREATE_READY_TIMEOUT", 60*time.Second))
		defer cancel()
		if err := s.waitForPodReady(ctx, ns, podName); err != nil {
			return
		}
		recordCreateReady(time.Since(start).Milliseconds())
	}()
}

func (s *server) updateLastExec(ctx context.Context, ns string) error {
	n, err := s.client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if n.Annotations == nil {
		n.Annotations = map[string]string{}
	}
	n.Annotations["sbx.last_exec_at"] = strconv.FormatInt(time.Now().Unix(), 10)
	_, err = s.client.CoreV1().Namespaces().Update(ctx, n, metav1.UpdateOptions{})
	return err
}

func (s *server) execCommand(ctx context.Context, ns, pod, container string, cmd []string) (string, string, error) {
	req := s.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(s.cfg, "POST", req.URL())
	if err != nil {
		return "", "", err
	}
	var stdout, stderr strings.Builder
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return stdout.String(), stderr.String(), err
	}
	return stdout.String(), stderr.String(), nil
}

func (s *server) execCommandStream(ctx context.Context, ns, pod, container, execID string, cmd []string) {
	req := s.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(s.cfg, "POST", req.URL())
	if err != nil {
		s.stream.publish(execEvent{
			SandboxID: ns,
			ExecID:    execID,
			Seq:       s.stream.nextSeq(),
			Type:      "exit",
			Stream:    "stderr",
			Data:      err.Error(),
			ExitCode:  1,
			Time:      nowTS(),
		})
		return
	}

	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	_ = err
	_ = s.updateLastExec(context.Background(), ns)
}

func (s *server) streamSandbox(c *gin.Context) {
	id := c.Param("id")
	ns := id
	execID := c.Query("exec_id")
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch, snapshot := s.stream.subscribe(ns)
	defer s.stream.unsubscribe(ns, ch)
	for _, evt := range snapshot {
		if execID != "" && evt.ExecID != execID {
			continue
		}
		outEvt := evt
		if id != "" {
			outEvt.SandboxID = id
		}
		if err := writeEventJSON(conn, outEvt); err != nil {
			return
		}
	}
	for evt := range ch {
		if execID != "" && evt.ExecID != execID {
			continue
		}
		outEvt := evt
		if id != "" {
			outEvt.SandboxID = id
		}
		if err := writeEventJSON(conn, outEvt); err != nil {
			return
		}
	}
}

func (s *server) ingestSandbox(c *gin.Context) {
	id := c.Param("id")
	ns := id
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var evt execEvent
		if err := json.Unmarshal(msg, &evt); err != nil {
			continue
		}
		evt.SandboxID = ns
		evt.Seq = s.stream.nextSeq()
		if evt.Time == "" {
			evt.Time = nowTS()
		}
		s.stream.publish(evt)
	}
}

func sandboxNamespace(id string) string {
	return "sbx-" + id
}

func validID(id string) bool {
	re := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	return re.MatchString(id)
}

func writeJSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}

func writeError(c *gin.Context, status int, msg string) {
	writeJSON(c, status, map[string]string{"error": msg})
}

func getenv(key, fallback string) string {
	if v, ok := configString(key); ok {
		return v
	}
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v, ok := configInt(key); ok {
		return v
	}
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := configDuration(key); ok {
		return v
	}
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func getenvBool(key string, fallback bool) bool {
	if v, ok := configBool(key); ok {
		return v
	}
	if v := os.Getenv(key); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return fallback
}

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader("X-Request-Id")
		if reqID == "" {
			reqID = fmtRequestID()
			c.Request.Header.Set("X-Request-Id", reqID)
		}
		c.Writer.Header().Set("X-Request-Id", reqID)
		c.Next()
	}
}

func ginLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		reqID := c.GetHeader("X-Request-Id")
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		log.Printf("req_id=%s method=%s path=%s status=%d duration=%s", reqID, c.Request.Method, path, c.Writer.Status(), time.Since(start))
	}
}

func fmtRequestID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func generateID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func generateExecID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func formatAge(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	secs := int(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	hrs := mins / 60
	if hrs < 24 {
		return fmt.Sprintf("%dh", hrs)
	}
	days := hrs / 24
	return fmt.Sprintf("%dd", days)
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.IndexFunc(arg, func(r rune) bool {
		return r == '\'' || r == '"' || r == '\\' || r == '$' || r == '`' || r == ' ' || r == '\t' || r == '\n'
	}) == -1 {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
}

func shellJoin(args []string) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func wrapCommandForSidecar(execID string, cmd []string, eventsDir string) []string {
	escaped := shellJoin(cmd)
	if eventsDir == "" {
		eventsDir = "/sbx-events"
	}
	script := fmt.Sprintf(
		"mkdir -p %s; out=%s/%s.stdout; err=%s/%s.stderr; (%s) >$out 2>$err; code=$?; echo $code > %s/%s.exit; exit $code",
		shellQuote(eventsDir),
		shellQuote(eventsDir),
		execID,
		shellQuote(eventsDir),
		execID,
		escaped,
		shellQuote(eventsDir),
		execID,
	)
	return []string{"bash", "-lc", script}
}

func sandboxResources() corev1.ResourceRequirements {
	reqs := corev1.ResourceList{}
	limits := corev1.ResourceList{}

	if v := os.Getenv("SANDBOX_CPU_REQUEST"); v != "" {
		reqs[corev1.ResourceCPU] = resource.MustParse(v)
	}
	if v := os.Getenv("SANDBOX_MEM_REQUEST"); v != "" {
		reqs[corev1.ResourceMemory] = resource.MustParse(v)
	}
	if v := os.Getenv("SANDBOX_CPU_LIMIT"); v != "" {
		limits[corev1.ResourceCPU] = resource.MustParse(v)
	}
	if v := os.Getenv("SANDBOX_MEM_LIMIT"); v != "" {
		limits[corev1.ResourceMemory] = resource.MustParse(v)
	}

	return corev1.ResourceRequirements{
		Requests: reqs,
		Limits:   limits,
	}
}
