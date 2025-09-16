#!/usr/bin/env node

const { spawn } = require('child_process');
const http = require('http');

// Configuration from environment variables
// PROVIDERS format: "address1,priority1;address2,priority2"
const PROVIDERS = process.env.PROVIDERS ? process.env.PROVIDERS.split(';').map(p => {
    const [address, priority] = p.split(',');
    return { address: address.trim(), priority: priority ? parseInt(priority.trim()) : 100 };
}) : [];
const DIRECT_ENDPOINTS = process.env.DIRECT_ENDPOINTS ? process.env.DIRECT_ENDPOINTS.split(';') : []; // Semicolon separated for multiple endpoints
const KEYS = process.env.KEYS ? process.env.KEYS.split(',') : [];
const PORT = process.env.PORT || 3000;
const HOST = process.env.HOST || '0.0.0.0';
const RPC = process.env.RPC_ENDPOINT;
const LEDGER_CA = process.env.LEDGER_CA;
const INFERENCE_CA = process.env.INFERENCE_CA;
const GAS_PRICE = process.env.GAS_PRICE;
const DEFAULT_PROVIDER_PRIORITY = process.env.DEFAULT_PROVIDER_PRIORITY || '100';
const DEFAULT_ENDPOINT_PRIORITY = process.env.DEFAULT_ENDPOINT_PRIORITY || '50';

const HEALTH_CHECK_INTERVAL = 30000; // 30 seconds
const RESTART_DELAY = 5000; // 5 seconds

class RouterManager {
    constructor() {
        this.instances = new Map();
        this.currentActiveInstance = null;
        this.isShuttingDown = false;
        this.healthCheckInterval = null;
    }

    async start() {
        // Check if we have either providers or direct endpoints
        if (!PROVIDERS.length && !DIRECT_ENDPOINTS.length) {
            console.error('ERROR: No providers or direct endpoints specified.');
            console.error('Set PROVIDERS or DIRECT_ENDPOINTS environment variables.');
            process.exit(1);
        }

        // Only require keys if we have on-chain providers
        if (PROVIDERS.length > 0 && !KEYS.length) {
            console.error('ERROR: No keys specified for on-chain providers. Set KEYS environment variable.');
            process.exit(1);
        }

        console.log(`Starting Router High-Availability Manager`);
        if (PROVIDERS.length > 0) {
            console.log(`On-chain Providers: ${PROVIDERS.map(p => `${p.address}(priority:${p.priority})`).join(', ')}`);
            console.log(`Keys: ${KEYS.length} keys configured`);
        }
        if (DIRECT_ENDPOINTS.length > 0) {
            console.log(`Direct Endpoints: ${DIRECT_ENDPOINTS.length} configured`);
        }
        console.log(`Port: ${PORT}, Host: ${HOST}`);

        // Start instances based on configuration
        // Use ports starting from PORT + 100 to avoid conflicts with main port
        if (KEYS.length > 0) {
            // Start instances with different keys for on-chain providers
            for (let i = 0; i < KEYS.length; i++) {
                const key = KEYS[i];
                const instancePort = parseInt(PORT) + 100 + i; // Start from PORT+100
                const instanceId = `router-${i}`;
                
                await this.startInstance(instanceId, key, instancePort);
                await this.sleep(2000); // Stagger startup
            }
        } else {
            // Start a single instance for direct endpoints only
            const instancePort = parseInt(PORT) + 100;
            const instanceId = `router-0`;
            await this.startInstance(instanceId, null, instancePort);
        }

        // Setup health monitoring
        this.startHealthCheck();

        // Setup graceful shutdown
        this.setupSignalHandlers();

        // Start load balancer
        this.startLoadBalancer();
    }

    async startInstance(instanceId, key, port) {
        console.log(`Starting instance ${instanceId} on port ${port}...`);

        // First check if command exists
        const { execSync } = require('child_process');
        try {
            execSync('which 0g-compute-cli', { stdio: 'pipe' });
        } catch (error) {
            console.error(`[${instanceId}] 0g-compute-cli not found in PATH`);
            // Try with npx or full path
        }

        const args = ['router-serve'];
        
        // Add on-chain providers with priorities
        for (const provider of PROVIDERS) {
            args.push('--add-provider', `${provider.address},${provider.priority}`);
        }
        
        // Add direct endpoints
        for (const endpoint of DIRECT_ENDPOINTS) {
            args.push('--add-endpoint', endpoint);
        }
        
        // Add key if provided (for on-chain providers)
        if (key) {
            args.push('--key', key);
        }
        
        args.push('--port', port.toString());
        args.push('--host', HOST);
        args.push('--default-provider-priority', DEFAULT_PROVIDER_PRIORITY);
        args.push('--default-endpoint-priority', DEFAULT_ENDPOINT_PRIORITY);

        if (RPC) args.push('--rpc', RPC);
        if (LEDGER_CA) args.push('--ledger-ca', LEDGER_CA);
        if (INFERENCE_CA) args.push('--inference-ca', INFERENCE_CA);
        if (GAS_PRICE) args.push('--gas-price', GAS_PRICE);

        console.log(`[${instanceId}] Executing: 0g-compute-cli ${args.join(' ')}`);

        const child = spawn('0g-compute-cli', args, {
            stdio: ['pipe', 'pipe', 'pipe'],
            env: { ...process.env }
        });

        const instance = {
            id: instanceId,
            process: child,
            port: port,
            key: key,
            healthy: false,
            lastHealthCheck: null,
            startTime: Date.now(),
            restartCount: 0
        };

        this.instances.set(instanceId, instance);

        child.stdout.on('data', (data) => {
            console.log(`[${instanceId}] ${data.toString().trim()}`);
        });

        child.stderr.on('data', (data) => {
            console.error(`[${instanceId}] ERROR: ${data.toString().trim()}`);
        });

        child.on('close', (code) => {
            console.log(`[${instanceId}] Process exited with code ${code}`);
            instance.healthy = false;
            
            if (!this.isShuttingDown && code !== 0) {
                console.log(`[${instanceId}] Scheduling restart in ${RESTART_DELAY}ms...`);
                setTimeout(() => {
                    if (!this.isShuttingDown) {
                        instance.restartCount++;
                        this.startInstance(instanceId, key, port);
                    }
                }, RESTART_DELAY);
            }
        });

        child.on('error', (error) => {
            console.error(`[${instanceId}] Failed to start: ${error.message}`);
            instance.healthy = false;
        });

        // Wait for instance to be ready
        await this.waitForInstanceReady(instanceId, port);
    }

    async waitForInstanceReady(instanceId, port, maxWait = 60000) {
        const startTime = Date.now();
        
        while (Date.now() - startTime < maxWait) {
            if (await this.checkInstanceHealth(port)) {
                console.log(`[${instanceId}] Instance ready and healthy`);
                this.instances.get(instanceId).healthy = true;
                return true;
            }
            await this.sleep(1000);
        }
        
        console.error(`[${instanceId}] Timeout waiting for instance to become ready`);
        return false;
    }

    async checkInstanceHealth(port) {
        return new Promise((resolve) => {
            const req = http.request({
                hostname: 'localhost',
                port: port,
                path: '/v1/providers/status',
                method: 'GET',
                timeout: 5000
            }, (res) => {
                resolve(res.statusCode === 200);
            });

            req.on('error', () => resolve(false));
            req.on('timeout', () => {
                req.destroy();
                resolve(false);
            });
            
            req.end();
        });
    }

    startHealthCheck() {
        this.healthCheckInterval = setInterval(async () => {
            for (const [instanceId, instance] of this.instances) {
                const wasHealthy = instance.healthy;
                instance.healthy = await this.checkInstanceHealth(instance.port);
                instance.lastHealthCheck = Date.now();

                if (wasHealthy && !instance.healthy) {
                    console.warn(`[${instanceId}] Instance became unhealthy`);
                } else if (!wasHealthy && instance.healthy) {
                    console.log(`[${instanceId}] Instance recovered`);
                }
            }
        }, HEALTH_CHECK_INTERVAL);
    }

    getHealthyInstance() {
        const healthyInstances = Array.from(this.instances.values()).filter(i => i.healthy);
        
        if (healthyInstances.length === 0) {
            return null;
        }

        // Simple round-robin selection
        const currentIndex = healthyInstances.findIndex(i => i.id === this.currentActiveInstance?.id) || 0;
        const nextIndex = (currentIndex + 1) % healthyInstances.length;
        this.currentActiveInstance = healthyInstances[nextIndex];
        
        return this.currentActiveInstance;
    }

    startLoadBalancer() {
        const server = http.createServer(async (req, res) => {
            const instance = this.getHealthyInstance();
            
            if (!instance) {
                res.writeHead(503, { 'Content-Type': 'application/json' });
                res.end(JSON.stringify({ 
                    error: 'No healthy instances available',
                    instances: Array.from(this.instances.values()).map(i => ({
                        id: i.id,
                        healthy: i.healthy,
                        port: i.port,
                        restartCount: i.restartCount
                    }))
                }));
                return;
            }

            // Handle health and status endpoints specially
            if (req.url === '/health') {
                const healthyInstances = Array.from(this.instances.values()).filter(i => i.healthy);
                const isHealthy = healthyInstances.length > 0;
                
                const status = {
                    status: isHealthy ? 'healthy' : 'unhealthy',
                    activeInstances: healthyInstances.length,
                    totalInstances: this.instances.size
                };
                
                const statusCode = isHealthy ? 200 : 503;
                res.writeHead(statusCode, { 'Content-Type': 'application/json' });
                res.end(JSON.stringify(status, null, 2));
                return;
            }
            
            if (req.url === '/status') {
                const healthyInstances = Array.from(this.instances.values()).filter(i => i.healthy);
                const isHealthy = healthyInstances.length > 0;
                
                const status = {
                    status: isHealthy ? 'healthy' : 'unhealthy',
                    activeInstances: healthyInstances.length,
                    totalInstances: this.instances.size,
                    instances: Array.from(this.instances.values()).map(i => ({
                        id: i.id,
                        healthy: i.healthy,
                        port: i.port,
                        uptime: Date.now() - i.startTime,
                        restartCount: i.restartCount,
                        lastHealthCheck: i.lastHealthCheck
                    }))
                };
                res.writeHead(200, { 'Content-Type': 'application/json' });
                res.end(JSON.stringify(status, null, 2));
                return;
            }

            // Proxy request to healthy instance
            const proxyReq = http.request({
                hostname: 'localhost',
                port: instance.port,
                path: req.url,
                method: req.method,
                headers: {
                    ...req.headers,
                    'X-Router-Instance': instance.id
                }
            }, (proxyRes) => {
                res.writeHead(proxyRes.statusCode, {
                    ...proxyRes.headers,
                    'X-Router-Instance': instance.id
                });
                proxyRes.pipe(res);
            });

            proxyReq.on('error', (err) => {
                console.error(`[LoadBalancer] Proxy error to ${instance.id}: ${err.message}`);
                instance.healthy = false; // Mark as unhealthy immediately
                res.writeHead(502, { 'Content-Type': 'application/json' });
                res.end(JSON.stringify({ error: 'Bad Gateway', instance: instance.id }));
            });

            req.pipe(proxyReq);
        });

        server.listen(PORT, HOST, () => {
            console.log(`\n🚀 High-Availability Router running on ${HOST}:${PORT}`);
            console.log(`📊 Health check endpoint: http://${HOST}:${PORT}/health`);
            const instanceCount = KEYS.length > 0 ? KEYS.length : 1;
            console.log(`🔀 Load balancing across ${instanceCount} instances`);
        });
    }

    setupSignalHandlers() {
        const shutdown = async (signal) => {
            console.log(`\n🛑 Received ${signal}. Shutting down gracefully...`);
            this.isShuttingDown = true;

            if (this.healthCheckInterval) {
                clearInterval(this.healthCheckInterval);
            }

            // Terminate all instances
            for (const [instanceId, instance] of this.instances) {
                console.log(`Stopping ${instanceId}...`);
                if (instance.process && !instance.process.killed) {
                    instance.process.kill('SIGTERM');
                }
            }

            // Wait a bit for graceful shutdown
            await this.sleep(3000);

            // Force kill if necessary
            for (const [instanceId, instance] of this.instances) {
                if (instance.process && !instance.process.killed) {
                    console.log(`Force killing ${instanceId}...`);
                    instance.process.kill('SIGKILL');
                }
            }

            console.log('Shutdown complete');
            process.exit(0);
        };

        process.on('SIGTERM', () => shutdown('SIGTERM'));
        process.on('SIGINT', () => shutdown('SIGINT'));
    }

    sleep(ms) {
        return new Promise(resolve => setTimeout(resolve, ms));
    }
}

// Start the high-availability router
const manager = new RouterManager();
manager.start().catch(error => {
    console.error('Failed to start router manager:', error);
    process.exit(1);
});