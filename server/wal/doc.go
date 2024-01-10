// Copyright 2015 The etcd Authors
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

/*
Package wal provides an implementation of a write ahead log that is used by
etcd.

A WAL is created at a particular directory and is made up of a number of
segmented WAL files. Inside of each file the raft state and entries are appended
to it with the Save method:

	metadata := []byte{}
	w, err := wal.Create(zap.NewExample(), "/var/lib/etcd", metadata)
	...
	err := w.Save(s, ents)

After saving a raft snapshot to disk, SaveSnapshot method should be called to
record it. So WAL can match with the saved snapshot when restarting.

	err := w.SaveSnapshot(walpb.Snapshot{Index: 10, Term: 2})

When a user has finished using a WAL it must be closed:

	w.Close()

Each WAL file is a stream of WAL records. A WAL record is a length field and a wal record
protobuf. The record protobuf contains a CRC, a type, and a data payload. The length field is a
64-bit packed structure holding the length of the remaining logical record data in its lower
56 bits and its physical padding in the first three bits of the most significant byte. Each
record is 8-byte aligned so that the length field is never torn. The CRC contains the CRC32
value of all record protobufs preceding the current record.
每个 WAL 文件是一个 WAL 记录的流。一个 WAL 记录由一个长度字段和一个 wal 记录的 protobuf 组成。
protobuf 包含一个 CRC、一个类型和一个数据负载。
长度字段是一个 64 位的打包结构，其低 56 位保存剩余逻辑记录数据的长度，最高有效字节的前 3 位保存物理填充。
为了保证长度字段的完整，每个记录都按照 8 字节进行对齐。
CRC 包含了当前记录之前所有记录 protobuf 的 CRC32 值。

WAL files are placed inside of the directory in the following format:
$seq-$index.wal

The first WAL file to be created will be 0000000000000000-0000000000000000.wal
indicating an initial sequence of 0 and an initial raft index of 0. The first
entry written to WAL MUST have raft index 0.
第一个写入 WAL 的 entry 必须是 raft index 为 0。

WAL will cut its current tail wal file if its size exceeds 64MB. This will increment an internal
sequence number and cause a new file to be created. If the last raft index saved
was 0x20 and this is the first time cut has been called on this WAL then the sequence will
increment from 0x0 to 0x1. The new file will be: 0000000000000001-0000000000000021.wal.
If a second cut issues 0x10 entries with incremental index later then the file will be called:
0000000000000002-0000000000000031.wal.
WAL 文件超过 64M 将会被截断。然后增加内部序列号，并创建一个新文件。
如果最后保存的 Raft index 是 0x20，并且这是第一次对这个 WAL 截断，那么 seq 将从 0x0 增加到 0x1，
新文件将被命名为：0000000000000001-0000000000000021.wal。
如果第 2 次截断后，后续增加了 0x10 个条目的递增索引，则文件将被命名为：0000000000000002-0000000000000031.wal。

At a later time a WAL can be opened at a particular snapshot.
在稍后的时间，可以针对特定的 snap 打开一个 WAL。
If there is no snapshot, an empty snapshot should be passed in.
如果没有 snap，将会传入一个空的 snap。

	w, err := wal.Open("/var/lib/etcd", walpb.Snapshot{Index: 10, Term: 2})
	...

The snapshot must have been written to the WAL.

Additional items cannot be Saved to this WAL until all of the items from the given
snapshot to the end of the WAL are read first:
在给定快照到 WAL 结尾的所有 item 都被读取之前，无法将额外的 item 保存到该 WAL。

	metadata, state, ents, err := w.ReadAll()

This will give you the metadata, the last raft.State and the slice of
raft.Entry items in the log.
*/
package wal
