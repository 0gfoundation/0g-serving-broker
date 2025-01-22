# Mock fine-tuning execution image

1. Build

   ```bash
   docker build -t mock-fine-tuning .
   ```

2. Run

   ```bash
   curl -X POST http://127.0.0.1:8081/v1/task -d '{
      "UserAddress": "0xabc",
      "PreTrainedModelHash": "0x7f2244b25cd2219dfd9d14c052982ecce409356e0f08e839b79796e270d110a7",
      "DatasetHash": "0xaae9b4e031e06f84b20f10ec629f36c57719ea512992a6b7e2baea93f447a5fa",
      "ServiceName": "fine-tune-service1",
      "TrainingParams": "{}",
      "Fee": "123",
      "Nonce": "nonce",
      "Signature": ""
   }'


   curl -X POST  http://192.168.1.104:8081/v1/user/0x960E74Fc0AF1a6fBcADA3eEFCBe3152fA5E87A52/task -d '{
   "userAddress": "0x960E74Fc0AF1a6fBcADA3eEFCBe3152fA5E87A52",
   "serviceName": "fine-tune-service1",
   "datasetHash": "0xaae9b4e031e06f84b20f10ec629f36c57719ea512992a6b7e2baea93f447a5fa",
   "trainingParams": "{}\n",
   "preTrainedModelHash": "0x7f2244b25cd2219dfd9d14c052982ecce409356e0f08e839b79796e270d110a7",
   "fee": "5",
   "nonce": "0",
   "signature": "0x"
   }'
   ```

3. Get task

   ```bash
   curl -X GET http://127.0.0.1:8081/v1/task/<task_id>
   ```

4. Get task result

   ```bash
   curl -X GET http://127.0.0.1:8081/v1/task-progress/<task_id>
   ```
