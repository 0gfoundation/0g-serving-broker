#!/usr/bin/env python3
"""
PDF提取工具 - 支持文本提取、表格提取和元数据查看
"""

import sys
import json
import pdfplumber
import fitz  # PyMuPDF


def extract_text_pdfplumber(pdf_path, output_path=None):
    """使用pdfplumber提取文本（保留布局）"""
    text = ""
    with pdfplumber.open(pdf_path) as pdf:
        for i, page in enumerate(pdf.pages):
            text += f"\n{'='*60}\n"
            text += f"第 {i+1} 页 / 共 {len(pdf.pages)} 页\n"
            text += f"{'='*60}\n\n"
            text += page.extract_text() or "[此页无文本]"
            text += "\n\n"

    if output_path:
        with open(output_path, 'w', encoding='utf-8') as f:
            f.write(text)
        print(f"文本已保存到: {output_path}")
    return text


def extract_tables(pdf_path):
    """提取PDF中的所有表格"""
    tables = []
    with pdfplumber.open(pdf_path) as pdf:
        for i, page in enumerate(pdf.pages):
            page_tables = page.extract_tables()
            if page_tables:
                for j, table in enumerate(page_tables):
                    tables.append({
                        "page": i + 1,
                        "table_index": j + 1,
                        "data": table
                    })
    return tables


def extract_metadata(pdf_path):
    """使用PyMuPDF提取PDF元数据"""
    doc = fitz.open(pdf_path)
    metadata = {
        "文件名": pdf_path,
        "页数": len(doc),
        "标题": doc.metadata.get('title', 'N/A'),
        "作者": doc.metadata.get('author', 'N/A'),
        "主题": doc.metadata.get('subject', 'N/A'),
        "创建者": doc.metadata.get('creator', 'N/A'),
        "生产者": doc.metadata.get('producer', 'N/A'),
    }
    doc.close()
    return metadata


def quick_extract(pdf_path, output_txt=None):
    """快速提取：文本 + 元数据"""
    print(f"正在处理: {pdf_path}\n")

    # 提取元数据
    print("📄 PDF 元数据:")
    metadata = extract_metadata(pdf_path)
    for key, value in metadata.items():
        print(f"  {key}: {value}")
    print()

    # 提取文本
    print("📝 提取文本内容...")
    text = extract_text_pdfplumber(pdf_path, output_txt)

    # 尝试提取表格
    print("📊 检查表格...")
    tables = extract_tables(pdf_path)
    if tables:
        print(f"  发现 {len(tables)} 个表格")
        for t in tables[:3]:  # 只显示前3个
            print(f"    - 第{t['page']}页, 表格{t['table_index']}: {len(t['data'])} 行")
    else:
        print("  未发现表格")

    return text


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("用法: python3 pdf_extractor.py <pdf文件> [输出txt文件]")
        print("示例: python3 pdf_extractor.py doc.pdf output.txt")
        sys.exit(1)

    pdf_file = sys.argv[1]
    output_file = sys.argv[2] if len(sys.argv) > 2 else None

    quick_extract(pdf_file, output_file)
