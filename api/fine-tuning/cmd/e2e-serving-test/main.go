package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	commonConfig "github.com/0glabs/0g-serving-broker/common/config"
	commonLog "github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/serving"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func main() {
	logCfg := &commonConfig.LoggerConfig{
		Format: "text",
		Level:  "debug",
	}
	logger, err := commonLog.GetLogger(logCfg)
	if err != nil {
		panic(err)
	}

	fmt.Println("========================================")
	fmt.Println("  0G Broker Serving Module E2E Test")
	fmt.Println("========================================")

	// --- Step 1: Connect to MySQL ---
	fmt.Println("\n[1/7] Connecting to MySQL...")
	cfg := &config.Config{}
	cfg.Database.FineTune = "root:123456@tcp(127.0.0.1:3306)/fineTune?parseTime=true"
	database, err := db.NewDB(cfg, logger)
	if err != nil {
		fmt.Printf("FAIL: MySQL connection: %v\n", err)
		os.Exit(1)
	}
	if err := database.Migrate(); err != nil {
		fmt.Printf("FAIL: migration: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS: MySQL connected, schema migrated")

	// --- Step 2: Generate test user keypair ---
	fmt.Println("\n[2/7] Creating test user keypair...")
	testPrivKey, err := crypto.GenerateKey()
	if err != nil {
		fmt.Printf("FAIL: key generation: %v\n", err)
		os.Exit(1)
	}
	testAddr := crypto.PubkeyToAddress(testPrivKey.PublicKey)
	fmt.Printf("PASS: Test user address: %s\n", testAddr.Hex())

	authSig := generateAuthSignature(testPrivKey)
	fmt.Printf("PASS: Auth signature generated (len=%d)\n", len(authSig))

	// --- Step 3: Insert test tasks ---
	fmt.Println("\n[3/7] Inserting simulated finished tasks...")
	taskIDs := make([]uuid.UUID, 3)
	loraAdapters := []string{
		"/root/lora-modules/ft-lora-adapter-0",
		"/root/lora-modules/ft-lora-adapter-1",
		"/root/lora-modules/ft-lora-adapter-2",
	}
	for i := 0; i < 3; i++ {
		id := uuid.New()
		taskIDs[i] = id
		if err := database.InsertTestTask(id, strings.ToLower(testAddr.Hex()), "Qwen2.5-0.5B"); err != nil {
			fmt.Printf("FAIL: insert task %d: %v\n", i, err)
			os.Exit(1)
		}
		fmt.Printf("  Task %d: %s\n", i, id.String())
	}
	fmt.Println("PASS: 3 finished tasks inserted")

	// --- Step 4: Create serving Manager ---
	fmt.Println("\n[4/7] Starting serving Manager + vLLM...")
	servingCfg := serving.ServingConfig{
		Enable:              true,
		BaseModelPath:       "/root/models/Qwen2.5-0.5B-Instruct",
		InferenceGPUIDs:     "0",
		VLLMPort:            8000,
		MaxLoraRank:         64,
		MaxLoraModules:      16,
		MaxCpuLoras:         32,
		LoraModulesDir:      "/root/e2e-lora-modules",
		OffloadAfterMinutes:     60,
		EnableColdStorage:       false,
		ModelLoadTimeoutSeconds: 300,
		GpuMemoryUtilization:    0.6,
	}

	mgr := serving.NewManager(database, servingCfg, logger, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		fmt.Printf("FAIL: Manager start: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS: Manager started, vLLM launching in background")

	// --- Step 5: Register LoRA adapters ---
	fmt.Println("\n[5/7] Registering LoRA adapters...")
	var modelNames []string
	for i, taskID := range taskIDs {
		name, err := mgr.RegisterModel(taskID, strings.ToLower(testAddr.Hex()), "Qwen2.5-0.5B", loraAdapters[i], "")
		if err != nil {
			fmt.Printf("FAIL: register adapter %d: %v\n", i, err)
			os.Exit(1)
		}
		modelNames = append(modelNames, name)
		fmt.Printf("  Registered: %s -> %s\n", name, loraAdapters[i])
	}
	fmt.Println("PASS: 3 LoRA adapters registered")

	// --- Step 6: Start HTTP proxy ---
	fmt.Println("\n[6/7] Starting HTTP proxy on :3080...")
	proxy := serving.NewProxy(mgr, logger)
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())
	v1 := engine.Group("/v1")
	proxy.RegisterRoutes(v1)

	go func() {
		if err := engine.Run(":3080"); err != nil {
			fmt.Printf("HTTP server error: %v\n", err)
		}
	}()
	time.Sleep(500 * time.Millisecond)
	fmt.Println("PASS: HTTP proxy started")

	// --- Step 7: Wait for vLLM ---
	fmt.Println("\n[7/7] Waiting for vLLM to become ready (may take 1-3 minutes)...")
	if !waitForVLLM(ctx, 300*time.Second) {
		fmt.Println("FAIL: vLLM did not become ready within timeout")
		cleanup(database, taskIDs)
		os.Exit(1)
	}
	fmt.Println("PASS: vLLM is ready!")

	// --- Run test cases ---
	fmt.Println("\n========================================")
	fmt.Println("  Running E2E Test Cases")
	fmt.Println("========================================")

	passed, failed := 0, 0
	run := func(name string, fn func() error) {
		fmt.Printf("\n--- Test: %s ---\n", name)
		if err := fn(); err != nil {
			fmt.Printf("FAIL: %v\n", err)
			failed++
		} else {
			fmt.Printf("PASS\n")
			passed++
		}
	}

	authHeader := "Bearer " + authSig

	run("Health endpoint", func() error {
		resp, body, err := httpGet("http://localhost:3080/v1/serving/health", nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		fmt.Printf("  Response: %s\n", body)
		return nil
	})

	run("List all served models", func() error {
		resp, body, err := httpGet("http://localhost:3080/v1/serving/models", nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		var models []map[string]interface{}
		json.Unmarshal([]byte(body), &models)
		if len(models) != 3 {
			return fmt.Errorf("expected 3 models, got %d", len(models))
		}
		for _, m := range models {
			fmt.Printf("  Model: %s (state: %s)\n", m["modelName"], m["state"])
		}
		return nil
	})

	run("List models for user (authenticated)", func() error {
		resp, body, err := httpGet("http://localhost:3080/v1/serving/v1/models", map[string]string{
			"Authorization": authHeader,
		})
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		fmt.Printf("  Response: %s\n", truncate(body, 200))
		return nil
	})

	run("Unauthorized request rejected", func() error {
		resp, _, err := httpGet("http://localhost:3080/v1/serving/v1/models", nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != 401 {
			return fmt.Errorf("expected 401, got %d", resp.StatusCode)
		}
		return nil
	})

	run("Chat completion with LoRA adapter 0", func() error {
		return testChatCompletion(modelNames[0], authHeader)
	})

	run("Chat completion with LoRA adapter 1", func() error {
		return testChatCompletion(modelNames[1], authHeader)
	})

	run("Chat completion with LoRA adapter 2", func() error {
		return testChatCompletion(modelNames[2], authHeader)
	})

	run("Non-existent model returns 404", func() error {
		reqBody := map[string]interface{}{
			"model":    "non-existent-model",
			"messages": []map[string]string{{"role": "user", "content": "Hello"}},
		}
		bodyBytes, _ := json.Marshal(reqBody)
		resp, _, err := httpPost("http://localhost:3080/v1/serving/v1/chat/completions", bodyBytes, map[string]string{
			"Authorization": authHeader,
			"Content-Type":  "application/json",
		})
		if err != nil {
			return err
		}
		if resp.StatusCode != 404 {
			return fmt.Errorf("expected 404, got %d", resp.StatusCode)
		}
		return nil
	})

	run("Wrong user cannot access model", func() error {
		otherKey, _ := crypto.GenerateKey()
		otherSig := generateAuthSignature(otherKey)
		reqBody := map[string]interface{}{
			"model":    modelNames[0],
			"messages": []map[string]string{{"role": "user", "content": "Hello"}},
		}
		bodyBytes, _ := json.Marshal(reqBody)
		resp, _, err := httpPost("http://localhost:3080/v1/serving/v1/chat/completions", bodyBytes, map[string]string{
			"Authorization": "Bearer " + otherSig,
			"Content-Type":  "application/json",
		})
		if err != nil {
			return err
		}
		if resp.StatusCode != 403 {
			return fmt.Errorf("expected 403, got %d", resp.StatusCode)
		}
		return nil
	})

	run("Streaming chat completion", func() error {
		reqBody := map[string]interface{}{
			"model":      modelNames[0],
			"messages":   []map[string]string{{"role": "user", "content": "Count 1 to 5"}},
			"stream":     true,
			"max_tokens": 50,
		}
		bodyBytes, _ := json.Marshal(reqBody)
		resp, err := httpPostRaw("http://localhost:3080/v1/serving/v1/chat/completions", bodyBytes, map[string]string{
			"Authorization": authHeader,
			"Content-Type":  "application/json",
		})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
		}
		chunks := 0
		buf := make([]byte, 4096)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				chunks++
				if chunks <= 3 {
					fmt.Printf("  Chunk %d: %s\n", chunks, truncate(string(buf[:n]), 100))
				}
			}
			if readErr != nil {
				break
			}
		}
		fmt.Printf("  Total chunks: %d\n", chunks)
		if chunks < 2 {
			return fmt.Errorf("expected multiple chunks, got %d", chunks)
		}
		return nil
	})

	run("Chat completion with wait_for_model on active model", func() error {
		reqBody := map[string]interface{}{
			"model":          modelNames[0],
			"messages":       []map[string]string{{"role": "user", "content": "What is 2+2?"}},
			"max_tokens":     30,
			"wait_for_model": true,
		}
		bodyBytes, _ := json.Marshal(reqBody)
		resp, body, err := httpPost("http://localhost:3080/v1/serving/v1/chat/completions", bodyBytes, map[string]string{
			"Authorization": authHeader,
			"Content-Type":  "application/json",
		})
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("expected 200 for active model with wait_for_model, got %d: %s", resp.StatusCode, body)
		}
		fmt.Printf("  Response (wait_for_model=true, active): %s\n", truncate(body, 120))
		return nil
	})

	run("Health endpoint shows model_load_timeout_sec", func() error {
		resp, body, err := httpGet("http://localhost:3080/v1/serving/health", nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("expected 200, got %d", resp.StatusCode)
		}
		var health map[string]interface{}
		json.Unmarshal([]byte(body), &health)
		if _, ok := health["model_load_timeout_sec"]; !ok {
			return fmt.Errorf("model_load_timeout_sec missing from health response: %s", body)
		}
		fmt.Printf("  model_load_timeout_sec: %v\n", health["model_load_timeout_sec"])
		return nil
	})

	run("Concurrent requests to different adapters", func() error {
		type result struct {
			idx int
			err error
		}
		ch := make(chan result, 3)
		for i := 0; i < 3; i++ {
			go func(idx int) {
				ch <- result{idx, testChatCompletion(modelNames[idx], authHeader)}
			}(i)
		}
		for i := 0; i < 3; i++ {
			r := <-ch
			if r.err != nil {
				return fmt.Errorf("concurrent request %d failed: %v", r.idx, r.err)
			}
		}
		fmt.Printf("  All 3 concurrent requests succeeded\n")
		return nil
	})

	// --- Summary ---
	fmt.Println("\n========================================")
	fmt.Printf("  Results: %d passed, %d failed\n", passed, failed)
	fmt.Println("========================================")

	cleanup(database, taskIDs)
	cancel()
	mgr.Stop()

	if failed > 0 {
		os.Exit(1)
	}
}

func cleanup(database *db.DB, taskIDs []uuid.UUID) {
	for _, id := range taskIDs {
		database.DeleteTestTask(id)
	}
	fmt.Println("Cleaned up test tasks from DB")
}

func generateAuthSignature(key *ecdsa.PrivateKey) string {
	message := "0g-serving-inference-auth"
	hash := accounts.TextHash([]byte(message))
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		panic(err)
	}
	if sig[64] < 27 {
		sig[64] += 27
	}
	return "0x" + hex.EncodeToString(sig)
}

func testChatCompletion(modelName, authHeader string) error {
	reqBody := map[string]interface{}{
		"model":      modelName,
		"messages":   []map[string]string{{"role": "user", "content": "What is 2+2?"}},
		"max_tokens": 30,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	resp, body, err := httpPost("http://localhost:3080/v1/serving/v1/chat/completions", bodyBytes, map[string]string{
		"Authorization": authHeader,
		"Content-Type":  "application/json",
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	choices, ok := result["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return fmt.Errorf("no choices: %s", truncate(body, 200))
	}
	fmt.Printf("  Model: %s -> %s\n", modelName, truncate(body, 120))
	return nil
}

func waitForVLLM(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		resp, err := http.Get("http://localhost:8000/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(5 * time.Second)
	}
	return false
}

func httpGet(url string, headers map[string]string) (*http.Response, string, error) {
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body), nil
}

func httpPost(url string, data []byte, headers map[string]string) (*http.Response, string, error) {
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(data))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body), nil
}

func httpPostRaw(url string, data []byte, headers map[string]string) (*http.Response, error) {
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(data))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return (&http.Client{Timeout: 120 * time.Second}).Do(req)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
