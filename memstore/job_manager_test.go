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
	"encoding/json"
	"fmt"
	"time"

	memCom "github.com/uber/aresdb/memstore/common"
	metaCom "github.com/uber/aresdb/metastore/common"
	metaMocks "github.com/uber/aresdb/metastore/mocks"
	"github.com/uber/aresdb/utils"

	"github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	sysmock "github.com/stretchr/testify/mock"
)

var _ = ginkgo.Describe("job manager", func() {
	const (
		tableName = "cities"
	)

	table1 := "Table1"
	table2 := "Table2"
	table3 := "Table3"

	now := uint32(1498600000)

	utils.SetClockImplementation(func() time.Time {
		return time.Unix(int64(now), 0)
	})

	mockErr := errors.New("UpdateArchivingCutoff fails")
	m := GetFactory().NewMockMemStore()
	(m.metaStore).(*metaMocks.MetaStore).On(
		"UpdateArchivingCutoff", sysmock.Anything, sysmock.Anything, sysmock.Anything).Return(mockErr)

	(m.metaStore).(*metaMocks.MetaStore).On(
		"UpdateArchivingCutoff", sysmock.Anything, sysmock.Anything, sysmock.Anything).Return(mockErr)

	hostMemoryManager := NewHostMemoryManager(m, 1<<32)

	shard1 := NewTableShard(&memCom.TableSchema{
		Schema: metaCom.Table{
			Name: table1,
			Config: metaCom.TableConfig{
				ArchivingDelayMinutes:    3 * 60, // 3 hours
				ArchivingIntervalMinutes: 30,     // 30 minutes
				BackfillIntervalMinutes:  10,     // 10 minutes
				BackfillThresholdInBytes: 10,
			},
			IsFactTable: true,
		},
	}, m.metaStore, m.diskStore, hostMemoryManager, 1, 1, m.options)

	shard1.ArchiveStore = &ArchiveStore{
		PurgeManager: NewPurgeManager(shard1),
		CurrentVersion: &ArchiveStoreVersion{
			ArchivingCutoff: now - 3*60*60,
		},
	}

	shard2 := NewTableShard(&memCom.TableSchema{
		Schema: metaCom.Table{
			Name: table1,
			Config: metaCom.TableConfig{
				ArchivingDelayMinutes:    3 * 60, // 3 hours
				ArchivingIntervalMinutes: 30,     // 30 minutes
				BackfillIntervalMinutes:  30,     // 30 minutes
				BackfillThresholdInBytes: 10,
			},
			IsFactTable: true,
		},
	}, m.metaStore, m.diskStore, hostMemoryManager, 2, 1, m.options)

	shard2.LiveStore.BackfillManager.CurrentBufferSize = 15

	shard2.ArchiveStore = &ArchiveStore{
		PurgeManager: NewPurgeManager(shard2),
		CurrentVersion: &ArchiveStoreVersion{
			ArchivingCutoff: now - 12*60*60,
		},
	}

	shard3 := NewTableShard(&memCom.TableSchema{
		Schema: metaCom.Table{
			Name: table2,
			Config: metaCom.TableConfig{
				ArchivingDelayMinutes:    3 * 60, // 3 hours
				ArchivingIntervalMinutes: 30,     // 30 minutes
				BackfillIntervalMinutes:  30,     // 30 minutes
				BackfillThresholdInBytes: 20,
				RecordRetentionInDays:    1,
			},
			IsFactTable: true,
		},
	}, m.metaStore, m.diskStore, hostMemoryManager, 1, 1, m.options)

	shard3.LiveStore.BackfillManager.CurrentBufferSize = 15

	shard3.ArchiveStore = &ArchiveStore{
		PurgeManager: NewPurgeManager(shard3),
		CurrentVersion: &ArchiveStoreVersion{
			ArchivingCutoff: now - 12*60*60,
		},
	}

	shard4 := NewTableShard(&memCom.TableSchema{
		Schema: metaCom.Table{
			Name: table3,
			Config: metaCom.TableConfig{
				SnapshotThreshold:       100,
				SnapshotIntervalMinutes: 5,
			},
			IsFactTable: false,
		},
	}, m.metaStore, m.diskStore, hostMemoryManager, 1, 1, m.options)

	shard4.LiveStore.SnapshotManager.NumMutations = 200

	shard4.ArchiveStore = &ArchiveStore{
		PurgeManager: NewPurgeManager(shard4),
		CurrentVersion: &ArchiveStoreVersion{
			ArchivingCutoff: now - 12*60*60,
		},
	}

	shardMap1 := map[int]*TableShard{
		1: shard1,
		2: shard2,
	}

	shardMap2 := map[int]*TableShard{
		1: shard3,
	}

	shardMap3 := map[int]*TableShard{
		1: shard4,
	}

	m.TableShards = map[string]map[int]*TableShard{
		table1: shardMap1,
		table2: shardMap2,
		table3: shardMap3,
	}

	ginkgo.BeforeEach(func() {
		utils.SetClockImplementation(func() time.Time {
			return time.Unix(int64(now), 0)
		})
	})

	ginkgo.AfterEach(func() {
		utils.ResetClockImplementation()
	})

	ginkgo.It("Test newScheduler", func() {
		scheduler := newScheduler(m)
		??(scheduler).Should(Not(BeNil()))
		??(scheduler.memStore).Should(Equal(m))
		??(scheduler.schedulerStopChan).Should(Not(BeNil()))
	})

	ginkgo.It("Test prepareArchiveJobs", func() {
		scheduler := newScheduler(m)
		jobs := scheduler.jobManagers[memCom.ArchivingJobType].generateJobs()
		??(jobs).Should(HaveLen(2))

		jobMap := make(map[string]*ArchivingJob)
		for _, job := range jobs {
			??(job).Should(BeAssignableToTypeOf(&ArchivingJob{}))
			archivingJob := job.(*ArchivingJob)
			jobMap[fmt.Sprintf("%s,%d", archivingJob.tableName, archivingJob.shardID)] = archivingJob
		}

		??(jobMap).ShouldNot(HaveKey("Table1,1"))
		??(jobMap).Should(HaveKey("Table1,2"))
		??(jobMap).Should(HaveKey("Table2,1"))

		table1Shard1Job := jobMap["Table1,2"]
		??(table1Shard1Job.memStore).Should(Equal(m))
		??(table1Shard1Job.cutoff).Should(BeNumerically("<", now))
		??(table1Shard1Job.cutoff).Should(BeNumerically(">", 0))

		scheduler.RLock()
		jsonStr, err := json.Marshal(scheduler.GetJobDetails(memCom.ArchivingJobType))
		??(err).Should(BeNil())
		??(jsonStr).Should(MatchJSON(`
		{
			"Table1|1|archiving": {
			  "currentCutoff": 1498589200,
			  "status": "waiting",
			  "stage": "",
			  "runningCutoff": 0,
			  "nextRun": "2017-06-27T22:16:40Z",
			  "lastCutoff": 0,
			  "lastStartTime": "0001-01-01T00:00:00Z",
			  "lastRun": "0001-01-01T00:00:00Z"
			},
			"Table1|2|archiving": {
			  "currentCutoff": 1498556800,
			  "status": "ready",
			  "stage": "",
			  "runningCutoff": 0,
			  "nextRun": "0001-01-01T00:00:00Z",
			  "lastCutoff": 0,
			  "lastStartTime": "0001-01-01T00:00:00Z",
			  "lastRun": "0001-01-01T00:00:00Z"
			},
			"Table2|1|archiving": {
			  "currentCutoff": 1498556800,
			  "status": "ready",
			  "stage": "",
			  "runningCutoff": 0,
			  "nextRun": "0001-01-01T00:00:00Z",
			  "lastCutoff": 0,
			  "lastStartTime": "0001-01-01T00:00:00Z",
			  "lastRun": "0001-01-01T00:00:00Z"
			}
		}
		`))
		scheduler.RUnlock()
	})

	ginkgo.It("Test prepareSnapshotJobs", func() {
		scheduler := newScheduler(m)
		jobManager := scheduler.jobManagers[memCom.SnapshotJobType]
		jobs := jobManager.generateJobs()
		??(jobs).Should(HaveLen(1))

		jobMap := make(map[string]*SnapshotJob)
		for _, job := range jobs {
			??(job).Should(BeAssignableToTypeOf(&SnapshotJob{}))
			snapshotJob := job.(*SnapshotJob)
			jobMap[fmt.Sprintf("%s,%d", snapshotJob.tableName, snapshotJob.shardID)] = snapshotJob
		}

		??(jobMap).Should(HaveKey("Table3,1"))

		table3Shard1Job := jobMap["Table3,1"]
		??(table3Shard1Job.memStore).Should(Equal(m))
		??(table3Shard1Job.tableName).Should(Equal(table3))
		??(table3Shard1Job.shardID).Should(Equal(1))

		scheduler.RLock()
		jsonStr, _ := json.Marshal(jobManager.getJobDetails())
		??(jsonStr).Should(MatchJSON(`
		{
			  "Table3|1|snapshot": {
			  "status": "ready",
			  "nextRun": "0001-01-01T00:00:00Z",
			  "lastRun": "0001-01-01T00:00:00Z",
			  "lastStartTime": "0001-01-01T00:00:00Z",
			  "numMutations": 0,
			  "numBatches": 0,
			  "redologFile": 0,
			  "batchOffset": 0,
			  "stage": ""
        	}
		}
		`))
		scheduler.RUnlock()
	})

	ginkgo.It("Test prepareBackfillJobs", func() {
		utils.SetCurrentTime(time.Unix(1799, 0))
		scheduler := newScheduler(m)
		jobManager := scheduler.jobManagers[memCom.BackfillJobType]
		key := getIdentifier(table1, 1, memCom.BackfillJobType)
		jobManager.reportJobDetail(key, func(jobDetail *JobDetail) {
			jobDetail.LastRun = time.Unix(1, 0).UTC()
		})

		jobs := jobManager.generateJobs()
		??(len(jobs)).Should(Equal(2))

		jobMap := make(map[string]*BackfillJob)
		for _, job := range jobs {
			??(job).Should(BeAssignableToTypeOf(&BackfillJob{}))
			backfillJob := job.(*BackfillJob)
			jobMap[fmt.Sprintf("%s,%d", backfillJob.tableName, backfillJob.shardID)] = backfillJob
		}

		??(jobMap).Should(HaveKey("Table1,1"))
		??(jobMap).Should(HaveKey("Table1,2"))

		table1Shard1Job := jobMap["Table1,1"]
		??(table1Shard1Job.memStore).Should(Equal(m))
		??(table1Shard1Job.tableName).Should(Equal(table1))
		??(table1Shard1Job.shardID).Should(Equal(1))

		scheduler.RLock()
		jsonStr, _ := json.Marshal(jobManager.getJobDetails())
		??(jsonStr).Should(MatchJSON(`
		  {
			   "Table1|1|backfill":{
				  "status": "ready",
				  "nextRun": "0001-01-01T00:00:00Z",
				  "lastRun": "1970-01-01T00:00:01Z",
				  "lastStartTime": "0001-01-01T00:00:00Z",
				  "stage": "",
				  "redologFile": 0,
				  "batchOffset": 0
				},
				"Table1|2|backfill": {
				  "status": "ready",
				  "nextRun": "0001-01-01T00:00:00Z",
				  "lastRun": "0001-01-01T00:00:00Z",
				  "lastStartTime": "0001-01-01T00:00:00Z",
				  "stage": "",
				  "redologFile": 0,
				  "batchOffset": 0
				},
				"Table2|1|backfill": {
				  "status": "waiting",
				  "nextRun": "0001-01-01T00:00:00Z",
				  "lastRun": "1970-01-01T00:29:59Z",
				  "lastStartTime": "0001-01-01T00:00:00Z",
				  "stage": "",
				  "redologFile": 0,
				  "batchOffset": 0
				}
		}
		`))
		scheduler.RUnlock()
	})

	ginkgo.It("Test NewArchivingJob", func() {
		tableName := "Table1"
		shardID := 1
		cutoff := uint32(1498601504)
		scheduler := newScheduler(m)
		job := scheduler.NewArchivingJob(tableName, shardID, cutoff).(*ArchivingJob)
		??(job).Should(Not(BeNil()))
		??(job.tableName).Should(Equal(tableName))
		??(job.shardID).Should(Equal(shardID))
		??(job.cutoff).Should(Equal(cutoff))
		??(job.memStore).Should(Equal(m))
	})

	ginkgo.It("Test deleteTable of jobManager", func() {
		scheduler := newScheduler(m)
		jobManager := scheduler.jobManagers[memCom.BackfillJobType]
		jobManager.generateJobs()
		jobDetails := jobManager.getJobDetails()
		??(jobDetails).Should(HaveLen(3))
		jobManager.deleteTable(table1)
		jobDetails = jobManager.getJobDetails()
		??(jobDetails).Should(HaveLen(1))
		for k := range jobDetails.(map[string]*BackfillJobDetail) {
			??(k).ShouldNot(HavePrefix(table1))
		}

		// Delete a non exist table should not panic.
		jobManager.deleteTable("whatever")
		jobDetails = jobManager.getJobDetails()
		??(jobDetails).Should(HaveLen(1))
	})

	ginkgo.It("Test deleteTable of Scheduler", func() {
		scheduler := newScheduler(m)
		backfillJobManager := scheduler.jobManagers[memCom.BackfillJobType]
		backfillJobManager.generateJobs()
		archiveJobManager := scheduler.jobManagers[memCom.ArchivingJobType]
		archiveJobManager.generateJobs()
		snapshotJobManager := scheduler.jobManagers[memCom.SnapshotJobType]
		snapshotJobManager.generateJobs()

		// Table 1: Fact table
		// Table 2: Fact table
		// Table 3: Dimension Table
		??(archiveJobManager.getJobDetails()).Should(HaveLen(3))
		??(backfillJobManager.getJobDetails()).Should(HaveLen(3))
		??(snapshotJobManager.getJobDetails()).Should(HaveLen(1))

		scheduler.DeleteTable(table1, true)
		??(archiveJobManager.getJobDetails()).Should(HaveLen(1))
		??(backfillJobManager.getJobDetails()).Should(HaveLen(1))
		??(snapshotJobManager.getJobDetails()).Should(HaveLen(1))

		scheduler.DeleteTable(table2, true)
		??(archiveJobManager.getJobDetails()).Should(HaveLen(0))
		??(backfillJobManager.getJobDetails()).Should(HaveLen(0))
		??(snapshotJobManager.getJobDetails()).Should(HaveLen(1))

		scheduler.DeleteTable(table3, false)
		??(archiveJobManager.getJobDetails()).Should(HaveLen(0))
		??(backfillJobManager.getJobDetails()).Should(HaveLen(0))
		??(snapshotJobManager.getJobDetails()).Should(HaveLen(0))
	})

	ginkgo.It("Test Job String", func() {
		tableName := "Table1"
		shardID := 1
		cutoff := uint32(1498601504)
		scheduler := newScheduler(m)
		job := scheduler.NewArchivingJob(tableName, shardID, cutoff)
		??(job.String()).Should(Equal("ArchivingJob<Table: Table1, ShardID: 1, Cutoff: 1498601504>"))
		??(scheduler.NewBackfillJob(tableName, shardID).String()).Should(Equal("BackfillJob<Table: Table1, ShardID: 1>"))
		??(scheduler.NewSnapshotJob(tableName, shardID).String()).Should(Equal("SnapshotJob<Table: Table1, ShardID: 1>"))
	})

	ginkgo.It("Test Snapshot job", func() {
		snapshotJob := SnapshotJob{
			tableName: tableName,
			shardID:   0,
			memStore:  nil,
		}
		expectedIdentifier := fmt.Sprintf("%s|%d|%s", tableName, 0, "snapshot")
		identifier := snapshotJob.GetIdentifier()
		??(identifier).Should(Equal(expectedIdentifier))

		// TODO due to mock issue
		// err := snapshotJob.Run()
		//??(err).Should(BeNil())
	})

	ginkgo.It("Test NewPurgeJob", func() {
		tableName := "Table1"
		shardID := 1
		batchIDCutoff := 1
		scheduler := newScheduler(m)
		job := scheduler.NewPurgeJob(tableName, shardID, 0, batchIDCutoff).(*PurgeJob)
		??(job).Should(Not(BeNil()))
		??(job.tableName).Should(Equal(tableName))
		??(job.shardID).Should(Equal(shardID))
		??(job.batchIDStart).Should(Equal(0))
		??(job.batchIDEnd).Should(Equal(batchIDCutoff))
		??(job.memStore).Should(Equal(m))
	})

	ginkgo.It("Test preparePurgeJobs", func() {
		utils.SetClockImplementation(func() time.Time {
			return time.Unix(int64(now)+86400*2, 0)
		})
		scheduler := newScheduler(m)
		jobManager := scheduler.jobManagers[memCom.PurgeJobType]
		jobs := jobManager.generateJobs()
		??(jobs).Should(HaveLen(1))

		jobMap := make(map[string]*PurgeJob)
		for _, job := range jobs {
			??(job).Should(BeAssignableToTypeOf(&PurgeJob{}))
			purgeJob := job.(*PurgeJob)
			jobMap[fmt.Sprintf("%s,%d", purgeJob.tableName, purgeJob.shardID)] = purgeJob
		}

		??(jobMap).Should(HaveKey("Table2,1"))

		table2Shard1Job := jobMap["Table2,1"]
		??(table2Shard1Job.memStore).Should(Equal(m))
		??(table2Shard1Job.tableName).Should(Equal(table2))
		??(table2Shard1Job.shardID).Should(Equal(1))

		scheduler.RLock()
		jsonStr, _ := json.Marshal(jobManager.getJobDetails())
		??(jsonStr).Should(MatchJSON(`
		{
			  "Table2|1|purge": {
			  "status": "ready",
			  "nextRun": "0001-01-01T00:00:00Z",
			  "lastRun": "0001-01-01T00:00:00Z",
			  "lastStartTime": "0001-01-01T00:00:00Z",
			  "numBatches": 0,
			  "batchIDStart": 0,
			  "batchIDEnd": 17345,
			  "stage": ""
        	}
		}
		`))
		scheduler.RUnlock()
	})

	ginkgo.It("Test Purge job", func() {
		purgeJob := PurgeJob{
			tableName: tableName,
			shardID:   0,
			memStore:  nil,
		}
		expectedIdentifier := fmt.Sprintf("%s|%d|%s", tableName, 0, "purge")
		identifier := purgeJob.GetIdentifier()
		??(identifier).Should(Equal(expectedIdentifier))
	})
})
