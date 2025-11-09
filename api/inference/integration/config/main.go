package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// Configuration structures
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
}

type NetworkConfig struct {
	URL                 string   `yaml:"url,omitempty"`
	ChainID             int64    `yaml:"chainID,omitempty"`
	PrivateKeys         []string `yaml:"privateKeys,omitempty"`
	TransactionLimit    uint64   `yaml:"transactionLimit,omitempty"`
	GasEstimationBuffer uint64   `yaml:"gasEstimationBuffer,omitempty"`
}

type Networks map[string]*NetworkConfig

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
	} `yaml:"interval,omitempty"`
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
	UseGPU          bool
	DeployLLM       bool   // Whether to deploy LLM service container
	LLMModel        string // LLM model to deploy (e.g., "Qwen/Qwen2.5-7B")
	TeeNode         TeeNode // TEE node selection (replaces UseTest)
	UseMonitoring   bool
	UseNginx        bool
	ConfigFile      string
	ConfigPath      string // Full path for mounting in docker-compose
	Ports           PortConfig
	ProjectName     string // Docker Compose project name for isolation
	TappServiceURL string // TAPP service URL for AliCloud mode
}

// nginxTemplate is no longer needed as nginx config is embedded in docker-compose.yml

// Docker compose template
const dockerComposeTemplate = `services:
{{- if .DeployLLM}}
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
    image: ghcr.io/0gfoundation/0g-serving-broker:v1.0.0
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
{{- end}}
    volumes:
      - {{.ConfigPath}}:/etc/config.yaml
{{- if .EnableFileLog}}
      - ./logs/broker:/var/log/inference
{{- end}}
{{- if and (ne .TeeNode "hardhat") (ne .TeeNode "alicloud")}}
      - /var/run/dstack.sock:/var/run/dstack.sock
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
    image: ghcr.io/0gfoundation/0g-serving-broker:v1.0.0
    environment:
      - CONFIG_FILE=/etc/config.yaml
{{- if eq .TeeNode "hardhat"}}
      - NETWORK=hardhat
{{- else if eq .TeeNode "phala"}}
      - NETWORK=phala
{{- else if eq .TeeNode "alicloud"}}
      - NETWORK=alicloud
      - TAPP_SERVICE_URL={{.TappServiceURL}}
{{- end}}
    volumes:
      - {{.ConfigPath}}:/etc/config.yaml
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
      test: ["CMD", "pgrep", "-f", "0g-inference-event"]
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
    healthcheck:
      test: ["CMD-SHELL", "wget -q --spider http://localhost:9100/metrics || curl -f http://localhost:9100/metrics"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 10s
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

networks:
  default:
    name: {{if .ProjectName}}{{.ProjectName}}-network{{else}}0g-serving-network{{end}}
    external: false
`

type TemplateData struct {
	UseGPU          bool
	DeployLLM       bool
	LLMModel        string
	TeeNode         TeeNode
	UseMonitoring   bool
	UseNginx        bool
	ConfigFile      string
	ConfigPath      string
	Ports           PortConfig
	ProjectName     string
	EnableFileLog   bool
	TappServiceURL string
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

	// Step 0.3: Ask about TEE node type early
	var teeNodeType TeeNode
	var verifierUrl string
	fmt.Print("\n🔒 Select TEE node type:\n")
	fmt.Print("   1. Local Hardhat (for testing)\n")
	fmt.Print("   2. Phala Network\n")
	fmt.Print("   3. Alibaba Cloud\n")
	fmt.Print("Enter your choice [1-3] (default: 1): ")
	response, _ := reader.ReadString('\n')
	choice := strings.TrimSpace(response)
	
	switch choice {
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
		// User chose not to deploy LLM in same environment
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

	// Step 1: Load and configure YAML config
	fmt.Println("\n📋 Step 1: Configuration File Setup")
	configFile, configPath, yamlConfig, err := generateYAMLConfig(originalDir, deployLLM, targetTeeAddress, targetSeparated, verifierUrl, additionalHeaders)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating YAML config: %v\n", err)
		os.Exit(1)
	}

	// Step 2: Environment setup
	fmt.Println("\n🌍 Step 2: Environment Configuration")
	deployConfig, err := promptEnvironmentConfig(yamlConfig, deployLLM, teeNodeType)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error configuring environment: %v\n", err)
		os.Exit(1)
	}
	deployConfig.ConfigFile = configFile
	deployConfig.ConfigPath = configPath
	deployConfig.DeployLLM = deployLLM
	deployConfig.LLMModel = llmModel
	deployConfig.TeeNode = teeNodeType

	// Step 3: Generate deployment files
	fmt.Println("\n🔧 Step 3: Generating deployment configuration...")
	if err := generateDeploymentFiles(deployConfig); err != nil {
		fmt.Fprintf(os.Stderr, "Error generating deployment files: %v\n", err)
		os.Exit(1)
	}

	// Step 4: Success summary
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

func generateYAMLConfig(originalDir string, deployLLM bool, targetTeeAddress string, targetSeparated bool, verifierUrl string, additionalHeaders map[string]string) (string, string, *Config, error) {
	reader := bufio.NewReader(os.Stdin)

	// Find base config file in original directory
	baseConfigPath := findBaseConfig(originalDir)
	if baseConfigPath == "" {
		return "", "", nil, fmt.Errorf("base config file (config.yml) not found in %s", originalDir)
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

	// Ask for mount path in docker-compose
	fmt.Print("📁 Enter the mount path for the configuration file in docker-compose [default: ./]: ")
	mountPath, _ := reader.ReadString('\n')
	mountPath = strings.TrimSpace(mountPath)

	if mountPath == "" {
		mountPath = "./"
	}

	// Ensure mountPath ends with /
	if !strings.HasSuffix(mountPath, "/") {
		mountPath += "/"
	}

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

	// Save final configuration
	if err := saveConfig(config, outputPath); err != nil {
		return "", "", nil, fmt.Errorf("error saving config: %v", err)
	}

	fmt.Printf("✅ Configuration saved to: %s\n", outputPath)
	fullMountPath := mountPath + filepath.Base(outputPath)
	return filepath.Base(outputPath), fullMountPath, config, nil
}

func promptEnvironmentConfig(yamlConfig *Config, deployLLM bool, teeNodeType TeeNode) (*DeploymentConfig, error) {
	reader := bufio.NewReader(os.Stdin)
	config := &DeploymentConfig{}
	config.TeeNode = teeNodeType // Set the TEE node type from earlier selection

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

	// Ask about monitoring services
	fmt.Print("\n📊 Do you want to add monitoring services (Prometheus/Grafana)? [y/N]: ")
	response, _ = reader.ReadString('\n')
	config.UseMonitoring = strings.ToLower(strings.TrimSpace(response)) == "y"
	if config.UseMonitoring {
		fmt.Println("   ✓ Monitoring services will be included")
	}

	// Configure ports based on selected services
	if err := promptPortConfiguration(config, yamlConfig); err != nil {
		return nil, fmt.Errorf("failed to configure ports: %v", err)
	}

	return config, nil
}

func promptPortConfiguration(config *DeploymentConfig, yamlConfig *Config) error {
	reader := bufio.NewReader(os.Stdin)

	// Extract the servingUrl port from the passed config
	servingPort := "3080" // fallback default

	if yamlConfig != nil {
		if port := extractPortFromURL(yamlConfig.Service.ServingURL); port != "" {
			servingPort = port
		}
	}

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

	// Configure HTTP port based on whether nginx is used
	if config.UseNginx {
		// Set Nginx HTTP port from service.servingUrl (no user input needed)
		config.Ports.Nginx80 = servingPort
		fmt.Printf("\n🌐 Nginx Proxy\n")
		fmt.Printf("   Nginx will proxy requests on port %s\n", servingPort)
	} else {
		// Use servingUrl port for direct broker access (no user input needed)
		config.Ports.Nginx80 = servingPort
		fmt.Printf("\n🚀 Direct Broker Access\n")
		fmt.Printf("   Direct broker access will use port %s\n", servingPort)
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

func extractPortFromURL(urlStr string) string {
	if urlStr == "" {
		return ""
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return ""
	}

	// Get port from URL
	port := parsedURL.Port()
	if port != "" {
		return port
	}

	// If no explicit port, use default based on scheme
	switch parsedURL.Scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
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
		UseGPU:          config.UseGPU,
		DeployLLM:       config.DeployLLM,
		LLMModel:        config.LLMModel,
		TeeNode:         config.TeeNode,
		UseMonitoring:   config.UseMonitoring,
		UseNginx:        config.UseNginx,
		ConfigFile:      config.ConfigFile,
		ConfigPath:      config.ConfigPath,
		Ports:           config.Ports,
		ProjectName:     config.ProjectName,
		EnableFileLog:   true, // Always enable file logging
		TappServiceURL: config.TappServiceURL,
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
	fmt.Printf("  • Config File: %s\n", config.ConfigFile)

	fmt.Printf("\n📁 Generated Files:\n")
	fmt.Printf("  • docker-compose.yml (with embedded configurations)\n")
	fmt.Printf("  • %s (main configuration file)\n", config.ConfigFile)
	if config.ProjectName != "" {
		fmt.Printf("  • .env (with project name)\n")
	}

	fmt.Printf("\n🔧 Deployment Benefits:\n")
	fmt.Printf("  • Single file mount: Only %s needs to be mounted\n", config.ConfigFile)
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
func findBaseConfig(originalDir string) string {
	possiblePaths := []string{
		filepath.Join(originalDir, "config.yml"),
		"config.yml", // Also check current directory as fallback
	}

	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
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
