//  Copyright (c) 2017-2018 Uber Technologies, Inc.
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

package memstore

import (
	"github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	diskMocks "github.com/uber/aresdb/diskstore/mocks"
	memCom "github.com/uber/aresdb/memstore/common"
	"github.com/uber/aresdb/memstore/list"
	metaCom "github.com/uber/aresdb/metastore/common"
	metaMocks "github.com/uber/aresdb/metastore/mocks"
	utilsMocks "github.com/uber/aresdb/utils/mocks"
	"go.uber.org/zap"
	"sync"
)

var _ = ginkgo.Describe("backfill", func() {
	var tableSchema *memCom.TableSchema
	var baseBatch, newBatch *ArchiveBatch
	var upsertBatches [4]*memCom.UpsertBatch
	var patch *backfillPatch
	var hostMemoryManager memCom.HostMemoryManager
	var backfillCtx backfillContext
	var m *memStoreImpl
	table := "test"
	shardID := 0
	jobKey := getIdentifier(table, shardID, memCom.BackfillJobType)
	var scheduler *schedulerImpl
	var jobManager *backfillJobManager
	var shard *TableShard

	ginkgo.BeforeEach(func() {
		tableSchema = &memCom.TableSchema{
			Schema: metaCom.Table{
				Name: table,
				Config: metaCom.TableConfig{
					ArchivingDelayMinutes:    500,
					ArchivingIntervalMinutes: 300,
					BackfillStoreBatchSize:   20000,
				},
				IsFactTable:          true,
				ArchivingSortColumns: []int{1, 5},
				PrimaryKeyColumns:    []int{1, 2},
				Columns: []metaCom.Column{
					{Deleted: false},
					{Deleted: false}, // sort col, pk 1
					{Deleted: false}, // pk 2
					{Deleted: true},  // should skip this column.
					{Deleted: false}, // unsort col
					{Deleted: false}, // sort col, non pk
					{Deleted: false}, // array column
				},
			},
			PrimaryKeyBytes:   8,
			ValueTypeByColumn: []memCom.DataType{memCom.Uint32, memCom.Uint32, memCom.Uint32, memCom.Uint32, memCom.Uint32, memCom.Uint32, memCom.ArrayInt16},
			DefaultValues: []*memCom.DataValue{&memCom.NullDataValue, &memCom.NullDataValue,
				&memCom.NullDataValue, &memCom.NullDataValue, &memCom.NullDataValue, &memCom.NullDataValue, &memCom.NullDataValue},
		}

		var err error

		upsertBatches[0], err = GetFactory().ReadUpsertBatch("backfill/upsertBatch0")
		??(err).Should(BeNil())
		upsertBatches[1], err = GetFactory().ReadUpsertBatch("backfill/upsertBatch1")
		??(err).Should(BeNil())
		upsertBatches[2], err = GetFactory().ReadUpsertBatch("backfill/upsertBatch2")
		??(err).Should(BeNil())
		upsertBatches[3], err = GetFactory().ReadUpsertBatch("backfill/upsertBatch3")
		??(err).Should(BeNil())

		patch = &backfillPatch{
			recordIDs: []memCom.RecordID{
				{BatchID: 0, Index: 0},
				{BatchID: 0, Index: 1},
				{BatchID: 0, Index: 2},
				{BatchID: 1, Index: 0},
				{BatchID: 1, Index: 1},
				{BatchID: 1, Index: 2},
				{BatchID: 2, Index: 0},
				{BatchID: 3, Index: 0},
				{BatchID: 3, Index: 1},
				{BatchID: 3, Index: 2},
				{BatchID: 3, Index: 3},
				{BatchID: 3, Index: 4},
				{BatchID: 3, Index: 5},
			},
			backfillBatches: upsertBatches[:],
		}

		m = GetFactory().NewMockMemStore()
		writer := new(utilsMocks.WriteSyncCloser)
		writer.On("Write", mock.Anything).Return(0, nil)
		writer.On("Close").Return(nil)
		writer.On("Sync").Return(nil)
		(m.diskStore).(*diskMocks.DiskStore).
			On("OpenVectorPartyFileForWrite",
				table, mock.Anything, shardID, 0, uint32(0), uint32(1)).Return(writer, nil)
		(m.metaStore).(*metaMocks.MetaStore).On(
			"AddArchiveBatchVersion", table, shardID,
			0, uint32(0), uint32(1), 12).Return(nil)
		(m.diskStore).(*diskMocks.DiskStore).On(
			"DeleteBatchVersions", table, shardID,
			0, uint32(0), uint32(0)).Return(nil)

		hostMemoryManager = NewHostMemoryManager(m, 1<<32)
		shard = NewTableShard(tableSchema, m.metaStore, m.diskStore, hostMemoryManager, shardID, 1, m.options)
		batch, err := GetFactory().ReadArchiveBatch("backfill/backfillBase")
		??(err).Should(BeNil())
		baseBatch = &ArchiveBatch{
			Size:  10,
			Batch: *batch,
			Shard: shard,
		}
		shard.ArchiveStore.CurrentVersion.Batches[0] = baseBatch
		batch, err = GetFactory().ReadArchiveBatch("backfill/backfillNew")
		??(err).Should(BeNil())
		newBatch = &ArchiveBatch{
			Size:  12,
			Batch: *batch,
			Shard: shard,
		}

		backfillCtx = newBackfillContext(baseBatch, patch, tableSchema, tableSchema.GetColumnDeletions(),
			tableSchema.Schema.ArchivingSortColumns, tableSchema.Schema.PrimaryKeyColumns,
			tableSchema.ValueTypeByColumn, tableSchema.DefaultValues, hostMemoryManager)

		m.TableShards[table] = map[int]*TableShard{
			shardID: shard,
		}

		scheduler = newScheduler(m)
		jobManager = scheduler.jobManagers[memCom.BackfillJobType].(*backfillJobManager)
	})

	ginkgo.AfterEach(func() {
		backfillCtx.release()
	})

	logger := zap.NewExample().Sugar()

	ginkgo.It("createBackfillPatches should work", func() {
		logger.Infof("Test createBackfillPatches should work Started")
		??(newBatch).ShouldNot(BeNil()) //TODO remove

		var upsertBatches [3]*memCom.UpsertBatch

		// upsert batch 0
		builder := memCom.NewUpsertBatchBuilder()
		err := builder.AddColumn(0, memCom.Uint32)
		??(err).Should(BeNil())

		builder.AddRow()
		// day 0
		builder.SetValue(0, 0, uint32(0))
		builder.AddRow()
		// day 1
		builder.SetValue(1, 0, uint32(86400))
		bs, err := builder.ToByteArray()
		??(err).Should(BeNil())
		upsertBatches[0], err = memCom.NewUpsertBatch(bs)
		??(err).Should(BeNil())

		// upsert batch 1
		builder = memCom.NewUpsertBatchBuilder()
		err = builder.AddColumn(0, memCom.Uint32)
		??(err).Should(BeNil())

		builder.AddRow()
		// day 1
		builder.SetValue(0, 0, uint32(86400))

		bs, err = builder.ToByteArray()
		??(err).Should(BeNil())
		upsertBatches[1], err = memCom.NewUpsertBatch(bs)
		??(err).Should(BeNil())

		// upsert batch 2
		err = builder.AddColumn(0, memCom.Uint32)
		??(err).Should(BeNil())

		// day 2
		builder.SetValue(0, 0, uint32(2*86400))

		bs, err = builder.ToByteArray()
		??(err).Should(BeNil())
		upsertBatches[2], err = memCom.NewUpsertBatch(bs)
		??(err).Should(BeNil())

		backfillPatches, err := createBackfillPatches(upsertBatches[:], jobManager.reportBackfillJobDetail, jobKey)
		??(err).Should(BeNil())
		// day 0, 1 ,2
		??(backfillPatches).Should(HaveLen(3))
		??(backfillPatches[0].recordIDs).Should(BeEquivalentTo([]memCom.RecordID{{BatchID: 0, Index: 0}}))
		??(backfillPatches[0].backfillBatches).Should(Equal(upsertBatches[:]))
		??(backfillPatches[1].recordIDs).Should(BeEquivalentTo([]memCom.RecordID{{BatchID: 0, Index: 1}, {BatchID: 1, Index: 0}}))
		??(backfillPatches[2].recordIDs).Should(BeEquivalentTo([]memCom.RecordID{{BatchID: 2, Index: 0}}))

		scheduler.RLock()
		??(*(jobManager.getJobDetail(jobKey))).Should(Equal(BackfillJobDetail{
			JobDetail: JobDetail{
				Current:    3,
				Total:      3,
				NumRecords: 4,
			},
			Stage: "create patch",
		}))
		scheduler.RUnlock()
		logger.Infof("Test createBackfillPatches should work Finished")
	})
	ginkgo.It("newBackfillStore should work", func() {
		logger.Infof("Test newBackfillStore should work Started")

		tableSchema := &memCom.TableSchema{
			Schema: metaCom.Table{
				Name: "test",
				Config: metaCom.TableConfig{
					ArchivingDelayMinutes:    500,
					ArchivingIntervalMinutes: 300,
					BackfillStoreBatchSize:   20000,
				},
				IsFactTable:          true,
				ArchivingSortColumns: []int{1, 2},
				Columns: []metaCom.Column{
					{Deleted: false},
					{Deleted: false},
					{Deleted: false},
				},
			},
			PrimaryKeyBytes: 8,
		}

		backfillStore := newBackfillStore(tableSchema, hostMemoryManager, 0)
		??(backfillStore.Batches).ShouldNot(BeNil())
		??(backfillStore.PrimaryKey).ShouldNot(BeNil())
		logger.Infof("Test newBackfillStore should work Finished")
	})

	ginkgo.It("newBackfillContext should work", func() {
		logger.Infof("Test newBackfillContext should work Started")
		tableSchema := &memCom.TableSchema{
			Schema: metaCom.Table{
				Name: "test",
				Config: metaCom.TableConfig{
					ArchivingDelayMinutes:    500,
					ArchivingIntervalMinutes: 300,
				},
				IsFactTable:          true,
				ArchivingSortColumns: []int{1, 2},
				PrimaryKeyColumns:    []int{1},
				Columns: []metaCom.Column{
					{Deleted: false},
					{Deleted: false},
					{Deleted: false},
				},
			},
			PrimaryKeyBytes: 8,
		}
		baseBatch := &ArchiveBatch{
			Batch: memCom.Batch{RWMutex: &sync.RWMutex{}},
		}
		patch := &backfillPatch{}
		backfillCtx := newBackfillContext(baseBatch, patch, tableSchema, tableSchema.GetColumnDeletions(),
			tableSchema.Schema.ArchivingSortColumns, tableSchema.Schema.PrimaryKeyColumns,
			tableSchema.ValueTypeByColumn, tableSchema.DefaultValues, hostMemoryManager)
		??(backfillCtx.base).Should(Equal(baseBatch))
		??(backfillCtx.patch).Should(Equal(patch))
		backfillCtx.release()
		logger.Infof("Test newBackfillContext should work Finished")
	})

	ginkgo.It("empty patch should work", func() {
		logger.Infof("Test empty patch should work Started")
		patch := &backfillPatch{}
		backfillCtx := newBackfillContext(baseBatch, patch, tableSchema, tableSchema.GetColumnDeletions(),
			tableSchema.Schema.ArchivingSortColumns, tableSchema.Schema.PrimaryKeyColumns,
			tableSchema.ValueTypeByColumn, tableSchema.DefaultValues, hostMemoryManager)
		err := backfillCtx.backfill(jobManager.reportBackfillJobDetail, jobKey)
		??(err).Should(BeNil())
		??(backfillCtx.new.Equals(&baseBatch.Batch)).Should(BeTrue())
		logger.Infof("Test empty patch should work Finished")
	})

	ginkgo.It("getChangedBaseRow should work", func() {
		logger.Infof("Test getChangedBaseRow should work Started")

		changedPatchRow, err := backfillCtx.getChangedPatchRow(memCom.RecordID{BatchID: 0, Index: 0}, upsertBatches[0])
		??(err).Should(BeNil())
		changedBaseRow := backfillCtx.getChangedBaseRow(memCom.RecordID{BatchID: 0, Index: 0}, changedPatchRow)
		??(changedBaseRow).Should(BeNil())

		changedPatchRow, err = backfillCtx.getChangedPatchRow(memCom.RecordID{BatchID: 0, Index: 1}, upsertBatches[0])
		??(err).Should(BeNil())
		changedBaseRow = backfillCtx.getChangedBaseRow(memCom.RecordID{BatchID: 0, Index: 1}, changedPatchRow)
		??(len(changedBaseRow)).Should(Equal(7))

		??(changedBaseRow[0].Valid).Should(BeTrue())
		??(*(*uint32)(changedBaseRow[0].OtherVal)).Should(BeEquivalentTo(1))

		??(changedBaseRow[1].Valid).Should(BeTrue())
		??(*(*uint32)(changedBaseRow[1].OtherVal)).Should(BeEquivalentTo(0))

		??(changedBaseRow[2].Valid).Should(BeTrue())
		??(*(*uint32)(changedBaseRow[2].OtherVal)).Should(BeEquivalentTo(1))

		??(changedBaseRow[3]).Should(BeNil())

		??(changedBaseRow[4].Valid).Should(BeTrue())
		??(*(*uint32)(changedBaseRow[4].OtherVal)).Should(BeEquivalentTo(1))

		??(changedBaseRow[5].Valid).Should(BeTrue())
		??(*(*uint32)(changedBaseRow[5].OtherVal)).Should(BeEquivalentTo(11))

		??(changedBaseRow[6].Valid).Should(BeTrue())
		reader := memCom.NewArrayValueReader(changedBaseRow[6].DataType, changedBaseRow[6].OtherVal)
		??(reader.GetLength()).Should(Equal(2))
		??(*(*int16)(reader.Get(0))).Should(Equal(int16(11)))
		??(*(*int16)(reader.Get(1))).Should(Equal(int16(12)))
		logger.Infof("Test getChangedBaseRow should work Finished")
	})

	ginkgo.It("getChangedPatchRow should work", func() {
		logger.Infof("Test getChangedPatchRow should work Started")
		changedPatchRow, err := backfillCtx.getChangedPatchRow(memCom.RecordID{BatchID: 0, Index: 1}, upsertBatches[0])
		??(err).Should(BeNil())
		??(len(changedPatchRow)).Should(Equal(7))

		??(changedPatchRow[0].Valid).Should(BeTrue())
		??(*(*uint32)(changedPatchRow[0].OtherVal)).Should(BeEquivalentTo(1))

		??(changedPatchRow[1].Valid).Should(BeTrue())
		??(*(*uint32)(changedPatchRow[1].OtherVal)).Should(BeEquivalentTo(0))

		??(changedPatchRow[2].Valid).Should(BeTrue())
		??(*(*uint32)(changedPatchRow[2].OtherVal)).Should(BeEquivalentTo(1))

		??(changedPatchRow[3]).Should(BeNil())

		??(changedPatchRow[4]).Should(BeNil())

		??(changedPatchRow[5].Valid).Should(BeTrue())
		??(*(*uint32)(changedPatchRow[5].OtherVal)).Should(BeEquivalentTo(11))
		??(changedPatchRow[6]).Should(BeNil())
		logger.Infof("Test getChangedPatchRow should work Finished")
	})

	ginkgo.It("writePatchValueForUnsortColumn should work", func() {
		logger.Infof("Test writePatchValueForUnsortColumn should work Started")
		changedPatchRow, err := backfillCtx.getChangedPatchRow(memCom.RecordID{BatchID: 1, Index: 1}, upsertBatches[1])
		??(err).Should(BeNil())

		??(backfillCtx.columnsForked[4]).Should(BeFalse())
		oldColumn := backfillCtx.new.Columns[4]
		backfillCtx.writePatchValueForUnsortedColumn(memCom.RecordID{BatchID: 0, Index: 2}, changedPatchRow)
		??(err).Should(BeNil())
		??(backfillCtx.columnsForked[4]).Should(BeTrue())

		newValue := backfillCtx.new.GetDataValue(2, 4)
		??(*(*uint32)(newValue.OtherVal)).Should(BeEquivalentTo(12))

		forkedColumn := backfillCtx.new.Columns[4]
		??(forkedColumn).ShouldNot(Equal(oldColumn))
		changedPatchRow, err = backfillCtx.getChangedPatchRow(memCom.RecordID{BatchID: 1, Index: 2}, upsertBatches[1])
		??(err).Should(BeNil())

		backfillCtx.writePatchValueForUnsortedColumn(memCom.RecordID{BatchID: 0, Index: 3}, changedPatchRow)
		??(err).Should(BeNil())

		newValue = backfillCtx.new.GetDataValue(3, 4)
		??(*(*uint32)(newValue.OtherVal)).Should(BeEquivalentTo(13))

		// column should not be forked again.
		??(forkedColumn).Should(Equal(backfillCtx.new.Columns[4]))

		logger.Infof("Test writePatchValueForUnsortColumn should work Finished")
	})

	ginkgo.It("apply backfill patch should work", func() {
		logger.Infof("Test apply backfill patch should work Started")
		err := backfillCtx.backfill(jobManager.reportBackfillJobDetail, jobKey)
		??(err).Should(BeNil())
		??(len(backfillCtx.backfillStore.Batches)).Should(Equal(1))
		??(backfillCtx.backfillStore.NextWriteRecord).Should(Equal(memCom.RecordID{BatchID: BaseBatchID, Index: 7}))
		??(backfillCtx.columnsForked).Should(BeEquivalentTo([]bool{false, false, false, false, true, false, true}))
		??(backfillCtx.baseRowDeleted).Should(HaveLen(5))
		??(backfillCtx.baseRowDeleted).Should(ConsistOf(1, 4, 6, 7, 9))

		// Compare result batch with expected batch.
		batch, err := GetFactory().ReadLiveBatch("backfill/backfillTempLiveStore")
		??(err).Should(BeNil())
		backfillBatch := backfillCtx.backfillStore.GetBatchForRead(BaseBatchID)
		defer backfillBatch.RUnlock()
		for _, column := range backfillBatch.Columns {
			if column != nil {
				if column.IsList() {
					column.(*list.LiveVectorParty).SetLength(7)
				} else {
					column.(*cLiveVectorParty).length = 7
				}
			}
		}

		??(batch.Equals(&backfillBatch.Batch)).Should(BeTrue())
		??(newBatch.Columns[6].IsList()).Should(BeTrue())
		??(backfillCtx.new.Batch.Columns[6].IsList()).Should(BeTrue())
		??(newBatch.Equals(&backfillCtx.new.Batch)).Should(BeTrue())
		logger.Infof("Test apply backfill patch should work Finished")
	})

	ginkgo.It("createArchivingPatch should work", func() {
		logger.Infof("Test createArchivingPatch should work Started")
		err := backfillCtx.backfill(jobManager.reportBackfillJobDetail, jobKey)
		??(err).Should(BeNil())
		backfillCtx.backfillStore.AdvanceLastReadRecord()
		ap := backfillCtx.backfillStore.snapshot().createArchivingPatch(tableSchema.GetArchivingSortColumns())
		??(len(ap.data.batches)).Should(Equal(1))
		??(len(ap.recordIDs)).Should(Equal(7))
		logger.Infof("Test createArchivingPatch should work Finished")
	})

	ginkgo.It("createNewArchiveStoreVersionForBackfill should work", func() {
		logger.Infof("Test createNewArchiveStoreVersionForBackfill should work Started")

		backfillPatches, err := createBackfillPatches(upsertBatches[:], jobManager.reportBackfillJobDetail, jobKey)
		??(err).Should(BeNil())
		err = shard.createNewArchiveStoreVersionForBackfill(backfillPatches, jobManager.reportBackfillJobDetail, jobKey)
		??(err).Should(BeNil())

		jobManager.RLock()
		jobManager.jobDetails[jobKey].LockDuration = 0
		??(*(jobManager.jobDetails[jobKey])).Should(Equal(BackfillJobDetail{
			JobDetail: JobDetail{
				Current:         1,
				Total:           1,
				NumRecords:      13,
				NumAffectedDays: 1,
			},
			Stage: "apply patch",
		}))
		jobManager.RUnlock()
		logger.Infof("Test createNewArchiveStoreVersionForBackfill should work Finished")
	})

	ginkgo.It("Live store with batch size of 1 should work", func() {
		logger.Infof("Test Live store with batch size of 1 should work Started")
		backfillCtx.backfillStore.BatchSize = 1
		err := backfillCtx.backfill(jobManager.reportBackfillJobDetail, jobKey)
		??(err).Should(BeNil())
		??(len(backfillCtx.backfillStore.Batches)).Should(Equal(7))
		??(backfillCtx.backfillStore.NextWriteRecord).Should(Equal(memCom.RecordID{BatchID: BaseBatchID + 7, Index: 0}))
		logger.Infof("Test Live store with batch size of 1 should work Finished")
	})
})
