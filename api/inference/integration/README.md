# 0G Serving Network Provider

## Prerequisites

- Docker Compose: 1.27+

## Make Configuration Script Executable

```bash
chmod +x config
```

## Run the Configuration Script

```bash
./config
```

This script will guide you through the configuration process. It will prompt you for various settings.

## Customizing Prometheus Configuration (Optional)

If you want to customize Prometheus monitoring configuration:

1. Create your custom `prometheus.yml` configuration file
2. Encode it to base64:

   ```bash
   cat your-prometheus.yml | base64 -w 0
   ```

3. Set the environment variable before starting:

   ```bash
   export PROMETHEUS_CONFIG="<your-base64-encoded-config>"
   docker compose up -d
   ```

## Run the Integration

```bash
docker compose up -d
```
