FROM nvidia/cuda:12.4.1-cudnn-devel-ubuntu22.04

RUN apt-get update && apt-get install -y \
    python3 python3-pip wget curl build-essential vim \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

    # Set Python 3 as default
RUN update-alternatives --install /usr/bin/python python /usr/bin/python3 1 \
    && update-alternatives --install /usr/bin/pip pip /usr/bin/pip3 1

RUN python -m pip config set global.break-system-packages true
RUN pip install torch torchvision torchaudio --index-url https://mirrors.aliyun.com/pypi/simple/

WORKDIR /app
COPY transformers/ /app/
RUN pip install -r /app/requirements.txt --index-url https://mirrors.aliyun.com/pypi/simple/

CMD ["bash"]
