# 0G Serving Broker - Code Review Standards

This document defines code review standards and architectural guidelines for the 0G Serving Broker project. Claude will follow these standards when reviewing code.

## Quick Reference (Must Read)

### Build & Test Commands
- Build: `cd api && go build ./...`
- Unit tests: `cd api && go test ./...`
- Integration tests: `cd api && go test -tags integration ./inference/... -timeout 600s`
- Lint: `cd api && make lint`
- Format: `cd api && go fmt ./...`

### Review Guardrails (Prohibited Actions)
- **Do NOT review or modify** abigen-generated Go bindings under `contract/`
- **Do NOT adjust** code formatting, indentation, or import ordering — for format issues, just say "please run `go fmt`"
- **Do NOT suggest** abstracting fewer than three similar lines of code
- **Do NOT add** defensive nil checks or redundant error wrapping "just in case"
- **Do NOT suggest** variable naming style changes (unless the name is clearly misleading)
- **Do NOT split** GORM chained calls into multiple lines (this is the project's code style)
- **Do NOT suggest** modifications to code in `libs/` or `token-counter/` submodules

### Review Priority (Enforced Order)
1. 🔴 Private key leaks / signature bypass / smart contract interaction safety
2. 🔴 TEE verification gaps or bypasses
3. 🟡 Goroutine leaks / unclosed resources / concurrency races
4. 🟡 Missing state machine transitions (fine-tuning task lifecycle)
5. 🟢 Database N+1 queries / performance regressions
6. ⚪ Other

### Output Requirements
- All issues must be tagged `[CRITICAL]` / `[HIGH]` / `[MEDIUM]` / `[NIT]`
- Tag code highlights with `[PRAISE]`
- Write review reports in English

---

## Project Overview

**0G Serving Broker** is the core infrastructure component of the 0G Compute Network, a decentralized GPU marketplace that connects AI service users with compute providers. The broker acts as a trusted intermediary, handling authentication, request routing, settlement, and verification.

### Key Value Propositions
- **90% Cost Reduction**: Compared to centralized AI services
- **Verifiable Computing**: TEE (Trusted Execution Environment) ensures computation integrity
- **Automated Settlement**: Smart contract-based payment and fee management
- **OpenAI Compatible**: Drop-in replacement for OpenAI API

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                     0G Compute Network                       │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  Users (CLI/SDK) ←→ [Broker] ←→ Providers (GPU Owners)     │
│                        │                                     │
│                        ↓                                     │
│              Smart Contracts (Blockchain)                    │
│           • Account Management                               │
│           • Service Registry                                 │
│           • Settlement & Verification                        │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### Components
1. **Inference Service** (`api/inference/`)
   - Real-time AI inference (LLM, Text-to-Image, Speech-to-Text)
   - Request proxying and authentication
   - Usage tracking and automatic settlement
   - TEE response verification

2. **Fine-tuning Service** (`api/fine-tuning/`)
   - Model fine-tuning with custom datasets
   - Task lifecycle management (Init → Training → Delivered → Finished)
   - Encrypted model delivery
   - 0G Storage integration for datasets and models

3. **Smart Contract Integration** (`contract/`)
   - Account ledger management
   - Service registration and discovery
   - Automated fee settlement
   - TEE attestation verification

## Service-Specific Design Logic

### Inference Service Architecture

#### Request Flow
```
User Request → Broker Authentication → Provider Service → Response
     ↓                                                       ↓
  Generate                                           Verify & Track
  Headers                                            Usage + Settlement
```

**Key Components:**
- **`internal/ctrl/`**: Request controllers for different service types
  - `chatbot.go`: LLM inference handling
  - `text_to_image.go`: Image generation
  - `speech_to_text.go`: Audio transcription
- **`internal/proxy/`**: HTTP proxy layer for provider communication
- **`internal/contract/`**: Smart contract interaction for settlement
- **Settlement Engine**: Automatic fee transfer based on usage

**Critical Design Patterns:**
1. **Authentication Flow**:
   - User generates signing key on blockchain
   - Broker creates request headers with signature
   - Provider verifies signature on-chain
   - Response includes usage data for billing

2. **Usage Tracking**:
   - Chatbot: Token count (input + output)
   - Text-to-Image: Image count and size
   - Speech-to-Text: Audio duration or token count
   - SDK caches usage and auto-transfers funds to prevent interruption

3. **TEE Verification**:
   - Response includes `ZG-Res-Key` header (chatID)
   - Broker verifies signature using provider's TEE public key
   - Ensures computation happened in TEE environment

4. **Fee Management**:
   - User maintains main account balance
   - Per-provider sub-accounts for pre-allocated funds
   - Automatic top-up when sub-account runs low
   - Settlement triggered by `processResponse()`

### Fine-tuning Service Architecture

#### Task Lifecycle
```
Init → SettingUp → SetUp → Training → Trained →
Delivering → Delivered → UserAcknowledged → Finished
                    ↓
                 Failed (any stage)
```

**Key Components:**
- **`internal/ctrl/task.go`**: Task lifecycle management
- **`internal/ctrl/model.go`**: Model encryption/decryption
- **`internal/db/`**: Task state persistence
- **`internal/handler/`**: HTTP API endpoints

**Critical Design Patterns:**
1. **Dataset Handling**:
   - User uploads dataset to 0G Storage
   - Returns root hash as dataset identifier
   - Provider downloads from 0G Storage using root hash
   - Validation: Dataset format must match model requirements

2. **Task State Machine**:
   - **Init**: Task created, contract updated
   - **SettingUp**: Provider prepares TEE environment
   - **SetUp**: Environment ready, waiting to start
   - **Training**: Model training in progress
   - **Trained**: Training completed
   - **Delivering**: Uploading encrypted model to 0G Storage
   - **Delivered**: Model uploaded, root hash on contract
   - **UserAcknowledged**: User confirmed download
   - **Finished**: Provider uploaded decryption key, settled

3. **Security Mechanism**:
   - Model encrypted with symmetric key (AES)
   - Symmetric key encrypted with user's public key (RSA)
   - Provider only uploads decryption key after user confirms download
   - Prevents user from getting model without payment

4. **Provider Queue**:
   - Single provider can only run one task at a time
   - Additional tasks enter waiting queue
   - FIFO processing of queued tasks

## Code Review Focus Areas

### 1. Go Code Standards

#### Must Follow
- Strict adherence to [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` for all code
- All exported functions/types must have godoc comments
- Error wrapping: Use `fmt.Errorf("context: %w", err)`
- Context propagation: Always pass `context.Context` for cancellation

#### Naming Conventions
- Packages: lowercase, singular (e.g., `handler`, `contract`)
- Interfaces: End with `er` (e.g., `Verifier`) or descriptive names
- Variables: camelCase, exported if uppercase first letter
- Constants: camelCase or UPPER_SNAKE_CASE (private use camelCase)

#### Prohibited Patterns
- ❌ `panic` for error handling (only for unrecoverable bugs)
- ❌ Global mutable variables (unless absolutely necessary)
- ❌ Complex `init()` functions (use explicit initialization)
- ❌ Deprecated libraries or APIs

### 2. Blockchain & Smart Contract Security

#### Critical Checks
- [ ] **Input Validation**: All blockchain inputs validated before use
- [ ] **Signature Verification**: All signed messages verified on-chain
- [ ] **Gas Limits**: Reasonable gas limits set for transactions
- [ ] **Private Key Security**: Keys loaded from secure storage, not hardcoded
- [ ] **Transaction Retry**: Exponential backoff for failed transactions
- [ ] **Nonce Management**: Proper nonce handling for concurrent transactions
- [ ] **Block Confirmations**: Wait for sufficient confirmations before acting

#### Smart Contract Interactions
```go
// ✅ GOOD: Proper error handling and gas estimation
tx, err := contract.MethodName(opts, param1, param2)
if err != nil {
    return fmt.Errorf("failed to call contract: %w", err)
}

receipt, err := bind.WaitMined(ctx, client, tx)
if err != nil {
    return fmt.Errorf("transaction failed: %w", err)
}

if receipt.Status != types.ReceiptStatusSuccessful {
    return fmt.Errorf("transaction reverted")
}

// ❌ BAD: Ignoring errors
contract.MethodName(opts, param1, param2)
```

#### Private Key Management
- Never hardcode private keys in source code
- Use environment variables or secure key management systems
- Clear private keys from memory after use
- Use `keystore` for encrypted storage

### 3. TEE Verification Standards

#### Attestation Verification
- [ ] **Public Key Extraction**: Extract signing key from TEE attestation
- [ ] **Signature Validation**: Verify all responses signed by TEE key
- [ ] **Attestation Freshness**: Check attestation timestamp
- [ ] **Hardware Verification**: Validate TEE hardware type (Intel TDX, NVIDIA H100)

```go
// ✅ GOOD: Complete TEE verification
func VerifyTEEResponse(response *Response, attestation *Attestation) error {
    // 1. Verify attestation is recent
    if time.Since(attestation.Timestamp) > 24*time.Hour {
        return errors.New("attestation too old")
    }

    // 2. Extract public key from attestation
    pubKey, err := ExtractPublicKey(attestation)
    if err != nil {
        return fmt.Errorf("failed to extract public key: %w", err)
    }

    // 3. Verify response signature
    if !VerifySignature(response.Data, response.Signature, pubKey) {
        return errors.New("invalid signature")
    }

    return nil
}
```

### 4. Error Handling & Logging

#### Standard Patterns
```go
// ✅ GOOD: Structured logging with context
log.Error("failed to process request",
    "error", err,
    "provider", providerAddr,
    "requestID", reqID,
)

// ✅ GOOD: Error wrapping with context
if err != nil {
    return fmt.Errorf("failed to settle fees for provider %s: %w",
        providerAddr, err)
}

// ❌ BAD: Silent error ignore
_ = someFunction()

// ❌ BAD: panic for business errors
if err != nil {
    panic(err) // Should return error instead
}
```

#### Logging Levels
- **Debug**: Detailed debugging information (disabled in production)
- **Info**: Normal operations (request processing, state changes)
- **Warn**: Non-critical issues (retries, fallback behavior)
- **Error**: Errors requiring attention (failed operations, contract errors)

#### Security in Logs
- ❌ Never log: Private keys, signing keys, user passwords
- ❌ Avoid logging: Full wallet addresses (use truncated form)
- ✅ Log: Transaction hashes, request IDs, error contexts

### 5. Inference Service Specific

#### Request Processing
```go
// Critical flow validation
func (c *Controller) HandleInferenceRequest(ctx context.Context, req *Request) error {
    // 1. Validate user account balance
    balance, err := c.contract.GetUserBalance(ctx, req.UserAddr)
    if err != nil {
        return fmt.Errorf("failed to get balance: %w", err)
    }
    if balance.Cmp(big.NewInt(0)) <= 0 {
        return errors.New("insufficient balance")
    }

    // 2. Generate authenticated headers
    headers, err := c.GenerateRequestHeaders(ctx, req)
    if err != nil {
        return fmt.Errorf("failed to generate headers: %w", err)
    }

    // 3. Forward request to provider
    response, err := c.proxy.ForwardRequest(ctx, req.ProviderURL, headers, req.Body)
    if err != nil {
        return fmt.Errorf("provider request failed: %w", err)
    }

    // 4. Process response (verify + settle)
    if err := c.ProcessResponse(ctx, req.UserAddr, req.ProviderAddr, response); err != nil {
        return fmt.Errorf("failed to process response: %w", err)
    }

    return nil
}
```

#### Usage Tracking Requirements
- [ ] Parse usage from response correctly
- [ ] Handle both streaming and non-streaming responses
- [ ] Cache usage to avoid repeated contract calls
- [ ] Trigger auto-transfer when balance low

#### Response Verification
- [ ] Extract `chatID` from response headers or body
- [ ] Verify signature using provider's TEE public key
- [ ] Handle verification failure gracefully
- [ ] Log verification results for audit

### 6. Fine-tuning Service Specific

#### Task State Transitions
```go
// ✅ GOOD: Validate state transitions
func (s *Service) UpdateTaskStatus(ctx context.Context, taskID string, newStatus Status) error {
    task, err := s.db.GetTask(ctx, taskID)
    if err != nil {
        return fmt.Errorf("failed to get task: %w", err)
    }

    // Validate transition
    if !IsValidTransition(task.Status, newStatus) {
        return fmt.Errorf("invalid transition from %s to %s", task.Status, newStatus)
    }

    // Update on-chain state
    if err := s.contract.UpdateTaskStatus(ctx, taskID, newStatus); err != nil {
        return fmt.Errorf("failed to update contract: %w", err)
    }

    // Update local DB
    if err := s.db.UpdateTaskStatus(ctx, taskID, newStatus); err != nil {
        return fmt.Errorf("failed to update database: %w", err)
    }

    return nil
}

func IsValidTransition(from, to Status) bool {
    validTransitions := map[Status][]Status{
        StatusInit:      {StatusSettingUp, StatusFailed},
        StatusSettingUp: {StatusSetUp, StatusFailed},
        StatusSetUp:     {StatusTraining, StatusFailed},
        StatusTraining:  {StatusTrained, StatusFailed},
        // ... etc
    }

    allowed, ok := validTransitions[from]
    if !ok {
        return false
    }

    for _, s := range allowed {
        if s == to {
            return true
        }
    }
    return false
}
```

#### Dataset & Model Handling
- [ ] **Validate Root Hash**: Ensure hash format is correct (0x prefix, 64 hex chars)
- [ ] **Size Verification**: Dataset size matches calculated token count
- [ ] **0G Storage Integration**: Proper error handling for download failures
- [ ] **Encryption Security**: Use strong encryption (AES-256, RSA-2048+)
- [ ] **Key Management**: Securely handle encryption/decryption keys

#### Provider Queue Management
```go
// ✅ GOOD: Handle provider busy state
func (s *Service) CreateTask(ctx context.Context, req *CreateTaskRequest) error {
    // Check if provider is busy
    isAvailable, err := s.contract.IsProviderAvailable(ctx, req.ProviderAddr)
    if err != nil {
        return fmt.Errorf("failed to check availability: %w", err)
    }

    if !isAvailable {
        // Offer to queue
        if req.QueueIfBusy {
            return s.AddToQueue(ctx, req)
        }
        return errors.New("provider is busy, task rejected")
    }

    // Create task...
}
```

### 7. Performance Requirements

#### Database Queries
- [ ] Use appropriate indexes for frequent queries
- [ ] Avoid N+1 query patterns
- [ ] Use pagination for large result sets
- [ ] Consider query complexity (no nested loops in queries)

#### Concurrency
- [ ] Use goroutines for parallel operations
- [ ] Proper synchronization (mutex, channels)
- [ ] Avoid goroutine leaks (ensure proper cleanup)
- [ ] Context cancellation propagation

#### Resource Management
```go
// ✅ GOOD: Proper resource cleanup
func ProcessFile(filename string) error {
    file, err := os.Open(filename)
    if err != nil {
        return err
    }
    defer file.Close() // Ensure cleanup

    // Process file...
    return nil
}

// ✅ GOOD: HTTP client with connection pooling
var httpClient = &http.Client{
    Timeout: 30 * time.Second,
    Transport: &http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
    },
}
```

#### Caching Strategy
- Cache TEE attestations (24-hour TTL)
- Cache service metadata (provider info)
- Cache user balance checks (short TTL, 1 minute)
- Invalidate cache on state changes

### 8. Testing Requirements

#### Unit Tests
- [ ] All core business logic must have unit tests
- [ ] Test coverage > 80% for new code
- [ ] Test edge cases and error paths
- [ ] Use table-driven tests for multiple scenarios

```go
// ✅ GOOD: Table-driven test
func TestProcessResponse(t *testing.T) {
    tests := []struct {
        name     string
        response *Response
        want     *Result
        wantErr  bool
    }{
        {
            name:     "valid chatbot response",
            response: &Response{Usage: &Usage{PromptTokens: 10, CompletionTokens: 20}},
            want:     &Result{TokensUsed: 30},
            wantErr:  false,
        },
        {
            name:     "missing usage data",
            response: &Response{Usage: nil},
            wantErr:  true,
        },
        // More test cases...
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := ProcessResponse(tt.response)
            if (err != nil) != tt.wantErr {
                t.Errorf("ProcessResponse() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("ProcessResponse() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

#### Integration Tests
- Test with local blockchain (Ganache/Hardhat)
- Mock external dependencies (0G Storage, Provider services)
- Test complete request flows end-to-end

#### Contract Tests
- Test all state transitions
- Verify gas estimates are reasonable
- Test revert scenarios
- Validate events are emitted correctly

### 9. API Design

#### HTTP Endpoints
- Use standard HTTP methods (GET, POST, PUT, DELETE)
- Use appropriate status codes
- Consistent JSON response format
- Versioned endpoints (/v1/...)

```go
// ✅ GOOD: Standard response format
type Response struct {
    Success bool        `json:"success"`
    Data    interface{} `json:"data,omitempty"`
    Error   *Error      `json:"error,omitempty"`
}

type Error struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}
```

#### Error Responses
```json
{
  "success": false,
  "error": {
    "code": "INSUFFICIENT_BALANCE",
    "message": "Account balance is too low to process request"
  }
}
```

### 10. Documentation Requirements

#### Must Document
- [ ] All exported functions, types, constants
- [ ] Complex algorithms and business logic
- [ ] Configuration options and environment variables
- [ ] API endpoints (use Swagger/OpenAPI if available)

#### Comment Style
```go
// ProcessResponse verifies the inference response and settles fees.
//
// Parameters:
//   - ctx: Request context for cancellation and timeout
//   - userAddr: Address of the user making the request
//   - providerAddr: Address of the provider serving the request
//   - response: The inference response containing usage data
//
// Returns:
//   - error: Non-nil if verification or settlement fails
//
// This function performs the following steps:
// 1. Extracts usage data from the response
// 2. Verifies TEE signature if chatID is present
// 3. Calculates fees based on usage
// 4. Transfers funds from user sub-account to provider
func ProcessResponse(ctx context.Context, userAddr, providerAddr string, response *Response) error {
    // Implementation...
}
```

## Common Anti-Patterns to Avoid

### ❌ Blockchain
```go
// BAD: Hardcoded private key
privateKey := "0x1234..."

// BAD: Ignoring transaction errors
contract.Transfer(opts, addr, amount)

// BAD: No gas limit
opts := &bind.TransactOpts{
    From:   userAddr,
    Signer: signer,
    // Missing: GasLimit
}
```

### ❌ Error Handling
```go
// BAD: Silent failure
_ = ImportantOperation()

// BAD: Generic error messages
return errors.New("error occurred")

// BAD: Using panic
panic("something went wrong")
```

### ❌ Resource Management
```go
// BAD: Not closing resources
file, _ := os.Open("file.txt")
// Missing: defer file.Close()

// BAD: Goroutine leak
go func() {
    for {
        // No exit condition
        doWork()
    }
}()
```

### ❌ Security
```go
// BAD: Logging sensitive data
log.Info("User login", "password", password)

// BAD: No input validation
amount := req.Amount // Direct use without validation

// BAD: SQL injection risk (if using SQL)
query := fmt.Sprintf("SELECT * FROM users WHERE id = %s", userID)
```

## Review Process Guidelines

### Severity Levels
1. **[CRITICAL]**: Security vulnerabilities, data loss risk, blockchain safety
2. **[HIGH]**: Logic errors, resource leaks, API incompatibility
3. **[MEDIUM]**: Code quality, insufficient testing, missing documentation
4. **[LOW/nit]**: Code style, naming, minor improvements

### Feedback Format
When reviewing code:
1. **Specify severity** and exact location (file:line)
2. **Explain the issue** and its impact
3. **Provide fix suggestions** with code examples
4. **Highlight good practices** with positive feedback

### Example Review Comments
```
[CRITICAL] contract/provider_contract.go:145
Issue: Private key is logged in error message
Impact: Private key exposure in logs is a critical security risk
Fix: Remove private key from error message, only log address
```

```
✅ inference/internal/ctrl/chatbot.go:78-92
Good: Excellent error handling with context wrapping. The retry logic
with exponential backoff is well implemented.
```

```
[nit] fine-tuning/internal/handler/task.go:234
Suggestion: Variable name `x` is not descriptive. Consider renaming to
`taskStatus` for clarity.
```

## Reference Resources

- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md)
- [0G Compute Network Docs](https://docs.0g.ai/)
- [OpenAI API Reference](https://platform.openai.com/docs/api-reference)

---

**Note**: Code reviews should be constructive and helpful. The goal is to improve code quality and help developers grow, not to criticize. Always assume good intent and provide actionable feedback.
