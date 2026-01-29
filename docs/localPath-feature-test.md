# LocalPath 功能测试文档

## 功能概述

`modelLocalPaths` 功能允许 Fine-tuning Broker 直接使用 Provider 机器上已下载的模型，而不是从 0G Storage 下载。这对于大模型（如 Qwen2.5-32B）特别有用，可以节省下载时间和带宽。

## 代码修改

### 1. config.go - 添加配置项

```go
type Service struct {
    // ... 其他字段 ...
    
    // ModelLocalPaths maps model hash to local file path for any model (including predefined models)
    // When set, the broker will use the local model instead of downloading from 0G Storage
    ModelLocalPaths map[string]string `yaml:"modelLocalPaths"`
}
```

### 2. setup.go - 实现本地模型加载逻辑

```go
func (s *Setup) prepareModel(ctx context.Context, task *db.Task, paths *utils.TaskPaths) error {
    // First check modelLocalPaths config (works for any model including predefined)
    if s.config.Service.ModelLocalPaths != nil {
        if localPath, ok := s.config.Service.ModelLocalPaths[task.PreTrainedModelHash]; ok && localPath != "" {
            return s.useLocalModel(localPath, paths)
        }
    }
    // ... fallback to 0G Storage download ...
}

func (s *Setup) useLocalModel(localPath string, paths *utils.TaskPaths) error {
    // 创建符号链接指向本地模型
    s.logger.Infof("Using local model from: %s", localPath)
    // ... 验证路径存在，创建 symlink ...
}
```

## 测试环境

| 组件 | 详情 |
|------|------|
| CVM | `617579ff3f3be1899e20091482000c286ed5bd02` (Phala Cloud) |
| GPU | H200 |
| 合约地址 | `0x4e4158DF35CfdC0ac63264D3E112F5B8E9a5c569` (zgTestnetDev) |
| Provider | `0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC` |
| 测试模型 | Qwen2.5-0.5B-Instruct |
| 模型 Hash | `0xb4f76a886b8655c92bb021922d60b5e4d9271a5c9da98b6cb10937a06c2c75a7` |

## 测试步骤

### Step 1: 准备本地模型

模型已预先下载到 TEE 机器：
```
/dstack/persistent/models/Qwen2.5-0.5B-Instruct/
```

### Step 2: 配置 Broker

在 `config.yaml` 中添加 `modelLocalPaths` 配置：

```yaml
service:
  servingUrl: "https://d4872603ae17c78f5d3a35318da14daedacf28a8-80.dstack-pha-use2.phala.network"
  pricePerToken: 1
  quota:
    cpuCount: 8
    memory: 187
    storage: 900
    gpuType: "H200"
    gpuCount: 1
  modelLocalPaths:
    "0xb4f76a886b8655c92bb021922d60b5e4d9271a5c9da98b6cb10937a06c2c75a7": "/dstack/persistent/models/Qwen2.5-0.5B-Instruct"
```

### Step 3: 部署 Broker 容器

```bash
docker run -d \
  --name broker \
  --privileged \
  --restart always \
  --gpus all \
  -e CONFIG_FILE=/etc/config/config.yaml \
  -p 80:8080 \
  -v /tmp/broker-static:/usr/bin/broker \
  -v /tmp/config-simple.yaml:/etc/config/config.yaml \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /var/run/dstack.sock:/var/run/dstack.sock \
  -v /var/run/tappd.sock:/var/run/tappd.sock \
  -v /dstack/persistent:/dstack/persistent \
  -v /tmp:/tmp \
  --device /dev/tdx_guest:/dev/tdx_guest \
  --network provider_default \
  ghcr.io/0gfoundation/0g-serving-broker:dev-amd64 \
  0g-fine-tuning-server
```

**关键挂载**：
- `-v /dstack/persistent:/dstack/persistent` - 模型存储
- `-v /tmp:/tmp` - 任务临时文件共享（broker ↔ 训练容器）

### Step 4: 准备账户资金

```bash
# 充值到 Ledger 账户
node cli.commonjs/cli/cli.js deposit \
  --amount 5 \
  --rpc "https://evmrpc-testnet.0g.ai" \
  --ledger-ca "0x815B93ab4Ba4BDF530dbF1552649a3c534F8BbF7"

# 转账到 Fine-tuning 子账户
node cli.commonjs/cli/cli.js transfer-fund \
  --provider "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC" \
  --service "fine-tuning" \
  --amount 3 \
  --rpc "https://evmrpc-testnet.0g.ai" \
  --ledger-ca "0x815B93ab4Ba4BDF530dbF1552649a3c534F8BbF7" \
  --fine-tuning-ca "0x4e4158DF35CfdC0ac63264D3E112F5B8E9a5c569"

# 给 Provider 钱包充值（用于 gas 费用）
# 通过直接转账 A0GI 到 Provider 地址
```

### Step 5: 创建测试任务

```bash
export ZEROG_PRIVATE_KEY="YOUR_PRIVATE_KEY"

node cli.commonjs/cli/index.js fine-tuning create-task \
    --model "Qwen2.5-0.5B-Instruct" \
    --dataset "0x5f70ae6f1a5d1f02ad4df34e72b82bd9f393e2c10bdfe15063dafd7dd5548446" \
    --data-size 1000 \
    --config-path "/tmp/qwen-train-config.json" \
    --provider "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC" \
    --fine-tuning-ca "0x4e4158DF35CfdC0ac63264D3E112F5B8E9a5c569" \
    --rpc "https://evmrpc-testnet.0g.ai"
```

训练配置 (`/tmp/qwen-train-config.json`)：
```json
{
  "learning_rate": 2e-4,
  "num_train_epochs": 1,
  "per_device_train_batch_size": 1,
  "gradient_accumulation_steps": 4,
  "max_seq_length": 512,
  "lora_r": 8,
  "lora_alpha": 16,
  "lora_dropout": 0.05
}
```

### Step 6: 监控任务进度

```bash
# 查询任务状态
curl -s "https://[BROKER_URL]/v1/user/[USER_ADDRESS]/task/[TASK_ID]" | jq '.progress'

# 查询任务日志
curl -s "https://[BROKER_URL]/v1/user/[USER_ADDRESS]/task/[TASK_ID]/log"
```

## 测试结果

### 成功的任务

**Task ID**: `09a5110e-9c42-45a4-841c-f604fd9e6d05`

**状态流转**：
```
Init → SettingUp → SetUp → Training → Trained → Delivering → Delivered ✅
```

**关键日志**：

```
[2026-01-29T10:13:58Z] creating task....
[2026-01-29T10:14:59Z] Training model for setup task 09a5110e-9c42-45a4-841c-f604fd9e6d05 successfully
[2026-01-29T10:15:37Z] Training model for executor task 09a5110e-9c42-45a4-841c-f604fd9e6d05 successfully
[2026-01-29T10:17:07Z] Training model for finalizer task 09a5110e-9c42-45a4-841c-f604fd9e6d05 successfully
```

**Broker 日志验证 localPath 功能**：
```
time="2026-01-29T10:XX:XXZ" level=info msg="Using local model from: /dstack/persistent/models/Qwen2.5-0.5B-Instruct" name=setup
time="2026-01-29T10:XX:XXZ" level=info msg="Created symlink from /dstack/persistent/models/Qwen2.5-0.5B-Instruct to /tmp/[TASK_ID]/model" name=setup
```

## 遇到的问题及解决方案

### 问题 1: GPU Evidence 收集失败

**错误**：
```
Exception: Error occurred while collecting GPU evidence
```

**原因**：重新创建容器时未正确挂载 TEE 设备

**解决**：添加必要的设备和 socket 挂载
```bash
--gpus all \
--device /dev/tdx_guest:/dev/tdx_guest \
-v /var/run/tappd.sock:/var/run/tappd.sock
```

### 问题 2: Provider 余额不足

**错误**：
```
insufficient provider balance: expected 1000000000000000000, got 0
```

**原因**：Provider 钱包没有 A0GI

**解决**：转账 A0GI 到 Provider 钱包地址

### 问题 3: 临时目录不共享

**错误**：
```
bind source path does not exist: /tmp/[TASK_ID]
```

**原因**：Broker 容器的 `/tmp` 未映射到宿主机，训练容器无法访问

**解决**：添加 `/tmp` 卷映射
```bash
-v /tmp:/tmp
```

## 结论

`modelLocalPaths` 功能测试成功：

1. ✅ Broker 正确读取 `modelLocalPaths` 配置
2. ✅ 使用本地模型替代 0G Storage 下载
3. ✅ 创建符号链接指向本地模型
4. ✅ 完整的 Fine-tuning 流程（Setup → Train → Deliver）成功执行

## 代码仓库

- **分支**：`feature/qwen-support`
- **提交**：`feat: add modelLocalPaths support for using pre-downloaded models`
