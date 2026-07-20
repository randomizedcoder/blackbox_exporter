// Copyright 2026 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package prober

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/promslog"

	"github.com/prometheus/blackbox_exporter/config"
)

// TestICMPUnknownImplementation verifies the dispatcher rejects an unknown
// backend without touching a socket. (Config validation normally prevents this,
// but ProbeICMP must be defensive if a Module is constructed directly.)
func TestICMPUnknownImplementation(t *testing.T) {
	module := config.Module{
		Prober: "icmp",
		ICMP:   config.ICMPProbe{Implementation: "nope"},
	}
	registry := prometheus.NewRegistry()
	if ProbeICMP(context.Background(), "127.0.0.1", module, registry, promslog.NewNopLogger()) {
		t.Fatal("ProbeICMP with unknown implementation should return false")
	}
}

// TestICMPEngineProbe exercises the icmpengine backend against loopback. It is
// skipped where non-privileged ICMP is unavailable (net.ipv4.ping_group_range
// not permissive, or IPv4/IPv6 loopback missing), matching the environment
// dependence of the repo's other network tests. Priming the shared pool here
// both detects availability and mirrors real runtime behavior.
func TestICMPEngineProbe(t *testing.T) {
	if _, err := icmpEngines.get(engineKey{ttl: config.DefaultICMPTTL}); err != nil {
		t.Skipf("skipping: non-privileged ICMP engine unavailable "+
			"(needs net.ipv4.ping_group_range and IPv4+IPv6 loopback): %v", err)
	}

	tests := []struct {
		name   string
		target string
		proto  string
	}{
		{"ipv4 loopback", "127.0.0.1", "ip4"},
		{"ipv6 loopback", "::1", "ip6"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			registry := prometheus.NewRegistry()
			module := config.Module{
				Prober:  "icmp",
				Timeout: time.Second,
				ICMP: config.ICMPProbe{
					IPProtocol:         tc.proto,
					IPProtocolFallback: false,
					TTL:                config.DefaultICMPTTL,
					Implementation:     "icmpengine",
				},
			}

			if !ProbeICMP(ctx, tc.target, module, registry, promslog.NewNopLogger()) {
				t.Fatalf("ProbeICMP(icmpengine) failed for %s", tc.target)
			}

			mfs, err := registry.Gather()
			if err != nil {
				t.Fatalf("registry.Gather() err = %v", err)
			}

			// The three duration phases must be present.
			checkMetrics(map[string]map[string]map[string]struct{}{
				"probe_icmp_duration_seconds": {
					"phase": {"resolve": {}, "setup": {}, "rtt": {}},
				},
			}, mfs, t)

			// The icmpengine backend must not emit reply hop limit.
			for _, mf := range mfs {
				if mf.GetName() == "probe_icmp_reply_hop_limit" {
					t.Errorf("probe_icmp_reply_hop_limit should be absent for the icmpengine backend")
				}
			}
		})
	}
}

// TestICMPEngineSourceAndDontFragment exercises the source_ip_address and
// dont_fragment options through the icmpengine backend against IPv4 loopback,
// confirming full parity with the native prober's socket-level features.
func TestICMPEngineSourceAndDontFragment(t *testing.T) {
	if _, err := icmpEngines.get(engineKey{ttl: config.DefaultICMPTTL}); err != nil {
		t.Skipf("skipping: non-privileged ICMP engine unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	module := config.Module{
		Prober:  "icmp",
		Timeout: time.Second,
		ICMP: config.ICMPProbe{
			IPProtocol:         "ip4",
			IPProtocolFallback: false,
			TTL:                config.DefaultICMPTTL,
			SourceIPAddress:    "127.0.0.1",
			DontFragment:       true,
			PayloadSize:        56,
			Implementation:     "icmpengine",
		},
	}

	if !ProbeICMP(ctx, "127.0.0.1", module, prometheus.NewRegistry(), promslog.NewNopLogger()) {
		t.Fatal("ProbeICMP(icmpengine) with source + dont_fragment failed for 127.0.0.1")
	}
}
