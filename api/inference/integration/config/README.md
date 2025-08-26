# 0G Serving Unified Configuration Generator

A comprehensive Go program that combines all the functionality previously split across multiple scripts (`unified-deploy.sh`, `config-merger/main.go`, and `scale-zk-services.sh`) into a single, unified configuration generator.

## Features

This program provides:

1. **Configuration Management**: Interactive configuration file generation with user prompts for required fields
2. **Environment Support**: 
   - TEE GPU environment with GPU device access
   - Test environment with hardhat services
   - Monitoring services (Prometheus/Grafana)
3. **ZK Load Balancing**: Configurable number of ZK service instances (1-10)
4. **Template-based Generation**: Uses Go templates to generate nginx.conf and docker-compose-nginx-lb.yml

## Usage

### Building

```bash
go build -o config-generator
```

### Running

```bash
./config-generator
```

The program will interactively guide you through:

1. **Configuration File Setup**: 
   - Option to merge with existing configuration
   - Interactive prompts for required fields
   - Generates `config.local.yml`

2. **Environment Configuration**:
   - TEE GPU environment support
   - Test environment (hardhat services)
   - Monitoring services
   - Number of ZK instances (1-10)

3. **Port Configuration**:
   - **MySQL Database**: Default 33060
   - **Nginx Load Balancer**: 
     - HTTP (Main API): Default 3080
     - HTTPS: Default 30443
   - **Hardhat Node** (if test env): Default 8545
   - **Prometheus** (if monitoring): Default 9090
   - **Grafana** (if monitoring): Default 3003
   - **ZK Services**: Container-only access (no external ports exposed)
   - Port validation (1-65535) with user-friendly error messages

4. **File Generation**:
   - `nginx.conf` with load balancing configuration
   - `docker-compose-nginx-lb.yml` with customized ports
   - Configuration file with user inputs
   - `init.sql` with MySQL database initialization
   - `prometheus/prometheus.yml` (if monitoring enabled) with scrape configs

### Generated Files

All files are generated in the current directory:

- **config.local.yml**: Your service configuration (generated in current directory)
- **nginx.conf**: Nginx load balancer configuration
- **docker-compose-nginx-lb.yml**: Docker Compose with all services  
- **init.sql**: MySQL database initialization script
- **prometheus/prometheus.yml**: Prometheus monitoring configuration (if monitoring enabled)

### Starting Services

After configuration generation:

```bash
docker compose -f docker-compose-nginx-lb.yml up -d
```

## Services and Ports

- **Main API**: http://localhost:3080
- **MySQL Database**: localhost:33060
- **ZK Services**: Container-only access
  - Internal path: http://nginx/zk/ (from containers)
  - Internal port: http://nginx:3001 (from containers)
- **Hardhat Node** (if enabled): http://localhost:8545
- **Prometheus** (if enabled): http://localhost:9090
- **Grafana** (if enabled): http://localhost:3003

## Management

```bash
# View logs
docker compose -f docker-compose-nginx-lb.yml logs -f

# Stop services
docker compose -f docker-compose-nginx-lb.yml down

# Check health
docker ps
```

## Requirements

- Go 1.21 or later
- Docker and Docker Compose
- Base configuration file (`config.yml`) in parent directory

## Important Notes

### ZK Service Configuration
The ZK provider URL is automatically configured as `nginx-server:3001` (without http:// prefix) to ensure proper URL formation. This avoids common errors like:
```
dial tcp: lookup nginx-server/zk: no such host
dial tcp: lookup http://nginx-server: no such host
```

The application automatically adds the `http://` prefix, forming the correct URL: `http://nginx-server:3001`

See [ZK-Provider-Fix.md](./ZK-Provider-Fix.md) for technical details.

## Migration from Old Scripts

This program replaces:

- `unified-deploy.sh` - Environment configuration and orchestration
- `config-merger/main.go` - Configuration file generation
- `scale-zk-services.sh` - ZK service scaling and docker-compose generation

All functionality is now combined into a single binary for easier deployment and maintenance.