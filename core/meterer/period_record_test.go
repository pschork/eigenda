package meterer_test

import (
	"testing"

	disperser_rpc "github.com/Layr-Labs/eigenda/api/grpc/disperser/v2"
	"github.com/Layr-Labs/eigenda/core"
	"github.com/Layr-Labs/eigenda/core/meterer"
	"github.com/Layr-Labs/eigenda/core/meterer/payment_logic"
	"github.com/stretchr/testify/assert"
)

func TestQuorumPeriodRecords_GetRelativePeriodRecord(t *testing.T) {
	tests := []struct {
		name                 string
		setupRecords         func() meterer.QuorumPeriodRecords
		accessSequence       []uint64
		quorumNumber         core.QuorumID
		expectedFinalUsages  []uint64
		expectedFinalIndices []uint32
		description          string
	}{
		// Basic functionality tests
		{
			name: "new quorum and record",
			setupRecords: func() meterer.QuorumPeriodRecords {
				return make(meterer.QuorumPeriodRecords)
			},
			accessSequence:       []uint64{5},
			quorumNumber:         core.QuorumID(1),
			expectedFinalUsages:  []uint64{0, 0, 0}, // slot 2 (5%3=2) should have Usage=0
			expectedFinalIndices: []uint32{0, 0, 5}, // slot 2 should have Index=5
			description:          "Should create new quorum and record when both don't exist",
		},
		{
			name: "existing quorum and record",
			setupRecords: func() meterer.QuorumPeriodRecords {
				records := make(meterer.QuorumPeriodRecords)
				records[core.QuorumID(1)] = []*meterer.PeriodRecord{
					nil,
					{Index: uint32(4), Usage: 100},
					nil,
				}
				return records
			},
			accessSequence:       []uint64{4},
			quorumNumber:         core.QuorumID(1),
			expectedFinalUsages:  []uint64{0, 100, 0}, // slot 1 should keep Usage=100
			expectedFinalIndices: []uint32{0, 4, 0},   // slot 1 should keep Index=4
			description:          "Should return existing record without modification",
		},
		{
			name: "index wraps around (modulo operation)",
			setupRecords: func() meterer.QuorumPeriodRecords {
				return make(meterer.QuorumPeriodRecords)
			},
			accessSequence:       []uint64{10}, // 10 % 3 = 1
			quorumNumber:         core.QuorumID(2),
			expectedFinalUsages:  []uint64{0, 0, 0},  // slot 1 should have Usage=0
			expectedFinalIndices: []uint32{0, 10, 0}, // slot 1 should have Index=10
			description:          "Should handle modulo operation correctly for wrapping indices",
		},

		// Circular wrapping refresh tests
		{
			name: "circular wrapping refresh - newer period overwrites older in same slot",
			setupRecords: func() meterer.QuorumPeriodRecords {
				records := make(meterer.QuorumPeriodRecords)
				// Setup initial record at index 1 (maps to slot 1)
				record := records.GetRelativePeriodRecord(1, core.QuorumID(1))
				record.Usage = 100
				return records
			},
			accessSequence:       []uint64{1, 4}, // index 4 also maps to slot 1 (4 % 3 = 1)
			quorumNumber:         core.QuorumID(1),
			expectedFinalUsages:  []uint64{0, 0, 0}, // slot 1 should be refreshed to Usage=0
			expectedFinalIndices: []uint32{0, 4, 0}, // slot 1 should have Index=4
			description:          "When index 4 maps to same slot as index 1, it should refresh the record since 4 > 1",
		},
		{
			name: "circular wrapping refresh - multiple wraps around",
			setupRecords: func() meterer.QuorumPeriodRecords {
				records := make(meterer.QuorumPeriodRecords)
				// Setup records at indices 0, 1, 2 (filling all slots)
				records.GetRelativePeriodRecord(0, core.QuorumID(1)).Usage = 50
				records.GetRelativePeriodRecord(1, core.QuorumID(1)).Usage = 60
				records.GetRelativePeriodRecord(2, core.QuorumID(1)).Usage = 70
				return records
			},
			accessSequence:       []uint64{0, 1, 2, 6, 7, 8}, // 6,7,8 map to slots 0,1,2 respectively
			quorumNumber:         core.QuorumID(1),
			expectedFinalUsages:  []uint64{0, 0, 0}, // all slots should be refreshed
			expectedFinalIndices: []uint32{6, 7, 8}, // all slots should have newer indices
			description:          "When indices 6,7,8 map to same slots as 0,1,2, they should refresh since 6>0, 7>1, 8>2",
		},
		{
			name: "circular wrapping refresh - no refresh when index is smaller",
			setupRecords: func() meterer.QuorumPeriodRecords {
				records := make(meterer.QuorumPeriodRecords)
				record := records.GetRelativePeriodRecord(10, core.QuorumID(1))
				record.Usage = 300
				return records
			},
			accessSequence:       []uint64{10, 7}, // 10 and 7 both map to slot 1 (10%3=1, 7%3=1)
			quorumNumber:         core.QuorumID(1),
			expectedFinalUsages:  []uint64{0, 300, 0}, // slot 1 should keep Usage=300
			expectedFinalIndices: []uint32{0, 10, 0},  // slot 1 should keep Index=10
			description:          "When accessing smaller index 7 after larger index 10, no refresh should occur since 7 < 10",
		},

		// Edge case tests

	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := tt.setupRecords()

			// Access records in sequence
			for _, index := range tt.accessSequence {
				record := records.GetRelativePeriodRecord(index, tt.quorumNumber)
				assert.NotNil(t, record, "Record should never be nil")
			}

			// Verify quorum exists after call
			_, quorumExists := records[tt.quorumNumber]
			assert.True(t, quorumExists, "Quorum should exist after GetRelativePeriodRecord call")

			// Verify final state of all slots
			for i := 0; i < 3; i++ { // MinNumBins = 3
				slot := records[tt.quorumNumber][i]
				if tt.expectedFinalUsages[i] == 0 && tt.expectedFinalIndices[i] == 0 {
					// Expect nil or zero values
					if slot != nil {
						assert.Equal(t, uint64(0), slot.Usage, "Slot %d usage should be 0 (refreshed)", i)
					}
				} else {
					assert.NotNil(t, slot, "Slot %d should not be nil", i)
					assert.Equal(t, tt.expectedFinalUsages[i], slot.Usage, "Slot %d usage mismatch", i)
					assert.Equal(t, tt.expectedFinalIndices[i], slot.Index, "Slot %d index mismatch", i)
				}
			}
		})
	}
}

func TestQuorumPeriodRecords_UpdateUsage(t *testing.T) {
	tests := []struct {
		name                  string
		initialRecords        meterer.QuorumPeriodRecords
		quorumNumber          core.QuorumID
		timestamp             int64
		numSymbols            uint64
		reservation           *core.ReservedPayment
		protocolConfig        *core.PaymentQuorumProtocolConfig
		expectedError         string
		expectedCurrentUsage  uint64
		expectedOverflowUsage uint64
		setupCurrentRecord    bool
		setupOverflowRecord   bool
		currentRecordUsage    uint64
		overflowRecordUsage   uint64
	}{
		{
			name:           "symbol usage exceeds bin limit",
			initialRecords: make(meterer.QuorumPeriodRecords),
			quorumNumber:   core.QuorumID(1),
			timestamp:      1000000000000, // 1 second in nanoseconds
			numSymbols:     550,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 50, // This will create bin limit of 50 * 10 = 500
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              100, // min symbols is 100, so 550 -> 600
				ReservationRateLimitWindow: 10,
			},
			expectedError: "symbol usage exceeds bin limit",
		},
		{
			name:           "usage within bin limit",
			initialRecords: make(meterer.QuorumPeriodRecords),
			quorumNumber:   core.QuorumID(1),
			timestamp:      1000000000000,
			numSymbols:     50,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 100,
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              10,
				ReservationRateLimitWindow: 10,
			},
			expectedCurrentUsage: 50,
		},
		{
			name:           "usage with minimum symbols applied",
			initialRecords: make(meterer.QuorumPeriodRecords),
			quorumNumber:   core.QuorumID(1),
			timestamp:      1000000000000,
			numSymbols:     5, // Below min symbols
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 100,
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              20, // Min symbols enforced
				ReservationRateLimitWindow: 10,
			},
			expectedCurrentUsage: 20, // Should use min symbols
		},
		{
			name:               "usage exceeds limit but overflow available",
			initialRecords:     make(meterer.QuorumPeriodRecords),
			quorumNumber:       core.QuorumID(1),
			timestamp:          1000000000000,
			numSymbols:         80,
			setupCurrentRecord: true,
			currentRecordUsage: 30,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 10, // bin limit = 10 * 10 = 100
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              1,
				ReservationRateLimitWindow: 10,
			},
			expectedCurrentUsage:  100, // capped at bin limit
			expectedOverflowUsage: 10,  // 30 + 80 - 100 = 10
		},
		{
			name:               "current usage already at limit",
			initialRecords:     make(meterer.QuorumPeriodRecords),
			quorumNumber:       core.QuorumID(1),
			timestamp:          1000000000000,
			numSymbols:         10,
			setupCurrentRecord: true,
			currentRecordUsage: 100,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 10, // bin limit = 100
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              1,
				ReservationRateLimitWindow: 10,
			},
			expectedError: "reservation limit exceeded for quorum 1",
		},
		{
			name:               "current usage exceeds limit",
			initialRecords:     make(meterer.QuorumPeriodRecords),
			quorumNumber:       core.QuorumID(1),
			timestamp:          1000000000000,
			numSymbols:         10,
			setupCurrentRecord: true,
			currentRecordUsage: 150,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 10, // bin limit = 100
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              1,
				ReservationRateLimitWindow: 10,
			},
			expectedError: "reservation limit exceeded for quorum 1",
		},
		{
			name:                "overflow bin already in use",
			initialRecords:      make(meterer.QuorumPeriodRecords),
			quorumNumber:        core.QuorumID(1),
			timestamp:           1000000000000,
			numSymbols:          80,
			setupCurrentRecord:  true,
			setupOverflowRecord: true,
			currentRecordUsage:  30,
			overflowRecordUsage: 50,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 10, // bin limit = 100
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              1,
				ReservationRateLimitWindow: 10,
			},
			expectedError: "reservation limit exceeded for quorum 1",
		},
		{
			name:           "exactly at bin limit",
			initialRecords: make(meterer.QuorumPeriodRecords),
			quorumNumber:   core.QuorumID(1),
			timestamp:      1000000000000,
			numSymbols:     100,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 10, // bin limit = 100
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              1,
				ReservationRateLimitWindow: 10,
			},
			expectedCurrentUsage: 100,
		},
		{
			name:               "zero usage (enforces min symbols)",
			initialRecords:     make(meterer.QuorumPeriodRecords),
			quorumNumber:       core.QuorumID(1),
			timestamp:          1000000000000,
			numSymbols:         0,
			setupCurrentRecord: true,
			currentRecordUsage: 50,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 100,
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              5, // Min symbols enforced even for 0 input
				ReservationRateLimitWindow: 10,
			},
			expectedCurrentUsage: 55, // 50 + 5 (min symbols)
		},
		{
			name:           "negative timestamp",
			initialRecords: make(meterer.QuorumPeriodRecords),
			quorumNumber:   core.QuorumID(1),
			timestamp:      -1000000000000,
			numSymbols:     50,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 100,
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              1,
				ReservationRateLimitWindow: 10,
			},
			expectedCurrentUsage: 50, // Should handle negative timestamp gracefully
		},
		{
			name:           "large reservation window",
			initialRecords: make(meterer.QuorumPeriodRecords),
			quorumNumber:   core.QuorumID(1),
			timestamp:      1000000000000,
			numSymbols:     1000,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 1,
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              1,
				ReservationRateLimitWindow: 10000, // Large window = large bin limit
			},
			expectedCurrentUsage: 1000,
		},
		{
			name:           "zero min symbols",
			initialRecords: make(meterer.QuorumPeriodRecords),
			quorumNumber:   core.QuorumID(1),
			timestamp:      1000000000000,
			numSymbols:     50,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 100,
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              0, // Zero min symbols
				ReservationRateLimitWindow: 10,
			},
			expectedCurrentUsage: 50, // Should use actual symbols since min is 0
		},
		{
			name:           "zero symbols per second (zero bin limit)",
			initialRecords: make(meterer.QuorumPeriodRecords),
			quorumNumber:   core.QuorumID(1),
			timestamp:      1000000000000,
			numSymbols:     1,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 0, // Zero symbols per second
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              1,
				ReservationRateLimitWindow: 10,
			},
			expectedError: "symbol usage exceeds bin limit", // bin limit would be 0
		},
		{
			name:           "different quorum numbers",
			initialRecords: make(meterer.QuorumPeriodRecords),
			quorumNumber:   core.QuorumID(255), // Max quorum ID
			timestamp:      1000000000000,
			numSymbols:     50,
			reservation: &core.ReservedPayment{
				SymbolsPerSecond: 100,
				StartTimestamp:   0,
				EndTimestamp:     2000,
			},
			protocolConfig: &core.PaymentQuorumProtocolConfig{
				MinNumSymbols:              1,
				ReservationRateLimitWindow: 10,
			},
			expectedCurrentUsage: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Calculate expected periods for setup
			currentPeriod := payment_logic.GetReservationPeriodByNanosecond(tt.timestamp, tt.protocolConfig.ReservationRateLimitWindow)
			overflowPeriod := payment_logic.GetOverflowPeriod(currentPeriod, tt.protocolConfig.ReservationRateLimitWindow)

			// Setup initial records if needed
			if tt.setupCurrentRecord {
				currentRecord := tt.initialRecords.GetRelativePeriodRecord(currentPeriod, tt.quorumNumber)
				currentRecord.Usage = tt.currentRecordUsage
			}
			if tt.setupOverflowRecord {
				overflowRecord := tt.initialRecords.GetRelativePeriodRecord(overflowPeriod, tt.quorumNumber)
				overflowRecord.Usage = tt.overflowRecordUsage
			}

			err := tt.initialRecords.UpdateUsage(
				tt.quorumNumber,
				tt.timestamp,
				tt.numSymbols,
				tt.reservation,
				tt.protocolConfig,
			)

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)

				// Check current record usage
				currentRecord := tt.initialRecords.GetRelativePeriodRecord(currentPeriod, tt.quorumNumber)
				assert.Equal(t, tt.expectedCurrentUsage, currentRecord.Usage)

				// Check overflow record usage if expected
				if tt.expectedOverflowUsage > 0 {
					overflowRecord := tt.initialRecords.GetRelativePeriodRecord(overflowPeriod, tt.quorumNumber)
					assert.Equal(t, tt.expectedOverflowUsage, overflowRecord.Usage)
				}
			}
		})
	}
}

func TestQuorumPeriodRecords_DeepCopy(t *testing.T) {
	tests := []struct {
		name            string
		originalRecords meterer.QuorumPeriodRecords
	}{
		{
			name:            "empty records",
			originalRecords: make(meterer.QuorumPeriodRecords),
		},
		{
			name: "single quorum with records",
			originalRecords: meterer.QuorumPeriodRecords{
				core.QuorumID(1): []*meterer.PeriodRecord{
					{Index: 0, Usage: 100},
					{Index: 1, Usage: 200},
					nil,
				},
			},
		},
		{
			name: "multiple quorums with mixed records",
			originalRecords: meterer.QuorumPeriodRecords{
				core.QuorumID(1): []*meterer.PeriodRecord{
					{Index: 0, Usage: 100},
					nil,
					{Index: 2, Usage: 300},
				},
				core.QuorumID(2): []*meterer.PeriodRecord{
					nil,
					{Index: 4, Usage: 400},
					{Index: 5, Usage: 500},
				},
			},
		},
		{
			name: "quorum with all nil records",
			originalRecords: meterer.QuorumPeriodRecords{
				core.QuorumID(3): []*meterer.PeriodRecord{
					nil,
					nil,
					nil,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copied := tt.originalRecords.DeepCopy()

			// Verify structure is copied
			assert.Equal(t, len(tt.originalRecords), len(copied))

			for quorumID, originalSlice := range tt.originalRecords {
				copiedSlice, exists := copied[quorumID]
				assert.True(t, exists)
				assert.Equal(t, len(originalSlice), len(copiedSlice))

				for i, originalRecord := range originalSlice {
					if originalRecord == nil {
						assert.Nil(t, copiedSlice[i])
					} else {
						assert.NotNil(t, copiedSlice[i])
						assert.Equal(t, originalRecord.Index, copiedSlice[i].Index)
						assert.Equal(t, originalRecord.Usage, copiedSlice[i].Usage)

						// Verify it's a deep copy (different memory addresses)
						assert.NotSame(t, originalRecord, copiedSlice[i])
					}
				}
			}

			// Verify modifying copy doesn't affect original
			if len(copied) > 0 {
				for quorumID, copiedSlice := range copied {
					for i, record := range copiedSlice {
						if record != nil {
							// Modify the copy
							record.Usage = 9999
							record.Index = 8888

							// Verify original is unchanged
							originalRecord := tt.originalRecords[quorumID][i]
							if originalRecord != nil {
								assert.NotEqual(t, 9999, originalRecord.Usage)
								assert.NotEqual(t, 8888, originalRecord.Index)
							}
							break
						}
					}
					break
				}
			}
		})
	}
}

func TestQuorumPeriodRecords_FromProtoRecords(t *testing.T) {
	tests := []struct {
		name         string
		protoRecords map[uint32]*disperser_rpc.PeriodRecords
		expected     meterer.QuorumPeriodRecords
	}{
		{
			name:         "empty proto records",
			protoRecords: make(map[uint32]*disperser_rpc.PeriodRecords),
			expected:     make(meterer.QuorumPeriodRecords),
		},
		{
			name: "single quorum with records",
			protoRecords: map[uint32]*disperser_rpc.PeriodRecords{
				1: {
					Records: []*disperser_rpc.PeriodRecord{
						{Index: 0, Usage: 100},
						{Index: 1, Usage: 200},
					},
				},
			},
			expected: meterer.QuorumPeriodRecords{
				core.QuorumID(1): []*meterer.PeriodRecord{
					{Index: 0, Usage: 100},
					{Index: 1, Usage: 200},
					{Index: 2, Usage: 0}, // Default initialized
				},
			},
		},
		{
			name: "multiple quorums",
			protoRecords: map[uint32]*disperser_rpc.PeriodRecords{
				1: {
					Records: []*disperser_rpc.PeriodRecord{
						{Index: 5, Usage: 500}, // 5 % 3 = 2
					},
				},
				2: {
					Records: []*disperser_rpc.PeriodRecord{
						{Index: 3, Usage: 300}, // 3 % 3 = 0
						{Index: 7, Usage: 700}, // 7 % 3 = 1
					},
				},
			},
			expected: meterer.QuorumPeriodRecords{
				core.QuorumID(1): []*meterer.PeriodRecord{
					{Index: 0, Usage: 0},   // Default
					{Index: 1, Usage: 0},   // Default
					{Index: 5, Usage: 500}, // Overwritten at index 2
				},
				core.QuorumID(2): []*meterer.PeriodRecord{
					{Index: 3, Usage: 300}, // Overwritten at index 0
					{Index: 7, Usage: 700}, // Overwritten at index 1
					{Index: 2, Usage: 0},   // Default
				},
			},
		},
		{
			name: "index wrapping with modulo",
			protoRecords: map[uint32]*disperser_rpc.PeriodRecords{
				0: {
					Records: []*disperser_rpc.PeriodRecord{
						{Index: 10, Usage: 1000}, // 10 % 3 = 1
						{Index: 11, Usage: 1100}, // 11 % 3 = 2
						{Index: 12, Usage: 1200}, // 12 % 3 = 0
					},
				},
			},
			expected: meterer.QuorumPeriodRecords{
				core.QuorumID(0): []*meterer.PeriodRecord{
					{Index: 12, Usage: 1200}, // index 0
					{Index: 10, Usage: 1000}, // index 1
					{Index: 11, Usage: 1100}, // index 2
				},
			},
		},
		{
			name: "empty records for quorum",
			protoRecords: map[uint32]*disperser_rpc.PeriodRecords{
				1: {
					Records: []*disperser_rpc.PeriodRecord{},
				},
			},
			expected: meterer.QuorumPeriodRecords{
				core.QuorumID(1): []*meterer.PeriodRecord{
					{Index: 0, Usage: 0},
					{Index: 1, Usage: 0},
					{Index: 2, Usage: 0},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := meterer.FromProtoRecords(tt.protoRecords)

			assert.Equal(t, len(tt.expected), len(result))

			for quorumID, expectedSlice := range tt.expected {
				resultSlice, exists := result[quorumID]
				assert.True(t, exists)
				assert.Equal(t, len(expectedSlice), len(resultSlice))

				for i, expectedRecord := range expectedSlice {
					assert.NotNil(t, resultSlice[i])
					assert.Equal(t, expectedRecord.Index, resultSlice[i].Index)
					assert.Equal(t, expectedRecord.Usage, resultSlice[i].Usage)
				}
			}
		})
	}
}
