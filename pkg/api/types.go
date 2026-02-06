package api

type CreateSandboxRequest struct {
	ID                   string            `json:"id"`
	Image                string            `json:"image"`
	Command              []string          `json:"command"`
	VolumeMode           string            `json:"volume_mode"` // emptydir|pvc
	CacheMode            string            `json:"cache_mode"`  // emptydir|hostpath|pvc
	CachePVCSize         string            `json:"cache_pvc_size"`
	CachePVCStorageClass string            `json:"cache_pvc_storage_class"`
	CachePVCAccessMode   string            `json:"cache_pvc_access_mode"`
	Env                  map[string]string `json:"env,omitempty"`
	AllowedHosts         []string          `json:"allowed_hosts,omitempty"`
	DisallowedHosts      []string          `json:"disallowed_hosts,omitempty"`
}

type CreateSandboxResponse struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	PodName   string `json:"pod_name"`
}

type ExecRequest struct {
	Command []string `json:"command"`
	Async   *bool    `json:"async"`
}

type ExecResponse struct {
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	ExecID   string `json:"exec_id,omitempty"`
	Status   string `json:"status,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

type SandboxStatus struct {
	ID           string `json:"id"`
	Namespace    string `json:"namespace"`
	Age          string `json:"age"`
	State        string `json:"state"`
	Allocated    string `json:"allocated"`
	LastExecTime string `json:"last_exec_time"`
}
