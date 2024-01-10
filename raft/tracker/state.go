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

// StateType is the state of a tracked follower.
type StateType uint64

const (
	// StateProbe indicates a follower whose last index isn't known.
	// StateProbe 状态表示，丢失了该 follower 的最新 index。
	// Such a follower is "probed" (i.e. an append sent periodically) to narrow down
	// its last index. In the ideal (and common) case, only one round of probing
	// is necessary as the follower will react with a hint. Followers that are
	// probed over extended periods of time are often offline.
	// 理想情况下，只需要一次探测，因为 follower 会在 response 中返回它的 last index。
	// 持续处于 StateProbe 状态的 follower 通常是 offline 的。
	StateProbe StateType = iota
	// StateReplicate is the state steady in which a follower eagerly receives
	// log entries to append to its log.
	// follower 稳定接受来自 leader 的数据，并 append 到自己的 log
	StateReplicate
	// StateSnapshot indicates a follower that needs log entries not available
	// from the leader's Raft log. Such a follower needs a full snapshot to
	// return to StateReplicate.
	// 接收快照状态。
	StateSnapshot
)

var prstmap = [...]string{
	"StateProbe",
	"StateReplicate",
	"StateSnapshot",
}

func (st StateType) String() string { return prstmap[uint64(st)] }
