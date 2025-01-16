FROM python:3.9-slim

WORKDIR /app

RUN mkdir /app/mnt

COPY finetune.py /app/finetune.py

RUN pip install --no-cache-dir datasets

CMD ["bash"]