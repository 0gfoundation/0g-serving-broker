# syntax=docker/dockerfile:1

FROM node:20-alpine

RUN npm install -g pnpm

ENV PNPM_HOME="/root/.local/share/pnpm"
ENV PATH="$PNPM_HOME:$PATH"

RUN pnpm install @0glabs/0g-serving-broker -g

# Verify installation
RUN which 0g-compute-cli && 0g-compute-cli --version

WORKDIR /app

# Copy the high-availability router script
COPY router-ha.js /app/router-ha.js
RUN chmod +x /app/router-ha.js

# Environment variables for configuration
ENV PORT=3000
ENV HOST=0.0.0.0

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
    CMD node -e "const http = require('http'); \
    const req = http.request({hostname: '${HOST}', port: ${PORT}, path: '/health', timeout: 5000}, (res) => { \
        process.exit(res.statusCode === 200 ? 0 : 1); \
    }); \
    req.on('error', () => process.exit(1)); \
    req.on('timeout', () => { req.destroy(); process.exit(1); }); \
    req.end();"

# Expose the main port (load balancer) and instance ports
EXPOSE ${PORT}
# Instance ports are PORT+100 to PORT+100+N

# Default entrypoint runs the high-availability manager
ENTRYPOINT ["node", "/app/router-ha.js"]
