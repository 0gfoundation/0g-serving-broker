# Chatbot example integrating serving system

## Prerequisites

A chat service implementing OpenAI api format

## How to use

1. Start provider agent, user agent, hardhat node environment

   ```bash
   docker compose -f ../../api/integration/dev/docker-compose.yml up -d
   ```

2. Register your prepared chatbot service (Simulate provider behavior). As an example, here is a configuration for using the openAI api as service.

   ```bash
   curl -X POST http://localhost:8080/v1/service -H "Content-Type: application/json" -d '{
       "URL": "https://api.openai.com/v1",
       "inputPrice": 1,
       "outputPrice": 3,
       "Type": "chatbot",
       "Name": "chatopenai"
   }'
   ```

3. Add a account to use the service (Simulate user behavior)

   ```bash
   curl -X POST http://localhost:1034/v1/provider -d '{"provider": "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266", "balance": 10000}'
   ```

4. Start chatbot UI

   Here we use the Web UI from open source AI chat platform LibreChat as demo. The corresponding image was built from https://github.com/Ravenyjh/LibreChat

   1. Modify the `librechat.yaml` to add your custom endpoints. According to the service registered above, here is the corresponding configuration:

      Assume your IP address is `192.168.2.1`, use `192.168.2.1` to represent IP address.

      ```yaml
      - name: "0g-serving"
        apiKey: "${0G_SERVING_API}"
        baseURL: "http://192.168.2.1:1034/v1/provider/0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266/service/chatopenai/"
        models:
        default: ["gpt-3.5-turbo"]
        fetch: false
        titleConvo: true
        titleModel: "gpt-3.5-turbo"
        modelDisplayLabel: "0g-serving"
      ```

   2. Copy .env.example and rename to .env if it doesn’t already exist. According to the config above, the environment variable 0G_SERVING_API is expected and should be set:

      ```bash
      0G_SERVING_API=your_api_key
      ```

   3. Start server

      ```bash
      docker compose up -d
      ```

      Server listening on all interfaces at port 3080. Use `http://localhost:3080` to access it
