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
type Service struct {
	ServingURL       string                 `yaml:"servingUrl,omitempty"`
	TargetURL        string                 `yaml:"targetUrl,omitempty"`
	InputPrice       interface{}            `yaml:"inputPrice,omitempty"`
	OutputPrice      interface{}            `yaml:"outputPrice,omitempty"`
	Type             string                 `yaml:"type,omitempty"`
	ModelType        string                 `yaml:"model,omitempty"`
	Verifiability    string                 `yaml:"verifiability,omitempty"`
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
	AllowOrigins    []string    `yaml:"allowOrigins,omitempty"`
	ContractAddress string      `yaml:"contractAddress,omitempty"`
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
	ZK struct {
		Provider      string `yaml:"provider,omitempty"`
		RequestLength int    `yaml:"requestLength,omitempty"`
	} `yaml:"zk,omitempty"`
	ChatCacheExpiration interface{} `yaml:"chatCacheExpiration,omitempty"`
	NvGPU               bool        `yaml:"nvGPU,omitempty"`
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
	Nginx443   string
	Hardhat    string
	Prometheus string
	Grafana    string
}

// Deployment configuration
type DeploymentConfig struct {
	NumInstances  int
	UseGPU        bool
	UseTest       bool
	UseMonitoring bool
	ConfigFile    string
	Ports         PortConfig
	ProjectName   string  // Docker Compose project name for isolation
}

// Templates for nginx.conf
const nginxTemplate = `events { }

http {
    # Unified ZK service cluster with load balancing
    upstream zk_cluster {
        # Round-robin load balancing between all zk service instances
{{- range .ZKServers}}
        server {{.Name}}:{{.Port}};
{{- end}}
    }

    # ZK service proxy on port 3001 for backward compatibility - container access only
    server {
        listen 3001;
        
        location / {
            # Only allow access from Docker containers (internal network)
            allow 172.16.0.0/12;    # Docker default bridge networks
            allow 10.0.0.0/8;       # Docker custom networks
            allow 192.168.0.0/16;   # Docker custom networks
            deny all;
            
            proxy_pass http://zk_cluster;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            
            # Connection pooling and keep-alive
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            
            # Timeout settings for slow ZK operations
            proxy_connect_timeout 60s;
            proxy_send_timeout 120s;
            proxy_read_timeout 120s;
        }
    }

    server {
        listen 80;
        
        # Use Docker's DNS resolver with valid parameter for caching
        resolver 127.0.0.11 valid=30s;
        
        # Use variables to enable dynamic resolution
        set $broker_backend 0g-serving-provider-broker:3080;

        location /v1/proxy {
            proxy_pass http://$broker_backend;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
        }

        location /v1/quote {
            proxy_pass http://$broker_backend;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
        }

        # Unified ZK service routing with load balancing - container access only
        location /zk/ {
            # Only allow access from Docker containers (internal network)
            allow 172.16.0.0/12;    # Docker default bridge networks
            allow 10.0.0.0/8;       # Docker custom networks
            allow 192.168.0.0/16;   # Docker custom networks
            deny all;
            
            proxy_pass http://zk_cluster/;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            
            # Connection pooling and keep-alive
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            
            # Timeout settings for slow ZK operations
            proxy_connect_timeout 60s;
            proxy_send_timeout 120s;
            proxy_read_timeout 120s;
        }

        location / {
            allow 127.0.0.1;
            allow 172.16.0.0/12;
            deny all;
            proxy_pass http://$broker_backend;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
        }

        location /stub_status {
            stub_status on;
        }
    }
}
`

// Docker compose template
const dockerComposeTemplate = `services:
{{- if .UseTest}}
  hardhat-node-with-contract:
    image: raven20241/hardhat-compute-network-contract:dev
    ports:
      - "{{.Ports.Hardhat}}:8545"
    healthcheck:
      test: ["CMD-SHELL", "/usr/local/bin/healthcheck.sh"]
      interval: 10s
      retries: 5
    networks:
      - localhost

{{- end}}
  mysql:
    image: mysql:8.0
    ports:
      - "{{.Ports.MySQL}}:3306"
    environment:
      MYSQL_ROOT_PASSWORD: 123456
    volumes:
      - mysql-data:/var/lib/mysql
      - ./init.sql:/docker-entrypoint-initdb.d/init.sql
    healthcheck:
      test: ["CMD-SHELL", "mysqladmin ping -h localhost"]
      interval: 10s
      retries: 5
    networks:
      - localhost

  # Nginx only depends on ZK services, not on broker (to avoid circular dependency)
  # It can start and proxy to broker when broker becomes available
  nginx:
    image: nginx:1.27.0
    ports:
      - "{{.Ports.Nginx80}}:80"
      - "{{.Ports.Nginx443}}:443"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf
    networks:
      - localhost
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:80/stub_status"]
      interval: 10s
      timeout: 5s
      retries: 3
      start_period: 10s
    depends_on:
{{- range .ZKServers}}
      {{.Name}}:
        condition: service_healthy
{{- end}}

  # Main broker starts after nginx is ready
  0g-serving-provider-broker:
    image: ghcr.io/0glabs/0g-serving-broker:dev-amd64
    environment:
      - PORT=3080
      - CONFIG_FILE=/etc/config.yaml
{{- if .UseTest}}
      - NETWORK=hardhat
{{- end}}
    volumes:
      - ./{{.ConfigFile}}:/etc/config.yaml
{{- if not .UseTest}}
      - /var/run/tappd.sock:/var/run/tappd.sock
{{- end}}
    command: 0g-inference-server
    networks:
      - localhost
    healthcheck:
      test: ["CMD-SHELL", "test -L /proc/1/exe && readlink /proc/1/exe | grep -q broker"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 60s
{{- if .UseGPU}}
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
{{- end}}
    depends_on:
      mysql:
        condition: service_healthy
{{- if .UseTest}}
      hardhat-node-with-contract:
        condition: service_healthy
{{- end}}
      nginx:
        condition: service_healthy

  # Event service starts after broker is ready
  0g-serving-provider-event:
    image: ghcr.io/0glabs/0g-serving-broker:dev-amd64
    environment:
      - CONFIG_FILE=/etc/config.yaml
{{- if .UseTest}}
      - NETWORK=hardhat
{{- end}}
    volumes:
      - ./{{.ConfigFile}}:/etc/config.yaml
{{- if not .UseTest}}
      - /var/run/tappd.sock:/var/run/tappd.sock
{{- end}}
    command: 0g-inference-event
    networks:
      - localhost
    healthcheck:
      test: ["CMD", "pgrep", "-f", "0g-inference-event"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 30s
    depends_on:
      0g-serving-provider-broker:
        condition: service_healthy
      nginx:
        condition: service_healthy

  # ZK service instances - all identical, load balanced by nginx
{{- range .ZKServers}}
  {{.Name}}:
    image: ghcr.io/0glabs/zk:0.2.1
    environment:
      JS_PROVER_PORT: {{.Port}}
    volumes:
      - type: tmpfs
        target: /app/logs
    healthcheck:
      test:
        ["CMD", "curl", "-f", "-X", "GET", "http://localhost:{{.Port}}/sign-keypair"]
      interval: 30s
      timeout: 10s
      retries: 20
      start_period: 30s
    networks:
      - localhost

{{- end}}
{{- if .UseMonitoring}}
  prometheus:
    image: prom/prometheus:v2.45.2
    volumes:
      - ./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml
    ports:
      - "{{.Ports.Prometheus}}:9090"
    networks:
      - localhost
    healthcheck:
      test: ["CMD", "wget", "--no-verbose", "--tries=1", "--spider", "http://localhost:9090/-/healthy"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 30s

  grafana:
    image: grafana/grafana-oss:11.4.0
    volumes:
      - ./grafana/provisioning:/etc/grafana/provisioning
      - ./grafana/dashboards:/var/lib/grafana/dashboards
    ports:
      - "{{.Ports.Grafana}}:3000"
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
    networks:
      - localhost
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
      - localhost
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

networks:
  localhost:
    name: localhost
    external: false
`

type ZKServer struct {
	Name string
	Port int
}

type TemplateData struct {
	ZKServers     []ZKServer
	UseGPU        bool
	UseTest       bool
	UseMonitoring bool
	ConfigFile    string
	Ports         PortConfig
}

var requiredFields = []RequiredField{
	{
		Path:        "service.servingUrl",
		Description: "URL where the serving broker exposes its API (e.g., http://localhost:8080)",
		Validator:   isValidURL,
	},
	{
		Path:        "service.targetUrl",
		Description: "Target URL for the actual model inference backend (e.g., http://localhost:8000)",
		Validator:   isValidURL,
	},
	{
		Path:        "service.inputPrice",
		Description: "Price per input token in wei (e.g., 1000000000000000)",
		Validator:   nil,
	},
	{
		Path:        "service.outputPrice",
		Description: "Price per output token in wei (e.g., 2000000000000000)",
		Validator:   nil,
	},
	{
		Path:        "service.model",
		Description: "Model type identifier (e.g., llama, gpt, etc.)",
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

	// Step 1: Load and configure YAML config
	fmt.Println("\n📋 Step 1: Configuration File Setup")
	configFile, err := generateYAMLConfig(originalDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating YAML config: %v\n", err)
		os.Exit(1)
	}

	// Step 2: Environment setup
	fmt.Println("\n🌍 Step 2: Environment Configuration")
	deployConfig, err := promptEnvironmentConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error configuring environment: %v\n", err)
		os.Exit(1)
	}
	deployConfig.ConfigFile = configFile

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

func generateYAMLConfig(originalDir string) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	
	// Find base config file in original directory
	baseConfigPath := findBaseConfig(originalDir)
	if baseConfigPath == "" {
		return "", fmt.Errorf("base config file (config.yml) not found in %s", originalDir)
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

	// Ensure .yml extension
	if !strings.HasSuffix(configName, ".yml") && !strings.HasSuffix(configName, ".yaml") {
		configName += ".yml"
	}

	outputPath := configName

	// Load and merge configs
	config, err := loadAndMergeConfigs(baseConfigPath, userConfigPath)
	if err != nil {
		return "", fmt.Errorf("failed to load configs: %v", err)
	}

	// Check and prompt for required fields
	if err := checkAndPromptRequiredFields(config); err != nil {
		return "", fmt.Errorf("error checking required fields: %v", err)
	}

	// Save final configuration
	if err := saveConfig(config, outputPath); err != nil {
		return "", fmt.Errorf("error saving config: %v", err)
	}

	fmt.Printf("✅ Configuration saved to: %s\n", outputPath)
	return filepath.Base(outputPath), nil
}

func promptEnvironmentConfig() (*DeploymentConfig, error) {
	reader := bufio.NewReader(os.Stdin)
	config := &DeploymentConfig{}

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

	// Ask about TEE GPU environment
	fmt.Print("\n🖥️  Are you running in a TEE GPU environment? [y/N]: ")
	response, _ = reader.ReadString('\n')
	config.UseGPU = strings.ToLower(strings.TrimSpace(response)) == "y"
	if config.UseGPU {
		fmt.Println("   ✓ GPU support will be enabled")
	}

	// Ask about test environment
	fmt.Print("\n🧪 Is this a test environment (include hardhat services)? [y/N]: ")
	response, _ = reader.ReadString('\n')
	config.UseTest = strings.ToLower(strings.TrimSpace(response)) == "y"
	if config.UseTest {
		fmt.Println("   ✓ Test environment services will be included")
	}

	// Ask about monitoring services
	fmt.Print("\n📊 Do you want to add monitoring services (Prometheus/Grafana)? [y/N]: ")
	response, _ = reader.ReadString('\n')
	config.UseMonitoring = strings.ToLower(strings.TrimSpace(response)) == "y"
	if config.UseMonitoring {
		fmt.Println("   ✓ Monitoring services will be included")
	}

	// Ask for ZK instances
	fmt.Print("\n🔄 How many ZK service instances do you want to deploy? [default: 3]: ")
	response, _ = reader.ReadString('\n')
	response = strings.TrimSpace(response)
	if response == "" {
		config.NumInstances = 3
	} else {
		num, err := strconv.Atoi(response)
		if err != nil || num < 1 || num > 10 {
			return nil, fmt.Errorf("number of ZK instances must be between 1 and 10")
		}
		config.NumInstances = num
	}

	fmt.Printf("   ✓ Will deploy %d ZK service instances\n", config.NumInstances)

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
	
	// Nginx ports (always required)
	fmt.Printf("\n🌐 Nginx Load Balancer")
	
	// Main HTTP port
	defaultPort = "3080"
	fmt.Printf("\n   Enter host port for HTTP (main API) [default: %s]: ", defaultPort)
	response, _ = reader.ReadString('\n')
	response = strings.TrimSpace(response)
	if response == "" {
		config.Ports.Nginx80 = defaultPort
	} else {
		if err := validatePort(response); err != nil {
			return fmt.Errorf("invalid Nginx HTTP port: %v", err)
		}
		config.Ports.Nginx80 = response
	}
	
	
	// HTTPS port
	defaultPort = "30443"
	fmt.Printf("   Enter host port for HTTPS [default: %s]: ", defaultPort)
	response, _ = reader.ReadString('\n')
	response = strings.TrimSpace(response)
	if response == "" {
		config.Ports.Nginx443 = defaultPort
	} else {
		if err := validatePort(response); err != nil {
			return fmt.Errorf("invalid Nginx HTTPS port: %v", err)
		}
		config.Ports.Nginx443 = response
	}
	
	// Hardhat port (if test environment)
	if config.UseTest {
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
	fmt.Printf("   HTTP (Main API): %s\n", config.Ports.Nginx80)
	fmt.Printf("   HTTPS: %s\n", config.Ports.Nginx443)
	if config.UseTest {
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
	// Generate ZK servers list
	zkServers := make([]ZKServer, config.NumInstances)
	for i := 0; i < config.NumInstances; i++ {
		zkServers[i] = ZKServer{
			Name: fmt.Sprintf("zk-service-%d", i+1),
			Port: 3001 + i,
		}
	}

	templateData := TemplateData{
		ZKServers:     zkServers,
		UseGPU:        config.UseGPU,
		UseTest:       config.UseTest,
		UseMonitoring: config.UseMonitoring,
		ConfigFile:    config.ConfigFile,
		Ports:         config.Ports,
	}

	// Generate nginx.conf
	if err := generateNginxConfig(templateData); err != nil {
		return fmt.Errorf("failed to generate nginx config: %v", err)
	}

	// Generate docker-compose.yml
	if err := generateDockerCompose(templateData); err != nil {
		return fmt.Errorf("failed to generate docker compose: %v", err)
	}

	// Generate prometheus.yml if monitoring is enabled
	if config.UseMonitoring {
		if err := generatePrometheusConfig(); err != nil {
			return fmt.Errorf("failed to generate prometheus config: %v", err)
		}
	}

	// Generate init.sql for MySQL initialization
	if err := generateInitSQL(); err != nil {
		return fmt.Errorf("failed to generate init.sql: %v", err)
	}

	// Generate .env file if project name is specified
	if config.ProjectName != "" {
		if err := generateEnvFile(config.ProjectName); err != nil {
			return fmt.Errorf("failed to generate .env file: %v", err)
		}
	}

	return nil
}

func generateNginxConfig(data TemplateData) error {
	tmpl, err := template.New("nginx").Parse(nginxTemplate)
	if err != nil {
		return err
	}

	file, err := os.Create("nginx.conf")
	if err != nil {
		return err
	}
	defer file.Close()

	return tmpl.Execute(file, data)
}

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

func generatePrometheusConfig() error {
	prometheusConfig := `global:
  scrape_interval: 15s

scrape_configs:
  - job_name: "prometheus-go"
    static_configs:
      - targets:
          ["0g-serving-provider-broker:3080", "0g-serving-provider-event:3081"]
          # node-exporter
      - targets: ["prometheus-node-exporter:9100"]
`

	// Create prometheus directory if it doesn't exist
	if err := os.MkdirAll("prometheus", 0755); err != nil {
		return fmt.Errorf("failed to create prometheus directory: %v", err)
	}

	file, err := os.Create("prometheus/prometheus.yml")
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(prometheusConfig)
	return err
}

func generateInitSQL() error {
	initSQLContent := `CREATE DATABASE IF NOT EXISTS provider CHARACTER SET utf8mb4;

CREATE USER IF NOT EXISTS 'provider'@'%' IDENTIFIED BY 'provider';

GRANT ALL PRIVILEGES ON provider.* TO 'provider'@'%';

FLUSH PRIVILEGES;
`

	file, err := os.Create("init.sql")
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(initSQLContent)
	return err
}

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
	fmt.Printf("  • ZK Instances: %d (container-only access)\n", config.NumInstances)
	fmt.Printf("  • GPU Support: %t\n", config.UseGPU)
	fmt.Printf("  • Test Environment: %t\n", config.UseTest)
	fmt.Printf("  • Monitoring: %t\n", config.UseMonitoring)
	fmt.Printf("  • Config File: %s\n", config.ConfigFile)

	fmt.Printf("\n📁 Generated Files:\n")
	fmt.Printf("  • nginx.conf\n")
	fmt.Printf("  • docker-compose.yml\n")
	fmt.Printf("  • %s\n", config.ConfigFile)
	fmt.Printf("  • init.sql\n")
	if config.ProjectName != "" {
		fmt.Printf("  • .env (with project name)\n")
	}
	if config.UseMonitoring {
		fmt.Printf("  • prometheus/prometheus.yml\n")
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
	fmt.Printf("  • ZK Service: Only accessible from within Docker containers\n")
	fmt.Printf("    - Internal path: http://nginx/zk/ (from containers)\n")
	fmt.Printf("    - Internal port: http://nginx:3001 (from containers)\n")

	if config.UseTest {
		fmt.Printf("  • Hardhat Node: http://localhost:%s\n", config.Ports.Hardhat)
	}

	if config.UseMonitoring {
		fmt.Printf("  • Prometheus: http://localhost:%s\n", config.Ports.Prometheus)
		fmt.Printf("  • Grafana: http://localhost:%s (admin/admin)\n", config.Ports.Grafana)
	}

	fmt.Printf("\n⚙️ Management Commands:\n")
	fmt.Printf("  • View logs: docker compose logs -f\n")
	fmt.Printf("  • Stop services: docker compose down\n")
	fmt.Printf("  • Health check: docker ps\n")

	fmt.Printf("\n💡 All services should be healthy in ~60 seconds after starting\n")
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
	if userConfigPath != "" && userConfigPath != "" {
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

	return &config, nil
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
		base.Service.InputPrice = user.Service.InputPrice
	}
	if user.Service.OutputPrice != nil {
		base.Service.OutputPrice = user.Service.OutputPrice
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

	// Merge ZK
	if user.ZK.Provider != "" {
		base.ZK.Provider = user.ZK.Provider
	}
	if user.ZK.RequestLength != 0 {
		base.ZK.RequestLength = user.ZK.RequestLength
	}

	if user.ChatCacheExpiration != nil {
		base.ChatCacheExpiration = user.ChatCacheExpiration
	}
	base.NvGPU = user.NvGPU
}

func checkAndPromptRequiredFields(config *Config) error {
	reader := bufio.NewReader(os.Stdin)
	hasChanges := false

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
			if currentValue != "" && currentValue != "1" {
				fmt.Printf("   Current value: %s\n", currentValue)
			}
			
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
		config.Service.InputPrice = value
	case "service.outputPrice":
		config.Service.OutputPrice = value
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