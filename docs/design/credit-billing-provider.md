# 链下 Credit 计费 Provider 设计(链上身份 + 链下 credit + TEE 签名回执)

> 状态:MVP 设计 + 组件已实现。目标是**最快跑通**:最大化复用现有 broker / CLI 的链上身份与 API key 机制,
> 只把"结算"这一步换成"调用中心化 credit 服务,凭 TEE 签名回执扣费"。
>
> ## 实现状态(已构建并通过编译/测试)
>
> | 组件 | 位置 | 状态 |
> |---|---|---|
> | **Credit 服务**(全新,独立 repo) | `/home/raven/workspace/0g/0g-credit-service`(分支 `main`) | ✅ 编译/vet/测试通过。balance / deduct(验签+原子扣+幂等) / receipt / admin topup 全部实现 |
> | **Broker `creditbilling` 包**(回执签名 + credit 客户端 + USD 费用计算) | broker `api/inference/internal/creditbilling/` | ✅ 编译/测试通过 |
> | **Broker 配置** `CreditBillingConfig` | broker `api/inference/config/config.go` | ✅ 编译通过,默认 `enable=false`(链上用户零影响) |
> | **SDK credit 模式开关** `setCreditMode({skipAutoFunding, skipAcknowledgement})` | user-broker `src.ts/sdk/inference/broker/`(分支 `raven/credit-billing`) | ✅ tsc 通过 |
> | **跨 repo 互操作锁定** | 两侧共享 canonical 文本测试向量 + 签名/验签往返测试 | ✅ 两侧测试通过,格式漂移会被测试捕获 |
> | **Broker 活路径接线**(admission 查余额 / 响应后签回执+扣费) | broker 各服务 handler(chatbot/stt/image/video)+ settlement | ⏳ **唯一剩余步骤**,见 §10、§11 |
>
> 接线之所以单列:它要逐个改 broker 既有的多服务计费活路径(含流式),应当 config-gated + 配合团队 review,
> 不宜盲改以免影响链上用户。组件已就绪,接线即"在两处调用已实现的 `creditbilling` API"。

## 1. 目标与范围

**要做的:**
- 一个代理上游模型服务的 provider(下称"本 Provider"),**隐藏上游域名**。
- 用户仍用现有 CLI / SDK(钱包 + 链上 session token)鉴权、创建 / 吊销 API key。
- 计费走**链下中心化 credit 服务**:用户预付 USD → credit;每请求按 `fee = usage × price` 扣减。
- 计费**不可被乱收费**:Provider broker 在 TEE 内对每笔费用签名,credit 服务**验签后才扣**,签名回执留存供用户审计。
- 本 Provider **不被 owner acknowledge** → 对一般 CLI / SDK / router 用户**不可见**。

**MVP 不做(后续):**
- 自助 USD 充值(Stripe 等);MVP 由运营手动充值。
- 链上结算 / USDC / 跨 provider 通用 credit。
- 退款 / 争议工作流(仅保留可审计回执)。

**明确接受的取舍:**
- credit 服务是**中心化的单点信任 + 单点故障**。MVP 接受,靠 TEE 签名把"不能乱收费"找回来,
  并对故障采取 fail-closed 策略。
- credit **仅对本 Provider 有效**(per-provider),不跨 provider。

## 2. 架构总览

```
用户(CLI / SDK,钱包私钥)
   │  Authorization: Bearer app-sk-<base64(sessionToken|签名)>   ← 现有链上机制,不变
   ▼
┌─────────────────────────────────────────────────────────┐
│  Provider Broker(运行在 TEE enclave 内)                  │
│  1. ValidateSession(钱包签名)→ userAddr     [复用]       │
│  2. admission:调 credit 服务查余额,不足则拒  [新增]       │
│  3. 转发到上游(URL 仅 enclave 配置,不暴露)  [复用代理]   │
│  4. 解析 usage → fee = usage × price          [复用]       │
│  5. TEE 签回执:(user,provider,reqHash,respHash,           │
│     usage,fee,nonce,ts)                        [复用 SyncQuote/Sign] │
│  6. 调 credit 服务 deduct(回执 + 签名)        [新增]       │
└───────────────┬─────────────────────────────────────────┘
                │ (TEE 签名的扣费请求)
                ▼
┌─────────────────────────────────────────────────────────┐
│  Credit 服务(中心化,独立配置的认证域名)                 │
│  - 验签(对链上 teeSignerAddress)→ 原子 check-and-deduct  │
│  - 存签名回执(审计)、credit 流水                         │
│  - 运营充值入口(USD → credit)                            │
└─────────────────────────────────────────────────────────┘

链上(合约,同一份):
  - 本 Provider 的 service:addOrUpdateService 注册一次,**不 acknowledge** → 隐藏 + 公布 teeSignerAddress
  - 用户:链上账户,仅用于**身份 + API key 吊销**(generation / bitmap),不存计费资金
```

## 3. 鉴权与身份(全部复用现有链上机制)

- **身份 = 钱包地址**;用 CLI 需要 wallet private key。
- **API key = 现有 session token**(`app-sk-<base64(token|签名)>`),本地钱包签名生成。
- **创建 key**:CLI 用钱包签一个 session token(持久 token 可设 `ExpiresAt=0`)。
- **吊销 key**:走链上 `generation`(批量)/ `RevokedBitmap`(精确)—— **需要用户有链上账户**。
  - 代价:吊销是一笔**链上交易**(gas + 确认延迟)。MVP 接受。
- **用户链上账户**:仅作身份 + 吊销注册表用,**不需要充值**。计费资金全在 credit 服务。

> 说明:provider 端 `ValidateSession` 是纯签名校验;`validateTokenRevocation` 在链上查不到账户时
> 默认放行(generation=0)。要让吊销**生效**,用户必须有链上账户并设置 generation/bitmap。

## 4. 计费流程(逐步)

**Admission(转发前):**
1. `ValidateSession` → `userAddr`。
2. 调 credit 服务 `GET /balance?user=&provider=`,余额不足阈值 → 拒(`insufficient_credit`,标记为用户侧错误,不计入健康指标)。
   - 可对余额做**短 TTL 缓存**降低延迟;权衡见 §9。

**转发:** 现有 decentralized 代理模式,上游 URL 仅 enclave 配置,**不暴露**。

**Post-response(拿到响应、fee 已知;流式在流结束时):**
3. 解析 usage → `fee = usage × price`(USD 计价,复用 broker)。
4. 构造回执 `Receipt = {user, provider, reqHash=sha256(req), respHash=sha256(resp), usage, price, fee, nonce, ts}`。
5. TEE 签名:`sig = Sign(keccak256(canonical(Receipt)))`,签名 key 即链上 `teeSignerAddress`(复用 `common/tee` 的 `SyncQuote`/`Sign`)。
6. 调 credit 服务 `POST /deduct {receipt, sig}`:
   - 验签(对链上 `teeSignerAddress`)。
   - 校验 `nonce` 单调递增(防回执重放重复扣)。
   - **原子** check-and-deduct;余额不足时扣到 0 并标记欠费(见 §9 单请求封顶)。
   - 存回执 + 写 credit 流水,返回新余额。

## 5. 可验证性(把"不能乱收费"找回来)

- Provider 在 enclave 内派生签名 key,地址写入 TDX quote 的 `report_data`,并注册到链上 `teeSignerAddress`。
- **credit 服务**:只接受验签通过(对链上 `teeSignerAddress`)的扣费 → 即使 broker→服务通道被冒用,也无法乱扣。
- **用户审计**:任意时刻可取回执,
  1. 验签 recover 地址 == 链上 `teeSignerAddress`(出自被 attest 的 enclave);
  2. `sha256(自己手里的请求/响应)` == 回执哈希(账对应这次交互);
  3. `usage × 公开 price` == 回执 fee(没多收 / 没偷换价)。
- **一次性**:验 Provider 的 `/attestation`(TDX quote)→ 确认 `teeSignerAddress` 属于跑着已知度量值 M 的真 enclave。
  - **前提(真正工作量)**:计量代码可复现构建 + 公布预期 M,否则 attestation 只证"没被改",证不了"不黑"。

## 6. 数据模型(credit 服务)

- `balance`:`(user_addr, provider_addr)` → credit(USD,decimal), updated_at
- `receipt`:id, user, provider, req_hash, resp_hash, usage, price, fee, nonce, ts, sig, applied
- `credit_tx`:user, provider, type(topup/debit/refund), amount, ref, ts
- `nonce_state`:`(user_addr, provider_addr)` → last_nonce(防重放)

## 7. 接口

**Credit 服务:**
```
GET  /balance?user=&provider=        → 余额        (调用方:broker / 用户本人)
POST /deduct {receipt, sig}          → 验签+原子扣  (调用方:仅本 Provider TEE,凭 TEE 签名)
GET  /receipt/{id}                   → 签名回执     (公开 / 用户)
POST /admin/topup {user, amount}     → USD→credit  (调用方:运营,内部密钥)
```

**Provider broker(对用户):**
```
POST /v1/chat/completions ...        → 代理转发      (inference,session token)
GET  /attestation                    → TDX quote     (公开)
```

## 8. 链上动作

- **本 Provider 注册一次**:`addOrUpdateService`,填 `{url=Provider自己的入口, model, price, teeSignerAddress=回执签名key, additionalInfo}`。
  - **不争取 owner acknowledge** → 标准客户端按 `teeSignerAcknowledged==true` 过滤掉它 → 一般用户看不到。
  - `serviceExists=true` 但未 ack → 链上结算关闭(本就不用),`teeSignerAddress` 公布供验签。
- **用户**:创建链上账户(身份 + 吊销);无需充值。

## 9. 已知缺陷与对策

| 缺陷 | 对策(MVP) |
|---|---|
| credit 服务单点信任 + 故障 | 接受;TEE 签名保证不可乱扣;故障时 **fail-closed**(查不到余额则拒绝服务,不免费放行);余额短 TTL 缓存平滑抖动 |
| check-then-deduct 竞态 / 超扣 | credit 服务侧**原子** check-and-deduct;`nonce` 单调防重放 |
| fee 响应后才知,admission 只能估 | 按当前余额给**单请求封顶**(超限直接拒/截断),避免负余额亏损 |
| 热路径多一次同步往返(查余额) | admission 余额短 TTL 缓存;deduct 在响应后,不阻塞首字节 |
| 吊销 API key 需链上交易(gas/延迟) | 接受;沿用现有链上 generation/bitmap 机制 |
| 通道鉴权 | deduct 以 **TEE 签名**为准(验链上 teeSignerAddress);可叠加网络 ACL |

## 10. 改动清单

**Provider broker(基于现有 inference broker,在 `provider-ambr` worktree 改):**
- 配置:`creditBilling.{enable,endpoint,timeout,minBalanceMicroUsd}`(已加于 `config.go`);`providerType=decentralized`、`targetUrl=上游`。
- 已实现可直接调用的组件(`internal/creditbilling/`):
  - `creditbilling.NewClient(endpoint, timeout)` → `.Balance(ctx, user, provider)` / `.Deduct(ctx, receipt, sigHex)`
  - `creditbilling.Sign(teeService, receipt)` → 用 `common/tee` 的 TEE key 签回执(与 credit 服务验签互通)
  - `creditbilling.FeeMicroUsd(in, out, inPriceMicroPerMillion, outPriceMicroPerMillion)` / `UsdDecimalToMicro(usd)`
- **接线点(剩余步骤)**:
  1. **admission**:在 `internal/ctrl/request.go` 的 `validateBalanceAdequacy` 加 `if c.Service.CreditBilling.Enable` 分支,改调 `client.Balance` 并按 `minBalanceMicroUsd` fail-closed;链上分支保持原样。
  2. **响应后扣费**:在各服务签名处(`internal/ctrl/signing.go` 的 `signChatWithKey` / `signImageResponse` 调用点,以及 stt/video 流程)拿到 `reqBody/respData/usage` 后,构造 `creditbilling.Receipt`(fee 用 `FeeMicroUsdString`,price 由 USD 配置经 `UsdDecimalToMicro` 得到),`Sign` 后 `client.Deduct`;并对该用户跳过链上结算入队(`CreateRequest`/settlement)。
- 复用:usage 解析、`common/tee` 的 `SyncQuote`/`Sign`/`/attestation`(后者即可验性所需 quote)。

**CLI / SDK(0g-serving-user-broker,`raven/credit-billing` worktree)— ✅ 已实现:**
- `broker.inference.setCreditMode({ skipAutoFunding, skipAcknowledgement })`(`base.ts` / `request.ts` / `broker.ts`)。
- `skipAutoFunding`:跳过 `checkAndFund` 的链上转账,否则 credit 用户被 `Insufficient balance in ledger` 卡死。
- `skipAcknowledgement`:链下计费下 on-chain acknowledge 无意义,跳过 `userAcknowledged` 检查,用户只需"链上账户(供吊销)+ 钱包"。
- 其余(session token 生成、签名、请求)不变;默认两者皆 false,链上用户零影响。

**Credit 服务(`/home/raven/workspace/0g/0g-credit-service`)— ✅ 已实现:**
- §6 数据模型 + §7 接口;原子 check-and-deduct(行级锁)+ 按 `receiptId` 幂等 + 验签 + 回执存储 + 运营充值。

## 11. 落地顺序

1. credit 服务:`balance` / `deduct`(验签 + 原子扣 + nonce) / `receipt` / `admin/topup`。
2. Provider broker:admission 改为查 credit 服务;post-response 改为 TEE 签回执 → deduct。
3. SDK:加 `skipAutoFunding`(+ `skipAcknowledgement`)。
4. 链上:Provider 注册(不 ack);用户账户创建流程。
5. attestation 暴露 + 计量代码可复现构建 + 公布预期度量值 M。

## 12. 待确认

- 充值:MVP 手动 `admin/topup`,后续接 Stripe。
- 单请求封顶策略:超额是拒绝还是按余额截断。
- credit 服务故障策略:确认 fail-closed(默认)。
- 回执签名格式:MVP 自定义 canonical;是否预留 EIP-712 `TEESettlementData` 以便将来链上结算。
