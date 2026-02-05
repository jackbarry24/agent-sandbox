package main

import (
	"context"
	"os"
	"sort"
	"strings"

	"sandbox/pkg/api"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type cacheConfig struct {
	mode            string
	hostPath        string
	pvcSize         string
	pvcStorageClass string
	pvcAccessMode   string
}

type streamConfig struct {
	mode         string
	sidecarImage string
	endpoint     string
	eventsDir    string
}

func cacheConfigFromEnv() cacheConfig {
	return cacheConfig{
		mode:            getenv("SANDBOX_CACHE_MODE", defaultCacheMode),
		hostPath:        getenv("SANDBOX_CACHE_HOSTPATH", "/var/lib/sbx-cache"),
		pvcSize:         getenv("SANDBOX_CACHE_PVC_SIZE", "5Gi"),
		pvcStorageClass: getenv("SANDBOX_CACHE_PVC_STORAGE_CLASS", ""),
		pvcAccessMode:   getenv("SANDBOX_CACHE_PVC_ACCESS_MODE", "ReadWriteOnce"),
	}
}

func streamConfigFromEnv() streamConfig {
	return streamConfig{
		mode:         getenv("SANDBOX_STREAM_MODE", "control-plane"),
		sidecarImage: getenv("SANDBOX_STREAM_SIDECAR_IMAGE", ""),
		endpoint:     getenv("SANDBOX_STREAM_ENDPOINT", ""),
		eventsDir:    getenv("SANDBOX_STREAM_EVENTS_DIR", "/sbx-events"),
	}
}

func cacheConfigFromRequest(req api.CreateSandboxRequest) cacheConfig {
	cfg := cacheConfigFromEnv()
	if req.CacheMode != "" {
		cfg.mode = req.CacheMode
	}
	if req.CachePVCSize != "" {
		cfg.pvcSize = req.CachePVCSize
	}
	if req.CachePVCStorageClass != "" {
		cfg.pvcStorageClass = req.CachePVCStorageClass
	}
	if req.CachePVCAccessMode != "" {
		cfg.pvcAccessMode = req.CachePVCAccessMode
	}
	return cfg
}

func defaultSandboxEnv() map[string]string {
	envs := map[string]string{}
	cfgEnv := configEnv()
	for k, v := range cfgEnv {
		if k == "" {
			continue
		}
		envs[k] = v
	}
	for _, pair := range os.Environ() {
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		if !strings.HasPrefix(key, "SANDBOX_ENV_") {
			continue
		}
		name := strings.TrimPrefix(key, "SANDBOX_ENV_")
		if name == "" {
			continue
		}
		envs[name] = val
	}
	return envs
}

func mergeEnv(base map[string]string, overlay map[string]string) map[string]string {
	if base == nil {
		base = map[string]string{}
	}
	for k, v := range overlay {
		if k == "" {
			continue
		}
		base[k] = v
	}
	return base
}

func mapToEnvVars(env map[string]string) []corev1.EnvVar {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]corev1.EnvVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, corev1.EnvVar{Name: k, Value: env[k]})
	}
	return out
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func joinCSV(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return strings.Join(list, ",")
}

func normalizeAllowedHosts(req api.CreateSandboxRequest) (allowed []string, disallowed []string) {
	allowed = req.AllowedHosts
	disallowed = req.DisallowedHosts
	if len(allowed) == 0 {
		cfgAllowed, _ := configAllowedHosts()
		if len(cfgAllowed) > 0 {
			allowed = cfgAllowed
		} else {
			allowed = splitCSV(getenv("SANDBOX_ALLOWED_HOSTS", ""))
		}
	}
	if len(disallowed) == 0 {
		_, cfgDisallowed := configAllowedHosts()
		if len(cfgDisallowed) > 0 {
			disallowed = cfgDisallowed
		} else {
			disallowed = splitCSV(getenv("SANDBOX_DISALLOWED_HOSTS", ""))
		}
	}
	return allowed, disallowed
}

func ensureCachePVC(ctx context.Context, client *kubernetes.Clientset, ns, name string, cfg cacheConfig) error {
	if cfg.mode != "pvc" {
		return nil
	}
	_, err := client.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{parseAccessMode(cfg.pvcAccessMode)},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(cfg.pvcSize),
				},
			},
		},
	}
	if cfg.pvcStorageClass != "" {
		pvc.Spec.StorageClassName = &cfg.pvcStorageClass
	}
	_, err = client.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{})
	return err
}

func parseAccessMode(val string) corev1.PersistentVolumeAccessMode {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "readwriteonce", "rwo":
		return corev1.ReadWriteOnce
	case "readwritemany", "rwx":
		return corev1.ReadWriteMany
	case "readonlymany", "rox":
		return corev1.ReadOnlyMany
	default:
		return corev1.ReadWriteOnce
	}
}

func sandboxCacheVolume(cfg cacheConfig) corev1.Volume {
	switch cfg.mode {
	case "hostpath":
		return corev1.Volume{
			Name: "cache",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: cfg.hostPath},
			},
		}
	case "pvc":
		return corev1.Volume{
			Name: "cache",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "cache"},
			},
		}
	default:
		return corev1.Volume{
			Name:         "cache",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}
	}
}

func sandboxPodSpec(image string, cmd []string, volumeMode, pvcName string, cacheCfg cacheConfig, envVars []corev1.EnvVar) corev1.PodSpec {
	if len(cmd) == 0 {
		cmd = []string{"sleep", "infinity"}
	}
	vols := []corev1.Volume{
		sandboxCacheVolume(cacheCfg),
	}
	mounts := []corev1.VolumeMount{
		{Name: "cache", MountPath: "/cache"},
	}
	if volumeMode == "pvc" {
		vols = append(vols, corev1.Volume{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName},
			},
		})
	} else {
		vols = append(vols, corev1.Volume{
			Name:         "workspace",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	}
	mounts = append(mounts, corev1.VolumeMount{Name: "workspace", MountPath: "/workspace"})

	streamCfg := streamConfigFromEnv()
	if streamCfg.mode == "sidecar" {
		vols = append(vols, corev1.Volume{
			Name:         "sbx-events",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: "sbx-events", MountPath: streamCfg.eventsDir})
	}

	containers := []corev1.Container{
		{
			Name:         "sandbox",
			Image:        image,
			Command:      cmd,
			VolumeMounts: mounts,
			Resources:    sandboxResources(),
			Env:          envVars,
		},
	}
	if streamCfg.mode == "sidecar" && streamCfg.sidecarImage != "" {
		sidecarEnv := []corev1.EnvVar{
			{Name: "SBX_STREAM_ENDPOINT", Value: streamCfg.endpoint},
			{Name: "SBX_EVENTS_DIR", Value: streamCfg.eventsDir},
			{
				Name: "SBX_SANDBOX_ID",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
				},
			},
		}
		containers = append(containers, corev1.Container{
			Name:  "stream",
			Image: streamCfg.sidecarImage,
			Env:   sidecarEnv,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "sbx-events", MountPath: streamCfg.eventsDir},
			},
		})
	}

	return corev1.PodSpec{
		Tolerations: []corev1.Toleration{
			{
				Key:      "node-role.kubernetes.io/control-plane",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			},
			{
				Key:      "node.kubernetes.io/not-ready",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			},
		},
		Containers: containers,
		Volumes:    vols,
	}
}
