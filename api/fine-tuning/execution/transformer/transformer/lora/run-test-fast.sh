#!/bin/bash
#
# 使用快速版镜像运行完整测试（无 Flash Attention 2）
#

set -e

echo "=========================================="
echo "Qwen2.5-Coder-32B LoRA 完整测试"
echo "使用快速版镜像（无 Flash Attention 2）"
echo "=========================================="

# 步骤 1: 检查并下载模型
echo ""
echo "[步骤 1/3] 检查并下载模型..."
echo "----------------------------------------"

if [ -d "/data/models/Qwen2.5-Coder-32B" ] && [ -f "/data/models/Qwen2.5-Coder-32B/config.json" ]; then
    echo "✓ 模型已存在，跳过下载"
    ls -lh /data/models/Qwen2.5-Coder-32B/ | head -10
else
    echo "下载 Qwen2.5-Coder-32B (约 64GB，需要 30-60 分钟)..."

    python << 'PYEOF'
from modelscope import snapshot_download
import os

print("开始下载...")
model_dir = snapshot_download(
    'Qwen/Qwen2.5-Coder-32B',
    cache_dir='/data/models/',
    revision='master'
)
print(f"\n模型已下载到: {model_dir}")

# 创建符号链接
target = '/data/models/Qwen2.5-Coder-32B'
if not os.path.exists(target):
    if model_dir != target:
        os.symlink(model_dir, target)
        print(f"已创建符号链接: {target}")
else:
    print(f"模型路径: {target}")
PYEOF

    echo "✓ 模型下载完成"
fi

# 步骤 2: 训练测试
echo ""
echo "[步骤 2/3] 运行 LoRA 训练测试..."
echo "----------------------------------------"
echo "数据集: Alpaca 格式 (45条样本)"
echo "配置: batch_size=1, max_length=2048, epochs=1"
echo "注意: 使用普通 attention，速度会比 Flash Attention 2 慢一些"
echo ""

python /data/test-lora-finetune-no-flash.py \
    --model_path /data/models/Qwen2.5-Coder-32B \
    --data_path /data/test-datasets/test_alpaca_50.json \
    --output_dir /data/test-output/lora-alpaca \
    --num_epochs 1 \
    --max_length 2048 \
    --batch_size 1 \
    --gradient_accumulation_steps 8 \
    --learning_rate 2e-4 \
    --logging_steps 5 \
    --save_steps 50

echo ""
echo "✓ 训练完成"

# 步骤 3: 推理测试
echo ""
echo "[步骤 3/3] 运行推理测试..."
echo "----------------------------------------"

python /data/test-lora-inference.py \
    --base_model /data/models/Qwen2.5-Coder-32B \
    --lora_path /data/test-output/lora-alpaca \
    --prompt "用 Python 实现一个快速排序函数" \
    --max_new_tokens 512

echo ""
echo "=========================================="
echo "✓ 所有测试完成！"
echo "=========================================="
echo ""
echo "查看结果："
echo "  - 训练指标: /data/test-output/lora-alpaca/train_metrics.json"
echo "  - 显存日志: /data/test-output/gpu_memory.log"
echo "  - LoRA 模型: /data/test-output/lora-alpaca/"
echo ""
echo "LoRA adapter 大小："
du -sh /data/test-output/lora-alpaca/ 2>/dev/null || echo "输出目录不存在"
echo ""
