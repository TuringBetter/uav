// Package message defines the core Message structure used throughout the UAV
// distributed communication framework.
//
// Wire format (little-endian, 24-byte fixed header):
//
//	byte  0    : Type       (1 byte)
//	bytes 1–2  : From       (2 bytes)
//	bytes 3–4  : To         (2 bytes)
//	bytes 5–8  : Seq        (4 bytes)
//	bytes 9–16 : Timestamp  (8 bytes)
//	bytes 17–18: TTL        (2 bytes)
//	byte  19   : Priority   (1 byte)
//	bytes 20–23: PayloadLen (4 bytes)
//	bytes 24+  : Payload    (PayloadLen bytes)
package message

import "time"

// MessageType classifies a message for routing and handling purposes.
type MessageType uint8

const (
	TypeHeartbeat MessageType = 0 // HEARTBEAT — 心跳保活
	TypeState     MessageType = 1 // STATE     — 状态同步
	TypeControl   MessageType = 2 // CONTROL   — 控制指令
	TypeConsensus MessageType = 3 // CONSENSUS — 共识协议
	TypeCustom    MessageType = 4 // CUSTOM    — 算法自定义
)

// String returns a human-readable name for a MessageType.
func (t MessageType) String() string {
	switch t {
	case TypeHeartbeat:
		return "HEARTBEAT"
	case TypeState:
		return "STATE"
	case TypeControl:
		return "CONTROL"
	case TypeConsensus:
		return "CONSENSUS"
	case TypeCustom:
		return "CUSTOM"
	default:
		return "UNKNOWN"
	}
}

// Priority represents the scheduling priority of a message.
// Lower numeric value = higher priority (0 is highest).
type Priority uint8

const (
	PriorityCollision Priority = 0 // 避碰（最高优先级）
	PriorityState     Priority = 1 // 状态同步
	PriorityTask      Priority = 2 // 任务协调
	PriorityDebug     Priority = 3 // 调试信息（最低优先级）
)

// Common TTL values in milliseconds.
const (
	TTLCollision uint16 = 100  // 避碰信息有效期
	TTLState     uint16 = 500  // 状态信息有效期
	TTLDefault   uint16 = 1000 // 默认有效期
	TTLForever   uint16 = 0    // 0 = 永不过期
)

// BroadcastID is the To value that indicates a broadcast to all peers.
const BroadcastID uint16 = 0

// HeaderSize is the fixed-size binary encoding of all Message fields excluding
// the variable-length Payload bytes (PayloadLen field IS included in the header).
const HeaderSize = 1 + 2 + 2 + 4 + 8 + 2 + 1 + 4 // = 24 bytes

// Message is the fundamental communication unit in the UAV network.
// It carries a fixed binary header optimised for weak-network characteristics
// (deduplication, out-of-order recovery, expiry, priority scheduling) plus
// an opaque Payload defined by each algorithm layer.
type Message struct {
	Type      MessageType // 消息类型（路由依据）
	From      uint16      // 发送节点 ID（≥1；0 保留）
	To        uint16      // 目标节点 ID（0 = 广播）
	Seq       uint32      // 递增序列号（去重 / 乱序恢复）
	Timestamp int64       // 发送时间戳（Unix 毫秒）
	TTL       uint16      // 有效期（ms）；0 = 永不过期
	Priority  Priority    // 优先级（0 = 最高）
	Payload   []byte      // 算法自定义内容（由算法层编解码）
}

// IsExpired reports whether the message's TTL has elapsed since it was sent.
// Messages with TTL == 0 (TTLForever) never expire.
func (m *Message) IsExpired() bool {
	if m.TTL == 0 {
		return false
	}
	return time.Now().UnixMilli()-m.Timestamp > int64(m.TTL)
}

// IsBroadcast reports whether the message should be delivered to all peers.
func (m *Message) IsBroadcast() bool {
	return m.To == BroadcastID
}
