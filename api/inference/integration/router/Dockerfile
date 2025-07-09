# syntax=docker/dockerfile:1

FROM node:20-alpine

RUN npm install -g pnpm

ENV PNPM_HOME="/root/.local/share/pnpm"
ENV PATH="$PNPM_HOME:$PATH"

RUN pnpm install @0glabs/0g-serving-broker -g

WORKDIR /app

ENTRYPOINT ["0g-compute-cli", "serve"]
