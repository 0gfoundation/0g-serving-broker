# 认证信任链（中文临时版）

> 本文只写**最终方案**和**信任链**，不写推导过程。
> 对应英文版 `doc/attestation-trust-chain.md`。

要在两个约束下成立：

- 请求经过一个**非 TEE、且不可信的 router**
- **broker 组件可以就地升级**，而 CVM 的 `compose_hash` 不会因此改变

---

## 要向用户证明的那句话

> **这个 response 是在一个 TDX CVM 里、由我认得的那个 broker 镜像产生的 —— 而且是现在产生的，不是过去某个时刻。**

弱一点都没用：

- 只说「哪个**部署**」而不说「哪个**镜像**」—— 就地升级到任何东西都满足它
- 只说「过去某个时刻」—— 重放一份旧 attestation 就满足它

---

## 信任链

⚙ = 硬件或密码学保证　👤 = 用户的一次性人工检查

| # | 证明什么 | 靠什么 |
|---|---|---|
| 1 | attestation 是真的，且传输中没被改 | ⚙ quote 上的 DCAP 签名。router 转发得了，改不了。 |
| 2 | 这是**哪个部署** | ⚙ `compose_hash` 在被签名的报文体里（`mr_config_id[1:33]`），且 `sha256(tcb_info.app_compose) == compose_hash` —— 所以 quote **自带一份被认证的 compose 原文**。 |
| 3 | 就是我审过的那个部署 | 👤 用户把 `compose_hash` 和自己读过的那份 compose 的哈希比对。 |
| 4 | **只有 controller 能写账本** | 👤 从那份审过的 compose 里读出来：broker 没有挂 `/var/run/dstack.sock`，只有 controller 有。⚙ 改这一点就会改变 `compose_hash`，第 3 环抓到。 |
| 5 | broker 跑的是**哪个镜像** | ⚙ RTMR3 只能追加、且被签名覆盖。重放 runtime 事件，取最后一条 `zg-image-update`。第 4 环是这条记录可信的理由。 |
| 6 | 那个镜像是我认得的 | 👤 digest 与一个**随用户自己的软件分发**的集合比对，绝不从 provider 那里取。 |
| 7 | 这份 attestation 描述的是**现在** | ⚙ signer key 按镜像派生：`S = KDF(appKey, 当前镜像 digest)`。升级会换掉 `S`，于是**旧 quote 命名的那把 key 不再工作** —— 陈旧的 quote 自我失效。 |
| 8 | 这个 **response** 来自那个镜像 | ⚙ 每个 response 都带一个 `S` 对「实际发出的字节」的签名。`S` 由 controller 持有并代签，**私钥永不离开 controller**，所以任何 broker 镜像都无法把它留到升级之后。 |
| 9 | router 没改动、也没重放 | ⚙ 它没有 `S`。签名绑定的是这次 response 的字节，挪不到别的请求上。 |

1、2、5、7、8、9 是机械保证。3、4、6 是用户的活，而且是同一条纪律：

> **信任根跟着用户自己安装的软件走，绝不跟着被验证的一方走。**

---

## 为什么 router 不可信不影响

router 看到的是密文和签名。它能丢包、延迟、弄坏一个 response（用户会察觉的可用性故障），但它不能：

- **改动** response —— 它造不出 `S` 的签名
- **重放**旧 response —— 签名绑的是这次的字节
- **换成一份旧 attestation** —— 镜像一变，旧 quote 命名的 `S` 就验不过了（第 7 环）

所以 router 完全不需要被信任。它就是个传输层。

---

## 为什么 broker 可升级不影响

就地升级不改变 `compose_hash`，所以在开机 measurement 里看不见。方案不去改变这一点，而是让升级**可见**且**有约束力**：

- **可见**：controller 在变更**之前**把新引用记进 RTMR3，而且记录的时刻**没有任何 broker 在运行** —— 所以在任何一个能取到 quote 的瞬间，账本都是真的。每一条中止路径都**追加真相**，而不是留下一个陈旧的声明；当真相无法确定时，那条记录**故意不含 digest**，读取方会直接拒绝。
- **有约束力**：升级会换掉 `S`。所以还拿着上一份 attestation 的用户会突然**验不过签名**，被迫去重读账本。**一次升级无法从一个正在检查的用户眼前溜过去。**

被换过的 broker 镜像不会因为「不跑我们的代码」而占到便宜：它写不了账本（第 4 环）、拿不到之前的 `S`（第 8 环）、也无法自己派生密钥 —— 因为 controller 只提供 `GetQuote` 和 `Info`，**永不提供 `GetKey` 和 `EmitEvent`**。

---

## 各方必须诚实吗

| 谁 | 必须诚实？ |
|---|---|
| Intel TDX + DCAP | **必须** —— 密码学信任根 |
| dstack OS 镜像、KMS 的密钥释放策略 | **必须** —— 由 `mrtd` / `rtmr0-2` 和链上策略覆盖 |
| **controller 镜像** | **必须** —— 但被 `compose_hash` 钉死、用户审过、且它不能升级自己 |
| **broker 镜像** | **不必** ← 这就是整个设计的目标 |
| **router** | **不必** |
| provider 的宿主机、网络、DNS | **不必** |
| 用户自己的 SDK 和 digest 白名单 | **必须** —— 天然如此，信任根就住在这里 |

---

## 用户实际要做什么

1. **每个版本一次**：读 compose 文件，确认 broker 没有 dstack socket，记下 `compose_hash` 和可接受的 broker digest 集合。两者都进用户自己的软件。
2. **每个会话一次**：取 quote → 验 DCAP 签名 → 比对 `compose_hash` → 重放 RTMR3 → 读最后一条 `zg-image-update` → 比对 digest 白名单 → 从 `report_data` 取出 signer 地址。
3. **每个 response**：用那个 signer 地址验签名。

第 3 步失败 = **镜像变了**。用户回到第 2 步，自己决定认不认这个新 digest —— 这是**预期行为，不是错误**。

---

## 残留假设（如实列出）

- **第 1 步无法自动化掉。** 「这个部署有没有把写账本的能力收住」是 compose 文件的属性。验证方**无法从 compose 原文推导它**：容器能触达一个 socket 的写法是**开放集合**，而能定位 broker 的字段又不是 controller 实际使用的那些。所以它是一次**文档审阅**，而 `compose_hash` 让这次审阅对**所有后续版本持续有效**。
- **机密性是向前的。** 一个**当下**在跑的恶意镜像可以留存它此刻持有的密钥。按镜像派生保证的是：它无法在**升级之后**继续使用，也无法获得任何**其他版本**的密钥。
- **controller 进入了响应热路径。** 它对每个 response 走一次本地 socket 签名。controller 的可用性成为服务的可用性。
- **升级对用户不是透明的。** `teeSignerAddress` 或 `additionalInfo` 变化会重置 `Service.teeSignerAcknowledged`，而恢复是 `onlyOwner` —— **provider 自己救不了**。所以每一次就地升级都需要合约 owner 重新 ack。这是第 7 环的**预期代价**：一次用户不可能忽略的升级。

---

## 实现状态

| 环 | 位置 | 状态 |
|---|---|---|
| pull 正确性、升级入口只收 digest | `controller/internal/{docker,ctrl}` | 已合并（#622、#624） |
| controller 权限面运行时不可扩张、不能操作自己 | `controller/internal/{ctrl,docker}` | 已合并（#623） |
| broker 不持 docker socket，镜像身份来自环境变量 | `inference/internal/contract`、`controller/internal/docker` | #625 |
| 账本：变更前记账、中止时追加真相、串行化 | `controller/internal/ctrl` | #626 |
| 读取方：RTMR3 重放、quote 偏移、运行态解析 | `common/attest` | #627 |
| controller 不再有「改了行为却不留痕」的动作 | `controller/internal/{ctrl,handler}` | #635 |
| 升级后的容器不再冒充符合 compose 定义 | `controller/internal/docker` | #643 |
| controller 代发 quote，使 broker 能放弃 dstack socket | `controller/internal/attestproxy` | #644 |
| **按镜像派生密钥 + controller 代签（第 7、8 环）** | `common/tee`、`controller/internal/attestproxy` | **未开始** |

**最后一行落地之前，第 7、8 环不成立**：signer key 是按 `compose_hash` 派生的，能跨就地升级存活，所以**升级之前取的 attestation 仍然验得过**。

它上面的所有环今天都成立 —— 也就是**账本是诚实的**，但用户还**无法判断自己读到的这一份是不是当前的**。
