// Copyright 2019 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tracker

import (
	"fmt"
	"sort"
	"strings"
)

// Progress represents a follower’s progress in the view of the leader.
// Progress 表示从 Leader 视角看到的某个 follower 的状态。
// Leader 拥有所有 Follower 的进度，并根据其进度向 Follower 发送日志。

// Leader maintains progresses of all followers, and sends entries to the follower
// based on its progress.
//
// NB(tbg): Progress is basically a state machine whose transitions are mostly
// strewn around `*raft.raft`. Additionally, some fields are only used when in a
// certain State. All of this isn't ideal.
// Progress 基本上是一个围绕 *raft.raft 做状态转换的状态机。另外，其中部分成员只在特定状态下使用。
type Progress struct {
	// Match 已复制给该 follower 的日志的最高索引值
	// Next 保存下一次 leader 发送 append 消息给该 follower 的日志索引，即下一次复制日志时，leader 会从 Next 开始发送日志。
	// 在正常情况下，Next = Match + 1

	// (0, Next) 的日志已经发送给 follower 了，(0,Match] 是 follower 已经接收到的日志
	Match, Next uint64
	// State defines how the leader should interact with the follower.
	// State 定义了 leader 应该如何与 follower 的交互。
	//
	// When in StateProbe, leader sends at most one replication message
	// per heartbeat interval. It also probes actual progress of the follower.
	// - StateProbe 状态，leader 每次最多 append 一条日志（ 需要试探 ），这也是 follower 初始状态。
	//   如果收到的回应中带有 RejectHint 信息，则回退 Next 索引，以便下次重试。
	//
	// When in StateReplicate, leader optimistically increases next
	// to the latest entry sent after sending replication message. This is
	// an optimized state for fast replicating log entries to the follower.
	// - StateReplicate 状态，leader 在发送完 replication 消息后，乐观地递增最新的 entry 。
	//
	// When in StateSnapshot, leader should have sent out snapshot
	// before and stops sending any replication message.
	// - StateSnapshot 状态，leader 应该已经发出了 snapshot，并停止发送 replication 消息。
	//  当 follower 需要的日志已经写入快照了。当快照数据同步追上之后，并不是直接切换到 Replicate 状态，而是首先切换到Probe状态。
	State StateType

	// PendingSnapshot is used in StateSnapshot. StateSnapshot 状态时使用。
	// If there is a pending snapshot, the pendingSnapshot will be set to the index of the snapshot.
	// 如果正在发送 snap，pendingSnapshot 会被置为其 index。
	//
	// If pendingSnapshot is set, the replication process of this Progress will be paused.
	// 如果 pendingSnapshot 被设置，则该 Progress 的同步过程会停止。
	//
	// raft will not resend snapshot until the pending one is reported to be failed.
	// 当前发送 snap 失败，raft 会重发 snapshot。
	PendingSnapshot uint64

	// RecentActive is true if the progress is recently active. Receiving any messages
	// from the corresponding follower indicates the progress is active.
	// RecentActive can be reset to false after an election timeout.
	//
	// TODO(tbg): the leader should always have this set to true.
	// 如果该 Progress 近处于活跃状态，则为 true。在选举超时后，重置为 false。
	RecentActive bool

	// ProbeSent is used while this follower is in StateProbe. When ProbeSent is
	// true, raft should pause sending replication message to this peer until
	// ProbeSent is reset. See ProbeAcked() and IsPaused().
	// ProbeSent 用在 StateProbe 状态。
	// 如果 ProbeSent 为 true，raft 应该停止发送 replication 消息，直至 ProbeSent 被重置。（ 函数 ResetState ）
	ProbeSent bool

	// Inflights is a sliding window for the inflight messages.
	// Inflights 是一个滑动窗口，用来做流量控制。
	//
	// Each inflight message contains one or more log entries.
	// 每个 inflight 消息包含一个或多个日志条目.
	//
	// The max number of entries per message is defined in raft config as MaxSizePerMsg.
	// 每条消息中包含的日志条目数量使用 raft 配置 MaxSizePerMsg 指定。
	//
	// Thus inflight effectively limits both the number of inflight messages
	// and the bandwidth each Progress can use.
	// 因此，inflight 有效限制了 inflight 消息数量和每个 Progress 可以使用的带宽。
	//
	// When inflights is Full, no more message should be sent.
	// 当 inflights 满了时，不能再发送任何消息。
	//
	// When a leader sends out a message, the index of the last entry should be added to inflights.
	// The index MUST be added into inflights in order.
	// 当 leader 发出一个消息，最新的 entry 索引必须加到 inflights，索引必须按顺序添加。
	//
	// When a leader receives a reply, the previous inflights should
	// be freed by calling inflights.FreeLE with the index of the last received entry.
	// 当 leader 收到一条回复，之前的 inflights 中相应的 entry 应该通过调用 inflights.FreeLE 释放。
	Inflights *Inflights

	// IsLearner is true if this progress is tracked for a learner.
	IsLearner bool
}

// ResetState moves the Progress into the specified State, resetting ProbeSent,
// PendingSnapshot, and Inflights.
func (pr *Progress) ResetState(state StateType) {
	pr.ProbeSent = false
	pr.PendingSnapshot = 0
	pr.State = state
	pr.Inflights.reset()
}

func max(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func min(a, b uint64) uint64 {
	if a > b {
		return b
	}
	return a
}

// ProbeAcked is called when this peer has accepted an append. It resets
// ProbeSent to signal that additional append messages should be sent without
// further delay.
// ProbeAcked 在该 peer 成功接受一个 append 消息时调用。
// 它重置了 ProbeSent，意在告知，额外的 append 消息可以发送了。
func (pr *Progress) ProbeAcked() {
	pr.ProbeSent = false
}

// BecomeProbe transitions into StateProbe. Next is reset to Match+1 or,
// optionally and if larger, the index of the pending snapshot.
func (pr *Progress) BecomeProbe() {
	// If the original state is StateSnapshot, progress knows that
	// the pending snapshot has been sent to this peer successfully, then
	// probes from pendingSnapshot + 1.
	if pr.State == StateSnapshot {
		pendingSnapshot := pr.PendingSnapshot
		pr.ResetState(StateProbe)
		pr.Next = max(pr.Match+1, pendingSnapshot+1)
	} else {
		pr.ResetState(StateProbe)
		pr.Next = pr.Match + 1
	}
}

// BecomeReplicate transitions into StateReplicate, resetting Next to Match+1.
func (pr *Progress) BecomeReplicate() {
	pr.ResetState(StateReplicate)
	pr.Next = pr.Match + 1
}

// BecomeSnapshot moves the Progress to StateSnapshot with the specified pending
// snapshot index.
func (pr *Progress) BecomeSnapshot(snapshoti uint64) {
	pr.ResetState(StateSnapshot)
	pr.PendingSnapshot = snapshoti
}

// MaybeUpdate is called when an MsgAppResp arrives from the follower, with the
// index acked by it. The method returns false if the given n index comes from
// an outdated message. Otherwise it updates the progress and returns true.
func (pr *Progress) MaybeUpdate(n uint64) bool {
	var updated bool
	if pr.Match < n { // peer 收到了消息
		pr.Match = n // Match 更新到最新
		updated = true
		pr.ProbeAcked() // raft 可以把日志消息、心跳消息当做探测消息
	}
	// peer 怎么可能会回复一个比 Next 更大的日志索引呢？
	// 在系统初始化时或每轮选举完成后，新 Leader 还不知道 Peer 接收的最大日志索引，所以此时的 Next 还是个初识值
	pr.Next = max(pr.Next, n+1)
	return updated
}

// OptimisticUpdate signals that appends all the way up to and including index n
// are in-flight. As a result, Next is increased to n+1.
func (pr *Progress) OptimisticUpdate(n uint64) { pr.Next = n + 1 }

// MaybeDecrTo adjusts the Progress to the receipt of a MsgApp rejection.
// MaybeDecrTo 根据 MsgApp 拒绝的情况，调整 Progress。
//
// The arguments are the index of the append message rejected by the follower,
// and the hint that we want to decrease to.
// 参数是被 follower 拒绝的 append 消息 index，以及我们想到缩减到的目标值。
//
// Rejections can happen spuriously as messages are sent out of order or duplicated.
// 由于消息的乱序或重复发送，可能会出现拒绝的情况。

// In such cases, the rejection pertains to an index that the
// Progress already knows were previously acknowledged, and false is returned
// without changing the Progress.
//
// If the rejection is genuine, Next is lowered sensibly, and the Progress is
// cleared for sending log entries.
func (pr *Progress) MaybeDecrTo(rejected, matchHint uint64) bool {
	// 复制状态下 Match 是有效的，可以通过 Match 判断拒绝的日志是否已经无效了
	if pr.State == StateReplicate {
		// The rejection must be stale if the progress has matched and "rejected"
		// is smaller than "match".
		if rejected <= pr.Match {
			return false
		}
		// Directly decrease next to match + 1.
		//
		// TODO(tbg): why not use matchHint if it's larger?
		pr.Next = pr.Match + 1
		return true
	}

	// The rejection must be stale if "rejected" does not match next - 1. This
	// is because non-replicating followers are probed one entry at a time.
	if pr.Next-1 != rejected {
		return false
	}

	// 根据 peer 的反馈调整 Next
	pr.Next = max(min(rejected, matchHint+1), 1)
	pr.ProbeSent = false
	return true
}

// IsPaused returns whether sending log entries to this node has been throttled.
// IsPaused 返回发送日志条目到该节点是否被阻塞住了。
// 暂停即表示 Leader不能再向 peer 发日志消息了，必须等待 peer 回复打破这个状态。
//
// This is done when a node has rejected recent MsgApps,
// is currently waiting for a snapshot, or has reached the MaxInflightMsgs limit.
// 以下三种情况会触发该操作：
// - 当一个节点拒绝了最近的 MsgApps 消息，
// - 正在等待一个 snapshot，
// - 达到了 MaxInflightMsgs 限制
//
// In normal operation, this is false. A throttled node will be contacted less frequently
// until it has reached a state in which it's able to accept a steady stream of
// log entries again.
// 在正常的操作中，这个值为 false。
func (pr *Progress) IsPaused() bool {
	switch pr.State {
	case StateProbe:
		return pr.ProbeSent
	case StateReplicate:
		return pr.Inflights.Full()
	case StateSnapshot:
		return true
	default:
		panic("unexpected state")
	}
}

func (pr *Progress) String() string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "%s match=%d next=%d", pr.State, pr.Match, pr.Next)
	if pr.IsLearner {
		fmt.Fprint(&buf, " learner")
	}
	if pr.IsPaused() {
		fmt.Fprint(&buf, " paused")
	}
	if pr.PendingSnapshot > 0 {
		fmt.Fprintf(&buf, " pendingSnap=%d", pr.PendingSnapshot)
	}
	if !pr.RecentActive {
		fmt.Fprintf(&buf, " inactive")
	}
	if n := pr.Inflights.Count(); n > 0 {
		fmt.Fprintf(&buf, " inflight=%d", n)
		if pr.Inflights.Full() {
			fmt.Fprint(&buf, "[full]")
		}
	}
	return buf.String()
}

// ProgressMap is a map of *Progress.
type ProgressMap map[uint64]*Progress

// String prints the ProgressMap in sorted key order, one Progress per line.
func (m ProgressMap) String() string {
	ids := make([]uint64, 0, len(m))
	for k := range m {
		ids = append(ids, k)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})
	var buf strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&buf, "%d: %s\n", id, m[id])
	}
	return buf.String()
}
