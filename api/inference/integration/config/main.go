package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// Configuration structures

// ModelArchitecture describes the model's input/output modalities.
type ModelArchitecture struct {
	Modality         string   `yaml:"modality,omitempty"`
	InputModalities  []string `yaml:"inputModalities,omitempty"`
	OutputModalities []string `yaml:"outputModalities,omitempty"`
	InstructType     string   `yaml:"instructType,omitempty"`
	Tokenizer        string   `yaml:"tokenizer,omitempty"`
}

// MarshalYAML renders short slice fields in flow style (e.g., ["text"]).
func (a ModelArchitecture) MarshalYAML() (interface{}, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}

	addScalar := func(key, value string) {
		if value == "" {
			return
		}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Value: value},
		)
	}
	addFlowSeq := func(key string, values []string) {
		if len(values) == 0 {
			return
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, v := range values {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
		}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			seq,
		)
	}

	addScalar("modality", a.Modality)
	addFlowSeq("inputModalities", a.InputModalities)
	addFlowSeq("outputModalities", a.OutputModalities)
	addScalar("instructType", a.InstructType)
	addScalar("tokenizer", a.Tokenizer)

	return node, nil
}

// ModelInfo holds optional metadata for the /v1/models endpoint.
type ModelInfo struct {
	Name                string                 `yaml:"name,omitempty"`
	Description         string                 `yaml:"description,omitempty"`
	ContextLength       int                    `yaml:"contextLength,omitempty"`
	MaxCompletionTokens int                    `yaml:"maxCompletionTokens,omitempty"`
	Architecture        *ModelArchitecture     `yaml:"architecture,omitempty"`
	SupportedParameters []string               `yaml:"supportedParameters,omitempty"`
	SupportedFormats    []string               `yaml:"supportedFormats,omitempty"`
	DefaultParameters   map[string]interface{} `yaml:"defaultParameters,omitempty"`
	TeeType             string                 `yaml:"teeType,omitempty"`
	ExpirationDate      string                 `yaml:"expirationDate,omitempty"`
	VideoSizeRatios     map[string]float64     `yaml:"videoSizeRatios,omitempty"`
}

// MarshalYAML renders supportedFormats in flow style to match the actual config format.
func (m ModelInfo) MarshalYAML() (interface{}, error) {
	// Marshal via yaml.Node so we can control per-field style.
	var node yaml.Node
	if err := node.Encode(struct {
		Name                string                 `yaml:"name,omitempty"`
		Description         string                 `yaml:"description,omitempty"`
		ContextLength       int                    `yaml:"contextLength,omitempty"`
		MaxCompletionTokens int                    `yaml:"maxCompletionTokens,omitempty"`
		Architecture        *ModelArchitecture     `yaml:"architecture,omitempty"`
		SupportedParameters []string               `yaml:"supportedParameters,omitempty"`
		SupportedFormats    []string               `yaml:"supportedFormats,omitempty"`
		DefaultParameters   map[string]interface{} `yaml:"defaultParameters,omitempty"`
		TeeType             string                 `yaml:"teeType,omitempty"`
		ExpirationDate      string                 `yaml:"expirationDate,omitempty"`
		VideoSizeRatios     map[string]float64     `yaml:"videoSizeRatios,omitempty"`
	}(m)); err != nil {
		return nil, err
	}

	// Walk the mapping node and set flow style on supportedFormats sequence.
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			if key == "supportedFormats" && node.Content[i+1].Kind == yaml.SequenceNode {
				node.Content[i+1].Style = yaml.FlowStyle
			}
		}
	}

	return &node, nil
}

type Service struct {
	ServingURL       string                 `yaml:"servingUrl,omitempty"`
	TargetURL        string                 `yaml:"targetUrl,omitempty"`
	InputPrice       interface{}            `yaml:"inputPrice,omitempty"`
	OutputPrice      interface{}            `yaml:"outputPrice,omitempty"`
	Type             string                 `yaml:"type,omitempty"`
	ModelType        string                 `yaml:"model,omitempty"`
	Verifiability    string                 `yaml:"verifiability,omitempty"`
	TargetTeeAddress string                 `yaml:"targetTeeAddress,omitempty"`
	TargetSeparated  bool                   `yaml:"targetSeparated"`
	VerifierUrl      string                 `yaml:"verifierUrl,omitempty"`
	AdditionalSecret map[string]interface{} `yaml:"additionalSecret,omitempty"`
	ProviderStake    string                 `yaml:"providerStake,omitempty"`
	OwnedBy          string                 `yaml:"ownedBy,omitempty"`
	ModelInfo        *ModelInfo             `yaml:"modelInfo,omitempty"`
	ProviderType     string                 `yaml:"providerType,omitempty"`
	ProviderIdentity string                 `yaml:"providerIdentity,omitempty"`
}

type NetworkConfig struct {
	URL                 string   `yaml:"url,omitempty"`
	ChainID             int64    `yaml:"chainID,omitempty"`
	PrivateKeys         []string `yaml:"privateKeys,omitempty"`
	TransactionLimit    uint64   `yaml:"transactionLimit,omitempty"`
	GasEstimationBuffer uint64   `yaml:"gasEstimationBuffer,omitempty"`
}

type Networks map[string]*NetworkConfig

type ControllerConfig struct {
	Enable         bool     `yaml:"enable,omitempty"`
	AdminAddresses []string `yaml:"adminAddresses,omitempty"`
	Image          string   `yaml:"image,omitempty"`
}

type Config struct {
	AllowOrigins    []string `yaml:"allowOrigins,omitempty"`
	ContractAddress string   `yaml:"contractAddress,omitempty"`
	Database        struct {
		Provider string `yaml:"provider,omitempty"`
	} `yaml:"database,omitempty"`
	Event struct {
		ProviderAddr string `yaml:"providerAddr,omitempty"`
	} `yaml:"event,omitempty"`
	GasPrice    interface{} `yaml:"gasPrice,omitempty"`
	MaxGasPrice interface{} `yaml:"maxGasPrice,omitempty"`
	Interval    struct {
		AutoSettleBufferTime     int `yaml:"autoSettleBufferTime,omitempty"`
		ForceSettlementProcessor int `yaml:"forceSettlementProcessor,omitempty"`
		SettlementProcessor      int `yaml:"settlementProcessor,omitempty"`
		ReconciliationProcessor  int `yaml:"reconciliationProcessor,omitempty"`
	} `yaml:"interval,omitempty"`
	RevenueTransfer struct {
		TargetAddress string `yaml:"targetAddress,omitempty"`
		ReserveAmount string `yaml:"reserveAmount,omitempty"`
		Interval      int    `yaml:"interval,omitempty"`
	} `yaml:"revenueTransfer,omitempty"`
	Service  Service  `yaml:"service,omitempty"`
	Networks Networks `yaml:"networks,omitempty"`
	Monitor  struct {
		Enable       bool   `yaml:"enable,omitempty"`
		EventAddress string `yaml:"eventAddress,omitempty"`
	} `yaml:"monitor,omitempty"`
	ChatCacheExpiration interface{} `yaml:"chatCacheExpiration,omitempty"`
	NvGPU               bool        `yaml:"nvGPU,omitempty"`
	Logger              struct {
		Format        string `yaml:"format,omitempty"`
		Level         string `yaml:"level,omitempty"`
		Path          string `yaml:"path,omitempty"`
		RotationCount int    `yaml:"rotationCount,omitempty"`
	} `yaml:"logger,omitempty"`
	LogPaths struct {
		BrokerLogDir string `yaml:"brokerLogDir,omitempty"`
		EventLogDir  string `yaml:"eventLogDir,omitempty"`
	} `yaml:"logPaths,omitempty"`
	Controller        ControllerConfig `yaml:"controller,omitempty"`
	CacheTokenBilling struct {
		Enabled bool  `yaml:"enabled,omitempty"`
		Divisor int64 `yaml:"divisor,omitempty"`
	} `yaml:"cacheTokenBilling,omitempty"`
	Whitelist struct {
		Enabled       bool     `yaml:"enabled,omitempty"`
		UserAddresses []string `yaml:"userAddresses,omitempty"`
	} `yaml:"whitelist,omitempty"`
	Async struct {
		Enabled                bool `yaml:"enabled,omitempty"`
		MaxConcurrentJobs      int  `yaml:"maxConcurrentJobs,omitempty"`
		MaxQueueSize           int  `yaml:"maxQueueSize,omitempty"`
		ResultTTLMinutes       int  `yaml:"resultTTLMinutes,omitempty"`
		CleanupIntervalSeconds int  `yaml:"cleanupIntervalSeconds,omitempty"`
		JobTimeoutMinutes      int  `yaml:"jobTimeoutMinutes,omitempty"`
	} `yaml:"async,omitempty"`
	ConcurrencyLimit struct {
		MaxGlobalConcurrent  int `yaml:"maxGlobalConcurrent,omitempty"`
		MaxPerUserConcurrent int `yaml:"maxPerUserConcurrent,omitempty"`
		PerUserRPM           int `yaml:"perUserRPM,omitempty"`
		PerUserBurst         int `yaml:"perUserBurst,omitempty"`
	} `yaml:"concurrencyLimit,omitempty"`
}

// Required fields definition
type RequiredField struct {
	Path        string
	Description string
	Validator   func(string) bool
}

// Port configuration
type PortConfig struct {
	MySQL      string
	Nginx80    string
	Hardhat    string
	Prometheus string
	Grafana    string
}

// TEE Node options
type TeeNode string

const (
	TeeNodeLocalHardhat TeeNode = "hardhat"
	TeeNodePhala        TeeNode = "phala"
	TeeNodeAliCloud     TeeNode = "alicloud"
)

// Deployment configuration
type DeploymentConfig struct {
	UseGPU         bool
	DeployLLM      bool    // Whether to deploy LLM service container
	LLMModel       string  // LLM model to deploy (e.g., "Qwen/Qwen2.5-7B")
	TeeNode        TeeNode // TEE node selection (replaces UseTest)
	UseMonitoring  bool
	UseNginx       bool
	ConfigFile     string // Local config file name
	ConfigPath     string // Source path in TEE node (e.g., /dstack/user_config.yml)
	Ports          PortConfig
	ProjectName    string // Docker Compose project name for isolation
	TappServiceURL string // TAPP service URL for AliCloud mode
	TappAppID      string // TAPP AppID for AliCloud mode
	// Controller configuration
	UseController        bool   // Whether to deploy controller service
	ControllerPort       string // Host port for controller (if exposed)
	ControllerExposePort bool   // Whether to expose controller port
}

// nginxTemplate is no longer needed as nginx config is embedded in docker-compose.yml

// Docker compose template
const dockerComposeTemplate = `services:
{{- if .DeployLLM}}
{{- if and (eq .TeeNode "alicloud") .DeployLLM }}
  vllm:
    image: egs-registry.cn-hangzhou.cr.aliyuncs.com/egs/vllm:0.8.5-pytorch2.6-cu124-20250429
    container_name: vllm
    shm_size: "10.24gb"
    volumes:
      - /data/models:/root/.cache/huggingface
    command: >
      python3 -m vllm.entrypoints.openai.api_server
      --model {{.LLMModel}}
      --served-model-name {{.LLMModel}}
    runtime: nvidia
    environment:
      - NVIDIA_VISIBLE_DEVICES=all
      - HF_ENDPOINT=https://hf-mirror.com
    restart: always
    logging:
      driver: "json-file"
      options:
        max-size: "100m"
        max-file: "5"
{{- else }}
  vllm:
    image: vllm/vllm-openai:v0.6.3.post1
    container_name: vllm
    shm_size: "10.24gb"
    volumes:
      - ~/.cache/huggingface:/root/.cache/huggingface
    command: >
      --model {{.LLMModel}}
      --served-model-name {{.LLMModel}}
    runtime: nvidia
    environment:
      - NVIDIA_VISIBLE_DEVICES=all
    restart: always
    logging:
      driver: "json-file"
      options:
        max-size: "100m"
        max-file: "5"
    networks:
      - default
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8000/health"]
      interval: 30s
      timeout: 10s
      retries: 5
      start_period: 120s
{{- end}}

{{- end}}
{{- if eq .TeeNode "hardhat"}}
  hardhat-node-with-contract:
    image: raven20241/hardhat-compute-network-contract:dev
    ports:
      - "{{.Ports.Hardhat}}:8545"
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "/usr/local/bin/healthcheck.sh"]
      interval: 10s
      retries: 5
    networks:
      - default

{{- end}}
  mysql:
    image: mysql:8.0
    ports:
      - "{{.Ports.MySQL}}:3306"
    restart: unless-stopped
    environment:
      - MYSQL_ROOT_PASSWORD=123456
      - MYSQL_DATABASE=provider
      - MYSQL_USER=provider
      - MYSQL_PASSWORD=provider
    volumes:
      - mysql-data:/var/lib/mysql
    healthcheck:
      test: ["CMD-SHELL", "mysql -uroot -p123456 -e 'USE provider'"]
      interval: 15s
      timeout: 5s
      retries: 15
      start_period: 60s
    networks:
      - default

{{- if .UseNginx}}
  # Nginx load balancer
  nginx:
    image: nginx:1.27.0
    ports:
      - "{{.Ports.Nginx80}}:80"
    restart: unless-stopped
    environment:
      - NGINX_ENVSUBST_OUTPUT_DIR=/etc/nginx
      - BROKER_BACKEND=0g-serving-provider-broker:3080
      - NGINX_ENVSUBST_TEMPLATE_SUFFIX=.template
    command: |
      sh -c '
      mkdir -p /etc/nginx/templates
      cat > /etc/nginx/templates/nginx.conf.template << EOF
      events { }
      
      http {
          server {
              listen 80;
              resolver 127.0.0.11 valid=30s;
              set $$broker_backend ${BROKER_BACKEND};
      
              location /v1/proxy {
                  proxy_pass http://$$broker_backend;
                  proxy_set_header Host $$host;
                  proxy_set_header X-Real-IP $$remote_addr;
              }
      
              location /v1/quote {
                  proxy_pass http://$$broker_backend;
                  proxy_set_header Host $$host;
                  proxy_set_header X-Real-IP $$remote_addr;
              }
      
              location / {
                  allow 127.0.0.1;
                  allow 172.16.0.0/12;
                  deny all;
                  proxy_pass http://$$broker_backend;
                  proxy_set_header Host $$host;
                  proxy_set_header X-Real-IP $$remote_addr;
              }
      
              location /stub_status {
                  stub_status on;
              }
          }
      }
      EOF
      /docker-entrypoint.sh nginx -g "daemon off;"
      '
    networks:
      - default
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:80/stub_status"]
      interval: 10s
      timeout: 5s
      retries: 3
      start_period: 10s

{{- end}}

  # Main broker service
  0g-serving-provider-broker:
    image: ghcr.io/0gfoundation/0g-serving-broker@sha256:02f86cec7e827c16888e667fbcfa889aea7532a188df36ee06bd57375c9a89dd
{{- if not .UseNginx}}
    ports:
      - "{{.Ports.Nginx80}}:3080"
{{- end}}
    environment:
      - PORT=3080
      - CONFIG_FILE=/etc/config.yaml
{{- if eq .TeeNode "hardhat"}}
      - NETWORK=hardhat
{{- else if eq .TeeNode "phala"}}
      - NETWORK=phala
{{- else if eq .TeeNode "alicloud"}}
      - NETWORK=alicloud
      - TAPP_SERVICE_URL={{.TappServiceURL}}
{{- if .TappAppID }}
      - TAPP_APP_ID={{.TappAppID}}
{{- end}}
{{- end}}
    volumes:
      - {{.ConfigPath}}:/etc/config.yaml
{{- if eq .TeeNode "alicloud"}}
      - tee-key-data:/data
{{- end}}
{{- if .EnableFileLog}}
      - ./logs/broker:/var/log/inference
      - ./logs/event:/var/log/event
{{- end}}
{{- if and (ne .TeeNode "hardhat") (ne .TeeNode "alicloud")}}
      - /var/run/dstack.sock:/var/run/dstack.sock
      - /var/run/docker.sock:/var/run/docker.sock
{{- end}}
    command: 0g-inference-server
    networks:
      - default
    healthcheck:
      test: ["CMD-SHELL", "test -L /proc/1/exe && readlink /proc/1/exe | grep -q broker"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 60s
    logging:
      driver: "json-file"
      options:
        max-size: "100m"
        max-file: "5"
{{- if .UseGPU}}
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
{{- end}}
    restart: unless-stopped
    depends_on:
      mysql:
        condition: service_healthy
{{- if .DeployLLM}}
      vllm:
        condition: service_healthy
{{- end}}
{{- if eq .TeeNode "hardhat"}}
      hardhat-node-with-contract:
        condition: service_healthy
{{- end}}
{{- if .UseNginx}}
      nginx:
        condition: service_healthy
{{- end}}

  # Event service starts after broker is ready
  0g-serving-provider-event:
    image: ghcr.io/0gfoundation/0g-serving-broker@sha256:02f86cec7e827c16888e667fbcfa889aea7532a188df36ee06bd57375c9a89dd
    environment:
      - CONFIG_FILE=/etc/config.yaml
{{- if eq .TeeNode "hardhat"}}
      - NETWORK=hardhat
{{- else if eq .TeeNode "phala"}}
      - NETWORK=phala
{{- else if eq .TeeNode "alicloud"}}
      - NETWORK=alicloud
      - TAPP_SERVICE_URL={{.TappServiceURL}}
{{- if .TappAppID }}
      - TAPP_APP_ID={{.TappAppID}}
{{- end}}
{{- end}}
    volumes:
      - {{.ConfigPath}}:/etc/config.yaml
{{- if eq .TeeNode "alicloud"}}
      - tee-key-data:/data
{{- end}}
{{- if .EnableFileLog}}
      - ./logs/event:/var/log/inference
{{- end}}
{{- if and (ne .TeeNode "hardhat") (ne .TeeNode "alicloud")}}
      - /var/run/dstack.sock:/var/run/dstack.sock
{{- end}}
    command: 0g-inference-event
    networks:
      - default
    healthcheck:
      test: ["CMD-SHELL", "grep -q '0g-inference-event' /proc/1/cmdline"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 30s
    logging:
      driver: "json-file"
      options:
        max-size: "100m"
        max-file: "5"
    restart: unless-stopped
    depends_on:
      0g-serving-provider-broker:
        condition: service_healthy
{{- if .UseNginx}}
      nginx:
        condition: service_healthy
{{- end}}

{{- if .UseController}}
  0g-controller:
    image: ghcr.io/0gfoundation/0g-serving-broker@sha256:02f86cec7e827c16888e667fbcfa889aea7532a188df36ee06bd57375c9a89dd
{{- if .ControllerExposePort}}
    ports:
      - "{{.ControllerPort}}:3090"
{{- end}}
    environment:
      - PORT=3090
      - CONFIG_FILE=/etc/config.yaml
    volumes:
      - {{.ConfigPath}}:/etc/config.yaml
      - /var/run/docker.sock:/var/run/docker.sock
    command: 0g-controller
    networks:
      - default
    healthcheck:
      test: ["CMD-SHELL", "grep -q '0g-controller' /proc/1/cmdline"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s
    restart: unless-stopped
    depends_on:
      0g-serving-provider-broker:
        condition: service_healthy

{{- end}}
{{- if .UseMonitoring}}
  # Init container for Prometheus config
  prometheus-init:
    image: alpine:3.18
    environment:
      - PROMETHEUS_CONFIG=${PROMETHEUS_CONFIG:-}
    volumes:
      - prometheus-config:/tmp
    command: |
      sh -c 'if [ -n "$$PROMETHEUS_CONFIG" ]; then
        echo "$$PROMETHEUS_CONFIG" | base64 -d > /tmp/prometheus.yml
      else
        cat > /tmp/prometheus.yml << "EOF"
      global:
        scrape_interval: 15s
      scrape_configs:
        - job_name: "0g-serving"
          static_configs:
            - targets: ["0g-serving-provider-broker:3080", "0g-serving-provider-event:3081"]
        - job_name: "node-exporter"
          static_configs:
            - targets: ["prometheus-node-exporter:9100"]
      EOF
      fi'

  prometheus:
    image: prom/prometheus:v2.45.2
    restart: unless-stopped
    volumes:
      - prometheus-config:/etc/prometheus
    ports:
      - "{{.Ports.Prometheus}}:9090"
    networks:
      - default
    healthcheck:
      test: ["CMD", "wget", "--no-verbose", "--tries=1", "--spider", "http://localhost:9090/-/healthy"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 30s
    depends_on:
      prometheus-init:
        condition: service_completed_successfully

  grafana:
    image: grafana/grafana-oss:11.4.0
    restart: unless-stopped
    volumes:
      - ./grafana/provisioning:/etc/grafana/provisioning
      - ./grafana/dashboards:/var/lib/grafana/dashboards
    ports:
      - "{{.Ports.Grafana}}:3000"
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
    networks:
      - default
    healthcheck:
      test: ["CMD-SHELL", "curl -f http://localhost:3000/api/health || wget -q --spider http://localhost:3000/api/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 60s
    depends_on:
      prometheus:
        condition: service_healthy

  prometheus-node-exporter:
    image: prom/node-exporter:v1.7.0
    restart: always
    volumes:
      - /proc:/host/proc:ro
      - /sys:/host/sys:ro
      - /:/rootfs:ro
    command:
      - "--path.procfs=/host/proc"
      - "--path.sysfs=/host/sys"
      - --collector.filesystem.ignored-mount-points
      - "^/(sys|proc|dev|host|etc|rootfs/var/lib/docker/containers|rootfs/var/lib/docker/overlay2|rootfs/run/docker/netns|rootfs/var/lib/docker/aufs)($$|/)"
    networks:
      - default
    privileged: true
    depends_on:
      prometheus:
        condition: service_healthy
    expose:
      - 9100

{{- end}}
volumes:
  mysql-data:
{{- if .UseMonitoring}}
  prometheus-config:
{{- end}}
{{- if eq .TeeNode "alicloud"}}
  tee-key-data:
{{- end}}

networks:
  default:
    name: {{if .ProjectName}}{{.ProjectName}}-network{{else}}0g-serving-network{{end}}
    external: false
`

type TemplateData struct {
	UseGPU               bool
	DeployLLM            bool
	LLMModel             string
	TeeNode              TeeNode
	UseMonitoring        bool
	UseNginx             bool
	ConfigFile           string
	ConfigPath           string
	Ports                PortConfig
	ProjectName          string
	EnableFileLog        bool
	TappServiceURL       string
	TappAppID            string
	UseController        bool
	ControllerPort       string
	ControllerExposePort bool
}

var requiredFields = []RequiredField{
	{
		Path:        "service.servingUrl",
		Description: "URL where the serving broker exposes its API (e.g., http://192.168.1.1:8080)",
		Validator:   isValidURL,
	},
	{
		Path:        "service.targetUrl",
		Description: "URL where the LLM service is running (e.g., http://localhost:8000)",
		Validator:   isValidURL,
	},
	{
		Path:        "service.inputPrice",
		Description: "Price per input token in neuron (e.g., 900000000000)",
		Validator:   nil,
	},
	{
		Path:        "service.outputPrice",
		Description: "Price per output token in neuron (e.g., 150000000000)",
		Validator:   nil,
	},
	{
		Path:        "service.model",
		Description: "Model name, which needs to be consistent with the model field name when sending requests to the LLM (e.g., Qwen/Qwen2.5-7B)",
		Validator:   isNotEmpty,
	},
	{
		Path:        "networks.ethereum0g.privateKeys[0]",
		Description: "Private key for blockchain transactions (64 hex characters)",
		Validator:   nil,
	},
}

var placeholderPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^<.*>$`),
	regexp.MustCompile(`^YOUR_.*_HERE$`),
	regexp.MustCompile(`^YOUR_.*$`),
	regexp.MustCompile(`.*YOUR_.*`),
	regexp.MustCompile(`.*<.*>.*`),
}

func main() {
	fmt.Println("🚀 0G Serving Unified Configuration Generator")
	fmt.Println("==========================================")

	// Step 0: Ask for output directory
	outputDir, err := promptOutputDirectory()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error setting output directory: %v\n", err)
		os.Exit(1)
	}

	// Store original directory for accessing config files
	originalDir, _ := os.Getwd()

	// Change to output directory if not current
	if outputDir != "." && outputDir != "" {
		if err := os.Chdir(outputDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error changing to output directory: %v\n", err)
			os.Exit(1)
		}
		defer os.Chdir(originalDir) // Restore original directory when done
		fmt.Printf("✅ Working in directory: %s\n", outputDir)
	}

	reader := bufio.NewReader(os.Stdin)

	// Step 0.1: Ask about network type early
	var networkType string
	fmt.Print("\n🌐 Select network type:\n")
	fmt.Print("   1. Mainnet (production)\n")
	fmt.Print("   2. Testnet (development)\n")
	fmt.Print("Enter your choice [1-2] (default: 2): ")
	response, _ := reader.ReadString('\n')
	choice := strings.TrimSpace(response)

	switch choice {
	case "1":
		networkType = "mainnet"
		fmt.Println("   ✓ Mainnet selected")
	default:
		networkType = "testnet"
		fmt.Println("   ✓ Testnet selected")
	}

	// Step 0.3: Ask about TEE node type early
	var teeNodeType TeeNode
	var verifierUrl string
	fmt.Print("\n🔒 Select TEE node type:\n")
	fmt.Print("   1. Local Hardhat (for testing)\n")
	fmt.Print("   2. Phala Network\n")
	fmt.Print("   3. Alibaba Cloud\n")
	fmt.Print("Enter your choice [1-3] (default: 1): ")
	teeResponse, _ := reader.ReadString('\n')
	teeChoice := strings.TrimSpace(teeResponse)

	switch teeChoice {
	case "2":
		teeNodeType = TeeNodePhala
		verifierUrl = "https://github.com/Dstack-TEE/dstack/releases/tag/verifier-v0.5.4"
		fmt.Println("   ✓ Phala Network selected")
		fmt.Printf("   ✓ VerifierUrl set to: %s\n", verifierUrl)
	case "3":
		teeNodeType = TeeNodeAliCloud
		verifierUrl = ""
		fmt.Println("   ✓ Alibaba Cloud selected")
	default:
		teeNodeType = TeeNodeLocalHardhat
		verifierUrl = ""
		fmt.Println("   ✓ Local Hardhat selected (test environment)")
	}

	// Step 0.5: Ask about LLM deployment
	var targetTeeAddress string
	var targetSeparated bool
	var additionalHeaders map[string]string
	var providerType string     // "decentralized" (default) or "centralized"
	var providerIdentity string // e.g., "openai", "anthropic"

	fmt.Print("\n🤖 Do you want to deploy an LLM service container in the same environment? [y/N]: ")
	response, _ = reader.ReadString('\n')
	deployLLM := strings.ToLower(strings.TrimSpace(response)) == "y"

	var llmModel string
	if deployLLM {
		fmt.Println("   ✓ LLM service container will be deployed")
		fmt.Println("   ℹ️  The targetUrl will be automatically configured as http://vllm:8000/v1")

		// Set fields for same environment deployment
		targetTeeAddress = ""
		targetSeparated = false
		// additionalSecret will not be set

		// Model name is required for LLM deployment
		for {
			fmt.Print("📝 Enter the model name to deploy (e.g., Qwen/Qwen2.5-7B): ")
			modelInput, _ := reader.ReadString('\n')
			llmModel = strings.TrimSpace(modelInput)
			if llmModel == "" {
				fmt.Println("   ❌ Model name is required for LLM deployment!")
				continue
			}
			break
		}
		fmt.Printf("   ✓ Model set to: %s\n", llmModel)
	} else {
		// Ask if this is a centralized API provider
		fmt.Print("\n🌐 Are you connecting to a centralized API provider (OpenAI, Anthropic)? [y/N]: ")
		response, _ = reader.ReadString('\n')
		isCentralized := strings.ToLower(strings.TrimSpace(response)) == "y"

		if isCentralized {
			// Centralized provider flow
			fmt.Println("\n   Supported centralized providers:")
			fmt.Println("   1. OpenAI  (api.openai.com)")
			fmt.Println("   2. Anthropic (api.anthropic.com)")
			fmt.Println("   3. Other (custom URL)")

			for {
				fmt.Print("\n   Select provider [1/2/3]: ")
				providerChoice, _ := reader.ReadString('\n')
				providerChoice = strings.TrimSpace(providerChoice)
				switch providerChoice {
				case "1":
					providerIdentity = "openai"
				case "2":
					providerIdentity = "anthropic"
				case "3":
					fmt.Print("   Enter provider identity name: ")
					nameInput, _ := reader.ReadString('\n')
					providerIdentity = strings.TrimSpace(nameInput)
				default:
					fmt.Println("   ❌ Invalid choice, please enter 1, 2, or 3")
					continue
				}
				if providerIdentity != "" {
					break
				}
			}
			fmt.Printf("   ✓ Provider: %s\n", providerIdentity)

			// Set centralized provider config variables (used later in generateYAMLConfig)
			targetSeparated = true

			// Ask for API key
			for {
				fmt.Print("\n🔑 Enter your API key for " + providerIdentity + ": ")
				apiKeyInput, _ := reader.ReadString('\n')
				apiKey := strings.TrimSpace(apiKeyInput)
				if apiKey == "" {
					fmt.Println("   ❌ API key is required!")
					continue
				}
				additionalHeaders = map[string]string{
					"Authorization": "Bearer " + apiKey,
				}
				fmt.Println("   ✓ API key configured")
				break
			}

			// Store centralized provider info for generateYAMLConfig
			providerType = "centralized"
		} else {
			// Decentralized separate LLM flow (existing behavior)
			fmt.Println("\n⚠️  Please note: The separate LLM service should also be deployed in a TEE environment")
			fmt.Printf("   and should use the same TEE node type (%s)\n", teeNodeType)
			fmt.Println("   and use a private key within the TEE to sign the response.")

			// Ask for LLM server's signing public key address
			for {
				fmt.Print("\n🔑 Enter the LLM server's signing public key address: ")
				addressInput, _ := reader.ReadString('\n')
				targetTeeAddress = strings.TrimSpace(addressInput)
				if targetTeeAddress == "" {
					fmt.Println("   ❌ Address is required!")
					continue
				}
				break
			}
			targetSeparated = true
			fmt.Printf("   ✓ Target TEE address set to: %s\n", targetTeeAddress)

			// Ask about additional headers
			fmt.Print("\n🔐 Does the separate LLM server require additional request headers for authentication? [y/N]: ")
			response, _ = reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(response)) == "y" {
				fmt.Println("   Enter headers in format 'Key: Value' (one per line)")
				fmt.Println("   Example: Authorization: sk-xxxx")
				fmt.Println("   Press Enter twice to finish:")

				additionalHeaders = make(map[string]string)
				for {
					fmt.Print("> ")
					headerInput, _ := reader.ReadString('\n')
					headerInput = strings.TrimSpace(headerInput)

					if headerInput == "" {
						break // Empty line means done
					}

					// Parse header
					parts := strings.SplitN(headerInput, ":", 2)
					if len(parts) != 2 {
						fmt.Println("   ❌ Invalid format. Use 'Key: Value'")
						continue
					}

					key := strings.TrimSpace(parts[0])
					value := strings.TrimSpace(parts[1])
					additionalHeaders[key] = value
					fmt.Printf("   ✓ Added header: %s\n", key)
				}

				if len(additionalHeaders) > 0 {
					fmt.Printf("   ✓ %d headers configured\n", len(additionalHeaders))
				}
			} else {
				fmt.Println("   ✓ No additional headers required")
			}
		}
	}

	// Step 1: Ask about monitoring services early (before config generation)
	fmt.Println("\n🌍 Step 1: Environment Configuration")
	monitoringReader := bufio.NewReader(os.Stdin)
	fmt.Print("\n📊 Do you want to add monitoring services (Prometheus/Grafana)? [y/N]: ")
	monitoringResponse, _ := monitoringReader.ReadString('\n')
	useMonitoring := strings.ToLower(strings.TrimSpace(monitoringResponse)) == "y"
	if useMonitoring {
		fmt.Println("   ✓ Monitoring services will be included")
	}

	// Ask about revenue transfer configuration
	var revenueTransferConfig struct {
		TargetAddress string
		ReserveAmount string
		Interval      int
	}
	fmt.Print("\n💰 Do you want to configure automatic revenue transfer to another address? [y/N]: ")
	revenueResponse, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(revenueResponse)) == "y" {
		fmt.Println("   ℹ️  Revenue transfer will periodically transfer earnings to a specified address")
		fmt.Println("   ℹ️  A reserve amount will be kept for gas fees")

		// Ask for target address
		for {
			fmt.Print("\n🏦 Enter the target address to receive revenue transfers: ")
			addressInput, _ := reader.ReadString('\n')
			revenueTransferConfig.TargetAddress = strings.TrimSpace(addressInput)
			if revenueTransferConfig.TargetAddress == "" {
				fmt.Println("   ❌ Target address is required!")
				continue
			}
			if !strings.HasPrefix(revenueTransferConfig.TargetAddress, "0x") {
				fmt.Println("   ❌ Invalid address format. Address should start with 0x")
				continue
			}
			break
		}
		fmt.Printf("   ✓ Target address set to: %s\n", revenueTransferConfig.TargetAddress)

		// Ask for reserve amount
		fmt.Print("\n💎 Enter the reserve amount in neuron to keep for gas (default: 10000000000000000000 = 10 0G): ")
		reserveInput, _ := reader.ReadString('\n')
		reserveInput = strings.TrimSpace(reserveInput)
		if reserveInput == "" {
			revenueTransferConfig.ReserveAmount = "10000000000000000000"
		} else {
			revenueTransferConfig.ReserveAmount = reserveInput
		}
		fmt.Printf("   ✓ Reserve amount set to: %s neuron\n", revenueTransferConfig.ReserveAmount)

		// Ask for transfer interval
		fmt.Print("\n⏱️  Enter the transfer interval in seconds (default: 3600 = 1 hour): ")
		intervalInput, _ := reader.ReadString('\n')
		intervalInput = strings.TrimSpace(intervalInput)
		if intervalInput == "" {
			revenueTransferConfig.Interval = 3600
		} else {
			if interval, err := strconv.Atoi(intervalInput); err == nil {
				revenueTransferConfig.Interval = interval
			} else {
				revenueTransferConfig.Interval = 3600
				fmt.Println("   ⚠️  Invalid interval, using default: 3600 seconds")
			}
		}
		fmt.Printf("   ✓ Transfer interval set to: %d seconds\n", revenueTransferConfig.Interval)
	} else {
		fmt.Println("   ✓ Revenue transfer disabled")
	}

	// Ask about controller configuration
	var controllerConfig struct {
		Enable       bool
		ExposePort   bool
		HostPort     string
		AdminAddress string
	}
	fmt.Print("\n🎮 Do you want to deploy the controller service? [y/N]: ")
	controllerResponse, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(controllerResponse)) == "y" {
		controllerConfig.Enable = true
		fmt.Println("   ✓ Controller service will be deployed")

		// Ask about port exposure
		fmt.Print("\n🔌 Do you want to expose the controller port to the host? [y/N]: ")
		portResponse, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(portResponse)) == "y" {
			controllerConfig.ExposePort = true
			fmt.Print("   Enter the host port for controller [default: 3090]: ")
			portInput, _ := reader.ReadString('\n')
			portInput = strings.TrimSpace(portInput)
			if portInput == "" {
				controllerConfig.HostPort = "3090"
			} else {
				if err := validatePort(portInput); err != nil {
					fmt.Printf("   ⚠️  Invalid port, using default: 3090\n")
					controllerConfig.HostPort = "3090"
				} else {
					controllerConfig.HostPort = portInput
				}
			}
			fmt.Printf("   ✓ Controller will be exposed on port %s\n", controllerConfig.HostPort)
		} else {
			fmt.Println("   ✓ Controller port will not be exposed")
		}

		// Ask for admin address
		for {
			fmt.Print("\n👤 Enter the controller admin address (e.g., 0x...): ")
			addressInput, _ := reader.ReadString('\n')
			controllerConfig.AdminAddress = strings.TrimSpace(addressInput)
			if controllerConfig.AdminAddress == "" {
				fmt.Println("   ❌ Admin address is required for controller!")
				continue
			}
			if !strings.HasPrefix(controllerConfig.AdminAddress, "0x") {
				fmt.Println("   ❌ Invalid address format. Address should start with 0x")
				continue
			}
			break
		}
		fmt.Printf("   ✓ Admin address set to: %s\n", controllerConfig.AdminAddress)
	} else {
		fmt.Println("   ✓ Controller service will not be deployed")
	}

	// Ask about model info configuration
	var modelInfoConfig *ModelInfo
	fmt.Print("\n📋 Do you want to configure model metadata for the /v1/models endpoint? [y/N]: ")
	modelInfoResponse, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(modelInfoResponse)) == "y" {
		modelInfoConfig = &ModelInfo{}

		// Name (required)
		for {
			fmt.Print("\n📝 Enter model display name (e.g., Meta: Llama 3.1 8B Instruct): ")
			input, _ := reader.ReadString('\n')
			modelInfoConfig.Name = strings.TrimSpace(input)
			if modelInfoConfig.Name == "" {
				fmt.Println("   ❌ Model name is required!")
				continue
			}
			break
		}
		fmt.Printf("   ✓ Model name: %s\n", modelInfoConfig.Name)

		// Description (required)
		for {
			fmt.Print("📝 Enter model description: ")
			input, _ := reader.ReadString('\n')
			modelInfoConfig.Description = strings.TrimSpace(input)
			if modelInfoConfig.Description == "" {
				fmt.Println("   ❌ Model description is required!")
				continue
			}
			break
		}
		fmt.Printf("   ✓ Description: %s\n", modelInfoConfig.Description)

		// Context length (required, unless video-generation)
		fmt.Print("📝 Enter max context window size in tokens (e.g., 131072, press Enter to skip for video models): ")
		ctxInput, _ := reader.ReadString('\n')
		ctxInput = strings.TrimSpace(ctxInput)
		if ctxInput != "" {
			if val, err := strconv.Atoi(ctxInput); err == nil && val > 0 {
				modelInfoConfig.ContextLength = val
				fmt.Printf("   ✓ Context length: %d\n", val)
			} else {
				fmt.Println("   ⚠️  Invalid value, skipping context length")
			}
		}

		// Max completion tokens (optional)
		fmt.Print("📝 Enter max output tokens (press Enter to skip): ")
		maxInput, _ := reader.ReadString('\n')
		maxInput = strings.TrimSpace(maxInput)
		if maxInput != "" {
			if val, err := strconv.Atoi(maxInput); err == nil && val > 0 {
				modelInfoConfig.MaxCompletionTokens = val
				fmt.Printf("   ✓ Max completion tokens: %d\n", val)
			} else {
				fmt.Println("   ⚠️  Invalid value, skipping")
			}
		}

		// Architecture (required)
		fmt.Println("\n🏗️  Model Architecture:")
		arch := &ModelArchitecture{}

		for {
			fmt.Print("   Modality (e.g., text->text, text+image->text): ")
			input, _ := reader.ReadString('\n')
			arch.Modality = strings.TrimSpace(input)
			if arch.Modality == "" {
				fmt.Println("   ❌ Modality is required!")
				continue
			}
			break
		}

		for {
			fmt.Print("   Input modalities (comma-separated, e.g., text,image): ")
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(input)
			if input == "" {
				fmt.Println("   ❌ Input modalities are required!")
				continue
			}
			for _, m := range strings.Split(input, ",") {
				m = strings.TrimSpace(m)
				if m != "" {
					arch.InputModalities = append(arch.InputModalities, m)
				}
			}
			break
		}

		for {
			fmt.Print("   Output modalities (comma-separated, e.g., text): ")
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(input)
			if input == "" {
				fmt.Println("   ❌ Output modalities are required!")
				continue
			}
			for _, m := range strings.Split(input, ",") {
				m = strings.TrimSpace(m)
				if m != "" {
					arch.OutputModalities = append(arch.OutputModalities, m)
				}
			}
			break
		}

		fmt.Print("   Instruct type (e.g., chatml, alpaca, press Enter to skip): ")
		input, _ := reader.ReadString('\n')
		arch.InstructType = strings.TrimSpace(input)

		fmt.Print("   Tokenizer (e.g., llama3, cl100k_base, press Enter to skip): ")
		input, _ = reader.ReadString('\n')
		arch.Tokenizer = strings.TrimSpace(input)

		modelInfoConfig.Architecture = arch
		fmt.Printf("   ✓ Architecture: %s\n", arch.Modality)

		// Supported parameters (required)
		for {
			fmt.Print("\n📝 Supported parameters (comma-separated, e.g., temperature,top_p,top_k,max_tokens): ")
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(input)
			if input == "" {
				fmt.Println("   ❌ Supported parameters are required!")
				continue
			}
			for _, p := range strings.Split(input, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					modelInfoConfig.SupportedParameters = append(modelInfoConfig.SupportedParameters, p)
				}
			}
			break
		}
		fmt.Printf("   ✓ Supported parameters: %v\n", modelInfoConfig.SupportedParameters)

		// Supported formats (optional)
		fmt.Print("📝 Supported API formats (comma-separated, e.g., openai,anthropic, press Enter for default [openai]): ")
		input, _ = reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input != "" {
			for _, f := range strings.Split(input, ",") {
				f = strings.TrimSpace(f)
				if f != "" {
					modelInfoConfig.SupportedFormats = append(modelInfoConfig.SupportedFormats, f)
				}
			}
			fmt.Printf("   ✓ Supported formats: %v\n", modelInfoConfig.SupportedFormats)
		}

		// Default parameters (optional)
		fmt.Print("\n📝 Do you want to configure default parameters? [y/N]: ")
		dpResponse, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(dpResponse)) == "y" {
			modelInfoConfig.DefaultParameters = make(map[string]interface{})
			fmt.Println("   Enter parameters in format 'key: value' (one per line)")
			fmt.Println("   Example: temperature: 0.7")
			fmt.Println("   Press Enter to finish:")
			for {
				fmt.Print("   > ")
				dpInput, _ := reader.ReadString('\n')
				dpInput = strings.TrimSpace(dpInput)
				if dpInput == "" {
					break
				}
				parts := strings.SplitN(dpInput, ":", 2)
				if len(parts) != 2 {
					fmt.Println("   ❌ Invalid format. Use 'key: value'")
					continue
				}
				key := strings.TrimSpace(parts[0])
				valueStr := strings.TrimSpace(parts[1])
				// Try to parse as number
				if floatVal, err := strconv.ParseFloat(valueStr, 64); err == nil {
					modelInfoConfig.DefaultParameters[key] = floatVal
				} else {
					modelInfoConfig.DefaultParameters[key] = valueStr
				}
				fmt.Printf("   ✓ %s = %s\n", key, valueStr)
			}
			if len(modelInfoConfig.DefaultParameters) > 0 {
				fmt.Printf("   ✓ %d default parameters configured\n", len(modelInfoConfig.DefaultParameters))
			}
		}

		// TEE type (optional)
		fmt.Print("📝 TEE hardware type (e.g., TDX, press Enter to skip): ")
		input, _ = reader.ReadString('\n')
		modelInfoConfig.TeeType = strings.TrimSpace(input)
		if modelInfoConfig.TeeType != "" {
			fmt.Printf("   ✓ TEE type: %s\n", modelInfoConfig.TeeType)
		}

		// Expiration date (optional)
		fmt.Print("📝 Model expiration date in RFC3339 format (e.g., 2026-12-31T00:00:00Z, press Enter to skip): ")
		input, _ = reader.ReadString('\n')
		modelInfoConfig.ExpirationDate = strings.TrimSpace(input)
		if modelInfoConfig.ExpirationDate != "" {
			fmt.Printf("   ✓ Expiration date: %s\n", modelInfoConfig.ExpirationDate)
		}

		fmt.Println("   ✓ Model info configured")
	} else {
		fmt.Println("   ✓ Model info skipped (can be added later in config file)")
	}

	// Ask about owned by
	fmt.Print("\n🏢 Enter organization name for owned_by in /v1/models (press Enter to skip): ")
	ownedByResponse, _ := reader.ReadString('\n')
	ownedBy := strings.TrimSpace(ownedByResponse)
	if ownedBy != "" {
		fmt.Printf("   ✓ Owned by: %s\n", ownedBy)
	}

	// Step 2: Load and configure YAML config (with monitoring setting)
	fmt.Println("\n📋 Step 2: Configuration File Setup")
	configFile, configPath, _, err := generateYAMLConfig(originalDir, deployLLM, targetTeeAddress, targetSeparated, verifierUrl, additionalHeaders, useMonitoring, networkType, revenueTransferConfig.TargetAddress, revenueTransferConfig.ReserveAmount, revenueTransferConfig.Interval, controllerConfig.Enable, controllerConfig.AdminAddress, modelInfoConfig, ownedBy, providerType, providerIdentity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating YAML config: %v\n", err)
		os.Exit(1)
	}

	// Step 3: Complete environment setup
	fmt.Println("\n🌍 Step 3: Additional Environment Configuration")
	deployConfig, err := promptEnvironmentConfig(deployLLM, teeNodeType, useMonitoring)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error configuring environment: %v\n", err)
		os.Exit(1)
	}
	deployConfig.ConfigFile = configFile
	deployConfig.ConfigPath = configPath
	deployConfig.DeployLLM = deployLLM
	deployConfig.LLMModel = llmModel
	deployConfig.TeeNode = teeNodeType
	deployConfig.UseMonitoring = useMonitoring
	deployConfig.UseController = controllerConfig.Enable
	deployConfig.ControllerExposePort = controllerConfig.ExposePort
	deployConfig.ControllerPort = controllerConfig.HostPort

	// Step 4: Generate deployment files
	fmt.Println("\n🔧 Step 4: Generating deployment configuration...")
	if err := generateDeploymentFiles(deployConfig); err != nil {
		fmt.Fprintf(os.Stderr, "Error generating deployment files: %v\n", err)
		os.Exit(1)
	}

	// Step 5: Success summary
	printSuccessSummary(deployConfig)
}

func promptOutputDirectory() (string, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\n📁 Output Directory Configuration")
	fmt.Print("Enter the directory where configuration files will be created [default: current directory]: ")

	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	outputDir := strings.TrimSpace(input)

	// Use current directory if no input
	if outputDir == "" {
		outputDir = "."
		fmt.Println("   ✓ Using current directory")
		return outputDir, nil
	}

	// Check if directory exists
	info, err := os.Stat(outputDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Ask if user wants to create the directory
			fmt.Printf("⚠️  Directory '%s' does not exist.\n", outputDir)
			fmt.Print("Do you want to create it? [Y/n]: ")
			response, _ := reader.ReadString('\n')
			response = strings.ToLower(strings.TrimSpace(response))

			if response == "" || response == "y" || response == "yes" {
				if err := os.MkdirAll(outputDir, 0755); err != nil {
					return "", fmt.Errorf("failed to create directory: %v", err)
				}
				fmt.Printf("   ✓ Created directory: %s\n", outputDir)
			} else {
				return "", fmt.Errorf("directory does not exist and user chose not to create it")
			}
		} else {
			return "", fmt.Errorf("error checking directory: %v", err)
		}
	} else if !info.IsDir() {
		return "", fmt.Errorf("'%s' exists but is not a directory", outputDir)
	}

	// Convert to absolute path for clarity
	absPath, err := filepath.Abs(outputDir)
	if err != nil {
		return outputDir, nil // Return relative path if absolute conversion fails
	}

	fmt.Printf("   ✓ Output directory set to: %s\n", absPath)
	return outputDir, nil
}

func generateYAMLConfig(originalDir string, deployLLM bool, targetTeeAddress string, targetSeparated bool, verifierUrl string, additionalHeaders map[string]string, useMonitoring bool, networkType string, revenueTargetAddress string, revenueReserveAmount string, revenueInterval int, controllerEnable bool, controllerAdminAddress string, modelInfo *ModelInfo, ownedBy string, providerType string, providerIdentity string) (string, string, *Config, error) {
	reader := bufio.NewReader(os.Stdin)

	// Find base config file in original directory
	baseConfigPath := findBaseConfig(originalDir, networkType)
	if baseConfigPath == "" {
		return "", "", nil, fmt.Errorf("base config file (config.%s.yml) not found in %s", networkType, originalDir)
	}

	// Ask for existing config file
	fmt.Print("\n📂 Enter path to your existing configuration file (press Enter to skip): ")
	userConfigPath, _ := reader.ReadString('\n')
	userConfigPath = strings.TrimSpace(userConfigPath)

	// If user config path is provided and not absolute, check in original directory
	if userConfigPath != "" && !filepath.IsAbs(userConfigPath) {
		// First try in original directory
		originalPath := filepath.Join(originalDir, userConfigPath)
		if _, err := os.Stat(originalPath); err == nil {
			userConfigPath = originalPath
		}
		// Otherwise assume it's relative to current directory or doesn't exist
	}

	// Ask for output filename
	fmt.Print("📝 Enter name for the configuration file [default: config.local.yml]: ")
	configName, _ := reader.ReadString('\n')
	configName = strings.TrimSpace(configName)

	if configName == "" {
		configName = "config.local.yml"
	}

	// Use the filename as provided by user (no automatic extension)

	// Ask for source path in TEE node (where the config file exists in TEE node)
	fmt.Print("📁 Enter the source path of configuration file in TEE node [default: ./config.yml]: ")
	fmt.Print("\n   (e.g., ./config.yml, /dstack/user_config): ")
	teeSourcePath, _ := reader.ReadString('\n')
	teeSourcePath = strings.TrimSpace(teeSourcePath)

	if teeSourcePath == "" {
		teeSourcePath = "./config.yml"
	}

	// Use exactly what the user provides, no automatic extension

	outputPath := configName

	// Load and merge configs
	config, err := loadAndMergeConfigs(baseConfigPath, userConfigPath)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to load configs: %v", err)
	}

	// Set the new TEE-related fields
	config.Service.TargetTeeAddress = targetTeeAddress
	config.Service.TargetSeparated = targetSeparated
	config.Service.VerifierUrl = verifierUrl

	// Set centralized provider fields if applicable
	if providerType == "centralized" {
		config.Service.ProviderType = providerType
		config.Service.ProviderIdentity = providerIdentity
	}

	// Set additionalSecret if headers were provided
	if len(additionalHeaders) > 0 {
		if config.Service.AdditionalSecret == nil {
			config.Service.AdditionalSecret = make(map[string]interface{})
		}
		for k, v := range additionalHeaders {
			config.Service.AdditionalSecret[k] = v
		}
	}

	// Check and prompt for required fields
	if err := checkAndPromptRequiredFields(config, deployLLM); err != nil {
		return "", "", nil, fmt.Errorf("error checking required fields: %v", err)
	}

	// Set monitoring configuration if enabled
	if useMonitoring {
		config.Monitor.Enable = true
	}

	// Set revenue transfer configuration if provided
	if revenueTargetAddress != "" {
		config.RevenueTransfer.TargetAddress = revenueTargetAddress
		config.RevenueTransfer.ReserveAmount = revenueReserveAmount
		config.RevenueTransfer.Interval = revenueInterval
	}

	// Add logger configuration for Docker deployment
	// Always set logger configuration for consistency
	config.Logger = struct {
		Format        string `yaml:"format,omitempty"`
		Level         string `yaml:"level,omitempty"`
		Path          string `yaml:"path,omitempty"`
		RotationCount int    `yaml:"rotationCount,omitempty"`
	}{
		Format:        "text",
		Level:         "info",
		Path:          "/var/log/inference/inference.log",
		RotationCount: 7,
	}

	// Add log paths configuration for multiple components
	config.LogPaths = struct {
		BrokerLogDir string `yaml:"brokerLogDir,omitempty"`
		EventLogDir  string `yaml:"eventLogDir,omitempty"`
	}{
		BrokerLogDir: "/var/log/inference",
		EventLogDir:  "/var/log/event",
	}

	// Set model info if provided
	if modelInfo != nil {
		config.Service.ModelInfo = modelInfo
	}

	// Set owned by if provided
	if ownedBy != "" {
		config.Service.OwnedBy = ownedBy
	}

	// Set controller configuration if enabled
	if controllerEnable {
		config.Controller = ControllerConfig{
			Enable:         true,
			AdminAddresses: []string{controllerAdminAddress},
			Image:          "ghcr.io/0gfoundation/0g-serving-broker@sha256:02f86cec7e827c16888e667fbcfa889aea7532a188df36ee06bd57375c9a89dd",
		}
	}

	// Save final configuration
	if err := saveConfig(config, outputPath); err != nil {
		return "", "", nil, fmt.Errorf("error saving config: %v", err)
	}

	fmt.Printf("✅ Configuration saved to: %s\n", outputPath)
	fmt.Printf("   ℹ️  Will be mounted as: %s:/etc/config.yaml in docker-compose\n", teeSourcePath)
	// Return the local file name and the TEE source path
	return filepath.Base(outputPath), teeSourcePath, config, nil
}

func promptEnvironmentConfig(deployLLM bool, teeNodeType TeeNode, useMonitoring bool) (*DeploymentConfig, error) {
	reader := bufio.NewReader(os.Stdin)
	config := &DeploymentConfig{}
	config.TeeNode = teeNodeType         // Set the TEE node type from earlier selection
	config.UseMonitoring = useMonitoring // Set monitoring from earlier selection

	// Ask for Docker Compose project name
	fmt.Print("\n🏷️  Enter a Docker Compose project name for this deployment (leave empty for default): ")
	response, _ := reader.ReadString('\n')
	config.ProjectName = strings.TrimSpace(response)
	if config.ProjectName != "" {
		fmt.Printf("   ✓ Project name set to: %s\n", config.ProjectName)
		fmt.Printf("   ℹ️  Use 'docker compose -p %s up -d' to start services\n", config.ProjectName)
	} else {
		fmt.Println("   ✓ Using default project name (directory name)")
		fmt.Println("   ℹ️  Use 'docker compose up -d' to start services")
	}

	// Ask about GPU environment only if not deploying LLM (LLM requires GPU)
	if deployLLM {
		config.UseGPU = true
		fmt.Println("\n🖥️  GPU support automatically enabled (required for LLM deployment)")
	} else {
		fmt.Print("\n🖥️  Do you have GPU available for inference? [y/N]: ")
		response, _ = reader.ReadString('\n')
		config.UseGPU = strings.ToLower(strings.TrimSpace(response)) == "y"
		if config.UseGPU {
			fmt.Println("   ✓ GPU support will be enabled for containers")
		}
	}

	// If AliCloud was selected earlier, ask for TAPP service URL
	if config.TeeNode == TeeNodeAliCloud {
		// Ask for TAPP service URL for AliCloud (required)
		for {
			fmt.Print("\n🔗 Enter TAPP service URL for AliCloud (e.g., https://172.16.1.100:50051): ")
			response, _ = reader.ReadString('\n')
			tappURL := strings.TrimSpace(response)
			if tappURL == "" {
				fmt.Printf("❌ TAPP service URL is required for AliCloud mode!\n")
				continue
			}
			config.TappServiceURL = tappURL
			fmt.Printf("   ✓ TAPP service URL set to: %s\n", config.TappServiceURL)
			break
		}
		// Ask for TAPP AppID for AliCloud (required)
		for {
			fmt.Print("🆔 Enter TAPP AppID for AliCloud: ")
			appidResp, _ := reader.ReadString('\n')
			appid := strings.TrimSpace(appidResp)
			if appid == "" {
				fmt.Printf("❌ TAPP AppID is required for AliCloud mode!\n")
				continue
			}
			config.TappAppID = appid
			fmt.Printf("   ✓ TAPP AppID set to: %s\n", config.TappAppID)
			break
		}
	}

	// Ask about nginx proxy
	fmt.Print("\n🌐 Do you want to use Nginx as a proxy? [y/N]: ")
	response, _ = reader.ReadString('\n')
	config.UseNginx = strings.ToLower(strings.TrimSpace(response)) == "y"
	if config.UseNginx {
		fmt.Println("   ✓ Nginx proxy will be configured")
	} else {
		fmt.Println("   ✓ Direct broker access will be configured")
	}

	// Configure ports based on selected services
	if err := promptPortConfiguration(config); err != nil {
		return nil, fmt.Errorf("failed to configure ports: %v", err)
	}

	return config, nil
}

func promptPortConfiguration(config *DeploymentConfig) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\n🔌 Port Configuration")
	fmt.Println("Configure the host ports for each service:")

	// MySQL port (always required)
	defaultPort := "33060"
	fmt.Printf("\n📊 MySQL Database")
	fmt.Printf("\n   Enter host port for MySQL [default: %s]: ", defaultPort)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(response)
	if response == "" {
		config.Ports.MySQL = defaultPort
	} else {
		if err := validatePort(response); err != nil {
			return fmt.Errorf("invalid MySQL port: %v", err)
		}
		config.Ports.MySQL = response
	}

	// Configure HTTP port - ask user for port configuration
	fmt.Printf("\n🌐 HTTP Service")
	defaultHTTPPort := "80"
	fmt.Printf("\n   Enter host port for HTTP service [default: %s]: ", defaultHTTPPort)
	response, _ = reader.ReadString('\n')
	response = strings.TrimSpace(response)
	if response == "" {
		config.Ports.Nginx80 = defaultHTTPPort
	} else {
		if err := validatePort(response); err != nil {
			return fmt.Errorf("invalid HTTP port: %v", err)
		}
		config.Ports.Nginx80 = response
	}

	if config.UseNginx {
		fmt.Printf("   ✓ Nginx will proxy requests on port %s\n", config.Ports.Nginx80)
	} else {
		fmt.Printf("   ✓ Direct broker access will use port %s\n", config.Ports.Nginx80)
	}

	// Hardhat port (if hardhat TEE node)
	if config.TeeNode == TeeNodeLocalHardhat {
		fmt.Printf("\n🧪 Hardhat Test Node")
		defaultPort = "8545"
		fmt.Printf("\n   Enter host port for Hardhat node [default: %s]: ", defaultPort)
		response, _ = reader.ReadString('\n')
		response = strings.TrimSpace(response)
		if response == "" {
			config.Ports.Hardhat = defaultPort
		} else {
			if err := validatePort(response); err != nil {
				return fmt.Errorf("invalid Hardhat port: %v", err)
			}
			config.Ports.Hardhat = response
		}
	}

	// Monitoring ports (if monitoring enabled)
	if config.UseMonitoring {
		fmt.Printf("\n📈 Monitoring Services")

		// Prometheus
		defaultPort = "9090"
		fmt.Printf("\n   Enter host port for Prometheus [default: %s]: ", defaultPort)
		response, _ = reader.ReadString('\n')
		response = strings.TrimSpace(response)
		if response == "" {
			config.Ports.Prometheus = defaultPort
		} else {
			if err := validatePort(response); err != nil {
				return fmt.Errorf("invalid Prometheus port: %v", err)
			}
			config.Ports.Prometheus = response
		}

		// Grafana
		defaultPort = "3003"
		fmt.Printf("   Enter host port for Grafana [default: %s]: ", defaultPort)
		response, _ = reader.ReadString('\n')
		response = strings.TrimSpace(response)
		if response == "" {
			config.Ports.Grafana = defaultPort
		} else {
			if err := validatePort(response); err != nil {
				return fmt.Errorf("invalid Grafana port: %v", err)
			}
			config.Ports.Grafana = response
		}

	}

	// Summary
	fmt.Printf("\n✅ Port configuration completed:\n")
	fmt.Printf("   MySQL: %s\n", config.Ports.MySQL)
	if config.UseNginx {
		fmt.Printf("   HTTP (Nginx Proxy): %s\n", config.Ports.Nginx80)
	} else {
		fmt.Printf("   HTTP (Direct Broker): %s\n", config.Ports.Nginx80)
	}
	if config.TeeNode == TeeNodeLocalHardhat {
		fmt.Printf("   Hardhat: %s\n", config.Ports.Hardhat)
	}
	if config.UseMonitoring {
		fmt.Printf("   Prometheus: %s\n", config.Ports.Prometheus)
		fmt.Printf("   Grafana: %s\n", config.Ports.Grafana)
	}

	return nil
}

func validatePort(port string) error {
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("port must be a number")
	}
	if portNum < 1 || portNum > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

func generateDeploymentFiles(config *DeploymentConfig) error {
	templateData := TemplateData{
		UseGPU:               config.UseGPU,
		DeployLLM:            config.DeployLLM,
		LLMModel:             config.LLMModel,
		TeeNode:              config.TeeNode,
		UseMonitoring:        config.UseMonitoring,
		UseNginx:             config.UseNginx,
		ConfigFile:           config.ConfigFile,
		ConfigPath:           config.ConfigPath,
		Ports:                config.Ports,
		ProjectName:          config.ProjectName,
		EnableFileLog:        true, // Always enable file logging
		TappServiceURL:       config.TappServiceURL,
		TappAppID:            config.TappAppID,
		UseController:        config.UseController,
		ControllerPort:       config.ControllerPort,
		ControllerExposePort: config.ControllerExposePort,
	}

	// Generate docker-compose.yml only
	if err := generateDockerCompose(templateData); err != nil {
		return fmt.Errorf("failed to generate docker compose: %v", err)
	}

	// Logs directories will be created automatically by Docker Compose

	// No additional files needed - all configurations are embedded

	// Generate .env file if project name is specified
	if config.ProjectName != "" {
		if err := generateEnvFile(config.ProjectName); err != nil {
			return fmt.Errorf("failed to generate .env file: %v", err)
		}
	}

	return nil
}

// generateNginxConfig is no longer needed as nginx config is now embedded in docker-compose.yml
func generateDockerCompose(data TemplateData) error {
	tmpl, err := template.New("dockercompose").Parse(dockerComposeTemplate)
	if err != nil {
		return err
	}

	file, err := os.Create("docker-compose.yml")
	if err != nil {
		return err
	}
	defer file.Close()

	return tmpl.Execute(file, data)
}

// generatePrometheusConfig is no longer needed as prometheus config is now handled via environment variables

// generateInitSQL is no longer needed as database initialization is handled by MySQL environment variables and broker migration

// generateEnvExample is no longer needed as configurations are embedded in docker-compose.yml

func generateEnvFile(projectName string) error {
	envContent := fmt.Sprintf("# Docker Compose project name for resource isolation\nCOMPOSE_PROJECT_NAME=%s\n", projectName)

	file, err := os.Create(".env")
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(envContent)
	return err
}

func printSuccessSummary(config *DeploymentConfig) {
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("🎉 Configuration Complete!")
	fmt.Println(strings.Repeat("=", 50))

	fmt.Printf("\n📊 Configuration Summary:\n")
	if config.ProjectName != "" {
		fmt.Printf("  • Project Name: %s\n", config.ProjectName)
	}
	fmt.Printf("  • GPU Support: %t\n", config.UseGPU)
	if config.DeployLLM {
		fmt.Printf("  • LLM Deployment: Yes (Model: %s)\n", config.LLMModel)
	} else {
		fmt.Printf("  • LLM Deployment: No (using external service)\n")
	}
	fmt.Printf("  • TEE Node: %s\n", config.TeeNode)
	fmt.Printf("  • Nginx Proxy: %t\n", config.UseNginx)
	fmt.Printf("  • Monitoring: %t\n", config.UseMonitoring)
	fmt.Printf("  • Controller: %t\n", config.UseController)
	fmt.Printf("  • Config File: %s\n", config.ConfigFile)

	fmt.Printf("\n📁 Generated Files:\n")
	fmt.Printf("  • docker-compose.yml (with embedded configurations)\n")
	fmt.Printf("  • %s (main configuration file)\n", config.ConfigFile)
	if config.ProjectName != "" {
		fmt.Printf("  • .env (with project name)\n")
	}

	fmt.Printf("\n🔧 Deployment Benefits:\n")
	fmt.Printf("  • Config mount: %s → /etc/config.yaml (in container)\n", config.ConfigPath)
	fmt.Printf("  • Environment variables: Static configs via env vars\n")
	fmt.Printf("  • Auto database init: No manual SQL scripts needed\n")
	if config.UseNginx {
		fmt.Printf("  • Embedded nginx config: No separate nginx.conf file\n")
	} else {
		fmt.Printf("  • Direct broker access: No nginx proxy needed\n")
	}

	fmt.Printf("\n🚀 To start the services, run:\n")
	if config.ProjectName != "" {
		fmt.Printf("  docker compose up -d  # Uses .env file automatically\n")
		fmt.Printf("  # Alternative: docker compose -p %s up -d\n", config.ProjectName)
	} else {
		fmt.Printf("  docker compose up -d\n")
	}

	fmt.Printf("\n🌐 After starting, services will be available at:\n")
	fmt.Printf("  • Main API: http://localhost:%s\n", config.Ports.Nginx80)
	fmt.Printf("  • MySQL Database: localhost:%s\n", config.Ports.MySQL)

	if config.DeployLLM {
		fmt.Printf("  • vLLM Service: http://localhost:8000 (internal)\n")
	}

	if config.TeeNode == TeeNodeLocalHardhat {
		fmt.Printf("  • Hardhat Node: http://localhost:%s\n", config.Ports.Hardhat)
	}

	if config.UseController {
		if config.ControllerExposePort {
			fmt.Printf("  • Controller: http://localhost:%s\n", config.ControllerPort)
		} else {
			fmt.Printf("  • Controller: http://0g-controller:3090 (internal)\n")
		}
	}

	if config.UseMonitoring {
		fmt.Printf("  • Prometheus: http://localhost:%s\n", config.Ports.Prometheus)
		fmt.Printf("  • Grafana: http://localhost:%s (admin/admin)\n", config.Ports.Grafana)
	}

	fmt.Printf("\n⚙️ Management Commands:\n")
	fmt.Printf("  • Copy and edit: cp .env.example .env\n")
	fmt.Printf("  • Start services: docker compose up -d\n")
	fmt.Printf("  • View logs: docker compose logs -f\n")
	fmt.Printf("  • Stop services: docker compose down\n")
	fmt.Printf("  • Health check: docker ps\n")

	fmt.Printf("\n💡 Custom Prometheus config:\n")
	fmt.Printf("   cat your-prometheus.yml | base64 -w 0\n")
	fmt.Printf("   export PROMETHEUS_CONFIG=<base64-output>\n")

	if config.DeployLLM {
		fmt.Printf("\n⚠️  Important Notes for LLM Deployment:\n")
		fmt.Printf("  • The vLLM configuration in docker-compose.yml is a sample template\n")
		fmt.Printf("  • Please modify the vLLM service configuration based on your actual requirements:\n")
		fmt.Printf("    - Model: Currently set to %s\n", config.LLMModel)
		fmt.Printf("    - Image version: vllm/vllm-openai:v0.6.3.post1\n")
		fmt.Printf("    - Memory settings, GPU allocation, etc.\n")
		fmt.Printf("  • The targetUrl is automatically mapped to: http://vllm:8000/v1\n")
		fmt.Printf("    (Container name: vllm, Port: 8000)\n")
	}

	fmt.Printf("\n🚀 All services should be healthy in ~60 seconds after starting\n")
}

// Helper functions (reuse from config-merger)
func findBaseConfig(originalDir string, networkType string) string {
	possiblePaths := []string{
		filepath.Join(originalDir, fmt.Sprintf("config.%s.yml", networkType)),
		fmt.Sprintf("config.%s.yml", networkType), // Also check current directory as fallback
		// Fallback to generic config.yml if network-specific not found
		filepath.Join(originalDir, "config.yml"),
		"config.yml",
	}

	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("✅ Using base config: %s\n", path)
			return path
		}
	}
	return ""
}

func loadAndMergeConfigs(baseConfigPath, userConfigPath string) (*Config, error) {
	// Load base configuration
	baseConfig, err := loadConfigFromFile(baseConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load base config: %v", err)
	}

	// If user config is provided, merge it
	if userConfigPath != "" {
		if _, err := os.Stat(userConfigPath); err == nil {
			fmt.Printf("🔄 Merging configuration from: %s\n", userConfigPath)
			userConfig, err := loadConfigFromFile(userConfigPath)
			if err != nil {
				return nil, fmt.Errorf("failed to load user config: %v", err)
			}
			mergeConfigs(baseConfig, userConfig)
			fmt.Printf("✅ Configuration merged successfully.\n")
		} else {
			fmt.Printf("⚠️  File not found: %s (using base configuration only)\n", userConfigPath)
		}
	}

	return baseConfig, nil
}

func loadConfigFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	// Ensure price fields are properly typed as numbers
	normalizePriceFields(&config)

	return &config, nil
}

// normalizePriceFields ensures price fields are stored as int64 instead of string
func normalizePriceFields(config *Config) {
	// Convert InputPrice
	if config.Service.InputPrice != nil {
		switch v := config.Service.InputPrice.(type) {
		case float64:
			config.Service.InputPrice = int64(v)
		case string:
			if num, err := strconv.ParseInt(v, 10, 64); err == nil {
				config.Service.InputPrice = num
			}
		}
	}

	// Convert OutputPrice
	if config.Service.OutputPrice != nil {
		switch v := config.Service.OutputPrice.(type) {
		case float64:
			config.Service.OutputPrice = int64(v)
		case string:
			if num, err := strconv.ParseInt(v, 10, 64); err == nil {
				config.Service.OutputPrice = num
			}
		}
	}
}

func mergeConfigs(base, user *Config) {
	// Merge with user config taking precedence
	if user.AllowOrigins != nil {
		base.AllowOrigins = user.AllowOrigins
	}
	if user.ContractAddress != "" {
		base.ContractAddress = user.ContractAddress
	}
	if user.Database.Provider != "" {
		base.Database.Provider = user.Database.Provider
	}
	if user.Event.ProviderAddr != "" {
		base.Event.ProviderAddr = user.Event.ProviderAddr
	}
	if user.GasPrice != nil {
		base.GasPrice = user.GasPrice
	}
	if user.MaxGasPrice != nil {
		base.MaxGasPrice = user.MaxGasPrice
	}

	// Merge intervals
	if user.Interval.AutoSettleBufferTime != 0 {
		base.Interval.AutoSettleBufferTime = user.Interval.AutoSettleBufferTime
	}
	if user.Interval.ForceSettlementProcessor != 0 {
		base.Interval.ForceSettlementProcessor = user.Interval.ForceSettlementProcessor
	}
	if user.Interval.SettlementProcessor != 0 {
		base.Interval.SettlementProcessor = user.Interval.SettlementProcessor
	}

	// Merge revenue transfer
	if user.RevenueTransfer.TargetAddress != "" {
		base.RevenueTransfer.TargetAddress = user.RevenueTransfer.TargetAddress
	}
	if user.RevenueTransfer.ReserveAmount != "" {
		base.RevenueTransfer.ReserveAmount = user.RevenueTransfer.ReserveAmount
	}
	if user.RevenueTransfer.Interval != 0 {
		base.RevenueTransfer.Interval = user.RevenueTransfer.Interval
	}

	// Merge service
	if user.Service.ServingURL != "" {
		base.Service.ServingURL = user.Service.ServingURL
	}
	if user.Service.TargetURL != "" {
		base.Service.TargetURL = user.Service.TargetURL
	}
	if user.Service.InputPrice != nil {
		// Convert to int64 if it's a float64 or string
		switch v := user.Service.InputPrice.(type) {
		case float64:
			base.Service.InputPrice = int64(v)
		case string:
			if num, err := strconv.ParseInt(v, 10, 64); err == nil {
				base.Service.InputPrice = num
			} else {
				base.Service.InputPrice = v
			}
		default:
			base.Service.InputPrice = user.Service.InputPrice
		}
	}
	if user.Service.OutputPrice != nil {
		// Convert to int64 if it's a float64 or string
		switch v := user.Service.OutputPrice.(type) {
		case float64:
			base.Service.OutputPrice = int64(v)
		case string:
			if num, err := strconv.ParseInt(v, 10, 64); err == nil {
				base.Service.OutputPrice = num
			} else {
				base.Service.OutputPrice = v
			}
		default:
			base.Service.OutputPrice = user.Service.OutputPrice
		}
	}
	if user.Service.Type != "" {
		base.Service.Type = user.Service.Type
	}
	if user.Service.ModelType != "" {
		base.Service.ModelType = user.Service.ModelType
	}
	if user.Service.Verifiability != "" {
		base.Service.Verifiability = user.Service.Verifiability
	}
	if user.Service.TargetTeeAddress != "" {
		base.Service.TargetTeeAddress = user.Service.TargetTeeAddress
	}
	base.Service.TargetSeparated = user.Service.TargetSeparated
	if user.Service.VerifierUrl != "" {
		base.Service.VerifierUrl = user.Service.VerifierUrl
	}
	if user.Service.AdditionalSecret != nil {
		if base.Service.AdditionalSecret == nil {
			base.Service.AdditionalSecret = make(map[string]interface{})
		}
		for k, v := range user.Service.AdditionalSecret {
			base.Service.AdditionalSecret[k] = v
		}
	}
	if user.Service.ProviderType != "" {
		base.Service.ProviderType = user.Service.ProviderType
	}
	if user.Service.ProviderIdentity != "" {
		base.Service.ProviderIdentity = user.Service.ProviderIdentity
	}

	// Merge networks
	if user.Networks != nil {
		if base.Networks == nil {
			base.Networks = make(Networks)
		}
		for name, network := range user.Networks {
			base.Networks[name] = network
		}
	}

	// Merge monitor
	base.Monitor.Enable = user.Monitor.Enable
	if user.Monitor.EventAddress != "" {
		base.Monitor.EventAddress = user.Monitor.EventAddress
	}

	if user.ChatCacheExpiration != nil {
		base.ChatCacheExpiration = user.ChatCacheExpiration
	}
	base.NvGPU = user.NvGPU
}

func checkAndPromptRequiredFields(config *Config, deployLLM bool) error {
	reader := bufio.NewReader(os.Stdin)
	hasChanges := false

	// If deployLLM is true, automatically set targetUrl
	if deployLLM {
		config.Service.TargetURL = "http://vllm:8000/v1"
		hasChanges = true
		fmt.Printf("✅ Target URL automatically set to: http://vllm:8000/v1\n")
		fmt.Printf("   ℹ️  This maps to the vLLM container (name: vllm, port: 8000)\n")
	}

	// Process fields in a specific order to ensure consistency
	orderedFields := []string{
		"service.servingUrl",
		"service.targetUrl",
		"service.inputPrice",
		"service.outputPrice",
		"service.model",
		"networks.ethereum0g.privateKeys[0]",
	}

	fieldMap := make(map[string]RequiredField)
	for _, field := range requiredFields {
		fieldMap[field.Path] = field
	}

	for _, fieldPath := range orderedFields {
		field, exists := fieldMap[fieldPath]
		if !exists {
			continue
		}

		// Skip targetUrl if deployLLM is true
		if deployLLM && fieldPath == "service.targetUrl" {
			continue
		}

		currentValue := getFieldValue(config, field.Path)

		// Check if field needs input
		needsInput := false
		if currentValue == "" {
			needsInput = true
		} else if strings.HasPrefix(currentValue, "<") && strings.HasSuffix(currentValue, ">") {
			needsInput = true
		}

		if needsInput {
			hasChanges = true
			fmt.Printf("\n🔧 %s\n", field.Description)

			var newValue string
			for {
				fmt.Printf("Enter value for %s (required): ", field.Path)
				input, err := reader.ReadString('\n')
				if err != nil {
					return err
				}
				newValue = strings.TrimSpace(input)

				if newValue == "" {
					fmt.Printf("❌ Required field cannot be empty!\n")
					continue
				}

				if field.Validator != nil && !field.Validator(newValue) {
					fmt.Printf("❌ Invalid value format. Please try again.\n")
					continue
				}

				break
			}

			if err := setFieldValue(config, field.Path, newValue); err != nil {
				return fmt.Errorf("failed to set %s: %v", field.Path, err)
			}
		}
	}

	if hasChanges {
		fmt.Printf("\n✅ All required fields have been configured.\n")
	} else {
		fmt.Printf("✅ All required fields are already configured.\n")
	}

	// Clean up non-required placeholder fields
	cleanupPlaceholderFields(config)

	return nil
}

func getFieldValue(config *Config, path string) string {
	switch path {
	case "service.servingUrl":
		return config.Service.ServingURL
	case "service.targetUrl":
		return config.Service.TargetURL
	case "service.inputPrice":
		if str, ok := config.Service.InputPrice.(string); ok {
			return str
		}
		if num, ok := config.Service.InputPrice.(int); ok {
			return fmt.Sprintf("%d", num)
		}
		if num, ok := config.Service.InputPrice.(int64); ok {
			return fmt.Sprintf("%d", num)
		}
		if num, ok := config.Service.InputPrice.(float64); ok {
			return fmt.Sprintf("%g", num)
		}
		return ""
	case "service.outputPrice":
		if str, ok := config.Service.OutputPrice.(string); ok {
			return str
		}
		if num, ok := config.Service.OutputPrice.(int); ok {
			return fmt.Sprintf("%d", num)
		}
		if num, ok := config.Service.OutputPrice.(int64); ok {
			return fmt.Sprintf("%d", num)
		}
		if num, ok := config.Service.OutputPrice.(float64); ok {
			return fmt.Sprintf("%g", num)
		}
		return ""
	case "service.model":
		return config.Service.ModelType
	case "networks.ethereum0g.privateKeys[0]":
		if config.Networks != nil && config.Networks["ethereum0g"] != nil && len(config.Networks["ethereum0g"].PrivateKeys) > 0 {
			return config.Networks["ethereum0g"].PrivateKeys[0]
		}
		return ""
	}
	return ""
}

func setFieldValue(config *Config, path, value string) error {
	switch path {
	case "service.servingUrl":
		config.Service.ServingURL = value
	case "service.targetUrl":
		config.Service.TargetURL = value
	case "service.inputPrice":
		if num, err := strconv.ParseInt(value, 10, 64); err == nil {
			config.Service.InputPrice = num
		} else {
			config.Service.InputPrice = value // Keep as string if not a valid number
		}
	case "service.outputPrice":
		if num, err := strconv.ParseInt(value, 10, 64); err == nil {
			config.Service.OutputPrice = num
		} else {
			config.Service.OutputPrice = value // Keep as string if not a valid number
		}
	case "service.model":
		config.Service.ModelType = value
	case "networks.ethereum0g.privateKeys[0]":
		if config.Networks == nil {
			config.Networks = make(Networks)
		}
		if config.Networks["ethereum0g"] == nil {
			config.Networks["ethereum0g"] = &NetworkConfig{}
		}
		if len(config.Networks["ethereum0g"].PrivateKeys) == 0 {
			config.Networks["ethereum0g"].PrivateKeys = []string{value}
		} else {
			config.Networks["ethereum0g"].PrivateKeys[0] = value
		}
	default:
		return fmt.Errorf("unknown field path: %s", path)
	}
	return nil
}

func isPlaceholder(value string) bool {
	for _, pattern := range placeholderPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func cleanupPlaceholderFields(config *Config) {
	// Remove placeholder values for optional fields
	if isPlaceholderInterface(config.GasPrice) {
		config.GasPrice = nil
	}
	if isPlaceholderInterface(config.MaxGasPrice) {
		config.MaxGasPrice = nil
	}
	if isPlaceholderInterface(config.ChatCacheExpiration) {
		config.ChatCacheExpiration = nil
	}

	// Clean service additional secrets
	if config.Service.AdditionalSecret != nil {
		cleanedSecrets := make(map[string]interface{})
		for k, v := range config.Service.AdditionalSecret {
			if !isPlaceholderInterface(v) {
				cleanedSecrets[k] = v
			}
		}
		if len(cleanedSecrets) == 0 {
			config.Service.AdditionalSecret = nil
		} else {
			config.Service.AdditionalSecret = cleanedSecrets
		}
	}

	// Clean up empty string values that are placeholders
	if config.ContractAddress != "" && isPlaceholder(config.ContractAddress) {
		config.ContractAddress = ""
	}

	// Clean up placeholder private keys in all networks
	if config.Networks != nil {
		for networkName, network := range config.Networks {
			if network.PrivateKeys != nil {
				var cleanedKeys []string
				for _, key := range network.PrivateKeys {
					if !isPlaceholder(key) {
						cleanedKeys = append(cleanedKeys, key)
					}
				}
				// If no valid keys remain, set to nil to omit from YAML
				if len(cleanedKeys) == 0 {
					network.PrivateKeys = nil
				} else {
					network.PrivateKeys = cleanedKeys
				}
			}

			// Remove entire network if it has no meaningful configuration
			if network.PrivateKeys == nil && network.URL == "" {
				delete(config.Networks, networkName)
			}
		}
	}
}

func isPlaceholderInterface(value interface{}) bool {
	if value == nil {
		return false
	}

	if str, ok := value.(string); ok {
		return isPlaceholder(str)
	}

	return false
}

func saveConfig(config *Config, path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := yaml.NewEncoder(file)
	encoder.SetIndent(2)
	defer encoder.Close()

	return encoder.Encode(config)
}

func isValidURL(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func isNotEmpty(value string) bool {
	return strings.TrimSpace(value) != ""
}
