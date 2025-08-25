#!/usr/bin/env node

/**
 * Test script for 0G Serving capability
 * This script tests the complete serving flow similar to index-local.ts
 * Focuses on core functionality: account creation, service discovery, and serving capability
 */

const { ethers } = require("ethers");
const { createZGComputeNetworkBroker } = require("@0glabs/0g-serving-broker");
const OpenAI = require("openai");

// Configuration
const DEFAULT_PRIVATE_KEY = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"; // hardhat default key
const HARDHAT_RPC_URL = "http://127.0.0.1:8545";
const PROVIDER_ADDRESS = "0x70997970C51812dc3A010C7d01b50e0d17dc79C8";

// Contract addresses (from index-local.ts)
const SERVING_CONTRACT = "0x8A791620dd6260079BF849Dc5567aDC3F2FdC318";
const SERVING_CONTRACT_2 = "0x0165878A594ca255338adfa4d48449f69242Eb8F";
const SERVING_CONTRACT_3 = "0x9fE46736679d2D9a65F0992F2272dE9f3c7fa6e0";

// Test configuration
const TEST_TIMEOUT = 60000; // 60 seconds

class ServingCapabilityTester {
    constructor() {
        this.results = {
            total: 0,
            passed: 0,
            failed: 0,
            tests: []
        };
    }

    log(level, message) {
        // Redirect to stderr to prevent stdout pollution
        const timestamp = new Date().toISOString();
        const prefix = `[${level}] ${timestamp}`;
        console.error(`${prefix} - ${message}`);
    }

    recordTest(name, success, message = '', details = {}) {
        this.results.total++;
        if (success) {
            this.results.passed++;
            this.log('SUCCESS', `${name}: ${message}`);
        } else {
            this.results.failed++;
            this.log('ERROR', `${name}: ${message}`);
        }
        
        this.results.tests.push({
            name,
            success,
            message,
            details,
            timestamp: new Date().toISOString()
        });
    }

    async setupProvider() {
        try {
            this.log('INFO', 'Setting up provider and wallet...');
            
            // Use environment variable or default private key
            const privateKey = process.env.ZG_PRIVATE_KEY || DEFAULT_PRIVATE_KEY;
            
            this.provider = new ethers.JsonRpcProvider(HARDHAT_RPC_URL);
            this.wallet = new ethers.Wallet(privateKey, this.provider);
            
            // Test connection
            const blockNumber = await this.provider.getBlockNumber();
            this.log('INFO', `Connected to Hardhat node, current block: ${blockNumber}`);
            
            this.recordTest('Provider Setup', true, `Connected to block ${blockNumber}`);
            return true;
        } catch (error) {
            this.recordTest('Provider Setup', false, `Failed: ${error.message}`);
            return false;
        }
    }

    async createBroker() {
        try {
            this.log('INFO', 'Creating ZG Compute Network Broker...');
            
            this.broker = await createZGComputeNetworkBroker(
                this.wallet,
                SERVING_CONTRACT,
                SERVING_CONTRACT_2,
                SERVING_CONTRACT_3
            );
            
            this.recordTest('Broker Creation', true, 'Broker created successfully');
            return true;
        } catch (error) {
            this.recordTest('Broker Creation', false, `Failed: ${error.message}`);
            return false;
        }
    }

    async listServices() {
        try {
            this.log('INFO', 'Listing available services...');
            
            const services = await this.broker.inference.listService();
            this.log('INFO', `Found ${services.length} services`);
            
            if (services.length === 0) {
                this.recordTest('Service Discovery', false, 'No services found');
                return false;
            }
            
            // Log service details (handle BigInt serialization)
            services.forEach((service, index) => {
                try {
                    const serviceStr = JSON.stringify(service, (_, value) =>
                        typeof value === 'bigint' ? value.toString() : value
                    );
                    this.log('INFO', `Service ${index + 1}: ${serviceStr}`);
                } catch (error) {
                    this.log('INFO', `Service ${index + 1}: [Object with non-serializable values]`);
                }
            });
            
            this.recordTest('Service Discovery', true, `Found ${services.length} services`);
            this.services = services;
            return true;
        } catch (error) {
            this.recordTest('Service Discovery', false, `Failed: ${error.message}`);
            return false;
        }
    }

    async testServiceMetadata() {
        try {
            this.log('INFO', 'Testing service metadata retrieval...');
            
            const { endpoint, model } = await this.broker.inference.getServiceMetadata(
                PROVIDER_ADDRESS
            );
            
            this.log('INFO', `Service endpoint: ${endpoint}`);
            this.log('INFO', `Service model: ${model}`);
            
            if (!endpoint || !model) {
                throw new Error('Invalid service metadata received');
            }
            
            this.recordTest('Service Metadata', true, `Endpoint: ${endpoint}, Model: ${model}`, {
                endpoint,
                model
            });
            
            this.serviceEndpoint = endpoint;
            this.serviceModel = model;
            return true;
        } catch (error) {
            this.recordTest('Service Metadata', false, `Failed: ${error.message}`);
            return false;
        }
    }

    async createAccount() {
        try {
            this.log('INFO', 'Creating ledger account...');
            
            const initialBalance = 0.01; // Small initial balance for testing
            await this.broker.ledger.addLedger(initialBalance, 1000000000);
            
            this.recordTest('Account Creation', true, `Account created with balance ${initialBalance}`);
            return true;
        } catch (error) {
            // Account might already exist, which is OK
            if (error.message.includes('account already exists') || 
                error.message.includes('already have an account')) {
                this.log('INFO', 'Account already exists, continuing...');
                this.recordTest('Account Creation', true, 'Account already exists');
                return true;
            }
            
            this.recordTest('Account Creation', false, `Failed: ${error.message}`);
            return false;
        }
    }

    async testProviderAcknowledgment() {
        try {
            this.log('INFO', 'Testing provider signer acknowledgment...');
            
            await this.broker.inference.acknowledgeProviderSigner(PROVIDER_ADDRESS);
            
            this.recordTest('Provider Acknowledgment', true, 'Provider signer acknowledged');
            return true;
        } catch (error) {
            this.recordTest('Provider Acknowledgment', false, `Failed: ${error.message}`);
            return false;
        }
    }

    async testServingCapability() {
        try {
            this.log('INFO', 'Testing complete serving capability...');
            
            const testMessage = "What is the capital of France?";
            const history = [
                {
                    role: "system",
                    content: "You are a helpful assistant that provides accurate information."
                }
            ];
            
            const result = await this.askLLM(PROVIDER_ADDRESS, testMessage, history);
            
            if (!result || !result.output) {
                throw new Error('No response received from service');
            }
            
            this.log('INFO', `Service Response: ${result.output.substring(0, 100)}...`);
            this.log('INFO', `Response verified: ${result.verified}`);
            
            this.recordTest('Serving Capability', true, 'Successfully received and processed response', {
                responseLength: result.output.length,
                verified: result.verified
            });
            
            return true;
        } catch (error) {
            this.recordTest('Serving Capability', false, `Failed: ${error.message}`);
            return false;
        }
    }

    async askLLM(providerAddress, inputParam, history = []) {
        const messages = [...history, { role: "user", content: inputParam }];
        
        // Get request headers
        const headers = await this.broker.inference.getRequestHeaders(
            providerAddress,
            JSON.stringify(messages)
        );
        
        // Create OpenAI client
        const openai = new OpenAI({
            baseURL: this.serviceEndpoint,
            apiKey: "",
        });
        
        // Make the request
        const completion = await openai.chat.completions.create(
            {
                messages: messages,
                model: this.serviceModel,
            },
            {
                headers: {
                    ...headers,
                },
                timeout: TEST_TIMEOUT,
            }
        );
        
        const chatID = completion.id;
        const content = completion.choices?.[0]?.message?.content ?? "";
        
        // Process and verify response
        const verified = await this.broker.inference.processResponse(
            providerAddress,
            content,
            chatID
        );
        
        return {
            output: content,
            verified,
            history: [...messages, { role: "assistant", content }],
        };
    }


    generateReport() {
        const { total, passed, failed } = this.results;
        const successRate = total > 0 ? (passed / total * 100).toFixed(2) : 0;
        
        // Always output JSON format for Bash script parsing
        const report = {
            timestamp: new Date().toISOString(),
            summary: {
                total: total,
                passed: passed,
                failed: failed,
                successRate: parseFloat(successRate)
            },
            allPassed: failed === 0,
            tests: this.results.tests
        };
        console.log(JSON.stringify(report, null, 2));
        
        return failed === 0 ? 0 : 1;
    }

    async runAllTests() {
        this.log('INFO', 'Starting 0G Serving Capability Test Suite');
        this.log('INFO', '==========================================');
        
        // Capture and redirect stdout pollution in JSON mode
        let originalConsoleLog = console.log;
        let originalConsoleWarn = console.warn;
        let originalConsoleError = console.error;
        
        // Always redirect console outputs to stderr to prevent stdout pollution
        console.log = (...args) => console.error(...args);
        console.warn = (...args) => console.error(...args);
        console.error = (...args) => process.stderr.write(args.join(' ') + '\n');
        
        try {
            // Basic setup tests
            if (!await this.setupProvider()) return this.generateReport();
            if (!await this.createBroker()) return this.generateReport();
            
            // Account setup
            if (!await this.createAccount()) return this.generateReport();
            
            // Service discovery tests
            if (!await this.listServices()) return this.generateReport();
            if (!await this.testServiceMetadata()) return this.generateReport();
            if (!await this.testProviderAcknowledgment()) return this.generateReport();
            
            // Capability tests
            if (!await this.testServingCapability()) return this.generateReport();
            
        } catch (error) {
            this.log('ERROR', `Unexpected error: ${error.message}`);
            this.recordTest('Unexpected Error', false, error.message);
        } finally {
            // Restore original console methods
            console.log = originalConsoleLog;
            console.warn = originalConsoleWarn;
            console.error = originalConsoleError;
        }
        
        return this.generateReport();
    }
}

// Main execution
async function main() {
    const tester = new ServingCapabilityTester();
    const exitCode = await tester.runAllTests();
    process.exit(exitCode);
}

// Run if called directly
if (require.main === module) {
    main().catch(error => {
        console.error('Fatal error:', error);
        process.exit(1);
    });
}

module.exports = { ServingCapabilityTester };