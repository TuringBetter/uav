# Gossip 状态同步收敛测试

## 一、测试场景

**场景名称：** 5 架无人机编队位置同步

**场景描述：**  
模拟一个 5 节点全连接无人机编队。每架无人机拥有自身的位置信息 `pos_X`（含坐标 x/y/z），通过 Gossip 协议将自身状态同步到集群中所有其他节点。测试目标是验证 Gossip 算法的**状态收敛能力**并采集通信指标。

```
    Node 1 ──── Node 2
    │ ╲     ╱  │
    │   Node 5   │
    │ ╱     ╲  │
    Node 3 ──── Node 4
```
**拓扑：** 全连接 (Full Mesh)，5 个节点互为 Peer

---

## 二、节点配置

| 参数 | 值 |
|------|-----|
| 节点数量 | 5 |
| 传输协议 | UDP |
| Gossip Fanout | 3（每轮选 3 个随机 Peer 推送） |
| Gossip Interval | 200ms（每 200ms 一轮） |
| 收敛超时 | 15s |
| 发送队列 | 512 |
| 接收缓冲 | 256 |

---

## 三、初始状态与目标状态

### 初始状态
每个节点仅拥有自己的位置 key：

| 节点 | 本地数据 |
|------|---------|
| Node 1 | `pos_1 = x=10.0,y=5.0,z=100.0` |
| Node 2 | `pos_2 = x=20.0,y=10.0,z=100.0` |
| Node 3 | `pos_3 = x=30.0,y=15.0,z=100.0` |
| Node 4 | `pos_4 = x=40.0,y=20.0,z=100.0` |
| Node 5 | `pos_5 = x=50.0,y=25.0,z=100.0` |

### 目标状态（收敛）
**所有 5 个节点均拥有全部 5 个 key（pos_1 ~ pos_5），值一致。**

收敛判定条件：
```
对于每个节点 N:
    len(N.state) == 5  &&  所有 key 的 value 与源节点一致
```

---

## 四、运行方式

### 一键运行（推荐）
```bash
cd /home/xr/workspace/uav
go run ./cmd/gossip_bench
```

### 自定义参数
```bash
go run ./cmd/gossip_bench -nodes=7 -fanout=2 -interval=300ms -timeout=30s
```

参数说明：
| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-nodes` | 5 | 节点数量 |
| `-fanout` | 3 | 每轮 Gossip 目标数 |
| `-interval` | 200ms | Gossip 轮次间隔 |
| `-timeout` | 15s | 最大等待时间 |

---

## 五、实际运行结果

以下为 默认参数 (5 节点, fanout=3, interval=200ms) 的实测结果：

### 5.1 收敛时间
```
✅ 全部节点收敛！耗时 = 450.09ms
```

### 5.2 各节点指标

| 节点 | 发送消息 | 发送字节 | 接收消息 | 接收字节 | 去重丢弃 | TTL丢弃 | 平均延迟 |
|------|---------|---------|---------|---------|---------|---------|---------|
| 1 | 9 条 | 1557 B | 8 条 | 1700 B | 0 | 0 | 0.1ms |
| 2 | 9 条 | 1836 B | 9 条 | 1506 B | 0 | 0 | 0.4ms |
| 3 | 9 条 | 1698 B | 11 条 | 2171 B | 0 | 0 | 0.4ms |
| 4 | 9 条 | 1701 B | 8 条 | 1425 B | 0 | 0 | 0.1ms |
| 5 | 9 条 | 1698 B | 9 条 | 1643 B | 0 | 0 | 0.3ms |

### 5.3 汇总

| 指标 | 值 |
|------|-----|
| 收敛时间 | 450ms |
| 总发送消息 | 45 条 |
| 总接收消息 | 45 条 |
| 总发送字节 | 8490 B (8.29 KB) |
| 总接收字节 | 8445 B (8.25 KB) |
| 平均每节点发送 | 9.0 条 / 1.66 KB |
| 去重丢弃总计 | 0 |
| TTL过期总计 | 0 |
| 队列溢出总计 | 0 |
| 发送失败总计 | 0 |
| 共识成功 | ✅ |

### 5.4 JSON 输出示例 (Node 1)
```json
{
  "node_id": 1,
  "uptime_sec": 0.651,
  "messages_sent": 9,
  "messages_recv": 8,
  "bytes_sent": 1557,
  "bytes_recv": 1700,
  "msg_sent_per_sec": 13.82,
  "msg_recv_per_sec": 12.29,
  "msg_sent_by_type": { "STATE": 9 },
  "msg_recv_by_type": { "STATE": 8 },
  "dedup_dropped": 0,
  "ttl_expired_dropped": 0,
  "drop_rate": 0,
  "avg_latency_ms": 0.125,
  "max_latency_ms": 1,
  "p50_latency_ms": 0,
  "p95_latency_ms": 0,
  "p99_latency_ms": 0
}
```

每个节点运行后自动生成 `gossip_bench_nodeX.json`，可用于后续数据分析。

---

## 六、测试程序源码

测试程序位于 `cmd/gossip_bench/main.go`，核心流程：

```
1. 创建 N 个节点（UDP 传输 + Gossip 算法 + Metrics 采集器）
2. 全连接组网（Peer 互相注册）
3. 每个节点写入自身位置信息
4. 每 50ms 检查所有节点是否拥有完整状态
5. 收敛后打印详细指标报告
6. 导出 JSON 快照文件
```

程序在单进程内运行所有节点，无需手动开多个终端。

---

## 七、测试分析

### 收敛速度
- 5 节点 / fanout=3 / interval=200ms 下，约 **2-3 轮 Gossip** 即完成收敛（450ms ≈ 2 × interval）
- Fanout=3 意味着每轮每个节点选 3/4 ≈ 75% 的 Peer 推送，收敛很快

### 通信开销
- 总共 45 条消息 / 8.29 KB 即完成 5 节点状态同步
- 平均每节点仅 1.66 KB，通信开销极低

### 本地环境特点
- 延迟极低（<1ms），因为是同机 UDP 通信
- 无丢包/无去重/无 TTL 过期（无弱网模拟）

### 后续对比实验建议
1. 在同一框架下测试 **WeakNet** 算法的收敛时间和通信开销
2. 用 `tc` 注入延迟/丢包后重新测试，观察去重丢弃和收敛时间变化：
   ```bash
   # 模拟 50ms 延迟 + 10% 丢包
   sudo tc qdisc add dev lo root netem delay 50ms loss 10%
   go run ./cmd/gossip_bench
   sudo tc qdisc del dev lo root
   ```
3. 增大节点数（7、10、20）观察通信开销的缩放趋势
