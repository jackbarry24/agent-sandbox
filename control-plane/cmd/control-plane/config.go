package main

import (
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Image                string            `yaml:"image"`
	VolumeMode           string            `yaml:"volume_mode"`
	CacheMode            string            `yaml:"cache_mode"`
	CacheHostPath        string            `yaml:"cache_hostpath"`
	CachePVCSize         string            `yaml:"cache_pvc_size"`
	CachePVCStorageClass string            `yaml:"cache_pvc_storage_class"`
	CachePVCAccessMode   string            `yaml:"cache_pvc_access_mode"`
	WarmPoolSize         int               `yaml:"warm_pool_size"`
	WarmPoolAutosize     bool              `yaml:"warm_pool_autosize"`
	WarmPoolMin          int               `yaml:"warm_pool_min"`
	WarmPoolMax          int               `yaml:"warm_pool_max"`
	IdleTTL              string            `yaml:"idle_ttl"`
	WarmControlNamespace string            `yaml:"warm_control_namespace"`
	CreateReadyTimeout   string            `yaml:"create_ready_timeout"`
	CPURequest           string            `yaml:"cpu_request"`
	MemRequest           string            `yaml:"mem_request"`
	CPULimit             string            `yaml:"cpu_limit"`
	MemLimit             string            `yaml:"mem_limit"`
	AllowedHosts         []string          `yaml:"allowed_hosts"`
	DisallowedHosts      []string          `yaml:"disallowed_hosts"`
	Env                  map[string]string `yaml:"env"`
	StreamMode           string            `yaml:"stream_mode"`
	StreamSidecarImage   string            `yaml:"stream_sidecar_image"`
	StreamEndpoint       string            `yaml:"stream_endpoint"`
	StreamEventsDir      string            `yaml:"stream_events_dir"`
	StreamBuffer         int               `yaml:"stream_buffer"`
	UseAsyncExec         bool              `yaml:"use_async_exec"`
}

var (
	configOnce sync.Once
	configErr  error
	config     Config
)

func loadConfig() (Config, error) {
	path := os.Getenv("SANDBOX_CONFIG")
	if path == "" {
		return Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func getConfig() (Config, error) {
	configOnce.Do(func() {
		config, configErr = loadConfig()
	})
	return config, configErr
}

func configString(key string) (string, bool) {
	cfg, err := getConfig()
	if err != nil {
		return "", false
	}
	switch key {
	case "SANDBOX_IMAGE":
		if cfg.Image != "" {
			return cfg.Image, true
		}
	case "SANDBOX_VOLUME_MODE":
		if cfg.VolumeMode != "" {
			return cfg.VolumeMode, true
		}
	case "SANDBOX_CACHE_MODE":
		if cfg.CacheMode != "" {
			return cfg.CacheMode, true
		}
	case "SANDBOX_CACHE_HOSTPATH":
		if cfg.CacheHostPath != "" {
			return cfg.CacheHostPath, true
		}
	case "SANDBOX_CACHE_PVC_SIZE":
		if cfg.CachePVCSize != "" {
			return cfg.CachePVCSize, true
		}
	case "SANDBOX_CACHE_PVC_STORAGE_CLASS":
		if cfg.CachePVCStorageClass != "" {
			return cfg.CachePVCStorageClass, true
		}
	case "SANDBOX_CACHE_PVC_ACCESS_MODE":
		if cfg.CachePVCAccessMode != "" {
			return cfg.CachePVCAccessMode, true
		}
	case "SANDBOX_IDLE_TTL":
		if cfg.IdleTTL != "" {
			return cfg.IdleTTL, true
		}
	case "SANDBOX_WARM_CONTROL_NAMESPACE":
		if cfg.WarmControlNamespace != "" {
			return cfg.WarmControlNamespace, true
		}
	case "SANDBOX_CREATE_READY_TIMEOUT":
		if cfg.CreateReadyTimeout != "" {
			return cfg.CreateReadyTimeout, true
		}
	case "SANDBOX_CPU_REQUEST":
		if cfg.CPURequest != "" {
			return cfg.CPURequest, true
		}
	case "SANDBOX_MEM_REQUEST":
		if cfg.MemRequest != "" {
			return cfg.MemRequest, true
		}
	case "SANDBOX_CPU_LIMIT":
		if cfg.CPULimit != "" {
			return cfg.CPULimit, true
		}
	case "SANDBOX_MEM_LIMIT":
		if cfg.MemLimit != "" {
			return cfg.MemLimit, true
		}
	case "SANDBOX_STREAM_MODE":
		if cfg.StreamMode != "" {
			return cfg.StreamMode, true
		}
	case "SANDBOX_STREAM_SIDECAR_IMAGE":
		if cfg.StreamSidecarImage != "" {
			return cfg.StreamSidecarImage, true
		}
	case "SANDBOX_STREAM_ENDPOINT":
		if cfg.StreamEndpoint != "" {
			return cfg.StreamEndpoint, true
		}
	case "SANDBOX_STREAM_EVENTS_DIR":
		if cfg.StreamEventsDir != "" {
			return cfg.StreamEventsDir, true
		}
	}
	return "", false
}

func configInt(key string) (int, bool) {
	cfg, err := getConfig()
	if err != nil {
		return 0, false
	}
	switch key {
	case "SANDBOX_WARM_POOL_SIZE":
		if cfg.WarmPoolSize != 0 {
			return cfg.WarmPoolSize, true
		}
	case "SANDBOX_WARM_POOL_MIN":
		if cfg.WarmPoolMin != 0 {
			return cfg.WarmPoolMin, true
		}
	case "SANDBOX_WARM_POOL_MAX":
		if cfg.WarmPoolMax != 0 {
			return cfg.WarmPoolMax, true
		}
	case "SANDBOX_STREAM_BUFFER":
		if cfg.StreamBuffer != 0 {
			return cfg.StreamBuffer, true
		}
	case "SANDBOX_USE_ASYNC_EXEC":
		if cfg.UseAsyncExec {
			return 1, true
		}
	}
	return 0, false
}

func configBool(key string) (bool, bool) {
	cfg, err := getConfig()
	if err != nil {
		return false, false
	}
	switch key {
	case "SANDBOX_WARM_POOL_AUTOSIZE":
		if cfg.WarmPoolAutosize {
			return true, true
		}
	case "SANDBOX_USE_ASYNC_EXEC":
		if cfg.UseAsyncExec {
			return true, true
		}
	}
	return false, false
}

func configDuration(key string) (time.Duration, bool) {
	cfg, err := getConfig()
	if err != nil {
		return 0, false
	}
	switch key {
	case "SANDBOX_IDLE_TTL":
		if cfg.IdleTTL != "" {
			if d, err := time.ParseDuration(cfg.IdleTTL); err == nil {
				return d, true
			}
		}
	case "SANDBOX_CREATE_READY_TIMEOUT":
		if cfg.CreateReadyTimeout != "" {
			if d, err := time.ParseDuration(cfg.CreateReadyTimeout); err == nil {
				return d, true
			}
		}
	}
	return 0, false
}

func configAllowedHosts() ([]string, []string) {
	cfg, err := getConfig()
	if err != nil {
		return nil, nil
	}
	return cfg.AllowedHosts, cfg.DisallowedHosts
}

func configEnv() map[string]string {
	cfg, err := getConfig()
	if err != nil {
		return nil
	}
	return cfg.Env
}
