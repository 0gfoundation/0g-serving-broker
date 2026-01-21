#!/usr/bin/env python3
"""
Qwen2.5-Coder-32B LoRA Fine-tuning 测试脚本（无 Flash Attention 2）
支持 Alpaca 和 ShareGPT 格式数据集
使用普通 attention 实现
"""

import argparse
import json
import os
import time
from pathlib import Path
from typing import Dict, List, Optional

import torch
from datasets import Dataset, load_dataset
from peft import LoraConfig, get_peft_model, prepare_model_for_kbit_training
from transformers import (
    AutoConfig,
    AutoModelForCausalLM,
    AutoTokenizer,
    DataCollatorForSeq2Seq,
    Trainer,
    TrainingArguments,
    TrainerCallback
)


class GPUMemoryCallback(TrainerCallback):
    """监控 GPU 显存使用"""

    def __init__(self, log_file="/data/test-output/gpu_memory.log"):
        self.log_file = log_file
        self.start_time = None

    def on_train_begin(self, args, state, control, **kwargs):
        self.start_time = time.time()
        self.log_memory("训练开始")

    def on_step_end(self, args, state, control, **kwargs):
        if state.global_step % 10 == 0:
            self.log_memory(f"Step {state.global_step}")

    def on_train_end(self, args, state, control, **kwargs):
        elapsed = time.time() - self.start_time
        self.log_memory(f"训练结束 (总时间: {elapsed:.2f}秒)")

    def log_memory(self, stage: str):
        if torch.cuda.is_available():
            allocated = torch.cuda.memory_allocated() / 1024**3
            reserved = torch.cuda.memory_reserved() / 1024**3
            max_allocated = torch.cuda.max_memory_allocated() / 1024**3

            log_msg = (
                f"[{stage}] "
                f"Allocated: {allocated:.2f}GB, "
                f"Reserved: {reserved:.2f}GB, "
                f"Max Allocated: {max_allocated:.2f}GB\n"
            )

            print(log_msg, end='')
            with open(self.log_file, 'a') as f:
                f.write(log_msg)


def detect_dataset_format(data: List[Dict]) -> str:
    """检测数据集格式"""
    if not data:
        raise ValueError("数据集为空")

    first_item = data[0]

    # 检查是否为 ShareGPT 格式
    if "messages" in first_item or "conversations" in first_item:
        return "sharegpt"

    # 检查是否为 Alpaca 格式
    if "instruction" in first_item and "output" in first_item:
        return "alpaca"

    raise ValueError(f"未知的数据集格式。第一条数据: {first_item}")


def format_alpaca_prompt(example: Dict, tokenizer) -> str:
    """格式化 Alpaca 样本为 prompt"""
    instruction = example["instruction"]
    input_text = example.get("input", "")
    output = example["output"]

    if input_text:
        prompt = f"""Below is an instruction that describes a task, paired with an input that provides further context. Write a response that appropriately completes the request.

### Instruction:
{instruction}

### Input:
{input_text}

### Response:
{output}"""
    else:
        prompt = f"""Below is an instruction that describes a task. Write a response that appropriately completes the request.

### Instruction:
{instruction}

### Response:
{output}"""

    return prompt


def format_sharegpt_prompt(example: Dict, tokenizer) -> str:
    """格式化 ShareGPT 样本为 prompt"""
    messages = example.get("messages") or example.get("conversations", [])

    # 使用 tokenizer 的 chat template（如果有）
    if hasattr(tokenizer, "apply_chat_template") and tokenizer.chat_template:
        return tokenizer.apply_chat_template(messages, tokenize=False, add_generation_prompt=False)

    # 否则使用简单格式
    formatted = []
    for msg in messages:
        role = msg["role"]
        content = msg["content"]
        if role == "user":
            formatted.append(f"User: {content}")
        elif role == "assistant":
            formatted.append(f"Assistant: {content}")

    return "\n\n".join(formatted)


def prepare_dataset(
    data_path: str,
    tokenizer,
    max_length: int = 2048,
    dataset_format: Optional[str] = None
) -> Dataset:
    """准备数据集"""
    print(f"加载数据集: {data_path}")

    # 读取 JSON 文件
    with open(data_path, 'r', encoding='utf-8') as f:
        data = json.load(f)

    print(f"数据集大小: {len(data)} 条")

    # 检测格式
    if dataset_format is None:
        dataset_format = detect_dataset_format(data)

    print(f"数据集格式: {dataset_format}")

    # 格式化数据
    formatted_data = []
    for example in data:
        if dataset_format == "alpaca":
            text = format_alpaca_prompt(example, tokenizer)
        elif dataset_format == "sharegpt":
            text = format_sharegpt_prompt(example, tokenizer)
        else:
            raise ValueError(f"不支持的格式: {dataset_format}")

        formatted_data.append({"text": text})

    # 创建 Dataset
    dataset = Dataset.from_list(formatted_data)

    # Tokenize
    def tokenize_function(examples):
        outputs = tokenizer(
            examples["text"],
            truncation=True,
            max_length=max_length,
            padding=False,
            return_tensors=None,
        )
        outputs["labels"] = outputs["input_ids"].copy()
        return outputs

    print("正在 tokenize 数据集...")
    tokenized_dataset = dataset.map(
        tokenize_function,
        batched=True,
        remove_columns=dataset.column_names,
        desc="Tokenizing"
    )

    print(f"Tokenize 完成，样本数: {len(tokenized_dataset)}")
    return tokenized_dataset


def create_lora_config() -> LoraConfig:
    """创建 LoRA 配置（固定参数，保证多 LoRA 兼容）"""
    return LoraConfig(
        task_type="CAUSAL_LM",
        r=16,                    # Rank
        lora_alpha=32,           # Alpha
        lora_dropout=0.05,
        bias="none",
        target_modules=[
            "q_proj",
            "k_proj",
            "v_proj",
            "o_proj",
            "gate_proj",
            "up_proj",
            "down_proj"
        ],
        modules_to_save=[],      # 不保存额外模块，节省显存
    )


def main():
    parser = argparse.ArgumentParser(description="Qwen2.5-Coder-32B LoRA Fine-tuning (No Flash Attention)")
    parser.add_argument("--model_path", type=str, required=True, help="模型路径")
    parser.add_argument("--data_path", type=str, required=True, help="数据集路径（JSON）")
    parser.add_argument("--output_dir", type=str, default="/data/test-output/lora-model", help="输出目录")
    parser.add_argument("--max_length", type=int, default=2048, help="最大序列长度")
    parser.add_argument("--batch_size", type=int, default=1, help="每设备训练 batch size")
    parser.add_argument("--gradient_accumulation_steps", type=int, default=8, help="梯度累积步数")
    parser.add_argument("--learning_rate", type=float, default=2e-4, help="学习率")
    parser.add_argument("--num_epochs", type=int, default=1, help="训练 epoch 数")
    parser.add_argument("--logging_steps", type=int, default=10, help="日志步数")
    parser.add_argument("--save_steps", type=int, default=100, help="保存步数")
    parser.add_argument("--dataset_format", type=str, choices=["alpaca", "sharegpt"], help="数据集格式（自动检测）")

    args = parser.parse_args()

    print("=" * 60)
    print("Qwen2.5-Coder-32B LoRA Fine-tuning 测试")
    print("使用普通 attention（无 Flash Attention 2）")
    print("=" * 60)
    print(f"模型路径: {args.model_path}")
    print(f"数据路径: {args.data_path}")
    print(f"输出目录: {args.output_dir}")
    print(f"最大长度: {args.max_length}")
    print(f"Batch Size: {args.batch_size}")
    print(f"梯度累积: {args.gradient_accumulation_steps}")
    print(f"学习率: {args.learning_rate}")
    print(f"Epochs: {args.num_epochs}")
    print("=" * 60)

    # 检查 GPU
    if not torch.cuda.is_available():
        raise RuntimeError("未检测到 GPU！")

    print(f"GPU: {torch.cuda.get_device_name(0)}")
    print(f"显存: {torch.cuda.get_device_properties(0).total_memory / 1024**3:.2f} GB")
    print("=" * 60)

    # 创建输出目录
    os.makedirs(args.output_dir, exist_ok=True)

    # 加载 tokenizer
    print("加载 tokenizer...")
    tokenizer = AutoTokenizer.from_pretrained(
        args.model_path,
        trust_remote_code=True,
        padding_side="right",  # 训练时右 padding
    )

    # 设置 pad_token
    if tokenizer.pad_token is None:
        tokenizer.pad_token = tokenizer.eos_token

    # 准备数据集
    dataset = prepare_dataset(
        args.data_path,
        tokenizer,
        max_length=args.max_length,
        dataset_format=args.dataset_format
    )

    # 划分训练集和验证集（90/10）
    split_dataset = dataset.train_test_split(test_size=0.1, seed=42)
    train_dataset = split_dataset["train"]
    eval_dataset = split_dataset["test"]

    print(f"训练集: {len(train_dataset)} 条")
    print(f"验证集: {len(eval_dataset)} 条")
    print("=" * 60)

    # 加载模型（使用普通 attention）
    print("加载模型...")
    print("这可能需要几分钟...")

    model = AutoModelForCausalLM.from_pretrained(
        args.model_path,
        torch_dtype=torch.bfloat16,  # 使用 BF16
        device_map="auto",
        trust_remote_code=True,
        # 不使用 flash_attention_2，用普通 attention
    )

    print(f"模型加载完成！参数量: {model.num_parameters() / 1e9:.2f}B")

    # 创建 LoRA 配置
    print("配置 LoRA...")
    lora_config = create_lora_config()

    # 应用 LoRA
    model = get_peft_model(model, lora_config)
    model.print_trainable_parameters()

    # 训练参数
    training_args = TrainingArguments(
        output_dir=args.output_dir,
        num_train_epochs=args.num_epochs,
        per_device_train_batch_size=args.batch_size,
        per_device_eval_batch_size=args.batch_size,
        gradient_accumulation_steps=args.gradient_accumulation_steps,
        learning_rate=args.learning_rate,

        # 优化设置
        bf16=True,                           # 使用 BF16
        fp16=False,
        tf32=True,                           # 启用 TF32
        gradient_checkpointing=True,         # 梯度检查点（必需）

        # 日志和保存
        logging_steps=args.logging_steps,
        logging_dir=os.path.join(args.output_dir, "logs"),
        save_steps=args.save_steps,
        save_total_limit=2,

        # 评估
        eval_strategy="steps",
        eval_steps=args.save_steps,
        load_best_model_at_end=True,
        metric_for_best_model="loss",

        # 其他
        warmup_steps=10,
        lr_scheduler_type="cosine",
        report_to=["none"],
        dataloader_num_workers=4,
        remove_unused_columns=False,
    )

    # Data Collator
    data_collator = DataCollatorForSeq2Seq(
        tokenizer=tokenizer,
        model=model,
        padding=True,
    )

    # Trainer
    trainer = Trainer(
        model=model,
        args=training_args,
        train_dataset=train_dataset,
        eval_dataset=eval_dataset,
        data_collator=data_collator,
        callbacks=[GPUMemoryCallback()],
    )

    # 开始训练
    print("=" * 60)
    print("开始训练...")
    print("=" * 60)

    torch.cuda.reset_peak_memory_stats()
    train_result = trainer.train()

    # 保存模型
    print("=" * 60)
    print("保存 LoRA adapter...")
    trainer.save_model()
    tokenizer.save_pretrained(args.output_dir)

    # 保存训练指标
    metrics = train_result.metrics
    metrics["train_samples"] = len(train_dataset)

    with open(os.path.join(args.output_dir, "train_metrics.json"), "w") as f:
        json.dump(metrics, f, indent=2)

    # 显存统计
    max_memory = torch.cuda.max_memory_allocated() / 1024**3
    print(f"最大显存使用: {max_memory:.2f} GB")

    print("=" * 60)
    print("训练完成！")
    print(f"LoRA adapter 已保存到: {args.output_dir}")
    print("=" * 60)

    # 检查 adapter 文件大小
    adapter_path = os.path.join(args.output_dir, "adapter_model.safetensors")
    if os.path.exists(adapter_path):
        size_mb = os.path.getsize(adapter_path) / 1024**2
        print(f"Adapter 文件大小: {size_mb:.2f} MB")


if __name__ == "__main__":
    main()
