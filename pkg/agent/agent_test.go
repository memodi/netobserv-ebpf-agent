package agent

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/gavv/monotime"
	test2 "github.com/mariomac/guara/pkg/test"
	"github.com/netobserv/netobserv-ebpf-agent/pkg/ebpf"
	"github.com/netobserv/netobserv-ebpf-agent/pkg/flow"
	"github.com/netobserv/netobserv-ebpf-agent/pkg/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var agentIP = "192.168.1.13"

const timeout = 2 * time.Second

func TestFlowsAgent_InvalidConfigs(t *testing.T) {
	for _, tc := range []struct {
		d string
		c Config
	}{{
		d: "invalid export type",
		c: Config{Export: "foo"},
	}, {
		d: "GRPC: missing host",
		c: Config{Export: "grpc", TargetPort: 3333},
	}, {
		d: "GRPC: missing port",
		c: Config{Export: "grpc", TargetHost: "flp"},
	}, {
		d: "Kafka: missing brokers",
		c: Config{Export: "kafka"},
	}} {
		t.Run(tc.d, func(t *testing.T) {
			_, err := FlowsAgent(&tc.c)
			assert.Error(t, err)
		})
	}
}

var (
	key1 = ebpf.BpfFlowId{
		SrcPort: 123,
		DstPort: 456,
		IfIndex: 3,
	}

	key2 = ebpf.BpfFlowId{
		SrcPort: 333,
		DstPort: 532,
		IfIndex: 3,
	}
)

func TestFlowsAgent_Deduplication(t *testing.T) {
	export := testAgent(t, &Config{
		CacheActiveTimeout: 10 * time.Millisecond,
		CacheMaxFlows:      100,
		DeduperJustMark:    false,
		Deduper:            DeduperFirstCome,
	})

	exported := export.Get(t, timeout)
	assert.Len(t, exported, 1)

	receivedKeys := map[ebpf.BpfFlowId]struct{}{}

	var key1Flows []*flow.Record
	for _, f := range exported {
		require.NotContains(t, receivedKeys, f.Id)
		receivedKeys[f.Id] = struct{}{}
		switch f.Id {
		case key1:
			assert.EqualValues(t, 3, f.Metrics.Packets)
			assert.EqualValues(t, 44, f.Metrics.Bytes)
			assert.False(t, f.Duplicate)
			assert.Equal(t, "foo", f.Interface)
			key1Flows = append(key1Flows, f)
		}
	}
	assert.Lenf(t, key1Flows, 1, "only one flow should have been forwarded: %#v", key1Flows)
}

func TestFlowsAgent_DeduplicationJustMark(t *testing.T) {
	export := testAgent(t, &Config{
		CacheActiveTimeout: 10 * time.Millisecond,
		CacheMaxFlows:      100,
		DeduperJustMark:    true,
		Deduper:            DeduperFirstCome,
	})

	exported := export.Get(t, timeout)
	receivedKeys := map[ebpf.BpfFlowId]struct{}{}

	assert.Len(t, exported, 1)
	duplicates := 0
	for _, f := range exported {
		require.NotContains(t, receivedKeys, f.Id)
		receivedKeys[f.Id] = struct{}{}
		switch f.Id {
		case key1:
			assert.EqualValues(t, 3, f.Metrics.Packets)
			assert.EqualValues(t, 44, f.Metrics.Bytes)
			if f.Duplicate {
				duplicates++
			}
			assert.Equal(t, "foo", f.Interface)
		}
	}
	assert.Equalf(t, 0, duplicates, "exported flows should have only one duplicate: %#v", exported)
}

func TestFlowsAgent_Deduplication_None(t *testing.T) {
	export := testAgent(t, &Config{
		CacheActiveTimeout: 10 * time.Millisecond,
		CacheMaxFlows:      100,
		Deduper:            DeduperNone,
	})

	exported := export.Get(t, timeout)
	assert.Len(t, exported, 1)
	receivedKeys := map[ebpf.BpfFlowId]struct{}{}

	var key1Flows []*flow.Record
	for _, f := range exported {
		require.NotContains(t, receivedKeys, f.Id)
		receivedKeys[f.Id] = struct{}{}
		switch f.Id {
		case key1:
			assert.EqualValues(t, 3, f.Metrics.Packets)
			assert.EqualValues(t, 44, f.Metrics.Bytes)
			assert.False(t, f.Duplicate)
			assert.Equal(t, "foo", f.Interface)
			key1Flows = append(key1Flows, f)
		}
	}
	assert.Lenf(t, key1Flows, 1, "both key1 flows should have been forwarded: %#v", key1Flows)
}

func TestFlowsAgent_Decoration(t *testing.T) {
	export := testAgent(t, &Config{
		CacheActiveTimeout: 10 * time.Millisecond,
		CacheMaxFlows:      100,
	})

	exported := export.Get(t, timeout)
	assert.Len(t, exported, 1)

	// Tests that the decoration stage has been properly executed. It should
	// add the interface name and the agent IP
	for _, f := range exported {
		assert.Equal(t, agentIP, f.AgentIP.String())
		switch f.Id {
		case key1, key2:
			assert.Equal(t, "foo", f.Interface)
		default:
			assert.Equal(t, "bar", f.Interface)
		}
	}
}

func testAgent(t *testing.T, cfg *Config) *test.ExporterFake {
	ebpfTracer := test.NewTracerFake()
	export := test.NewExporterFake()
	agent, err := flowsAgent(cfg,
		test.SliceInformerFake{
			{Name: "foo", Index: 3},
			{Name: "bar", Index: 4},
		}, ebpfTracer, export.Export,
		net.ParseIP(agentIP))
	require.NoError(t, err)

	go func() {
		require.NoError(t, agent.Run(context.Background()))
	}()
	test2.Eventually(t, timeout, func(t require.TestingT) {
		require.Equal(t, StatusStarted, agent.status)
	})

	now := uint64(monotime.Now())
	key1Metrics := ebpf.BpfFlowMetrics{Packets: 3, Bytes: 44, StartMonoTimeTs: now + 1000, EndMonoTimeTs: now + 1_000_000_000}

	ebpfTracer.AppendLookupResults(map[ebpf.BpfFlowId]ebpf.BpfFlowMetrics{
		key1: key1Metrics,
	})
	return export
}
